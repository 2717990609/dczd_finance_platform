package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeDraftKeepsSignedRegistrationRefundedAfterCutoff(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	const key = "PRACTICE:2:1:100"
	signed := time.Date(2026, 10, 16, 17, 21, 15, 0, shanghai)
	cutoff := time.Date(2026, 10, 31, 23, 59, 59, 0, shanghai)
	late := cutoff.Add(24 * time.Hour)
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_LATE_REFUNDS", Name: "实战班", PeriodMode: "class"}))
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "156期", Ordinal: 156,
		StartsOn: "2026-10-08", EndsOn: "2026-11-30", SourcePeriodKey: key}))
	if _, err := s.db.Exec(ctx, "UPDATE periods SET refund_cutoff_at=$2 WHERE id=$1", period, cutoff); err != nil {
		t.Fatal(err)
	}
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "1"}))
	seller := id(s.createPerson(ctx, createPersonInput{Name: "销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: seller, Source: "DCZD", ExternalID: "25", ValidFrom: "2026-01-01T00:00:00+08:00"}))
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "销售规则", Components: []component{
		{Type: "sales", Mode: "rate", Value: 1000, ClassMember: "new", SourceRole: "DIRECT_REFERRER"},
	}}); err != nil {
		t.Fatal(err)
	}

	enabled, activeOrder, lateOrder, earlyOrder := true, "31", "32", "33"
	active := practiceEnrollmentWire{SourceEnrollmentID: "10", SourceOrderID: &activeOrder, ParticipantID: "20",
		MembershipType: 3, CommissionEligible: true, SignedAt: signed.Format(time.RFC3339)}
	snapshot := practiceRegistrationSnapshot{practiceRosterWire: practiceRosterWire{
		SourceSystem: "DCZD", PeriodKey: key, ActivityID: 100, ActivityType: 2, StrategyID: 1,
		PeriodStartAt: signed.Add(-24 * time.Hour).Format(time.RFC3339), Enrollments: []practiceEnrollmentWire{active},
	}, ValidPaymentLinks: &enabled, Registrations: []practiceRegistrationSnapshotRow{
		{ID: "10", ParticipantID: "20", OrderID: &activeOrder, MembershipType: 3, Enabled: &enabled, State: 2, SignedAt: &signed},
		{ID: "11", ParticipantID: "21", OrderID: &lateOrder, MembershipType: 3, Enabled: &enabled, State: 3, SignedAt: &signed},
		{ID: "12", ParticipantID: "22", OrderID: &earlyOrder, MembershipType: 3, Enabled: &enabled, State: 3, SignedAt: &signed},
	}}
	snapshot.RosterHash = practiceRosterHash(snapshot.Enrollments)
	saveSnapshot := func() {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			return storePracticeRegistrationSnapshot(ctx, tx, snapshot)
		}); err != nil {
			t.Fatal(err)
		}
	}
	saveSnapshot()
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		full, tail, deposit := int64(10000), int64(7000), int64(3000)
		at := signed.Add(-time.Hour)
		facts := []sourceFact{
			{OrderID: "31", Kind: "ORDER_PAID", RecordKey: "pay:31", PaymentID: "31", TransactionType: "2", AmountMinor: &full, OccurredAt: at},
			{OrderID: "32", Kind: "ORDER_PAID", RecordKey: "pay:32", PaymentID: "32", TransactionType: "2", AmountMinor: &tail, OccurredAt: at,
				Display: orderDisplayFields{"originalAmountMinor": full, "settlesDepositOrderId": "34"}},
			{OrderID: "33", Kind: "ORDER_PAID", RecordKey: "pay:33", PaymentID: "33", TransactionType: "2", AmountMinor: &full, OccurredAt: at},
			{OrderID: "34", Kind: "ORDER_PAID", RecordKey: "pay:34", PaymentID: "34", TransactionType: "1", AmountMinor: &deposit, OccurredAt: at.Add(-time.Hour)},
			{OrderID: "32", Kind: "REFUND_SUCCEEDED", RecordKey: "refund:32", RefundID: "32", AmountMinor: &tail, OccurredAt: late,
				RefundsPayment: []sourcePaymentReference{{OrderID: "32", RecordID: "32"}}},
			{OrderID: "34", Kind: "REFUND_SUCCEEDED", RecordKey: "refund:34", RefundID: "34", AmountMinor: &deposit, OccurredAt: cutoff,
				RefundsPayment: []sourcePaymentReference{{OrderID: "34", RecordID: "34"}}},
			{OrderID: "33", Kind: "REFUND_SUCCEEDED", RecordKey: "refund:33", RefundID: "33", AmountMinor: &full, OccurredAt: cutoff.Add(-time.Hour),
				RefundsPayment: []sourcePaymentReference{{OrderID: "33", RecordID: "33"}}},
		}
		for i := range facts {
			f := &facts[i]
			f.Source, f.Domain, f.SourceKey, f.ReferrerID, f.Version, f.UpdatedAt = "DCZD", "PRACTICE", "1", "25", "1", f.OccurredAt
		}
		if _, err := storeSourceFactsFrom(ctx, tx, facts[:1], s.orderSourceKey("DCZD")); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM draft_refresh_queue WHERE period_id=$1", period); err != nil {
			return err
		}
		_, err := storeSourceFactsFrom(ctx, tx, facts[1:], s.orderSourceKey("DCZD"))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var generation int64
	if err := s.db.QueryRow(ctx, `SELECT generation FROM draft_refresh_queue WHERE period_id=$1`, period).Scan(&generation); err != nil {
		t.Fatalf("historical refund did not queue the class draft: %v", err)
	}
	calculate := func(wantedGeneration, total int64, count, advisories int) draftSummary {
		t.Helper()
		draft, err := s.calculateCurrentDraft(ctx, period, wantedGeneration)
		if err != nil || draft.Total != total || draft.IssueCount != 0 || draft.RefundCount != 0 || draft.PracticeRosterID == nil {
			t.Fatalf("draft total=%d issues=%d refunds=%d roster=%v: %v", draft.Total, draft.IssueCount, draft.RefundCount, draft.PracticeRosterID, err)
		}
		var attendance, lateAdvisories int
		err = s.db.QueryRow(ctx, `SELECT attendance_count,(SELECT count(*) FROM calculation_draft_issues
		 WHERE period_id=$1 AND code='PRACTICE_REFUND_AFTER_CUTOFF' AND advisory)
		 FROM practice_rosters WHERE id=$2`, period, *draft.PracticeRosterID).Scan(&attendance, &lateAdvisories)
		if err != nil || attendance != count || lateAdvisories != advisories {
			t.Fatalf("attendance=%d late advisories=%d: %v", attendance, lateAdvisories, err)
		}
		return draft
	}
	first := calculate(generation, 2000, 2, 2)
	var pending bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, period).Scan(&pending); err != nil || pending {
		t.Fatalf("successful draft left the period pending: %t %v", pending, err)
	}
	saveSnapshot()
	repeated := calculate(0, 2000, 2, 2)
	if repeated.Version != first.Version || *repeated.PracticeRosterID != *first.PracticeRosterID {
		t.Fatal("unchanged sync and recalculation should keep the same draft and roster versions")
	}
	if _, err := s.db.Exec(ctx, "UPDATE periods SET refund_cutoff_at=$2 WHERE id=$1", period, late.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	calculate(0, 1000, 1, 0)
}
