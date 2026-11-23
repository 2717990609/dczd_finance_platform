package main

import (
	"reflect"
	"testing"
	"time"
)

func TestPracticeDraftQuotesRecalculateOnlyChangedEnrollments(t *testing.T) {
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	payment := func(id int64, order string, amount int64) paymentInput {
		return paymentInput{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: order, PaymentID: order,
			OccurredAt: at, AmountMinor: &amount, ReferrerID: "sales"}}
	}
	orders := map[string][]paymentInput{
		"deposit": {payment(11, "deposit", 600)}, "balance": {payment(12, "balance", 400)}, "other": {payment(13, "other", 1000)},
	}
	members := []practiceEnrollment{
		{ID: 1, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"deposit", "balance"}, ConfirmedAt: at.Add(48 * time.Hour)},
		{ID: 2, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"other"}},
	}
	identities := map[identityKey][]identityAssignment{
		{Source: "DCZD", ExternalID: "sales"}: {{ID: 1, PersonID: 14, ValidFrom: at.Add(-time.Hour)}},
	}
	rule := paymentRule{ID: 1, Components: []component{{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "DIRECT_REFERRER"}}}
	cache := &practiceDraftQuoteCache{}
	refunds := make(map[int64][]practiceRefundMatch)
	calculate := func(revision int64) (paymentDraft, *practiceDraftQuoteRun) {
		t.Helper()
		runtime := practiceDraftRuntime{}
		run, err := cache.start(1, rule, revision, runtime.poolUnits)
		if err != nil {
			t.Fatal(err)
		}
		runtime.enrollmentQuotes = run
		got, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, refunds, runtime)
		if err != nil {
			t.Fatal(err)
		}
		runtime.enrollmentQuotes = nil
		full, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, refunds, runtime)
		if err != nil || !reflect.DeepEqual(got, full) {
			t.Fatalf("partial draft differs from full calculation: %+v, %+v, %v", got, full, err)
		}
		return got, run
	}
	first, before := calculate(1)
	if first.Total != 200 || len(before.current.quotes) != 2 {
		t.Fatalf("unexpected initial trial: %+v", first)
	}
	orders["balance"][0] = payment(14, "balance", 800)
	after, changed := calculate(1)
	if after.Total != 240 || after.Lines[1].FactID != 14 {
		t.Fatalf("the changed signup must include the new balance: %+v", after)
	}
	reused := func(a, b *practiceDraftQuoteRun, id int64) bool {
		return &a.current.quotes[id].lines[0] == &b.current.quotes[id].lines[0]
	}
	if reused(before, changed, 1) || !reused(before, changed, 2) {
		t.Fatal("only the signup with a changed balance should calculate new shares")
	}
	refund := payment(15, "balance-refund", 200)
	refund.Fact.OccurredAt = at.Add(time.Hour)
	refund.Fact.Kind, refund.Fact.RefundID = "REFUND_SUCCEEDED", "refund-1"
	refunds[14] = []practiceRefundMatch{{event: refund}}
	refunded, withRefund := calculate(1)
	if refunded.Total != 220 || reused(changed, withRefund, 1) || !reused(changed, withRefund, 2) {
		t.Fatal("a partial balance refund must recalculate the signup and preserve other quotes")
	}
	identities[identityKey{Source: "DCZD", ExternalID: "sales"}][0].PersonID = 20
	updated, config := calculate(2)
	if updated.Lines[0].PersonID != 20 || reused(withRefund, config, 2) {
		t.Fatal("an assignment revision must invalidate all quotes")
	}
	rule.ID++
	_, newRule := calculate(2)
	if reused(config, newRule, 2) {
		t.Fatal("a new rule version must invalidate all quotes")
	}
	rule.Components[0].Tiers = &practiceSalesTiers{}
	if run, err := cache.start(1, rule, 2, nil); err != nil || run != nil {
		t.Fatalf("tiered sales must use a full calculation: %v %v", run, err)
	}
	rule.Components[0].Tiers = nil
	rule.Components = append(rule.Components, component{PeriodBenefit: &practicePeriodBenefit{FixedPerClass: true}})
	if run, err := cache.start(1, rule, 2, nil); err != nil || run != nil {
		t.Fatalf("class-wide fixed awards must use a full calculation: %v %v", run, err)
	}
}
