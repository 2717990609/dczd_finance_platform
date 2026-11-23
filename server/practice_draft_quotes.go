package main

import (
	"crypto/sha256"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
)

type practiceDraftQuoteCache struct {
	draftQuoteCache[*practiceDraftQuote]
}

type practiceDraftQuoteRun struct {
	cache    *practiceDraftQuoteCache
	previous map[int64]*practiceDraftQuote
	current  draftQuotePeriod[*practiceDraftQuote]
}

type practiceQuotePayment struct {
	id, gross, net             int64
	company, recommendation    string
	category, internalCategory string
	referrerConflict, inactive bool
}

type practiceDraftQuoteInput struct {
	membershipType int
	noReferrer     bool
	payments       [2]practiceQuotePayment
}

type practiceDraftQuote struct {
	input  practiceDraftQuoteInput
	total  int64
	lines  []draftLine
	issues []draftIssue
}

func practiceEnrollmentQuotesEligible(rule paymentRule) bool {
	hasLocalAwards := false
	for _, part := range rule.Components {
		benefit := part.PeriodBenefit
		if part.Tiers != nil || benefit != nil && (benefit.FixedPerClass || len(benefit.Tiers) != 0) {
			return false
		}
		hasLocalAwards = hasLocalAwards || benefit == nil
	}
	return hasLocalAwards
}

func (c *practiceDraftQuoteCache) start(periodID int64, rule paymentRule, revision int64, pools map[int64]pgtype.UUID) (*practiceDraftQuoteRun, error) {
	if !practiceEnrollmentQuotesEligible(rule) {
		return nil, nil
	}
	// Rule versions are immutable. Assignment, sales category and company
	// ownership changes advance the configuration revision.
	encoded, err := json.Marshal(struct {
		RuleID, Configuration int64
		Pools                 map[int64]pgtype.UUID
	}{rule.ID, revision, pools})
	if err != nil {
		return nil, err
	}
	scope := sha256.Sum256(encoded)
	previous := c.read(periodID, scope)
	return &practiceDraftQuoteRun{cache: c, previous: previous,
		current: draftQuotePeriod[*practiceDraftQuote]{id: periodID, scope: scope, quotes: make(map[int64]*practiceDraftQuote, len(previous))}}, nil
}

// Quotes contain only enrollment-local awards. Class-wide rounding and
// performance postings are rebuilt from the current inputs every time.
func (r *practiceDraftQuoteRun) append(draft *paymentDraft, rule paymentRule, p practicePurchase,
	identities map[identityKey][]identityAssignment, runtime practiceDraftRuntime) error {
	if r.current.rows > maxDraftCachedRows || len(p.payments) > 2 {
		return appendPracticeEnrollmentAwards(draft, rule, p, identities, runtime, nil)
	}
	input := practiceDraftQuoteInput{membershipType: p.member.MembershipType, noReferrer: p.noReferrer}
	for i, payment := range p.payments {
		f := payment.Fact
		input.payments[i] = practiceQuotePayment{id: payment.ID, gross: *f.AmountMinor, net: p.netAmount(payment),
			company: f.SalesCompanyID, recommendation: f.ReferrerCompanyID, referrerConflict: f.ReferrerConflict,
			category: runtime.companyClassifications[f.PaymentID], internalCategory: runtime.internalSalesCategories[f.PaymentID],
			inactive: f.PracticeInactiveCompany.matches(p.member)}
	}
	quote, ok := r.previous[p.member.ID]
	if !ok || quote.input != input {
		var fragment paymentDraft
		if err := appendPracticeEnrollmentAwards(&fragment, rule, p, identities, runtime, nil); err != nil {
			return err
		}
		quote = &practiceDraftQuote{input: input, total: fragment.Total, lines: fragment.Lines, issues: fragment.Issues}
	}
	total, err := addMinor(draft.Total, quote.total)
	if err != nil {
		return err
	}
	draft.Total = total
	draft.Lines = append(draft.Lines, quote.lines...)
	draft.Issues = append(draft.Issues, quote.issues...)
	r.current.add(p.member.ID, quote, 1+len(quote.lines)+len(quote.issues))
	return nil
}
