package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const completionTestPolicy = `{"schema_version":"1.2","recognition_trigger":"PAID",
 "recognition_plan":{"mode":"ALL_OF","contract_version":"finance.compound-recognition/1.0","required_event_types":["ORDER_PAID","ORDER_COMPLETED"]},
 "settlement_policy":{"cycle":"MONTHLY_25_TO_24"},"policy_scope":{"sales_systems":["DCAI"],"financial_event_types":["order.paid","order.completed"],"business_sku_codes":["creator-sku"]}}`

func healthCompletionReply(product string, observed, completed time.Time, target string) orderWireRow {
	fact := func(kind, table, recordID string, at time.Time, extras string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"fact_schema_version":"source.order-fact/1.0","fact_type":%q,"occurred_at":%q,
 "source_record":{"source_table":%q,"record_id":%q,"record_version":%q,"updated_at":%q},"order":{"source_order_id":"order-42"},
 "business":{"source_domain_code":"HEALTH_IDENTITY","source_entity_type_code":"HEALTH_IDENTITY_ORDER","source_product_code":%q},%s}`,
			kind, at.Format(time.RFC3339), table, recordID, at.Format(time.RFC3339), at.Format(time.RFC3339), product, extras))
	}
	paidAt := time.Date(2026, 8, 2, 12, 0, 0, 0, shanghai)
	response := orderWireRow{
		Cursor: orderCursor{ID: "42", UpdatedAt: observed},
		Facts: []json.RawMessage{fact("ORDER_PAID", "ai_health_ambassador_order", "42", paidAt,
			`"payment":{"source_payment_id":"42"},"amounts":{"paid_minor":"1680000"}`)},
		Gates: []dcaiGate{{Code: "CONTRACT_PREREQUISITE", OrderID: "order-42", EffectiveAt: observed}},
	}
	if completed.IsZero() {
		incomplete := "CONTRACT_INCOMPLETE"
		response.Gates[0].Status, response.Gates[0].Reason = "BLOCKED", &incomplete
	} else {
		response.Gates[0].Status = "PASSED"
		response.Facts = append(response.Facts, fact("ORDER_COMPLETED", "ai_health_ambassador_apply", "3", completed,
			fmt.Sprintf(`"relations":[{"relation":"COMPLETES_ORDER","target_source_order_id":"order-42","target_source_record_id":%q}]`, target)))
	}
	return response
}

func TestDCAIHealthCompletionRequiresItsOwnPayment(t *testing.T) {
	at := time.Date(2026, 8, 25, 0, 0, 0, 0, shanghai)
	wrong := healthCompletionReply("HEALTH_CREATOR", at, at, "99")
	if _, err := normalizeDCAIOrderRow(dcaiHealthStream, at, wrong); err == nil {
		t.Error("completion was accepted for a different original payment")
	}
	missing := healthCompletionReply("HEALTH_CREATOR", at, at, "42")
	missing.Facts = missing.Facts[:1]
	if _, err := normalizeDCAIOrderRow(dcaiHealthStream, at, missing); err == nil {
		t.Error("a passed contract gate was accepted without a completion fact")
	}
	earlier := healthCompletionReply("HEALTH_CREATOR", at, at.AddDate(0, -1, 0), "42")
	if _, err := normalizeDCAIOrderRow(dcaiHealthStream, at, earlier); err == nil {
		t.Error("a completion before its original payment was accepted")
	}
}

func TestHealthLookupPreservesAValidEmptyObservation(t *testing.T) {
	observed := time.Date(2026, 8, 25, 0, 0, 0, 0, shanghai)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"contract_version": "dcai.finance-order-lookup/1.0", "source_fact_schema_version": sourceSchema,
			"stream": dcaiHealthStream, "source_order_id": "order-42", "item": orderWireRow{
				Cursor: orderCursor{ID: "42", UpdatedAt: observed}, Facts: []json.RawMessage{}, Gates: []dcaiGate{},
			},
		})
	}))
	defer server.Close()
	result, err := lookupOrder(context.Background(), sourceConfig{BaseURL: server.URL, Token: "fixture"},
		pullInput{Source: "DCAI", Stream: dcaiHealthStream, OrderID: "order-42"})
	if err != nil || len(result.Facts) != 0 || result.DCAI == nil || result.DCAI.OrderID != "order-42" {
		t.Fatalf("valid empty observation was lost: %+v, %v", result, err)
	}
}

func TestCurrentHealthCompletionQueuesOnlyAffectedOpenPeriods(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, shanghai)
	day := 25
	creator := productConfigEntry{
		SourceID: "eedc0000-0000-4000-8000-000000000001", Code: "CREATOR", Name: "创客", Project: "DCAI", Source: "DCAI",
		SourceKey: "dcai.creator", PeriodMode: "calendar", CycleStartDay: &day,
		Packages: []productRulePackageConfig{{SourceID: "eedc0000-0000-4000-8000-000000000002", Code: "creator", Name: "创客确认",
			Project: "DCAI", Kind: "COMPONENT_RULE", ScopeType: "BUSINESS_SCENARIO", ScopeID: "creator-sku",
			Versions: []productRuleVersionConfig{{SourceID: "eedc0000-0000-4000-8000-000000000003", Number: 1, Status: "PUBLISHED",
				From: from, Hash: "fixture", Definition: json.RawMessage(completionTestPolicy)}}}},
		BusinessRoutes: []catalogBusinessRoute{{SourceID: "eedc0000-0000-4000-8000-000000000004", Source: "DCAI", Domain: "HEALTH_IDENTITY",
			SourceProductCode: "HEALTH_CREATOR", Entity: "HEALTH_IDENTITY_ORDER", RecordTable: "ai_health_ambassador_order",
			BusinessKey: "dcai:health-creator:new", Classifier: "dcai.health-identity.v1", SKU: "creator-sku", Label: "创客首购", Version: 1, From: from}},
	}
	creator.Packages[0].Versions = append(creator.Packages[0].Versions, productRuleVersionConfig{
		SourceID: "eedc0000-0000-4000-8000-000000000005", Number: 2, Status: "PUBLISHED", Hash: "paid-only",
		From: time.Date(2026, 8, 25, 0, 0, 0, 0, shanghai), Definition: json.RawMessage(`{"schema_version":"1.2","recognition_trigger":"PAID",
 "settlement_policy":{"cycle":"MONTHLY_25_TO_24"},"policy_scope":{"priority":10,"sales_systems":["DCAI"],"financial_event_types":["order.paid"],"business_sku_codes":["creator-sku"]}}`),
	})
	input := productConfigFile{Environment: "test", CapturedAt: from, Products: []productConfigEntry{creator}}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, input) }); err != nil {
		t.Fatal(err)
	}
	var productID int64
	if err := s.db.QueryRow(ctx, `SELECT id FROM products WHERE code='CREATOR'`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	create := func(productID int64, month string) int64 {
		t.Helper()
		value, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: month, SettlementMonth: month})
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	august, september, october := create(productID, "2026-08"), create(productID, "2026-09"), create(productID, "2026-10")
	other, err := s.createProduct(ctx, createProductInput{Code: "OTHER", Name: "其他产品", PeriodMode: "calendar", CycleStartDay: &day})
	if err != nil {
		t.Fatal(err)
	}
	create(other.(map[string]int64)["id"], "2026-09")
	clearQueue := func() {
		t.Helper()
		if _, err := s.db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
			t.Fatal(err)
		}
	}
	wantQueue := func(expected ...int64) {
		t.Helper()
		var got []int64
		if err := s.db.QueryRow(ctx, "SELECT coalesce(array_agg(period_id ORDER BY period_id),'{}') FROM draft_refresh_queue").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, expected) {
			t.Fatalf("queued periods %v, want %v", got, expected)
		}
		clearQueue()
	}
	write := func(wire orderWireRow) error {
		row, err := normalizeDCAIOrderRow(dcaiHealthStream, time.Time{}, wire)
		if err != nil {
			return err
		}
		return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := storeSourceFacts(ctx, tx, row.Facts); err != nil {
				return err
			}
			return storeDCAIBlockers(ctx, tx, dcaiHealthStream, []orderPageRow{row})
		})
	}
	pull := func(wire orderWireRow) {
		t.Helper()
		if err := write(wire); err != nil {
			t.Fatal(err)
		}
	}
	activeAt := func(want time.Time) {
		t.Helper()
		var active *time.Time
		err := s.db.QueryRow(ctx, `SELECT v.occurred_at FROM dcai_order_blockers b
		 LEFT JOIN source_order_versions v ON v.id=b.completion_version_id WHERE b.stream=$1 AND b.source_row_id='42'`, dcaiHealthStream).Scan(&active)
		if err != nil || active == nil && !want.IsZero() || active != nil && !active.Equal(want) {
			t.Fatalf("active completion %v, want %v, error %v", active, want, err)
		}
	}
	clearQueue()
	first := time.Date(2026, 8, 25, 0, 0, 0, 0, shanghai)
	second := first.AddDate(0, 1, 0)
	order := func(observed, complete time.Time) orderWireRow {
		return healthCompletionReply("HEALTH_CREATOR", observed, complete, "42")
	}

	empty := order(first.Add(-time.Hour), time.Time{})
	empty.Facts, empty.Gates = []json.RawMessage{}, []dcaiGate{}
	pull(empty)
	wantQueue()
	staleAt := first.Add(-2 * time.Hour)
	pull(order(staleAt, staleAt))
	wantQueue(august)
	activeAt(time.Time{})
	pull(order(first, first))
	wantQueue(august, september)
	activeAt(first)
	pull(order(first, first))
	wantQueue()
	if err := write(order(first, time.Time{})); err == nil {
		t.Fatal("conflicting observation at the same source revision was accepted")
	}
	activeAt(first)
	if _, err := s.db.Exec(ctx, `UPDATE dcai_order_blockers SET completion_version_id=NULL,completion_observed=false`); err != nil {
		t.Fatal(err)
	}
	pull(order(first, first))
	wantQueue(august, september)
	activeAt(first)

	pull(order(second, second))
	wantQueue(august, september, october)
	activeAt(second)
	pull(order(first, first))
	wantQueue()
	activeAt(second)
	paidEdit := order(second.Add(time.Minute), second)
	paidEdit.Facts[0] = json.RawMessage(strings.NewReplacer(
		`"record_version":"2026-08-02T12:00:00+08:00"`, `"record_version":"corrected-payment"`,
		`"updated_at":"2026-08-02T12:00:00+08:00"`, `"updated_at":"2026-09-25T00:01:00+08:00"`,
		`"paid_minor":"1680000"`, `"paid_minor":"1600000"`,
	).Replace(string(paidEdit.Facts[0])))
	pull(paidEdit)
	wantQueue(august, october)
	activeAt(second)

	if _, err := s.db.Exec(ctx, `WITH draft AS (
	 INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	 VALUES($1,1,'\x01'::bytea,0,0,0,now()) RETURNING period_id,version
	) INSERT INTO settlements(period_id,draft_version,confirmed_by) SELECT period_id,version,'fixture' FROM draft`, august); err != nil {
		t.Fatal(err)
	}
	pull(order(second.Add(time.Hour), time.Time{}))
	wantQueue(october)
	activeAt(time.Time{})
	pull(order(second, second))
	wantQueue()
	activeAt(time.Time{})
	newer := second.Add(2 * time.Hour)
	pull(order(newer, second))
	wantQueue(october)
	empty = order(newer.Add(time.Minute), time.Time{})
	empty.Facts, empty.Gates = []json.RawMessage{}, []dcaiGate{}
	pull(empty)
	wantQueue(october)
	activeAt(time.Time{})

	// If the corrected payment changes the winning rule, also refresh the
	// period that previously included the completion under the prior rule.
	changeAt := newer.Add(time.Hour)
	pull(order(changeAt, second))
	wantQueue(october)
	revised := order(changeAt.Add(time.Minute), second)
	revised.Facts[0] = json.RawMessage(strings.NewReplacer(
		`"occurred_at":"2026-08-02T12:00:00+08:00"`, `"occurred_at":"2026-08-26T12:00:00+08:00"`,
		`"record_version":"2026-08-02T12:00:00+08:00"`, `"record_version":"corrected-payment-window"`,
		`"updated_at":"2026-08-02T12:00:00+08:00"`, `"updated_at":"2026-09-25T03:01:00+08:00"`,
	).Replace(string(revised.Facts[0])))
	pull(revised)
	wantQueue(september, october)

	withNewCompletion := order(changeAt.AddDate(0, 0, 1), second.AddDate(0, 0, 1))
	withNewCompletion.Facts[0] = revised.Facts[0]
	pull(withNewCompletion)
	wantQueue()
}
