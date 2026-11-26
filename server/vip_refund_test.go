package main

import (
	"testing"
	"time"
)

func TestVIPRefundTruncatesByOriginalRecipientAndCapsEachPriorBalance(t *testing.T) {
	// 29 cents originally paid at 70%. The posted distribution is 20 cents.
	// Allocate the refund against each original 8:4:4:4 posted share.
	// A small refund must not be moved from one recipient to another.
	parts := []refundObligation{
		{ID: 1, PaymentVersion: 10, Base: 29, Accrued: 8, ComponentType: "sales:SALES"},
		{ID: 2, PaymentVersion: 10, Base: 29, Accrued: 4, ComponentType: "sales:DCAI_GROUP_LEAD"},
		{ID: 3, PaymentVersion: 10, Base: 29, Accrued: 4, ComponentType: "sales:DCAI_SERVICE"},
		{ID: 4, PaymentVersion: 10, Base: 29, Accrued: 4, ComponentType: "sales:DCAI_TEAM_HEAD"},
	}
	terms := vipRefundTerms{Mode: "PROPORTIONAL"}
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
	if draft.RefundTotal != 0 || len(draft.Refunds) != 4 {
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
	// The service arrangement did not post for this renewal.
	unposted := parts[2].key()
	parts = append(parts[:2:2], parts[3])
	clear(history)
	for _, part := range parts {
		history[part.key()] = &refundHistory{byID: map[string]int64{}}
	}
	draft = paymentDraft{}
	refund("renewal-partial", 3, false)
	if draft.RefundTotal != 0 || history[unposted] != nil {
		t.Fatalf("partial refund of the actual postings = %+v", draft.Refunds)
	}
	refund("renewal-rest", 26, true)
	if draft.RefundTotal != -16 {
		t.Fatalf("full refund must return only the 16 cents originally posted, got %d", draft.RefundTotal)
	}
}
