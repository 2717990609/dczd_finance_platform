package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var errPracticeCompanyScopeOverlap = errors.New("同一报名类型有重复的销售类别、指定公司或外部公司默认比例，请调整配置")

type practiceCompanySales struct {
	Label                    string                    `json:"label,omitempty"`
	Payee                    *practiceCompanyPayee     `json:"payee,omitempty"`
	Categories               []practiceCompanyCategory `json:"categories"`
	DefaultRate              *int64                    `json:"defaultRate,omitempty"`
	InternalCompanies        []string                  `json:"internalCompanies,omitempty"`
	RecommendationRecipients []string                  `json:"recommendationRecipients,omitempty"`
}

type practiceCompanyPayee struct {
	Kind      string              `json:"kind"`
	Role      string              `json:"role,omitempty"`
	PersonID  int64               `json:"personId,omitempty"`
	CompanyID string              `json:"companyId,omitempty"`
	Pool      *practicePoolTarget `json:"pool,omitempty"`
}

func (p *practiceCompanyPayee) kind() string {
	if p == nil {
		return "sales_company"
	}
	return p.Kind
}

func (p *practiceCompanyPayee) valid() bool {
	if p == nil {
		return true
	}
	if p.Kind != "order_person" && p.Role != "" || p.Kind != "person" && p.PersonID != 0 ||
		p.Kind != "company" && p.CompanyID != "" || p.Kind != "pool" && p.Pool != nil {
		return false
	}
	switch p.Kind {
	case "sales_company":
		return true
	case "order_person":
		return validSourceRole(p.Role)
	case "person":
		return p.PersonID > 0
	case "company":
		id, ok := canonicalSourceUUID(p.CompanyID)
		return ok && id == p.CompanyID
	case "pool":
		return p.Pool != nil && p.Pool.valid()
	default:
		return false
	}
}

type practiceCompanyValue struct {
	Mode  string `json:"mode"`
	Value int64  `json:"value"`
}

type practiceCompanyCategory struct {
	Label              string                    `json:"label,omitempty"`
	SalesCategoryID    string                    `json:"salesCategoryId,omitempty"`
	SalesCategoryIDs   []string                  `json:"salesCategoryIds,omitempty"`
	Companies          []string                  `json:"companies"`
	Overrides          []practiceCompanyOverride `json:"overrides,omitempty"`
	RecommendationRate *int64                    `json:"recommendationRate,omitempty"`
	practiceCompanyValue
}

type practiceCompanyOverride struct {
	CompanyID          string   `json:"companyId,omitempty"`
	CompanyIDs         []string `json:"companyIds,omitempty"`
	RecommendationRate *int64   `json:"recommendationRate,omitempty"`
	practiceCompanyValue
}

func (c practiceCompanyCategory) categoryIDs() []string {
	if c.SalesCategoryID != "" {
		return []string{c.SalesCategoryID}
	}
	return c.SalesCategoryIDs
}

func (o practiceCompanyOverride) companyIDs() []string {
	if o.CompanyID != "" {
		return []string{o.CompanyID}
	}
	return o.CompanyIDs
}

func (v practiceCompanyValue) valid() bool {
	switch v.Mode {
	case "rate":
		return v.Value >= 0 && v.Value <= periodRateScale
	case "fixed":
		return v.Value >= 0 && v.Value <= 100000000000
	default:
		return false
	}
}

func validCompanyRecommendation(member string, rate *int64) bool {
	return rate == nil || member == "new" && *rate >= 0 && *rate <= periodRateScale
}

