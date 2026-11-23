package main

import (
	"context"
	"encoding/json"
	"maps"
	"math/big"
	"math/bits"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const periodRateScale = 1_000_000

func (s workspace) practiceRuleRecipients(w http.ResponseWriter, r *http.Request) {
	if ruleID := r.URL.Query().Get("ruleId"); ruleID != "" {
		s.practiceVersionRecipients(w, r, ruleID)
		return
	}
	product, err := strconv.ParseInt(r.URL.Query().Get("productId"), 10, 64)
	if err != nil || product < 1 {
		respond(w, nil, invalid)
		return
	}
	if actor := financeUser(r.Context()); actor != nil && !actor.permitsRule(product, true) {
		respond(w, nil, errFinanceForbidden)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), `SELECT jsonb_build_object(
	 'people',(`+peopleSQL+`),
	 'pools',coalesce((SELECT jsonb_agg(jsonb_build_object('id',pool.id,'name',pool.name,'unitName',u.name) ORDER BY pool.name,pool.id)
	  FROM practice_pools pool JOIN organization_units u ON u.id=pool.unit_id WHERE pool.product_id=$1 AND u.status='ACTIVE'),'[]'),
	 'units',coalesce((SELECT jsonb_agg(jsonb_build_object('id',u.id,'name',u.name,'kind',u.kind,
	  'scopeName',concat_ws(' / ',gp.name,p.name,u.name)) ORDER BY u.name,u.id)
	  FROM organization_units u LEFT JOIN organization_units p ON p.id=u.parent_id
	  LEFT JOIN organization_units gp ON gp.id=p.parent_id WHERE u.status='ACTIVE'),'[]'))
	 FROM products WHERE id=$1 AND period_mode='class'`, product).Scan(&result)
	respond(w, result, err)
}

func (s workspace) practiceVersionRecipients(w http.ResponseWriter, r *http.Request, ruleID string) {
	id, err := strconv.ParseInt(ruleID, 10, 64)
	if err != nil || id < 1 {
		respond(w, nil, invalid)
		return
	}
	var scope []int64
	if actor := financeUser(r.Context()); actor != nil {
		scope = make([]int64, 0, len(actor.Rules))
		for _, grant := range actor.Rules {
			scope = append(scope, grant.ProductID)
		}
	}
	var version json.RawMessage
	err = s.db.QueryRow(r.Context(), `SELECT jsonb_build_object('components',v.components)
	 FROM rule_versions v JOIN products p ON p.id=v.product_id
	 WHERE v.id=$1 AND p.period_mode='class' AND ($2::bigint[] IS NULL OR p.id=ANY($2))`, id, scope).Scan(&version)
	if err != nil {
		respond(w, nil, err)
		return
	}
	result, err := s.appliedRuleRecipients(r.Context(), version)
	respond(w, result, err)
}

func (s workspace) appliedRuleRecipients(ctx context.Context, rule json.RawMessage) (json.RawMessage, error) {
	var version struct {
		Components []component `json:"components"`
	}
	if err := json.Unmarshal(rule, &version); err != nil {
		return nil, err
	}
	var people, pools []int64
	var units, categories []string
	for _, target := range practicePoolTargets(version.Components) {
		pools = append(pools, target.PoolID)
		people = append(people, target.OwnerID)
	}
	for _, part := range version.Components {
		if part.SalesPersonID != 0 {
			people = append(people, part.SalesPersonID)
		}
		if scope := part.SaleScope; scope != nil {
			units = append(units, scope.SalesUnits...)
			categories = append(categories, scope.SalesCategoryIDs...)
		}
		if benefit := part.PeriodBenefit; benefit != nil {
			people = append(people, benefit.PersonID)
			people = append(people, benefit.SalesPeople...)
			units = append(units, benefit.UnitID, benefit.AppointmentUnitID)
			units = append(units, benefit.SalesUnits...)
			units = append(units, benefit.ReferralCompanyIDs...)
		}
		if part.Tiers != nil {
			for _, start := range part.Tiers.PersonCountStarts {
				people = append(people, start.PersonID)
			}
			for _, schedule := range part.Tiers.PersonSchedules {
				people = append(people, schedule.PersonID)
			}
		}
		if part.CompanySales != nil {
			units = append(units, part.CompanySales.RecommendationRecipients...)
			if payee := part.CompanySales.Payee; payee != nil {
				people = append(people, payee.PersonID)
				units = append(units, payee.CompanyID)
			}
		}
		for _, split := range part.retrainSplits() {
			people = append(people, split.ServicePersonID)
			people = append(people, split.ServiceSalesPeople...)
			units = append(units, split.ServiceSalesUnits...)
		}
	}
	return s.practiceRecipientNames(ctx, people, units, pools, categories)
}

