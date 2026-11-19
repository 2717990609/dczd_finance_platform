package main

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const periodRateScale = 1_000_000

func (s workspace) practiceRuleRecipients(w http.ResponseWriter, r *http.Request) {
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
	 'units',coalesce((SELECT jsonb_agg(jsonb_build_object('id',id,'name',name,'kind',kind) ORDER BY name,id)
	  FROM organization_units WHERE status='ACTIVE'),'[]'))
	 FROM products WHERE id=$1 AND period_mode='class'`, product).Scan(&result)
	respond(w, result, err)
}

type practicePeriodBenefit struct {
	Label             string               `json:"label"`
	PersonID          int64                `json:"personId,omitempty"`
	UnitID            string               `json:"unitId,omitempty"`
	SalesCompanies    []string             `json:"salesCompanies,omitempty"`
	SalesPeople       []int64              `json:"salesPeople,omitempty"`
	AppointmentUnitID string               `json:"appointmentUnitId,omitempty"`
	AppointmentKind   string               `json:"appointmentKind,omitempty"`
	TierOnWholeClass  bool                 `json:"tierOnWholeClass,omitempty"`
	Tiers             []practicePeriodRate `json:"tiers,omitempty"`
}

type practicePeriodRate struct {
	From    int64  `json:"from"`
	Through *int64 `json:"through,omitempty"`
	Value   int64  `json:"value"`
}

func validPracticePeriodBenefit(c component) bool {
	b := c.PeriodBenefit
	if (c.Type != "manager" && c.Type != "delivery") || b == nil || c.Value > periodRateScale || c.RefundPolicy != "" {
		return false
	}
	if !textOK(b.Label, 80) || b.PersonID < 0 || (b.PersonID > 0) == (b.UnitID != "") ||
		(b.AppointmentUnitID == "") != (b.AppointmentKind == "") || b.AppointmentUnitID != "" && b.PersonID == 0 ||
		len(b.SalesPeople) != 0 && !validSourceRole(c.SourceRole) {
		return false
	}
	for _, unit := range []string{b.UnitID, b.AppointmentUnitID} {
		if unit != "" {
			if normalized, ok := canonicalSourceUUID(unit); !ok || normalized != unit {
				return false
			}
		}
	}
	if b.AppointmentKind != "" && !validSourceRole(b.AppointmentKind) {
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
	 WHERE person_id=ANY($1::bigint[])`, people)
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

type practiceBenefitShare struct {
	purchase practicePurchase
	payments []paymentInput
	key      string
	net      int64
	amount   int64
	rem      int64
}

func (b practicePeriodBenefit) eligiblePayment(f sourceFact, role string, assignments []practiceAppointment,
	identities map[identityKey][]identityAssignment) (bool, string) {
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
	if len(b.SalesPeople) == 0 {
		return true, ""
	}
	seller, issue := paymentBeneficiary(f, role, identities)
	return slices.Contains(b.SalesPeople, seller.PersonID), issue
}