func validPracticeCompanySales(c component) bool {
	if c.Type != "company_sales" || c.Mode != "company_sales" ||
		c.CompanySales == nil || c.Value != 0 ||
		c.Tiers != nil || c.PeriodBenefit != nil || c.RefundPolicy != "" || !validSourceRole(c.SourceRole) {
		return false
	}
	defaultRate := c.CompanySales.DefaultRate
	if defaultRate == nil && len(c.CompanySales.Categories) == 0 ||
		defaultRate != nil && (*defaultRate < 0 || *defaultRate > periodRateScale) {
		return false
	}
	seen := make(map[string]bool)
	if !c.CompanySales.Payee.valid() || c.CompanySales.Label != "" && !textOK(c.CompanySales.Label, 80) ||
		c.ClassMember != "new" && len(c.CompanySales.RecommendationRecipients) != 0 {
		return false
	}
	for _, id := range c.CompanySales.RecommendationRecipients {
		if normalized, ok := canonicalSourceUUID(id); !ok || normalized != id || seen[id] {
			return false
		}
		seen[id] = true
	}
	clear(seen)
	seenCategories := make(map[string]bool)
	validRate := func(rate *int64) bool {
		return validCompanyRecommendation(c.ClassMember, rate) &&
			(rate == nil || *rate == 0 || len(c.CompanySales.RecommendationRecipients) != 0)
	}
	for _, category := range c.CompanySales.Categories {
		categoryIDs := category.categoryIDs()
		if (len(categoryIDs) == 0) == (len(category.Companies) == 0) ||
			category.SalesCategoryID != "" && len(category.SalesCategoryIDs) != 0 || category.Label != "" && !textOK(category.Label, 80) ||
			!category.practiceCompanyValue.valid() || !validRate(category.RecommendationRate) {
			return false
		}
		for _, id := range categoryIDs {
			normalized, ok := canonicalSourceUUID(id)
			if !ok || normalized != id || seenCategories[id] {
				return false
			}
			seenCategories[id] = true
		}
		member := make(map[string]bool)
		for _, company := range category.Companies {
			if !textOK(company, 128) || seen[company] {
				return false
			}
			seen[company], member[company] = true, true
		}
		for _, override := range category.Overrides {
			companyIDs := override.companyIDs()
			if len(companyIDs) == 0 || override.CompanyID != "" && len(override.CompanyIDs) != 0 ||
				!override.practiceCompanyValue.valid() || !validRate(override.RecommendationRate) {
				return false
			}
			for _, id := range companyIDs {
				if !(member[id] || len(categoryIDs) != 0 && textOK(id, 128) && !seen[id]) {
					return false
				}
				seen[id] = true
				delete(member, id)
			}
		}
	}
	return true
}

func checkPracticeCompanyScopes(items []component) error {
	type scope struct{ member, id string }
	categoryBlocks, companyBlocks := make(map[scope]int), make(map[scope]int)
	defaults := make(map[string]bool)
	allNamedCompanies := make(map[string]bool)
	var internal []string
	register := func(blocks map[scope]int, member string, ids []string, index int) bool {
		for _, id := range ids {
			key := scope{member, id}
			if previous, exists := blocks[key]; exists && previous != index {
				return false
			}
			blocks[key] = index
		}
		return true
	}
	for index, part := range items {
		for _, split := range part.retrainSplits() {
			internal = append(internal, split.CompanyID)
		}
		arrangement := part.CompanySales
		if arrangement == nil {
			continue
		}
		internal = append(internal, arrangement.InternalCompanies...)
		for _, member := range []string{"new", "retrain"} {
			if !part.matchesPracticeMember(member) {
				continue
			}
			if arrangement.DefaultRate != nil {
				if defaults[member] {
					return errPracticeCompanyScopeOverlap
				}
				defaults[member] = true
			}
			for _, category := range arrangement.Categories {
				if !register(categoryBlocks, member, category.categoryIDs(), index) ||
					!register(companyBlocks, member, category.Companies, index) {
					return errPracticeCompanyScopeOverlap
				}
				for _, override := range category.Overrides {
					if !register(companyBlocks, member, override.companyIDs(), index) {
						return errPracticeCompanyScopeOverlap
					}
				}
			}
		}
	}
	for key := range companyBlocks {
		allNamedCompanies[key.id] = true
	}
	for _, company := range internal {
		if !textOK(company, 128) || allNamedCompanies[company] {
			return invalid
		}
	}
	for _, part := range items {
		for _, company := range part.salesCompanies() {
			_, newSales := companyBlocks[scope{"new", company}]
			_, retrainSales := companyBlocks[scope{"retrain", company}]
			if part.matchesPracticeMember("new") && newSales ||
				part.matchesPracticeMember("retrain") && retrainSales {
				return invalid
			}
		}
	}
	return nil
}

type practiceCompanyAward struct {
	practiceCompanyValue
	role, key                string
	internal                 bool
	componentIndex           int
	payee                    *practiceCompanyPayee
	recommendationRate       int64
	recommendationRecipients []string
}

type practiceCompanyOffers struct {
	companies      map[string]map[string]practiceCompanyAward
	categories     map[string]map[string]practiceCompanyAward
	defaults       map[string]practiceCompanyAward
	companyMembers map[string]bool
	personal       map[string]bool
}