func (s workspace) practiceRecipientNames(ctx context.Context, people []int64, units []string, pools []int64, categories []string) (json.RawMessage, error) {
	if len(people)+len(units)+len(pools)+len(categories) == 0 {
		return nil, nil
	}
	var result json.RawMessage
	err := s.db.QueryRow(ctx, `SELECT jsonb_build_object(
	 'people',coalesce((SELECT jsonb_agg(jsonb_build_object('id',id,'name',name)) FROM people WHERE id=ANY($1::bigint[])),'[]'),
	 'units',coalesce((SELECT jsonb_agg(jsonb_build_object('id',u.id,'name',u.name,
	  'scopeName',concat_ws(' / ',gp.name,p.name,u.name)))
	  FROM organization_units u LEFT JOIN organization_units p ON p.id=u.parent_id
	  LEFT JOIN organization_units gp ON gp.id=p.parent_id
	  WHERE u.id IN (SELECT nullif(ref,'')::uuid FROM unnest($2::text[]) ref)),'[]'),
	 'pools',coalesce((SELECT jsonb_agg(jsonb_build_object('id',id,'name',name)) FROM practice_pools WHERE id=ANY($3::bigint[])),'[]'),
	 'categories',coalesce((SELECT jsonb_object_agg(id,name) FROM organization_sales_categories WHERE id=ANY($4::uuid[])),'{}'))`,
		people, units, pools, categories).Scan(&result)
	return result, err
}

type practicePeriodBenefit struct {
	Label                    string                     `json:"label"`
	LabelIsCustom            bool                       `json:"labelIsCustom,omitempty"`
	PersonID                 int64                      `json:"personId,omitempty"`
	RecipientDuty            string                     `json:"recipientDuty,omitempty"`
	OrderRecipientRole       string                     `json:"orderRecipientRole,omitempty"`
	OrderAppointmentLevel    string                     `json:"orderAppointmentLevel,omitempty"`
	OrderAppointmentKind     string                     `json:"orderAppointmentKind,omitempty"`
	SalesCompanyRecipient    bool                       `json:"salesCompanyRecipient,omitempty"`
	ReferralCompanyRecipient bool                       `json:"referralCompanyRecipient,omitempty"`
	ReferralCompanyIDs       []string                   `json:"referralCompanyIds,omitempty"`
	OwnSales                 bool                       `json:"ownSales,omitempty"`
	UnitID                   string                     `json:"unitId,omitempty"`
	SalesCategoryIDs         []string                   `json:"salesCategoryIds,omitempty"`
	SalesCompanies           []string                   `json:"salesCompanies,omitempty"`
	SalesPeople              []int64                    `json:"salesPeople,omitempty"`
	SalesUnits               []string                   `json:"salesUnits,omitempty"`
	RegistrationScope        *practiceRegistrationScope `json:"registrationScope,omitempty"`
	AppointmentUnitID        string                     `json:"appointmentUnitId,omitempty"`
	AppointmentKind          string                     `json:"appointmentKind,omitempty"`
	FixedMinor               *int64                     `json:"fixedMinor,omitempty"`
	FixedPerClass            bool                       `json:"fixedPerClass,omitempty"`
	TierOnWholeClass         bool                       `json:"tierOnWholeClass,omitempty"`
	Tiers                    []practicePeriodRate       `json:"tiers,omitempty"`
	Pool                     *practicePoolTarget        `json:"pool,omitempty"`
}

type practicePeriodRate struct {
	From    int64  `json:"from"`
	Through *int64 `json:"through,omitempty"`
	Value   int64  `json:"value"`
}

func (b practicePeriodBenefit) hasSalesScope() bool {
	return b.usesSalesPerson() || len(b.SalesCompanies)+len(b.SalesCategoryIDs) != 0
}

func practiceScopesUseCategories(rule paymentRule) bool {
	return slices.ContainsFunc(rule.Components, func(part component) bool {
		return part.PeriodBenefit != nil && len(part.PeriodBenefit.SalesCategoryIDs) != 0 ||
			part.SaleScope != nil && len(part.SaleScope.SalesCategoryIDs) != 0
	})
}

func practiceBenefitPaysOrderCompany(rule paymentRule) bool {
	return slices.ContainsFunc(rule.Components, func(part component) bool {
		b := part.PeriodBenefit
		return b != nil && (b.SalesCompanyRecipient || b.ReferralCompanyRecipient)
	})
}

func (b practicePeriodBenefit) usesSalesPerson() bool {
	return b.OwnSales || len(b.SalesPeople)+len(b.SalesUnits) != 0
}

