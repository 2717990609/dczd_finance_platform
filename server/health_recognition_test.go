package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestHealthRecognitionPlanRejectsIneligibleJSON(t *testing.T) {
	db := integrationDB(t)
	for _, plan := range []string{`null`, `"ALL_OF"`, `false`, `[]`, `{"mode":"ALL_OF"}`} {
		raw := fmt.Sprintf(`{"schema_version":"1.2","recognition_plan":%s,"policy_scope":{"financial_event_types":["order.completed"]}}`, plan)
		var valid bool
		err := db.QueryRow(context.Background(), `SELECT coalesce((`+catalogHealthRecognitionPlan+`),false) FROM (SELECT $1::jsonb definition) v`, raw).Scan(&valid)
		if err != nil || valid {
			t.Fatalf("plan %s: accepted=%v error=%v", plan, valid, err)
		}
	}
}

func TestHealthRecognitionUsesPaymentRuleInTheCompletionWindow(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	at := time.Date(2026, 8, 25, 0, 0, 0, 0, shanghai)
	end := at.AddDate(0, 0, -1)
	day := 25
	product := productConfigEntry{
		SourceID: "ab010000-0000-4000-8000-000000000001", Code: "CREATOR", Name: "健康创客", Source: "DCAI", Project: "DCAI",
		SourceKey: "creator", PeriodMode: "calendar", CycleStartDay: &day,
		BusinessRoutes: []catalogBusinessRoute{{SourceID: "ab010000-0000-4000-8000-000000000002",
			Source: "DCAI", SourceProductCode: "HEALTH_CREATOR", Domain: "HEALTH_IDENTITY", Entity: "HEALTH_IDENTITY_ORDER",
			RecordTable: "ai_health_ambassador_order", Classifier: "dcai.health-identity.v1", BusinessKey: "dcai:health-creator:new",
			SKU: "creator-sku", Label: "首购", Version: 1, From: at.AddDate(0, -2, 0)}},
		Packages: []productRulePackageConfig{{SourceID: "ab010000-0000-4000-8000-000000000003", Code: "creator", Name: "规则", Project: "DCAI",
			Kind: "COMPONENT_RULE", ScopeType: "BUSINESS_SCENARIO", ScopeID: "creator-sku",
			Versions: []productRuleVersionConfig{
				{SourceID: "ab010000-0000-4000-8000-000000000004", Number: 1, Status: "PUBLISHED", From: at.AddDate(0, -2, 0),
					Through: &at, Hash: "before", Definition: json.RawMessage(completionTestPolicy)},
				{SourceID: "ab010000-0000-4000-8000-000000000005", Number: 2, Status: "PUBLISHED", From: at,
					Hash: "after", Definition: json.RawMessage(completionTestPolicy)},
			}}},
	}
	input := productConfigFile{Environment: "test", CapturedAt: at, Products: []productConfigEntry{product}}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, input) }); err != nil {
		t.Fatal(err)
	}
	var productID int64
	if err := s.db.QueryRow(ctx, "SELECT id FROM products WHERE code='CREATOR'").Scan(&productID); err != nil {
		t.Fatal(err)
	}
	create := func(month string) int64 {
		t.Helper()
		v, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: month, SettlementMonth: month})
		if err != nil {
			t.Fatal(err)
		}
		return v.(map[string]int64)["id"]
	}
	august, september := create("2026-08"), create("2026-09")
	save := func(observed, completed time.Time, refundMinor int64) {
		t.Helper()
		wire := healthCompletionReply("HEALTH_CREATOR", observed, completed, "42")
		wire.Facts[0] = json.RawMessage(strings.ReplaceAll(string(wire.Facts[0]), "2026-08-02T12:00:00+08:00", end.Add(12*time.Hour).Format(time.RFC3339)))
		if refundMinor > 0 {
			refunded := end.Add(14 * time.Hour).Format(time.RFC3339)
			wire.Facts = append(wire.Facts, json.RawMessage(fmt.Sprintf(`{"fact_schema_version":"source.order-fact/1.0","fact_type":"REFUND_SUCCEEDED","occurred_at":%q,
 "source_record":{"source_table":"ai_health_ambassador_order","record_id":"42","record_version":"refund-first","updated_at":%q},"order":{"source_order_id":"order-42"},
 "business":{"source_domain_code":"HEALTH_IDENTITY","source_entity_type_code":"HEALTH_IDENTITY_ORDER","source_product_code":"HEALTH_CREATOR"},
 "amounts":{"refund_minor":%q},"refund":{"source_refund_id":"42"},
 "relations":[{"relation":"REFUNDS_PAYMENT","target_source_order_id":"order-42","target_source_record_id":"42"}]}`,
				refunded, refunded, fmt.Sprint(refundMinor))))
		}
		row, err := normalizeDCAIOrderRow(dcaiHealthStream, time.Time{}, wire)
		if err != nil {
			t.Fatal(err)
		}
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := storeSourceFacts(ctx, tx, row.Facts); err != nil {
				return err
			}
			return storeDCAIBlockers(ctx, tx, dcaiHealthStream, []orderPageRow{row})
		}); err != nil {
			t.Fatal(err)
		}
	}
	match := func(period int64, status string, rule int, matched, paid int64) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			got, err := readCatalogMatches(ctx, tx, period, 0, 25)
			if err != nil {
				return err
			}
			if len(got.Items) != 1 || got.Items[0].Status != status ||
				got.Items[0].RuleVersion != rule || got.MatchedMinor != matched || got.PaidMinor != paid {
				t.Fatalf("period %d expected %s v%d and %d/%d, got %+v", period, status, rule, matched, paid, got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	const paid = 1680000
	save(at.Add(-time.Hour), time.Time{}, 0)
	match(august, "COMPOUND_COMPLETION_PENDING", 1, 0, paid)
	save(at, at, 0)
	match(august, "COMPOUND_COMPLETED_IN_OTHER_PERIOD", 1, 0, paid)
	match(september, "MATCHED", 1, paid, 0)
	save(at.Add(time.Hour), time.Time{}, 0)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		report, err := readCatalogMatches(ctx, tx, september, 0, 25)
		if err != nil {
			return err
		}
		if report.OrderCount != 0 {
			t.Fatalf("revoked completion stayed in the later window: %+v", report)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	save(at.Add(2*time.Hour), time.Time{}, paid/2)
	draft, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: august})
	if err != nil || draft.(draftSummary).IssueCount != 0 || draft.(draftSummary).Total != 0 {
		t.Fatalf("refund before eligibility should have no booked commission to reverse: %+v, %v", draft, err)
	}
	save(at.Add(3*time.Hour), at, paid/2)
	match(september, "MATCHED", 1, paid/2, 0)

	if _, err := s.db.Exec(ctx, `UPDATE product_rule_versions SET effective_from=$2 WHERE source_id=$1`,
		product.Packages[0].Versions[0].SourceID, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM draft_refresh_queue`); err != nil {
		t.Fatal(err)
	}
	save(at.Add(4*time.Hour), at.Add(time.Hour), paid/2)
	match(september, "RULE_MISSING", 0, 0, 0)
	var queued bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, september).Scan(&queued); err != nil || !queued {
		t.Fatalf("completion without a payment-time rule should still refresh its later period: %t, %v", queued, err)
	}
	draft, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: september})
	if err != nil || draft.(draftSummary).IssueCount != 1 || draft.(draftSummary).Total != 0 {
		t.Fatalf("a missing payment-time rule should become a blocking issue: %+v, %v", draft, err)
	}
	var creditCount int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM calculation_draft_performance WHERE period_id=$1`, september).Scan(&creditCount); err != nil || creditCount != 0 {
		t.Fatalf("unresolved recognition should not credit performance: %d, %v", creditCount, err)
	}
}
