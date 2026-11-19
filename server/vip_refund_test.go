package main

import (
	"testing"
	"time"
)

func TestVIPRefundRoundsOnceForTheSplitAndCapsEachPriorBalance(t *testing.T) {
	// 29 cents originally paid at 70%. The rounded distribution is 20 cents.
	// After a 3-cent refund, the group retains 18 cents, allocated using the
	// original 8:4:4:4 stored amounts, without changing a prior recipient.
	parts := []refundObligation{
		{ID: 1, PaymentVersion: 10, Base: 29, Accrued: 8, ComponentType: "sales:SALES"},
		{ID: 2, PaymentVersion: 10, Base: 29, Accrued: 4, ComponentType: "sales:DCAI_GROUP_LEAD"},
		{ID: 3, PaymentVersion: 10, Base: 29, Accrued: 4, ComponentType: "sales:DCAI_SERVICE"},
		{ID: 4, PaymentVersion: 10, Base: 29, Accrued: 4, ComponentType: "sales:DCAI_TEAM_HEAD"},
	}
	terms := vipRefundTerms{Mode: "PROPORTIONAL", SplitItem: "sales"}
	for i := range parts {
		terms.apply(&parts[i], 7000)
	}
	history := map[refundLineKey]*refundHistory{}
	for _, part := range parts {
		history[part.key()] = &refundHistory{byID: map[string]int64{}}
	}
	var draft paymentDraft
	refund := func(id string, amount int64, all bool) {
		t.Helper()
		event := refundInput{ID: 42, RefundPeriodID: 17, Fact: sourceFact{Source: "DCAI", RefundID: id, AmountMinor: &amount,
			RefundsPayment: []sourcePaymentReference{{OrderID: "tiny-split"}}, OccurredAt: time.Unix(1, 0)}}
		if status, err := applyHistoricalRefund(&draft, event, parts, history); status != "" || err != nil {
			t.Fatalf("apply split refund %s %v", status, err)
		}
		for _, part := range parts {
			h := history[part.key()]
			if h.deducted > part.Accrued || all && h.deducted != part.Accrued {
				t.Fatalf("refund exceeded original amount: %+v", h)
			}
		}
	}
	refund("part-one", 3, false)
	if draft.RefundTotal != -2 || len(draft.Refunds) != 4 {
		t.Fatalf("first refund = %+v", draft.Refunds)
	}
	refund("part-two", 19, false)
	if draft.RefundTotal != -15 {
		t.Fatalf("second refund cumulative=%d", draft.RefundTotal)
	}
	refund("full", 7, true)
	if draft.RefundTotal != -20 {
		t.Fatalf("full refund cumulative=%d", draft.RefundTotal)
	}
	// A very asymmetric cap still produces the correct balance without iterating cents.
	shares := []vipRefundShare{
		{role: "a", original: 999999999, cap: 0}, {role: "b", original: 1000, cap: 1000}, {role: "c", original: 1000, cap: 1000},
	}
	if err := allocateVIPRefundRemaining("large", 1800, shares); err != nil || shares[1].remaining != 900 || shares[2].remaining != 900 {
		t.Fatalf("capped balances = %+v, %v", shares, err)
	}
}