func practiceCompanyStandards(rule paymentRule) *practiceCompanyOffers {
	var offers *practiceCompanyOffers
	for i, part := range rule.Components {
		if part.CompanySales == nil {
			continue
		}
		if offers == nil {
			offers = &practiceCompanyOffers{
				companies:      map[string]map[string]practiceCompanyAward{"new": {}, "retrain": {}},
				categories:     map[string]map[string]practiceCompanyAward{"new": {}, "retrain": {}},
				defaults:       make(map[string]practiceCompanyAward),
				companyMembers: make(map[string]bool),
				personal:       make(map[string]bool),
			}
		}
		for _, company := range part.CompanySales.InternalCompanies {
			for _, standards := range offers.companies {
				standards[company] = practiceCompanyAward{internal: true}
			}
		}
		key := practicePeriodComponentKey(part, i)
		awardBase := practiceCompanyAward{role: part.SourceRole, key: key, componentIndex: i,
			payee: part.CompanySales.Payee, recommendationRecipients: part.CompanySales.RecommendationRecipients}
		for member, standards := range offers.companies {
			if !part.matchesPracticeMember(member) {
				continue
			}
			offers.companyMembers[member] = true
			if rate := part.CompanySales.DefaultRate; rate != nil {
				award := awardBase
				award.practiceCompanyValue = practiceCompanyValue{Mode: "rate", Value: *rate}
				offers.defaults[member] = award
			}
			for _, category := range part.CompanySales.Categories {
				award := awardBase
				award.practiceCompanyValue = category.practiceCompanyValue
				if category.RecommendationRate != nil {
					award.recommendationRate = *category.RecommendationRate
				}
				for _, id := range category.categoryIDs() {
					offers.categories[member][id] = award
				}
				for _, company := range category.Companies {
					standards[company] = award
				}
				for _, override := range category.Overrides {
					custom := award
					custom.practiceCompanyValue = override.practiceCompanyValue
					if override.RecommendationRate != nil {
						custom.recommendationRate = *override.RecommendationRate
					}
					for _, id := range override.companyIDs() {
						standards[id] = custom
					}
				}
			}
		}
	}
	if offers != nil {
		for _, part := range rule.Components {
			if part.Type == "sales" {
				for _, member := range []string{"new", "retrain"} {
					if part.matchesPracticeMember(member) {
						offers.personal[member] = true
						for _, company := range part.salesCompanies() {
							offers.companies[member][company] = practiceCompanyAward{internal: true}
						}
					}
				}
			}
			for _, split := range part.retrainSplits() {
				for _, standards := range offers.companies {
					standards[split.CompanyID] = practiceCompanyAward{internal: true}
				}
			}
		}
	}
	return offers
}

func (p *practicePurchase) classifyCompany(offers *practiceCompanyOffers, categories map[string]string) {
	if p.issue != "" || offers == nil {
		return
	}
	member := classMember(p.member.MembershipType)
	standards := offers.companies[member]
	if !offers.companyMembers[member] && !offers.personal[member] {
		return
	}
	var company string
	company, p.companyRoutingIssue = practicePurchaseCompany(p.payments)
	if p.companyRoutingIssue != "" {
		return
	}
	terms, exists := standards[company]
	if fallback, hasDefault := offers.defaults[member]; !exists &&
		(len(offers.categories[member]) != 0 || hasDefault || offers.needsClassifications() && offers.personal[member]) {
		category, issue := practicePurchaseCompanyClassification(p.payments, categories, nil)
		if issue == "" {
			if category == practiceInternalCompany && offers.personal[member] {
				return
			}
			terms, exists = offers.categories[member][category]
		}
		if !exists && hasDefault && len(p.payments) != 0 && !slices.ContainsFunc(p.payments, func(payment paymentInput) bool {
			classification := categories[payment.Fact.PaymentID]
			_, hasCategory := offers.categories[member][classification]
			return !externalCompanyDefaultEligible(classification) || hasCategory
		}) {
			terms, exists = fallback, true
		}
	}
	if !exists {
		p.companyRoutingIssue = "PRACTICE_COMPANY_SCOPE_MISSING"
	} else if !terms.internal {
		p.companyAward = &terms
	}
}

func practicePurchaseCompanyClassification(payments []paymentInput, categories, internalCategories map[string]string) (string, string) {
	var category string
	for _, payment := range payments {
		id := strings.TrimPrefix(categories[payment.Fact.PaymentID], practiceUnverifiedExternalPrefix)
		if internal := internalCategories[payment.Fact.PaymentID]; id == practiceInternalCompany && internal != "" {
			id = internal
		}
		if id == "" {
			return "", "PRACTICE_TIER_SALES_CATEGORY_MISSING"
		}
		if category != "" && category != id {
			return "", "PRACTICE_TIER_SALES_CATEGORY_CONFLICT"
		}
		category = id
	}
	return category, ""
}