func validPracticePeriodBenefit(c component) bool {
	b := c.PeriodBenefit
	if (c.Type != "manager" && c.Type != "delivery" && c.Type != "service") || b == nil || c.Value > periodRateScale || c.RefundPolicy != "" {
		return false
	}
	recipients := 0
	if b.PersonID > 0 {
		recipients++
	}
	if b.UnitID != "" {
		recipients++
	}
	if b.OrderRecipientRole != "" {
		if !validSourceRole(b.OrderRecipientRole) {
			return false
		}
		recipients++
	}
	if b.OrderAppointmentLevel != "" || b.OrderAppointmentKind != "" {
		if !validVIPSalesOrganizationLevel(b.OrderAppointmentLevel) || !validSourceRole(b.OrderAppointmentKind) || !validSourceRole(c.SourceRole) {
			return false
		}
		recipients++
	}
	if b.SalesCompanyRecipient {
		recipients++
	}
	if b.ReferralCompanyRecipient {
		recipients++
	} else if len(b.ReferralCompanyIDs) != 0 {
		return false
	}
	for i, company := range b.ReferralCompanyIDs {
		if normalized, ok := canonicalSourceUUID(company); !ok || normalized != company || slices.Contains(b.ReferralCompanyIDs[:i], company) {
			return false
		}
	}
	if b.Pool != nil {
		if !b.Pool.valid() {
			return false
		}
		recipients++
	}
	if !textOK(b.Label, 80) || b.PersonID < 0 || recipients != 1 ||
		(b.AppointmentUnitID == "") != (b.AppointmentKind == "") || b.AppointmentUnitID != "" && b.PersonID == 0 ||
		b.usesSalesPerson() && !validSourceRole(c.SourceRole) ||
		b.OwnSales && (b.PersonID == 0 || len(b.SalesCompanies)+len(b.SalesCategoryIDs)+len(b.SalesPeople)+len(b.SalesUnits) != 0) ||
		b.RecipientDuty != "" && (b.PersonID == 0 && b.OrderRecipientRole == "" || !validSourceRole(b.RecipientDuty)) {
		return false
	}
	for _, unit := range []string{b.UnitID, b.AppointmentUnitID} {
		if unit != "" {
			if normalized, ok := canonicalSourceUUID(unit); !ok || normalized != unit {
				return false
			}
		}
	}
	for index, unit := range b.SalesUnits {
		if normalized, ok := canonicalSourceUUID(unit); !ok || normalized != unit || slices.Contains(b.SalesUnits[:index], unit) {
			return false
		}
	}
	for index, category := range b.SalesCategoryIDs {
		if normalized, ok := canonicalSourceUUID(category); !ok || normalized != category || slices.Contains(b.SalesCategoryIDs[:index], category) {
			return false
		}
	}
	if b.AppointmentKind != "" && !validSourceRole(b.AppointmentKind) {
		return false
	}
	if s := b.RegistrationScope; s != nil && (!s.valid() || b.hasSalesScope()) {
		return false
	}
	for _, company := range b.SalesCompanies {
		if !textOK(company, 128) {
			return false
		}
	}
	for _, person := range b.SalesPeople {
		if person <= 0 {
			return false
		}
	}
	if b.FixedMinor != nil {
		return c.Value == 0 && *b.FixedMinor >= 0 && *b.FixedMinor <= 100000000000 && len(b.Tiers) == 0 && !b.TierOnWholeClass
	}
	if b.FixedPerClass {
		return false
	}
	if len(b.Tiers) == 0 {
		return !b.TierOnWholeClass
	}
	if c.Value != 0 {
		return false
	}
	for n, tier := range b.Tiers {
		if tier.From < 0 || tier.Value < 0 || tier.Value > periodRateScale || tier.Through != nil && *tier.Through < tier.From {
			return false
		}
		if n > 0 && (b.Tiers[n-1].Through == nil || *b.Tiers[n-1].Through >= tier.From) {
			return false
		}
	}
	return true
}

type practiceAppointmentKey struct {
	PersonID int64
	UnitID   string
	Kind     string
}

type practiceAppointment struct {
	From time.Time
	To   *time.Time
}

func (b practicePeriodBenefit) appointmentKey() practiceAppointmentKey {
	return practiceAppointmentKey{b.PersonID, b.AppointmentUnitID, b.AppointmentKind}
}

