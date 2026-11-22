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
		{Type: "referral", Mode: "rate", Value: 1000, SourceRole: "PID_USER", RefundPolicy: "proportional"},
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
	if initial.Total != 1200 || initial.RefundTotal != -800 {
		t.Fatalf("same-period refund fixture: %+v", initial)
	}
	rule.Label, rule.Components[0].Value = "拟发布", 1500
	rule.Components[1].Value = 500
	result, err := s.postComponentRuleImpact(ctx, componentRuleImpactInput{PeriodID: period, Rule: rule})
	if err != nil {
		t.Fatal(err)
	}
	report := result.(ruleImpactReport)
	want := []ruleImpactItem{
		{Key: 1, OrderID: "order-1", Recipient: "销售样本", Arrangement: "推荐提成", EntryKind: "accrual", BeforeMinor: 1000, AfterMinor: 500, DeltaMinor: -500},
		{Key: 2, OrderID: "order-1", Recipient: "销售样本", Arrangement: "销售提成", EntryKind: "accrual", BeforeMinor: 1000, AfterMinor: 1500, DeltaMinor: 500},
		{Key: 3, OrderID: "order-1", Recipient: "销售样本", Arrangement: "推荐提成", EntryKind: "refund", BeforeMinor: -400, AfterMinor: -200, DeltaMinor: 200},
		{Key: 4, OrderID: "order-1", Recipient: "销售样本", Arrangement: "销售提成", EntryKind: "refund", BeforeMinor: -400, AfterMinor: -600, DeltaMinor: -200},
	}
	if !report.CurrentRuleAvailable || report.BeforeMinor != 1200 || report.AfterMinor != 1200 || report.DeltaMinor != 0 ||
		report.BeforeIssueCount != 0 || report.AfterIssueCount != 0 || !reflect.DeepEqual(report.Items, want) {
		t.Fatalf("impact: %+v", report)
	}
	matched, err := s.postComponentRuleImpact(ctx, componentRuleImpactInput{PeriodID: period, Rule: rule, MatchedOrders: true})
	if err != nil || !reflect.DeepEqual(matched.(ruleImpactReport).Items, want[:2]) {
		t.Fatalf("order preview must include proposed accruals only: %+v %v", matched, err)
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

func TestImpactArrangementsFollowNamedPracticeBenefitsAcrossVersionEdits(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	value, err := s.createProduct(ctx, createProductInput{Code: "IMPACT_ARRANGEMENT", Name: "试算收款安排", PeriodMode: "class"})
	if err != nil {
		t.Fatal(err)
	}
	product := value.(map[string]int64)["id"]
	benefit := func(name string) component {
		return component{Type: "delivery", Mode: "period_rate", Value: 10000, PeriodBenefit: &practicePeriodBenefit{Label: name, PersonID: 1}}
	}
	insert := func(version int, parts ...component) int64 {
		t.Helper()
		var id int64
		err := s.db.QueryRow(ctx, `INSERT INTO rule_versions(product_id,version,label,components) VALUES($1,$2,'试算样本',$3) RETURNING id`,
			product, version, parts).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	before := insert(1, benefit("可移除安排"), benefit("现场交付"), benefit("现场交付"), benefit("培训支持"))
	after := insert(2, benefit("培训支持"), benefit("现场交付"), benefit("现场交付"))
	lookup := func(rule int64, code string) impactComponentRef {
		return impactComponentRef{rule: rule, code: "period:delivery:" + code}
	}
	pairs := [][2]impactComponentRef{
		{lookup(before, "1"), lookup(after, "1")},
		{lookup(before, "2"), lookup(after, "2")},
		{lookup(before, "3"), lookup(after, "0")},
	}
	var postings []ruleImpactPosting
	for _, pair := range pairs {
		postings = append(postings, ruleImpactPosting{component: pair[0]}, ruleImpactPosting{component: pair[1]})
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		arrangements, err := impactArrangements(ctx, tx, postings)
		if err != nil {
			return err
		}
		for i, pair := range pairs {
			a, b := arrangements[pair[0]], arrangements[pair[1]]
			if a.key == "" || a.key != b.key || a.name != b.name {
				t.Fatalf("arrangement %d changed after reorder: %+v, %+v", i, a, b)
			}
		}
		if arrangements[pairs[0][0]].key == arrangements[pairs[1][0]].key {
			t.Fatal("two benefits with the same display name were merged")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRuleImpactShowsDifferentReasonsWithTheSameBlockingTotal(t *testing.T) {
	previous := []draftIssue{
		{Code: "RULE_MISSING"}, {Code: "MISSING_IDENTITY"},
		{Code: "PRACTICE_REFUND_AFTER_ROSTER", Advisory: true},
	}
	proposed := []draftIssue{
		{Code: "MISSING_IDENTITY"}, {Code: "PRACTICE_TIER_NOT_MATCHED"},
	}
	want := []ruleImpactIssueReason{
		{Code: "PRACTICE_TIER_NOT_MATCHED", AfterCount: 1},
		{Code: "MISSING_IDENTITY", BeforeCount: 1, AfterCount: 1},
		{Code: "RULE_MISSING", BeforeCount: 1},
	}
	if got := impactIssueReasons(previous, proposed); !reflect.DeepEqual(got, want) {
		t.Fatalf("issue reasons = %+v, want %+v", got, want)
	}
}