const (
	practiceInternalCompany      = "INTERNAL"
	practiceUncategorizedCompany = "EXTERNAL_NO_CATEGORY"
	// Retain the category standard when imported company-type changes lack a dated record.
	practiceUnverifiedExternalPrefix = "UNVERIFIED_EXTERNAL:"
)

func externalCompanyDefaultEligible(classification string) bool {
	return classification != "" && classification != practiceInternalCompany &&
		!strings.HasPrefix(classification, practiceUnverifiedExternalPrefix)
}

const practiceCompanyClassificationsSQL = `WITH matched AS (
 SELECT p.payment,c.company_id,
  CASE WHEN coalesce(change.previous_type,u.sales_company_type)='INTERNAL' THEN '` + practiceInternalCompany + `'
  WHEN $5 AND coalesce(change.previous_type,u.sales_company_type)='EXTERNAL' THEN
   CASE WHEN coalesce(change.version-1,u.sales_company_type_version)=1 OR recorded.version IS NOT NULL
      OR snapshot.captured_at<=p.paid_at THEN coalesce(h.category_id::text,'` + practiceUncategorizedCompany + `')
    ELSE coalesce('` + practiceUnverifiedExternalPrefix + `'||h.category_id::text,'') END
  ELSE coalesce(h.category_id::text,'') END AS scope,
  CASE WHEN coalesce(change.previous_type,u.sales_company_type)='INTERNAL' THEN h.category_id::text END AS internal_category
 FROM unnest($1::text[],$2::text[],$3::timestamptz[]) p(payment,company,paid_at)
 JOIN organization_company_sources c ON c.source='DCZD' AND c.source_company_id=p.company
  AND c.valid_from<=p.paid_at AND (c.valid_to IS NULL OR c.valid_to>p.paid_at)
 JOIN organization_units u ON u.id=c.company_id
 LEFT JOIN LATERAL (
  SELECT previous_type,version FROM organization_sales_company_type_changes t
  WHERE t.unit_id=u.id AND t.changed_at>p.paid_at ORDER BY t.version LIMIT 1
 ) change ON u.sales_company_type_version>1
 LEFT JOIN organization_sales_company_type_changes recorded ON $5 AND recorded.unit_id=u.id
  AND recorded.version=coalesce(change.version-1,u.sales_company_type_version) AND recorded.changed_at<=p.paid_at
 LEFT JOIN organization_import_state snapshot ON $5 AND snapshot.singleton AND change.version IS NULL
 LEFT JOIN organization_sales_category_history h ON h.unit_id=c.company_id
  AND (coalesce(change.previous_type,u.sales_company_type)='EXTERNAL'
    OR ($4 AND coalesce(change.previous_type,u.sales_company_type)='INTERNAL'))
  AND h.valid_from<=p.paid_at AND (h.valid_to IS NULL OR h.valid_to>p.paid_at)
)
 SELECT payment,min(scope),coalesce(min(internal_category),'') FROM matched GROUP BY payment
 HAVING count(DISTINCT (company_id,scope,internal_category))=1`

func loadPracticeCompanyClassifications(ctx context.Context, tx pgx.Tx, facts []paymentInput,
	includeInternalCategories, includeExternalDefaults bool) (map[string]string, map[string]string, error) {
	var payments, companies []string
	var paidAt []time.Time
	for _, p := range facts {
		f := p.Fact
		if f.SalesCompanyID != "" {
			payments = append(payments, f.PaymentID)
			companies = append(companies, f.SalesCompanyID)
			paidAt = append(paidAt, f.OccurredAt)
		}
	}
	if len(payments) == 0 {
		return nil, nil, nil
	}
	rows, err := tx.Query(ctx, practiceCompanyClassificationsSQL, payments, companies, paidAt, includeInternalCategories, includeExternalDefaults)
	if err != nil {
		return nil, nil, err
	}
	result := make(map[string]string)
	internalCategories := make(map[string]string)
	var payment, category, internal string
	_, err = pgx.ForEachRow(rows, []any{&payment, &category, &internal}, func() error {
		if category != "" {
			result[payment] = category
		}
		if internal != "" {
			internalCategories[payment] = internal
		}
		return nil
	})
	return result, internalCategories, err
}

