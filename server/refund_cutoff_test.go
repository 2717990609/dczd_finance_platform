package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRefundCutoffIncludesLateArrivalAndCarriesBoundary(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	day := 1
	product := id(s.createProduct(ctx, createProductInput{Code: "REF-CUTOFF", Name: "产品", PeriodMode: "calendar", CycleStartDay: &day}))
	first := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月", SettlementMonth: "2026-08"}))
	next := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月", SettlementMonth: "2026-09"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "VIP", SourceKey: "VIP499"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "收款人"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCAI", ExternalID: "seller", ValidFrom: "2026-01-01T00:00:00+08:00"}))
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "分润", Components: []component{
		{Type: "sales", Mode: "rate", Value: 3333, SourceRole: "LOCK_SALES_USER"},
	}}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 9, 2, 18, 0, 0, 0, shanghai)
	if _, err := s.setPeriodRefundCutoff(ctx, periodRefundCutoffInput{PeriodID: first, CutoffAt: &cutoff}); err != nil {
		t.Fatal(err)
	}
	paidAt := cutoff.AddDate(0, 0, -5)
	paid := int64(101)
	payment := sourceFact{Source: "DCAI", Kind: "ORDER_PAID", Domain: "VIP", SourceKey: "VIP499", Version: "1",
		OrderID: "order", RecordKey: "payment:17", PaymentID: "17", AmountMinor: &paid, OccurredAt: paidAt, UpdatedAt: paidAt,
		Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "seller"}}}
	store := func(facts ...sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, facts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	store(payment)
	initial, err := s.calculateCurrentDraft(ctx, first, 0)
	if err != nil || initial.Total != 33 {
		t.Fatalf("original draft: %+v %v", initial, err)
	}
	var precise string
	if err := s.db.QueryRow(ctx, "SELECT precise_amount::text FROM calculation_draft_lines WHERE period_id=$1", first).Scan(&precise); err != nil ||
		precise != "0.3366330000" {
		t.Fatalf("expected the pre-posting amount, got %q %v", precise, err)
	}
	refund := func(id string, occurred time.Time) sourceFact {
		amount := int64(30)
		return sourceFact{Source: "DCAI", Kind: "REFUND_SUCCEEDED", Domain: "VIP", SourceKey: "VIP499", Version: "1",
			OrderID: "order", RecordKey: "refund:" + id, RefundID: id, AmountMinor: &amount, OccurredAt: occurred,
			UpdatedAt: cutoff.Add(time.Hour), RefundsPayment: []sourcePaymentReference{{OrderID: "order", RecordID: "17"}}}
	}
	nextPayment := payment
	nextPayment.OrderID, nextPayment.PaymentID, nextPayment.RecordKey = "next", "18", "payment:18"
	nextPayment.OccurredAt, nextPayment.UpdatedAt = cutoff.Add(-time.Hour), cutoff.Add(-time.Hour)
	nextPaymentRefund := refund("r3", cutoff.Add(-time.Minute))
	nextPaymentRefund.OrderID, nextPaymentRefund.RefundsPayment = "next", []sourcePaymentReference{{OrderID: "next", RecordID: "18"}}
	store(refund("r1", cutoff.Add(-time.Second)), refund("r2", cutoff), nextPayment, nextPaymentRefund)
	var queued int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM draft_refresh_queue WHERE period_id=$1", first).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("late arrival failed to queue its original open period: %d %v", queued, err)
	}
	current, err := s.calculateCurrentDraft(ctx, first, 0)
	if err != nil || current.IssueCount != 0 || current.RefundTotal != -9 || current.RefundCount != 1 {
		t.Fatalf("expected only the pre-cutoff refund: %+v %v", current, err)
	}
	if err := s.db.QueryRow(ctx, "SELECT precise_amount::text FROM calculation_draft_refunds WHERE period_id=$1", first).Scan(&precise); err != nil ||
		precise != "-0.0980198020" {
		t.Fatalf("expected the original frozen amount prorated without cent rounding, got %q %v", precise, err)
	}
	if _, err := s.db.Exec(ctx, "INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')", first, current.Version); err != nil {
		t.Fatal(err)
	}
	following, err := s.calculateCurrentDraft(ctx, next, 0)
	if err != nil || following.IssueCount != 0 || following.Accrued != 33 || following.RefundTotal != -19 || following.RefundCount != 2 {
		t.Fatalf("expected the refund exactly at cutoff in the next period: %+v %v", following, err)
	}
}

func TestPracticeRefundCutoffAppliesAfterRosterAndHandlesFullRefund(t *testing.T) {
	confirmed := time.Date(2026, 9, 1, 10, 0, 0, 0, shanghai)
	cutoff := confirmed.Add(24 * time.Hour)
	paid := int64(100)
	orders := map[string][]paymentInput{
		"order": {{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "order", PaymentID: "p1", AmountMinor: &paid,
			ReferrerID: "seller", OccurredAt: confirmed.Add(-time.Hour)}}},
	}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 2}}}
	members := []practiceEnrollment{{ID: 1, SourceEnrollmentID: "a", CommissionEligible: true, MembershipType: 3,
		ConfirmedAt: confirmed, PaidOrderIDs: []string{"order"}}}
	rule := paymentRule{Components: []component{{Type: "sales", Mode: "rate", Value: 3333, SourceRole: "DIRECT_REFERRER"}}}
	for _, c := range []struct {
		name, precise string
		at            time.Time
		refund, total int64
		performance   int
	}{
		{"before cutoff", "0.2333100000", cutoff.Add(-time.Second), 30, 23, 2},
		{"at cutoff", "0.3333000000", cutoff, 30, 33, 1},
		{"fully refunded", "", cutoff.Add(-time.Second), 100, 0, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			refunds := map[int64][]practiceRefundMatch{1: {{event: paymentInput{ID: 2,
				Fact: sourceFact{AmountMinor: &c.refund, OccurredAt: c.at, UpdatedAt: cutoff.Add(time.Hour)}}}}}
			draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, refunds,
				practiceDraftRuntime{refundCutoff: &cutoff})
			if err != nil || draft.Total != c.total || draft.blockingIssues() != 0 || len(draft.Performance) != c.performance {
				t.Fatalf("unexpected draft total %d, performance %d, issues %v: %v", draft.Total, len(draft.Performance), draft.Issues, err)
			}
			if c.precise != "" && (len(draft.Lines) != 1 || draft.Lines[0].PreciseAmount != c.precise) {
				t.Fatalf("unexpected pre-posting amount: %+v", draft.Lines)
			}
		})
	}
}
