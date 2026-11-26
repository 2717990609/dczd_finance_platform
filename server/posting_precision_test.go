package main

import (
	"math/big"
	"testing"
)

func TestPostingPrecisionTruncatesToCents(t *testing.T) {
	if got := calculatePaymentAmount(101, component{Mode: "rate", Value: 3333}); got != 33 {
		t.Errorf("per-order accrual: got %d cents, want 33", got)
	}
	if got := practiceCompanyRate(101, 333300); got != 33 {
		t.Errorf("practice company accrual: got %d cents, want 33", got)
	}
	if got := prorateMinor(-34, 2, 3); got != -22 {
		t.Errorf("signed refund: got %d cents, want -22", got)
	}
	shares := []practiceBenefitShare{{key: "a", net: 1}, {key: "b", net: 1}}
	total, err := allocatePracticePeriodRate(shares, 750000)
	if err != nil || total != 1 || shares[0].amount != 1 || shares[1].amount != 0 {
		t.Errorf("group posting must conserve its truncated total: %+v, total=%d, err=%v", shares, total, err)
	}
}

func TestPostingPrecisionRefundUsesOriginallyPostedCents(t *testing.T) {
	policy := component{RefundPolicy: "proportional"}
	var deducted int64
	for i, expected := range []int64{-11, -11, -12} {
		change, err := refundDelta(policy, 3, 34, int64(i)+1, deducted)
		if change != expected || err != nil {
			t.Errorf("refund %d: got %d cents and %v, want %d", i+1, change, err, expected)
		}
		deducted -= change
	}
	if deducted != 34 {
		t.Fatalf("full refund must recover all originally posted cents, got %d", deducted)
	}
}

func TestRetrainSplitUsesHighPrecisionBeforePostingEachShare(t *testing.T) {
	split := practiceRetrainSplit{
		SalesRate:   &practiceRetrainRate{Basis: practiceRetrainCommissionBasis, Value: 560000},
		ServiceRate: &practiceRetrainRate{Basis: practiceRetrainCommissionBasis, Value: 300000},
	}
	posted, ok := split.amountsForPosting(10, 100, big.NewRat(109, 10), truncatePosting)
	if !ok || posted.sales != 6 || posted.service != 3 || posted.commission != 10 {
		t.Fatalf("expected 6 cents sales, 3 cents service and 1 cent pool: %+v", posted)
	}
}

func TestFixedAggregateRefundUsesActualPreviouslyDeductedCents(t *testing.T) {
	// The historical half-up refund posted 302 out of a 603 cent award.
	// The last refund must recover the actual 301 cent balance.
	history := catalogFixedRefundHistory{deducted: 302}
	original := history.original(603, 10000)
	delta, err := refundDelta(component{RefundPolicy: "proportional"}, original.Base, original.Accrued, 10000, history.deducted)
	if err != nil || delta != -301 {
		t.Fatalf("full reversal got %d cents, want -301: %v", delta, err)
	}
}