func loadPracticeAppointments(ctx context.Context, tx pgx.Tx, rule paymentRule) (map[practiceAppointmentKey][]practiceAppointment, error) {
	var people []int64
	for _, c := range rule.Components {
		if b := c.PeriodBenefit; b != nil && b.AppointmentUnitID != "" {
			people = append(people, b.PersonID)
		}
	}
	if len(people) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT person_id,unit_id::text,kind,valid_from,valid_to FROM organization_assignments
	 WHERE person_id=ANY($1::bigint[]) ORDER BY valid_from,id`, people)
	if err != nil {
		return nil, err
	}
	out := make(map[practiceAppointmentKey][]practiceAppointment)
	var key practiceAppointmentKey
	var appointment practiceAppointment
	_, err = pgx.ForEachRow(rows, []any{&key.PersonID, &key.UnitID, &key.Kind, &appointment.From, &appointment.To}, func() error {
		out[key] = append(out[key], appointment)
		return nil
	})
	return out, err
}

type practiceOrderAppointment struct {
	PersonID int64 `json:"personId"`
	vipInterval
}

type practiceOrderAppointments map[string]map[string][]practiceOrderAppointment

func loadPracticeOrderAppointments(ctx context.Context, tx pgx.Tx, rule paymentRule, organizations practiceSalesOrganizations, facts []paymentInput) (practiceOrderAppointments, error) {
	levels := make(map[string]map[string]bool)
	kinds := make(map[string]bool)
	for _, part := range rule.Components {
		if b := part.PeriodBenefit; b != nil && b.OrderAppointmentLevel != "" {
			if levels[part.SourceRole] == nil {
				levels[part.SourceRole] = make(map[string]bool)
			}
			levels[part.SourceRole][b.OrderAppointmentLevel], kinds[b.OrderAppointmentKind] = true, true
		}
	}
	if len(levels) == 0 {
		return nil, nil
	}
	units := make(map[string]bool)
	for _, payment := range organizations {
		for role, org := range payment {
			for level := range levels[role] {
				unit, issue := salesAppointmentOrganization(org.Company, org.Department, org.Group, org.ScopeAlternatives, level)
				if issue == "" {
					units[unit] = true
				}
			}
		}
	}
	if len(units) == 0 {
		return nil, nil
	}
	var first, last time.Time
	for _, payment := range facts {
		if len(organizations[payment.ID]) == 0 {
			continue
		}
		at := payment.Fact.OccurredAt
		if first.IsZero() || at.Before(first) {
			first = at
		}
		if last.IsZero() || at.After(last) {
			last = at
		}
	}
	if first.IsZero() {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT unit_id::text,kind,person_id,valid_from,valid_to FROM organization_assignments
	 WHERE unit_id=ANY($1::uuid[]) AND kind=ANY($2::text[]) AND valid_from<=$4 AND (valid_to IS NULL OR valid_to>$3)
	 ORDER BY unit_id,kind,valid_from,id`, slices.Collect(maps.Keys(units)), slices.Collect(maps.Keys(kinds)), first, last)
	if err != nil {
		return nil, err
	}
	result := make(practiceOrderAppointments)
	var unit, kind string
	var appointment practiceOrderAppointment
	_, err = pgx.ForEachRow(rows, []any{&unit, &kind, &appointment.PersonID, &appointment.From, &appointment.To}, func() error {
		if result[unit] == nil {
			result[unit] = make(map[string][]practiceOrderAppointment)
		}
		result[unit][kind] = append(result[unit][kind], appointment)
		return nil
	})
	return result, err
}

func practiceOrderAppointmentRecipient(payment paymentInput, benefit *practicePeriodBenefit, role string,
	identities map[identityKey][]identityAssignment, runtime practiceDraftRuntime) (int64, pgtype.UUID, string) {
	var id pgtype.UUID
	seller, issue := paymentBeneficiary(payment.Fact, role, identities)
	if issue != "" {
		return 0, id, issue
	}
	org, ok := runtime.salesOrganizations[payment.ID][role]
	if !ok || org.PersonID != seller.PersonID || org.SourceCompany != payment.Fact.SalesCompanyID {
		return 0, id, "PRACTICE_SALES_ORGANIZATION_MISSING"
	}
	key, issue := salesAppointmentOrganization(org.Company, org.Department, org.Group, org.ScopeAlternatives, benefit.OrderAppointmentLevel)
	if issue != "" {
		return 0, id, issue
	}
	if err := id.Scan(key); err != nil || !id.Valid {
		return 0, id, "PRACTICE_SALES_ORGANIZATION_MISSING"
	}
	var person int64
	for _, appointment := range runtime.orderAppointments[key][benefit.OrderAppointmentKind] {
		if !appointment.contains(payment.Fact.OccurredAt) {
			continue
		}
		if person != 0 && person != appointment.PersonID {
			return 0, id, "BENEFIT_APPOINTMENT_CONFLICT"
		}
		person = appointment.PersonID
	}
	return person, id, ""
}