func (offers *practiceCompanyOffers) hasDefault() bool {
	return offers != nil && len(offers.defaults) != 0
}

func (offers *practiceCompanyOffers) needsClassifications() bool {
	return offers != nil && (offers.hasDefault() || len(offers.categories["new"])+len(offers.categories["retrain"]) != 0)
}

func (offers *practiceCompanyOffers) match(member, company, category string) (practiceCompanyAward, bool) {
	if offers == nil {
		return practiceCompanyAward{}, false
	}
	if award, configured := offers.companies[member][company]; configured {
		return award, true
	}
	if award, configured := offers.categories[member][strings.TrimPrefix(category, practiceUnverifiedExternalPrefix)]; configured {
		return award, true
	}
	if externalCompanyDefaultEligible(category) {
		award, configured := offers.defaults[member]
		return award, configured
	}
	return practiceCompanyAward{}, false
}

func loadPracticeCompanyRecipients(ctx context.Context, tx pgx.Tx, offers *practiceCompanyOffers,
	orders map[string][]paymentInput, categories map[string]string, orderCompanyMembers []practiceEnrollment) (map[string][]companySourceAssignment, error) {
	if offers == nil && len(orderCompanyMembers) == 0 {
		return nil, nil
	}
	selected := make(map[string]bool)
	for _, member := range orderCompanyMembers {
		if !member.CommissionEligible {
			continue
		}
		for _, order := range member.PaidOrderIDs {
			for _, payment := range orders[order] {
				if company := payment.Fact.SalesCompanyID; company != "" {
					selected[company] = true
				}
			}
		}
	}
	// Keep configured company recipients available to replay a later attribution correction.
	if offers != nil {
		for _, group := range offers.companies {
			for company, award := range group {
				if !award.internal {
					selected[company] = true
				}
			}
		}
	}
	if offers.needsClassifications() {
		for _, matches := range orders {
			for _, payment := range matches {
				f := payment.Fact
				if f.SalesCompanyID == "" || selected[f.SalesCompanyID] {
					continue
				}
				for _, member := range []string{"new", "retrain"} {
					award, configured := offers.match(member, f.SalesCompanyID, categories[f.PaymentID])
					if configured && !award.internal {
						selected[f.SalesCompanyID] = true
					}
				}
			}
		}
	}
	var keys []string
	for key := range selected {
		keys = append(keys, key)
	}
	return loadPracticeCompanySources(ctx, tx, keys, false)
}

