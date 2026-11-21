package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeReusableRuleUsesConfirmedOpeningType(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_OPENING_TYPES", Name: "开班方式验收", PeriodMode: "class"}))
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "缺少订单角色",
		Components: []component{{Type: "sales", Mode: "rate", ClassMember: "new", Value: 100}}}); !errors.Is(err, errRuleOrderRole) {
		t.Fatalf("publishing a class sales item without an order role: %v", err)
	}
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "activity-rules"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "样本销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-08-01T00:00:00Z"}))
	offline := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "线下班", Ordinal: 1, StartsOn: "2026-08-08", EndsOn: "2026-08-08", SourcePeriodKey: "PRACTICE:1:1:8"}))
	online := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "线上班", Ordinal: 2, StartsOn: "2026-08-09", EndsOn: "2026-08-09", SourcePeriodKey: "PRACTICE:2:1:9"}))
	publish := func(extra string, amount int64) int {
		t.Helper()
		var input publishRuleInput
		body := fmt.Sprintf(`{"productId":%d,"label":"复训标准",%s"components":[{"type":"sales","mode":"fixed","classMember":"retrain","sourceRole":"DIRECT_REFERRER","refundPolicy":"proportional","value":%d}]}`, product, extra, amount)
		if err := json.Unmarshal([]byte(body), &input); err != nil {
			t.Fatal(err)
		}
		result, err := s.publishRule(ctx, input)
		if err != nil {
			t.Fatalf("publishing %s: %v", body, err)
		}
		return result.(map[string]int)["version"]
	}
	offlineVersion := publish(`"activityType":1,`, 6000)
	onlineVersion := publish(`"activityType":2,`, 3000)
	defaultVersion := publish("", 4000)
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, shanghai)
	paid := int64(20000)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for n, key := range []string{"PRACTICE:1:1:8", "PRACTICE:2:1:9"} {
			order := fmt.Sprint(n + 101)
			start := at.AddDate(0, 0, n)
			roster := practiceRoster{Key: key, SourceVersion: 1, Hash: key, StartsAt: start, ConfirmedAt: start.Add(time.Hour),
				Enrollments: []practiceEnrollment{{SourceEnrollmentID: order, ParticipantID: "1", MembershipType: 2,
					CommissionEligible: true, SignedAt: start, PaidOrderIDs: []string{order}, ContentHash: fmt.Sprintf("%064x", n+1)}}}
			if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
				return err
			}
			if _, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE",
				SourceKey: "activity-rules", TransactionType: "NEW", OrderID: order, PaymentID: order, RecordKey: "paid:" + order,
				Version: "1", ReferrerID: "seller", OccurredAt: start.Add(-time.Hour), UpdatedAt: start, AmountMinor: &paid}}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check := func(period int64, version int, amount int64) int64 {
		t.Helper()
		result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		got := result.(draftSummary)
		if got.RuleVersion != version || got.Total != amount || got.IssueCount != 0 {
			t.Fatalf("period %d: got version %d, amount %d, %d issues; want version %d, amount %d", period, got.RuleVersion, got.Total, got.IssueCount, version, amount)
		}
		return got.RuleID
	}
	check(offline, offlineVersion, 6000)
	onlineRuleID := check(online, onlineVersion, 3000)
	candidate := publishRuleInput{ProductID: product, Label: "拟发布线下规则", ActivityType: 1, EffectiveFrom: &at,
		Components: []component{{Type: "sales", Mode: "fixed", Value: 6500, ClassMember: "retrain", SourceRole: "DIRECT_REFERRER"}}}
	var ruleCount int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM rule_versions WHERE product_id=$1", product).Scan(&ruleCount); err != nil {
		t.Fatal(err)
	}
	result, err := s.postComponentRuleImpact(ctx, componentRuleImpactInput{PeriodID: offline, Rule: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if report := result.(ruleImpactReport); report.BeforeMinor != 6000 || report.AfterMinor != 6500 || report.DeltaMinor != 500 {
		t.Fatalf("the rule must apply to the class that started at the exact cutoff: %+v", report)
	}
	sameAmount := candidate
	sameAmount.Components = []component{{Type: "sales", Mode: "fixed", Value: 6000, ClassMember: "retrain", SourceRole: "DIRECT_REFERRER"}}
	result, err = s.postComponentRuleImpact(ctx, componentRuleImpactInput{PeriodID: offline, Rule: sameAmount})
	if err != nil {
		t.Fatal(err)
	}
	if report := result.(ruleImpactReport); report.BeforeMinor != 6000 || report.AfterMinor != 6000 || len(report.Items) != 0 {
		t.Fatalf("a matched rule may legitimately have no amount changes: %+v", report)
	}
	oneMinuteLater := at.Add(time.Minute)
	for _, check := range []componentRuleImpactInput{
		{PeriodID: online, Rule: candidate},
		{PeriodID: offline, Rule: publishRuleInput{ProductID: product, Label: "迟于开班", ActivityType: 1,
			EffectiveFrom: &oneMinuteLater, Components: candidate.Components}},
	} {
		if result, err := s.postComponentRuleImpact(ctx, check); !errors.Is(err, errPracticeRuleImpactNoMatch) {
			t.Errorf("preview must identify a class rule mismatch for period %d: %+v %v", check.PeriodID, result, err)
		}
	}
	var afterPreview int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM rule_versions WHERE product_id=$1", product).Scan(&afterPreview); err != nil || afterPreview != ruleCount {
		t.Fatalf("successful and rejected previews must both leave published rules unchanged: %d -> %d: %v", ruleCount, afterPreview, err)
	}
	onlineEdit := publish(fmt.Sprintf(`"editBaseId":%d,`, onlineRuleID), 3500)
	revisedOnlineRuleID := check(online, onlineEdit, 3500)
	check(offline, offlineVersion, 6000)
	if _, err := s.retireRule(ctx, retireRuleInput{ProductID: product, RuleID: revisedOnlineRuleID, EffectiveFrom: at}); err != nil {
		t.Fatal(err)
	}
	check(offline, offlineVersion, 6000)
	check(online, defaultVersion, 4000)
	exactVersion := publish(fmt.Sprintf(`"periodId":%d,`, offline), 8000)
	originalRuleID := check(offline, exactVersion, 8000)
	if result, err := s.postComponentRuleImpact(ctx, componentRuleImpactInput{PeriodID: offline, Rule: candidate}); !errors.Is(err, errPracticeRuleImpactNoMatch) {
		t.Errorf("preview must identify the overridden candidate: %+v %v", result, err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by)
	 SELECT period_id,version,'fixture' FROM calculation_drafts WHERE period_id=$1`, offline); err != nil {
		t.Fatal(err)
	}
	if _, err := s.postComponentRuleImpact(ctx, componentRuleImpactInput{PeriodID: offline, Rule: candidate}); !errors.Is(err, errSettlementClosed) {
		t.Fatalf("settled class must not be offered a prospective preview: %v", err)
	}
	publish(fmt.Sprintf(`"periodId":%d,`, offline), 12000)
	refundPeriod := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月退款", SettlementMonth: "2026-09"}))
	refundedAt := at.AddDate(0, 1, 0)
	half := int64(10000)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE",
			SourceKey: "activity-rules", OrderID: "101", RefundID: "1", RecordKey: "refund:1", Version: "1",
			AmountMinor: &half, OccurredAt: refundedAt, UpdatedAt: refundedAt,
			RefundsPayment: []sourcePaymentReference{{OrderID: "101", RecordID: "101"}}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	refund, err := s.calculateCurrentDraft(ctx, refundPeriod, 0)
	if err != nil || refund.RefundTotal != -4000 || refund.IssueCount != 0 {
		t.Fatalf("refund must prorate the original 8000 after the rule changes: %+v %v", refund, err)
	}
	var refundedRuleID int64
	if err := s.db.QueryRow(ctx, `SELECT l.rule_id FROM calculation_draft_refunds r
	 JOIN calculation_draft_lines l ON l.id=r.original_line_id WHERE r.period_id=$1`, refundPeriod).
		Scan(&refundedRuleID); err != nil || refundedRuleID != originalRuleID {
		t.Fatalf("expected refund to reference original rule %d, got %d %v", originalRuleID, refundedRuleID, err)
	}
}