type practiceBenefitShare struct {
	purchase      practicePurchase
	payments      []paymentInput
	people        []identityAssignment
	organizations []pgtype.UUID
	company       pgtype.UUID
	fixed         [2]int64
	key           string
	net           int64
	amount        int64
	rem           int64
}

func (b practicePeriodBenefit) eligiblePayment(payment paymentInput, role string, assignments []practiceAppointment,
	identities map[identityKey][]identityAssignment, runtime practiceDraftRuntime) (bool, string) {
	f := payment.Fact
	if b.AppointmentUnitID != "" && !slices.ContainsFunc(assignments, func(a practiceAppointment) bool {
		return !f.OccurredAt.Before(a.From) && (a.To == nil || f.OccurredAt.Before(*a.To))
	}) {
		return false, ""
	}
	if len(b.SalesCompanies) != 0 {
		if f.SalesCompanyID == "" {
			return false, "PRACTICE_TIER_COMPANY_MISSING"
		}
		if !slices.Contains(b.SalesCompanies, f.SalesCompanyID) {
			return false, ""
		}
	}
	if len(b.SalesCategoryIDs) != 0 {
		category, issue := practicePurchaseCompanyClassification([]paymentInput{payment}, runtime.companyClassifications, runtime.internalSalesCategories)
		if issue != "" || !slices.Contains(b.SalesCategoryIDs, category) {
			return false, issue
		}
	}
	if !b.usesSalesPerson() {
		return true, ""
	}
	seller, issue := paymentBeneficiary(f, role, identities)
	if issue != "" {
		return false, issue
	}
	if b.OwnSales {
		return seller.PersonID == b.PersonID, ""
	}
	if len(b.SalesPeople) != 0 && !slices.Contains(b.SalesPeople, seller.PersonID) {
		return false, ""
	}
	if len(b.SalesUnits) == 0 {
		return true, ""
	}
	organization, ok := runtime.salesOrganizations[payment.ID][role]
	if !ok || organization.PersonID != seller.PersonID || organization.SourceCompany != f.SalesCompanyID {
		return false, "PRACTICE_SALES_ORGANIZATION_MISSING"
	}
	if slices.Contains(b.SalesUnits, organization.Company) {
		return true, ""
	}
	matches, conflict := matchOrganizationScope(b.SalesUnits, organization.Department, organization.Group, organization.ScopeAlternatives)
	if conflict {
		return false, "SALES_ORGANIZATION_SCOPE_CONFLICT"
	}
	return matches, ""
}