func loadPracticeCompanySources(ctx context.Context, tx pgx.Tx, keys []string, activeOnly bool) (map[string][]companySourceAssignment, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT c.source_company_id,c.company_id,c.valid_from,c.valid_to
	 FROM organization_company_sources c JOIN organization_units u ON u.id=c.company_id AND u.kind='COMPANY'
	 WHERE c.source='DCZD' AND c.source_company_id=ANY($1::text[]) AND (NOT $2 OR u.status='ACTIVE')
	 ORDER BY c.valid_from,c.company_id`, keys, activeOnly)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]companySourceAssignment)
	var id string
	var a companySourceAssignment
	_, err = pgx.ForEachRow(rows, []any{&id, &a.Company, &a.From, &a.To}, func() error {
		out[id] = append(out[id], a)
		return nil
	})
	return out, err
}

func practiceCompanyRecipient(assignments []companySourceAssignment, at time.Time) uniqueUnit {
	var result uniqueUnit
	for _, a := range assignments {
		if !at.Before(a.From) && (a.To == nil || at.Before(*a.To)) {
			result.add(a.Company)
		}
	}
	return result
}

func practiceSalesCompanyUnit(payments []paymentInput, recipients map[string][]companySourceAssignment) (pgtype.UUID, string) {
	var unit pgtype.UUID
	for _, payment := range payments {
		f := payment.Fact
		mapping := practiceCompanyRecipient(recipients[f.SalesCompanyID], f.OccurredAt)
		switch {
		case mapping.Ambiguous || unit.Valid && mapping.ID.Valid && unit != mapping.ID:
			return pgtype.UUID{}, "PRACTICE_COMPANY_RECIPIENT_CONFLICT"
		case !mapping.ID.Valid:
			return pgtype.UUID{}, "PRACTICE_COMPANY_RECIPIENT_MISSING"
		}
		unit = mapping.ID
	}
	return unit, ""
}

func appendPracticeCompanySale(draft *paymentDraft, rule paymentRule, p practicePurchase,
	runtime practiceDraftRuntime, identities map[identityKey][]identityAssignment) error {
	award := p.companyAward
	if _, issue := practiceBeneficiaries(p.payments, award.role, identities); issue != "" {
		draft.Issues = append(draft.Issues, practiceIssue(issue, p.member, rule, award.key))
		return nil
	}
	fixedNew := award.Mode == "fixed" && classMember(p.member.MembershipType) == "new"
	line := draftLine{RuleID: rule.ID, EnrollmentID: p.member.ID, Component: award.key,
		practiceProportionalRefund: award.Mode == "rate" || fixedNew}
	var salesCompany pgtype.UUID
	var companyIssue string
	kind := award.payee.kind()
	if kind == "sales_company" || award.recommendationRate > 0 {
		salesCompany, companyIssue = practiceSalesCompanyUnit(p.payments, runtime.companyRecipients)
	}
	issue := ""
	switch kind {
	case "sales_company":
		line.BeneficiaryUnit, issue = &salesCompany, companyIssue
	case "order_person":
		people, problem := practiceBeneficiaries(p.payments, award.payee.Role, identities)
		issue = problem
		if issue == "" {
			line.PersonID = people[0].PersonID
		}
	case "person":
		line.PersonID = award.payee.PersonID
	case "company":
		unit := new(pgtype.UUID)
		if err := unit.Scan(award.payee.CompanyID); err != nil {
			return err
		}
		line.BeneficiaryUnit = unit
	case "pool":
		unit := runtime.poolUnits[award.payee.Pool.PoolID]
		if !unit.Valid {
			issue = "PRACTICE_POOL_MISSING"
		}
		line.BeneficiaryUnit, line.PracticePool = &unit, award.payee.Pool
	}
	if issue != "" {
		draft.Issues = append(draft.Issues, practiceIssue(issue, p.member, rule, award.key))
		return nil
	}
	amount := award.Value
	share := practiceBenefitShare{purchase: p, payments: p.payments}
	if fixedNew {
		if len(p.originalPayments) != 0 {
			share.payments = p.originalPayments
		}
		shares := []practiceBenefitShare{share}
		var err error
		amount, err = allocatePracticeFixedBenefit(shares, amount, false)
		if err != nil {
			return err
		}
		share = shares[0]
	} else if award.Mode == "rate" {
		amount = practiceCompanyRate(p.base, award.Value)
	}
	if amount > p.base {
		draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_COMPANY_AWARD_EXCEEDS_PAYMENT", p.member, rule, award.key))
		return nil
	}
	amounts := share.fixed
	if !fixedNew {
		amounts = p.benefitShares(p.payments, amount, !runtime.legacyGrossRateShares && award.Mode == "rate")
	}
	if err := appendPracticeCompanyShare(draft, line, share, amount, amounts); err != nil {
		return err
	}
	if award.recommendationRate > 0 {
		key := "period:company_referral:" + strconv.Itoa(award.componentIndex)
		var parent pgtype.UUID
		if companyIssue == "" {
			parent, companyIssue = runtime.recommendations.resolve(p.payments, salesCompany)
			if companyIssue == "" && parent.Valid && !slices.Contains(award.recommendationRecipients, parent.String()) {
				companyIssue = "PRACTICE_RECOMMENDATION_ROUTE_MISSING"
			}
		}
		if companyIssue != "" {
			pending := practiceIssue(companyIssue, p.member, rule, key)
			pending.FactID = p.payments[0].ID
			draft.Issues = append(draft.Issues, pending)
		} else if parent.Valid {
			referral := draftLine{RuleID: rule.ID, EnrollmentID: p.member.ID, Component: key, BeneficiaryUnit: &parent,
				practiceProportionalRefund: true}
			amount := practiceCompanyRate(p.base, award.recommendationRate)
			referralShare := practiceBenefitShare{purchase: p, payments: p.payments}
			return appendPracticeCompanyShare(draft, referral, referralShare, amount, p.benefitShares(p.payments, amount, !runtime.legacyGrossRateShares))
		}
	}
	return nil
}

func practiceCompanyRate(base, rate int64) int64 {
	return base/periodRateScale*rate + (base%periodRateScale*rate+periodRateScale/2)/periodRateScale
}

func appendPracticeCompanyShare(draft *paymentDraft, line draftLine, share practiceBenefitShare, amount int64, amounts [2]int64) error {
	var err error
	draft.Total, err = addMinor(draft.Total, amount)
	if err != nil {
		return err
	}
	appendPracticePeriodShare(draft, line, share, amounts)
	return nil
}
