package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestComponentRuleImpactUsesSamePeriodRefundsAndLeavesDraftUntouched(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	mustID := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	day := 25
	product := mustID(s.createProduct(ctx, createProductInput{Code: "DIFF", Name: "对比样品", PeriodMode: "calendar", CycleStartDay: &day}))
	period := mustID(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "六月", SettlementMonth: "2026-06"}))
	mustID(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "VIP", SourceKey: "sample"}))
	person := mustID(s.createPerson(ctx, createPersonInput{Name: "销售样本"}))
	mustID(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCAI", ExternalID: "seller", ValidFrom: "2026-01-01T00:00:00+08:00"}))

	rule := publishRuleInput{ProductID: product, Label: "现行", Components: []component{
		{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "PID_USER", RefundPolicy: "proportional"},
	}}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	paidAt := time.Date(2026, 6, 5, 12, 0, 0, 0, shanghai)
	refundAt := paidAt.AddDate(0, 0, 1)
	gross, refund := int64(10000), int64(4000)
	facts := []sourceFact{
		{Source: "DCAI", Kind: "ORDER_PAID", Domain: "VIP", SourceKey: "sample", OrderID: "order-1",
			RecordKey: "orders:1", PaymentID: "paid-1", Version: "1", OccurredAt: paidAt, UpdatedAt: paidAt, AmountMinor: &gross,
			Participants: []sourceParticipant{{Role: "PID_USER", UserID: "seller"}}},
		{Source: "DCAI", Kind: "REFUND_SUCCEEDED", Domain: "VIP", SourceKey: "sample", OrderID: "order-1",
			RecordKey: "refunds:1", RefundID: "refund-1", Version: "1", OccurredAt: refundAt, UpdatedAt: refundAt, AmountMinor: &refund,
			RefundsPayment: []sourcePaymentReference{{OrderID: "order-1", RecordID: "1"}}},
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, facts)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	readDraft := func() draftSummary {
		t.Helper()
		value, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		return value.(draftSummary)
	}
	initial := readDraft()
	if initial.Total != 600 || initial.RefundTotal != -400 {
		t.Fatalf("same-period refund fixture: %+v", initial)
	}
	rule.Label, rule.Components[0].Value = "拟发布", 1500
	result, err := s.postComponentRuleImpact(ctx, componentRuleImpactInput{PeriodID: period, Rule: rule})
	if err != nil {
		t.Fatal(err)
	}
	report := result.(ruleImpactReport)
	want := []ruleImpactItem{
		{Key: 1, OrderID: "order-1", Recipient: "销售样本", EntryKind: "accrual", BeforeMinor: 1000, AfterMinor: 1500, DeltaMinor: 500},
		{Key: 2, OrderID: "order-1", Recipient: "销售样本", EntryKind: "refund", BeforeMinor: -400, AfterMinor: -600, DeltaMinor: -200},
	}
	if !report.CurrentRuleAvailable || report.BeforeMinor != 600 || report.AfterMinor != 900 || report.DeltaMinor != 300 ||
		report.BeforeIssueCount != 0 || report.AfterIssueCount != 0 || !reflect.DeepEqual(report.Items, want) {
		t.Fatalf("impact: %+v", report)
	}
	var version int64
	var rules, pending int
	if err := s.db.QueryRow(ctx, `SELECT d.version,(SELECT count(*) FROM rule_versions WHERE product_id=$2),
	 (SELECT count(*) FROM draft_refresh_queue WHERE period_id=$1) FROM calculation_drafts d WHERE d.period_id=$1`, period, product).
		Scan(&version, &rules, &pending); err != nil || version != initial.Version || rules != 1 || pending != 0 {
		t.Fatalf("preview left state: draft=%d rules=%d pending=%d %v", version, rules, pending, err)
	}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	published := readDraft()
	if published.Total != report.AfterMinor {
		t.Fatalf("published result differs from preview: %+v", published)
	}
	if _, err := s.db.Exec(ctx, "INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')", period, published.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.postComponentRuleImpact(ctx, componentRuleImpactInput{PeriodID: period, Rule: rule}); !errors.Is(err, errSettlementClosed) {
		t.Fatalf("settled preview: %v", err)
	}
}