func collectPracticeBenefitShares(part component, purchases []practicePurchase, assignments []practiceAppointment,
	identities map[identityKey][]identityAssignment, paymentCounts map[paymentKey]int, runtime practiceDraftRuntime) ([]practiceBenefitShare, int64, draftIssue, error) {
	b := part.PeriodBenefit
	checkSalesAgreement := b.OrderAppointmentLevel == "" && b.RegistrationScope == nil && (b.hasSalesScope() || runtime.legacyBenefitSalesAgreement)
	checkConfiguredRole := b.usesSalesPerson() && !runtime.legacyBenefitAttribution
	var eligible []practiceBenefitShare
	var tierBase int64
	for _, p := range purchases {
		if p.issue == practicePaymentAlreadySettled {
			continue
		}
		memberMatches := part.ClassMember == "" || part.ClassMember == classMember(p.member.MembershipType)
		repeatedPayment := slices.ContainsFunc(p.payments, func(payment paymentInput) bool {
			return paymentCounts[paymentKey{payment.Fact.Source, payment.Fact.PaymentID}] > 1
		})
		if p.issue != "" || repeatedPayment {
			if memberMatches || b.TierOnWholeClass {
				return nil, 0, draftIssue{Code: "PRACTICE_PERIOD_BENEFIT_INCOMPLETE"}, nil
			}
			continue
		}
		if b.TierOnWholeClass {
			var err error
			tierBase, err = addMinor(tierBase, p.base)
			if err != nil {
				return nil, 0, draftIssue{}, err
			}
		}
		if !memberMatches {
			continue
		}
		if p.noReferrer && (b.RegistrationScope != nil || b.hasSalesScope() || b.OrderRecipientRole != "" ||
			b.OrderAppointmentLevel != "" || b.SalesCompanyRecipient || b.ReferralCompanyRecipient) {
			continue
		}
		if scope := b.RegistrationScope; scope != nil {
			input := runtime.registrations[p.member.ID]
			if input.VersionID == 0 || input.Issue != "" || !scope.matches(input.SellerID, input.CompanyID) {
				continue
			}
		}
		payments := p.payments
		if b.FixedMinor != nil && len(p.originalPayments) != 0 {
			payments = p.originalPayments
		}
		if len(payments) == 2 && checkSalesAgreement {
			left, right := payments[0].Fact, payments[1].Fact
			if left.SalesCompanyID != right.SalesCompanyID ||
				(!checkConfiguredRole && left.ReferrerID != "" && right.ReferrerID != "" && left.ReferrerID != right.ReferrerID) {
				return nil, 0, draftIssue{Code: "PRACTICE_PERIOD_BENEFIT_INCOMPLETE"}, nil
			}
			if checkConfiguredRole {
				if _, issue := practiceBeneficiaries(payments, part.SourceRole, identities); issue != "" {
					return nil, 0, draftIssue{Code: issue}, nil
				}
			}
		}
		share := practiceBenefitShare{purchase: p}
		for _, payment := range payments {
			yes, issue := b.eligiblePayment(payment, part.SourceRole, assignments, identities, runtime)
			if issue != "" {
				return nil, 0, draftIssue{Code: issue}, nil
			}
			if yes {
				if b.OrderAppointmentLevel != "" {
					person, unit, issue := practiceOrderAppointmentRecipient(payment, b, part.SourceRole, identities, runtime)
					if issue != "" {
						return nil, 0, draftIssue{Code: issue, FactID: payment.ID, EnrollmentID: p.member.ID}, nil
					}
					if person == 0 {
						continue
					}
					share.people = append(share.people, identityAssignment{PersonID: person})
					share.organizations = append(share.organizations, unit)
				}
				share.payments = append(share.payments, payment)
				share.net += p.netAmount(payment)
			}
		}
		if len(share.payments) == 0 {
			continue
		}
		var recipientIssue string
		if b.OrderRecipientRole != "" {
			share.people, recipientIssue = practiceBeneficiaries(share.payments, b.OrderRecipientRole, identities)
		}
		if b.SalesCompanyRecipient || b.ReferralCompanyRecipient {
			share.company, recipientIssue = practiceSalesCompanyUnit(share.payments, runtime.companyRecipients)
		}
		if b.ReferralCompanyRecipient && recipientIssue == "" {
			share.company, recipientIssue = runtime.recommendations.resolve(share.payments, share.company)
			if recipientIssue == "" {
				if !share.company.Valid {
					continue
				}
				if len(b.ReferralCompanyIDs) != 0 && !slices.Contains(b.ReferralCompanyIDs, share.company.String()) {
					recipientIssue = "PRACTICE_RECOMMENDATION_ROUTE_MISSING"
				}
			}
		}
		if recipientIssue != "" {
			return nil, 0, draftIssue{Code: recipientIssue, EnrollmentID: p.member.ID}, nil
		}
		share.key = share.payments[0].Fact.OrderID
		if len(payments) == 2 {
			share.key = p.member.SourceEnrollmentID
		}
		if b.FixedMinor == nil && !b.TierOnWholeClass {
			var err error
			tierBase, err = addMinor(tierBase, share.net)
			if err != nil {
				return nil, 0, draftIssue{}, err
			}
		}
		eligible = append(eligible, share)
	}
	return eligible, tierBase, draftIssue{}, nil
}

func (b practicePeriodBenefit) rate(configured, basis int64) (int64, bool) {
	if len(b.Tiers) == 0 {
		return configured, true
	}
	for _, tier := range b.Tiers {
		if basis >= tier.From && (tier.Through == nil || basis <= *tier.Through) {
			return tier.Value, true
		}
	}
	return 0, false
}

// Round once for the entitlement group, then assign leftover cents by the
// largest remainder. The enrollment or single-payment key breaks ties.
func allocatePracticePeriodRate(shares []practiceBenefitShare, rate int64) (int64, error) {
	var net, assigned int64
	for i := range shares {
		s := &shares[i]
		var err error
		net, err = addMinor(net, s.net)
		if err != nil {
			return 0, err
		}
		product := s.net % periodRateScale * rate
		s.amount = s.net/periodRateScale*rate + product/periodRateScale
		s.rem = product % periodRateScale
		assigned += s.amount
	}
	total := net/periodRateScale*rate + (net%periodRateScale*rate+periodRateScale/2)/periodRateScale
	if total == assigned {
		return total, nil
	}
	order := make([]int, len(shares))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := shares[order[i]], shares[order[j]]
		if a.rem == b.rem {
			return a.key < b.key
		}
		return a.rem > b.rem
	})
	for i := 0; int64(i) < total-assigned; i++ {
		shares[order[i]].amount++
	}
	return total, nil
}

