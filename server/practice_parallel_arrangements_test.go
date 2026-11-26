package main

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeOverlappingPersonalArrangementsAccrueSeparately(t *testing.T) {
	at := time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)
	paid := int64(10000)
	member := practiceEnrollment{ID: 1, RosterID: 1, MembershipType: 3, CommissionEligible: true, ConfirmedAt: at.Add(time.Hour), PaidOrderIDs: []string{"order"}}
	fact := sourceFact{Source: "DCZD", OrderID: "order", PaymentID: "order", AmountMinor: &paid, OccurredAt: at, ReferrerID: "referrer",
		Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "seller"}}}
	parts := []component{
		{Type: "sales", SourceRole: "DIRECT_REFERRER", Mode: "rate", Value: 1000},
		{Type: "sales", SourceRole: "LOCK_SALES_USER", Mode: "rate", Value: 500, ClassMember: "new"},
		{Type: "referral", SourceRole: "DIRECT_REFERRER", Mode: "fixed", Value: 150},
		{Type: "referral", SourceRole: "DIRECT_REFERRER", Mode: "fixed", Value: 250, ClassMember: "new"},
	}
	if err := validateComponents(parts); err != nil {
		t.Fatalf("overlapping arrangements cannot be saved: %v", err)
	}
	draft, err := calculatePracticeDraft([]practiceEnrollment{member}, paymentRule{ID: 1, Components: parts},
		map[string][]paymentInput{"order": {{ID: 1, Fact: fact}}},
		map[identityKey][]identityAssignment{{"DCZD", "referrer"}: {{ID: 1, PersonID: 11}}, {"DCZD", "seller"}: {{ID: 2, PersonID: 22}}},
		nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
	if err != nil || len(draft.Issues) != 0 || draft.Total != 1900 || len(draft.Lines) != 4 || len(draft.Performance) != 1 {
		t.Fatalf("expected four accruals and one sales performance posting: %+v, %v", draft, err)
	}
	var keys []string
	var amounts []int64
	for _, line := range draft.Lines {
		keys = append(keys, line.Component)
		amounts = append(amounts, line.Amount)
	}
	if !slices.Equal(keys, []string{"sales", "period:sales:1", "referral", "period:referral:3"}) ||
		!slices.Equal(amounts, []int64{1000, 500, 150, 250}) || draft.Lines[1].PersonID != 22 {
		t.Fatalf("wrong component identity, amount, or source role: %+v", draft.Lines)
	}
	if _, err := practiceReplayLines(draft.Lines); err != nil {
		t.Fatalf("arrangements cannot be replayed by original line: %v", err)
	}
}

func TestPracticeOverlappingArrangementsPersistAndRefundOriginalLines(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), draftQuotes: &draftCalculationCaches{}}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_ARRANGEMENTS", Name: "实战班", PeriodMode: "class"}))
	month := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "退款账期", SettlementMonth: "2026-09"}))
	class := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", StartsOn: "2026-08-08", EndsOn: "2026-08-08",
		Ordinal: 1, SourcePeriodKey: "PRACTICE:1:1:88"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "parallel"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "直接推荐人"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "10", ValidFrom: "2026-08-01T00:00:00Z"}))

	rule := publishRuleInput{ProductID: product, Label: "并行收款安排", Components: []component{
		{Type: "sales", Mode: "rate", SourceRole: "DIRECT_REFERRER", Value: 1000, ArrangementLabel: "成交基础提成"},
		{Type: "sales", Mode: "rate", SourceRole: "DIRECT_REFERRER", Value: 500, ArrangementLabel: "成交额外提成"},
		{Type: "referral", Mode: "rate", SourceRole: "DIRECT_REFERRER", Value: 200, ArrangementLabel: "基础推荐"},
		{Type: "referral", Mode: "rate", SourceRole: "DIRECT_REFERRER", Value: 300, ArrangementLabel: "额外推荐"},
	}}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, shanghai)
	paid := int64(10000)
	fact := sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "parallel", Kind: "ORDER_PAID", Version: "1",
		OrderID: "1", PaymentID: "1", RecordKey: "payment:1", ReferrerID: "10", OccurredAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Hour), AmountMinor: &paid}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, practiceRoster{Key: "PRACTICE:1:1:88", Hash: "parallel", SourceVersion: 1,
			StartsAt: at, ConfirmedAt: at.Add(time.Hour), Enrollments: []practiceEnrollment{{SourceEnrollmentID: "1", ParticipantID: "20",
				MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"1"}, SignedAt: at, ContentHash: "aa"}}}); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	draft, err := s.calculateCurrentDraft(ctx, class, 0)
	if err != nil || draft.LineCount != 4 || draft.Total != 2000 || draft.IssueCount != 0 {
		t.Fatalf("unexpected class draft: %+v %v", draft, err)
	}
	var details struct {
		Lines []struct{ ComponentLabel string }
	}
	var raw json.RawMessage
	if err := s.db.QueryRow(ctx, draftDetailSQL, class, 0, 0, draft.Version, 0, 0).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &details); err != nil || len(details.Lines) != 4 ||
		details.Lines[1].ComponentLabel != "成交额外提成" || details.Lines[3].ComponentLabel != "额外推荐" {
		t.Fatalf("wrong arrangement labels: %+v %v", details, err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, class, draft.Version); err != nil {
		t.Fatal(err)
	}
	refundAt := time.Date(2026, 9, 5, 9, 0, 0, 0, shanghai)
	half := int64(5000)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Domain: "PRACTICE", SourceKey: "parallel", Kind: "REFUND_SUCCEEDED",
			Version: "1", OrderID: "1", RefundID: "1", RecordKey: "refund:1", OccurredAt: refundAt, UpdatedAt: refundAt, AmountMinor: &half,
			RefundsPayment: []sourcePaymentReference{{OrderID: "1", RecordID: "1"}}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	refund, err := s.calculateCurrentDraft(ctx, month, 0)
	if err != nil || refund.IssueCount != 0 || refund.RefundCount != 4 || refund.Total != -1000 {
		t.Fatalf("expected four independent original-line debits: %+v %v", refund, err)
	}
	var debits []int64
	if err := s.db.QueryRow(ctx, `
	 SELECT array_agg(r.amount_minor ORDER BY l.id) FROM calculation_draft_refunds r
	 JOIN calculation_draft_lines l ON l.id=r.original_line_id WHERE r.period_id=$1 AND l.period_id=$2`, month, class).Scan(&debits); err != nil ||
		!slices.Equal(debits, []int64{-500, -250, -100, -150}) {
		t.Fatalf("refund debits do not match the original arrangements: %v %v", debits, err)
	}
}