func collectPracticeBenefitShares(part component, purchases []practicePurchase, assignments []practiceAppointment,
	identities map[identityKey][]identityAssignment, paymentCounts map[paymentKey]int) ([]practiceBenefitShare, int64, string, error) {
	b := part.PeriodBenefit
	var eligible []practiceBenefitShare
	var tierBase int64
	for _, p := range purchases {
		memberMatches := part.ClassMember == "" || part.ClassMember == classMember(p.member.MembershipType)
		repeatedPayment := slices.ContainsFunc(p.payments, func(payment paymentInput) bool {
			return paymentCounts[paymentKey{payment.Fact.Source, payment.Fact.PaymentID}] > 1
		})
		if p.issue != "" || repeatedPayment {
			if memberMatches || b.TierOnWholeClass {
				return nil, 0, "PRACTICE_PERIOD_BENEFIT_INCOMPLETE", nil
			}
			continue
		}
		if b.TierOnWholeClass {
			var err error
			tierBase, err = addMinor(tierBase, p.base)
			if err != nil {
				return nil, 0, "", err
			}
		}
		if !memberMatches {
			continue
		}
		if len(p.payments) == 2 {
			left, right := p.payments[0].Fact, p.payments[1].Fact
			if left.SalesCompanyID != right.SalesCompanyID ||
				(left.ReferrerID != "" && right.ReferrerID != "" && left.ReferrerID != right.ReferrerID) {
				return nil, 0, "PRACTICE_PERIOD_BENEFIT_INCOMPLETE", nil
			}
		}
		share := practiceBenefitShare{purchase: p}
		for _, payment := range p.payments {
			yes, issue := b.eligiblePayment(payment.Fact, part.SourceRole, assignments, identities)
			if issue != "" {
				return nil, 0, issue, nil
			}
			if yes {
				share.payments = append(share.payments, payment)
				share.net += p.netAmount(payment)
			}
		}
		if len(share.payments) == 0 {
			continue
		}
		share.key = share.payments[0].Fact.OrderID
		if len(p.payments) == 2 {
			share.key = p.member.SourceEnrollmentID
		}
		if !b.TierOnWholeClass {
			var err error
			tierBase, err = addMinor(tierBase, share.net)
			if err != nil {
				return nil, 0, "", err
			}
		}
		eligible = append(eligible, share)
	}
	return eligible, tierBase, "", nil
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

func appendPracticePeriodBenefits(draft *paymentDraft, rule paymentRule, purchases []practicePurchase,
	appointments map[practiceAppointmentKey][]practiceAppointment, identities map[identityKey][]identityAssignment,
	paymentCounts map[paymentKey]int) error {
	for index, part := range rule.Components {
		b := part.PeriodBenefit
		if b == nil {
			continue
		}
		key := practicePeriodComponentKey(part, index)
		validAppointments := appointments[b.appointmentKey()]
		if b.AppointmentUnitID != "" && len(validAppointments) == 0 {
			draft.Issues = append(draft.Issues, draftIssue{RuleID: rule.ID, Component: key, Code: "PRACTICE_PERIOD_APPOINTMENT_MISSING"})
			continue
		}
		shares, basis, issue, err := collectPracticeBenefitShares(part, purchases, validAppointments, identities, paymentCounts)
		if err != nil {
			return err
		}
		if issue != "" {
			draft.Issues = append(draft.Issues, draftIssue{RuleID: rule.ID, Component: key, Code: issue})
			continue
		}
		if len(shares) == 0 {
			continue
		}
		rate, found := b.rate(part.Value, basis)
		if !found {
			draft.Issues = append(draft.Issues, draftIssue{RuleID: rule.ID, Component: key, Code: "PRACTICE_PERIOD_BENEFIT_TIER_MISSING"})
			continue
		}
		total, err := allocatePracticePeriodRate(shares, rate)
		if err != nil {
			return err
		}
		draft.Total, err = addMinor(draft.Total, total)
		if err != nil {
			return err
		}
		if total == 0 {
			continue
		}
		var unit *pgtype.UUID
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
				Component: key, BeneficiaryUnit: unit, reportUnit: reportUnit}
			appendPracticePeriodShare(draft, base, share)
		}
	}
	return nil
}

func appendPracticePeriodShare(draft *paymentDraft, line draftLine, share practiceBenefitShare) {
	first := share.payments[0]
	line.FactID, line.Base, line.Amount = first.ID, share.purchase.netAmount(first), share.amount
	if len(share.payments) == 1 {
		draft.Lines = append(draft.Lines, line)
		return
	}
	// Preserve the split used for a credited entitlement. The balance receives
	// its floor share of the original gross payment and the deposit the rest.
	second := share.payments[1]
	var tail big.Int
	tail.Mul(big.NewInt(share.amount), big.NewInt(*second.Fact.AmountMinor))
	tail.Quo(&tail, big.NewInt(share.purchase.gross))
	line.Amount -= tail.Int64()
	draft.Lines = append(draft.Lines, line)
	line.FactID, line.Base, line.Amount = second.ID, share.purchase.netAmount(second), tail.Int64()
	draft.Lines = append(draft.Lines, line)
}