func practicePeriodComponentKey(part component, index int) string {
	return "period:" + part.Type + ":" + strconv.Itoa(index)
}

func allocatePracticeFixedBenefit(shares []practiceBenefitShare, award int64, perClass bool) (int64, error) {
	if perClass {
		if err := splitPracticeFixedClass(shares, award); err != nil {
			return 0, err
		}
	}
	var total int64
	for i := range shares {
		share := &shares[i]
		if !perClass {
			share.fixed = practiceFixedSignupShares(share.payments, award)
		}
		for n, payment := range share.payments {
			share.fixed[n] = share.purchase.remainingFixedShare(payment, share.fixed[n])
			var err error
			total, err = addMinor(total, share.fixed[n])
			if err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func allocatePracticeOrganizationFixedBenefit(shares []practiceBenefitShare, award int64) ([]practiceBenefitShare, int64, error) {
	byUnit := make(map[pgtype.UUID][]practiceBenefitShare)
	for _, share := range shares {
		for index, unit := range share.organizations {
			payment := share.payments[index]
			byUnit[unit] = append(byUnit[unit], practiceBenefitShare{purchase: share.purchase, payments: share.payments[index : index+1],
				people: share.people[index : index+1], organizations: share.organizations[index : index+1], key: payment.Fact.OrderID,
				net: share.purchase.netAmount(payment)})
		}
	}
	var result []practiceBenefitShare
	var total int64
	for _, unit := range slices.SortedFunc(maps.Keys(byUnit), func(a, b pgtype.UUID) int { return strings.Compare(a.String(), b.String()) }) {
		rows := byUnit[unit]
		amount, err := allocatePracticeFixedBenefit(rows, award, true)
		if err != nil {
			return nil, 0, err
		}
		total, err = addMinor(total, amount)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, rows...)
	}
	return result, total, nil
}

func splitPracticeFixedClass(shares []practiceBenefitShare, award int64) error {
	type paymentShare struct {
		amount   *int64
		paid, id int64
		rem      uint64
	}
	payments := make([]paymentShare, 0, len(shares))
	var gross int64
	for i := range shares {
		for n, payment := range shares[i].payments {
			paid := *payment.Fact.AmountMinor
			var err error
			gross, err = addMinor(gross, paid)
			if err != nil {
				return err
			}
			payments = append(payments, paymentShare{amount: &shares[i].fixed[n], paid: paid, id: payment.ID})
		}
	}
	remaining := award
	for i := range payments {
		p := &payments[i]
		high, low := bits.Mul64(uint64(award), uint64(p.paid))
		amount, rem := bits.Div64(high, low, uint64(gross))
		*p.amount, p.rem = int64(amount), rem
		remaining -= *p.amount
	}
	if remaining != 0 {
		sort.Slice(payments, func(i, j int) bool {
			a, b := payments[i], payments[j]
			if a.rem == b.rem {
				return a.id < b.id
			}
			return a.rem > b.rem
		})
		for i := int64(0); i < remaining; i++ {
			*payments[i].amount += 1
		}
	}
	return nil
}

func appendPracticePeriodBenefits(draft *paymentDraft, rule paymentRule, purchases, fullyRefunded []practicePurchase,
	runtime practiceDraftRuntime, identities map[identityKey][]identityAssignment, paymentCounts map[paymentKey]int) error {
	classPurchases := append(purchases, fullyRefunded...)
	reportedRegistrations := make(map[int64]bool)
	for index, part := range rule.Components {
		b := part.PeriodBenefit
		if b == nil {
			continue
		}
		key := practicePeriodComponentKey(part, index)
		var unit *pgtype.UUID
		if b.Pool != nil {
			sink := runtime.poolUnits[b.Pool.PoolID]
			if !sink.Valid {
				draft.Issues = append(draft.Issues, draftIssue{RuleID: rule.ID, Component: key, Code: "PRACTICE_POOL_MISSING"})
				continue
			}
			unit = &sink
		}
		validAppointments := runtime.appointments[b.appointmentKey()]
		if b.AppointmentUnitID != "" && len(validAppointments) == 0 {
			draft.Issues = append(draft.Issues, draftIssue{RuleID: rule.ID, Component: key, Code: "PRACTICE_PERIOD_APPOINTMENT_MISSING"})
			continue
		}
		participants := purchases
		if b.FixedPerClass {
			participants = classPurchases
		}
		if b.RegistrationScope != nil &&
			appendPracticeRegistrationIssues(draft, rule.ID, part.ClassMember, participants, runtime.registrations, reportedRegistrations) &&
			(b.FixedPerClass || len(b.Tiers) != 0) {
			continue
		}
		shares, basis, issue, err := collectPracticeBenefitShares(part, participants, validAppointments, identities, paymentCounts, runtime)
		if err != nil {
			return err
		}
		if issue.Code != "" {
			issue.RuleID, issue.Component = rule.ID, key
			draft.Issues = append(draft.Issues, issue)
			continue
		}
		if len(shares) == 0 {
			continue
		}
		var total int64
		if b.FixedPerClass && b.OrderAppointmentLevel != "" {
			shares, total, err = allocatePracticeOrganizationFixedBenefit(shares, *b.FixedMinor)
		} else if b.FixedMinor != nil {
			total, err = allocatePracticeFixedBenefit(shares, *b.FixedMinor, b.FixedPerClass)
		} else {
			rate, found := b.rate(part.Value, basis)
			if !found {
				draft.Issues = append(draft.Issues, draftIssue{RuleID: rule.ID, Component: key, Code: "PRACTICE_PERIOD_BENEFIT_TIER_MISSING"})
				continue
			}
			if b.ReferralCompanyRecipient {
				// The company referral standard rounds each enrollment's entitlement.
				for i := range shares {
					shares[i].amount = practiceCompanyRate(shares[i].net, rate)
					total, err = addMinor(total, shares[i].amount)
					if err != nil {
						return err
					}
				}
			} else {
				total, err = allocatePracticePeriodRate(shares, rate)
			}
		}
		if err != nil {
			return err
		}
		draft.Total, err = addMinor(draft.Total, total)
		if err != nil {
			return err
		}
		if total == 0 && !slices.ContainsFunc(b.Tiers, func(tier practicePeriodRate) bool { return tier.Value > 0 }) {
			continue
		}
		if b.UnitID != "" {
			unit = new(pgtype.UUID)
			if err := unit.Scan(b.UnitID); err != nil {
				return err
			}
		}
		var reportUnit pgtype.UUID
		if b.AppointmentUnitID != "" {
			if err := reportUnit.Scan(b.AppointmentUnitID); err != nil {
				return err
			}
		}
		for _, share := range shares {
			base := draftLine{PersonID: b.PersonID, RuleID: rule.ID, EnrollmentID: share.purchase.member.ID,
				Component: key, BeneficiaryUnit: unit, reportUnit: reportUnit, PracticePool: b.Pool,
				practiceProportionalRefund: b.FixedMinor == nil && len(b.Tiers) == 0, practiceTierRefund: len(b.Tiers) != 0}
			if share.company.Valid {
				base.BeneficiaryUnit = &share.company
			}
			amounts := share.fixed
			if b.FixedMinor == nil {
				amounts = share.purchase.benefitShares(share.payments, share.amount, !runtime.legacyGrossRateShares)
			}
			appendPracticePeriodShare(draft, base, share, amounts)
		}
	}
	return nil
}

func appendPracticePeriodShare(draft *paymentDraft, line draftLine, share practiceBenefitShare, amounts [2]int64) {
	for i, payment := range share.payments {
		net := share.purchase.netAmount(payment)
		if net == 0 || line.PracticePool != nil && amounts[i] == 0 && !line.practiceTierRefund {
			continue
		}
		line.FactID, line.Base, line.Amount = payment.ID, net, amounts[i]
		if share.people != nil {
			line.PersonID, line.IdentityID = share.people[i].PersonID, share.people[i].ID
		}
		if share.organizations != nil {
			line.reportUnit = share.organizations[i]
		}
		line.PracticeTierReference = line.practiceTierRefund && line.Amount == 0
		draft.Lines = append(draft.Lines, line)
	}
}

func (p practicePurchase) benefitShares(payments []paymentInput, amount int64, useNet bool) [2]int64 {
	if !useNet {
		return practiceGrossShares(p.gross, payments, amount)
	}
	if len(payments) == 1 {
		return [2]int64{amount, 0}
	}
	tail := p.netAmount(payments[1])
	return practiceSplitShares(p.netAmount(payments[0])+tail, tail, amount)
}

func practiceGrossShares(gross int64, payments []paymentInput, amount int64) [2]int64 {
	if len(payments) == 1 {
		return [2]int64{amount, 0}
	}
	return practiceSplitShares(gross, *payments[1].Fact.AmountMinor, amount)
}

func practiceSplitShares(base, tailBase, amount int64) [2]int64 {
	if amount == 0 {
		return [2]int64{}
	}
	// Preserve the split used for a credited entitlement. The balance receives
	// its floor share and the deposit the rest.
	var tail big.Int
	tail.Mul(big.NewInt(amount), big.NewInt(tailBase))
	tail.Quo(&tail, big.NewInt(base))
	return [2]int64{amount - tail.Int64(), tail.Int64()}
}
