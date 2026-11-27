package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestHealthCatalogSeparatesProductsInTheSharedOrderStream(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	raw := `{"environment":"test","capturedAt":"2026-08-01T00:00:00Z","products":[%s,%s]}`
	product := func(id int, code, slug string) string {
		recognition := `"recognition_plan":null,`
		if code == "HEALTH_CREATOR" {
			recognition = `"recognition_plan":{"mode":"ALL_OF","contract_version":"finance.compound-recognition/1.0","required_event_types":["ORDER_COMPLETED","ORDER_PAID"]},`
		}
		return fmt.Sprintf(`{"sourceId":"10800000-0000-4000-8000-%012d","code":%q,"name":%q,"project":"DCAI","source":"DCAI",
 "sourceKey":%q,"periodMode":"calendar","cycleStartDay":25,"businessRoutes":[
 {"sourceId":"10800000-0000-4000-8000-%012d","source":"DCAI","domain":"HEALTH_IDENTITY","sourceProductCode":%q,
 "entity":"HEALTH_IDENTITY_ORDER","recordTable":"ai_health_ambassador_order","businessKey":%q,"classifier":"dcai.health-identity.v1",
 "sku":%q,"label":%q,"version":1,"from":"2026-06-01T00:00:00Z"}],"packages":[
 {"sourceId":"10800000-0000-4000-8000-%012d","code":%q,"name":%q,"project":"DCAI","kind":"COMPONENT_RULE",
 "scopeType":"BUSINESS_SCENARIO","scopeId":%q,"versions":[{"sourceId":"10800000-0000-4000-8000-%012d","version":1,"status":"PUBLISHED",
 "from":"2026-06-01T00:00:00Z","hash":"fixture","definition":{%s"schema_version":"1.1","recognition_trigger":"PAID",
 "settlement_policy":{"cycle":"MONTHLY_25_TO_24"},"policy_scope":{"business_sku_codes":[%q],"sales_systems":["DCAI"],"financial_event_types":["order.paid"]}}}]}]}`,
			id, "FIXTURE_DCAI_"+code, code, "dcai."+slug, id+1, code, "dcai:"+slug+":new", code, code, id+2, code, code, code, id+3, recognition, code)
	}
	var input productConfigFile
	if err := json.Unmarshal([]byte(fmt.Sprintf(raw, product(10, "HEALTH_AMBASSADOR", "health-ambassador"), product(20, "HEALTH_CREATOR", "health-creator"))), &input); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, input) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE catalog_business_routes SET source_product_code=NULL"); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, p := range input.Products {
			var id int64
			if err := tx.QueryRow(ctx, "SELECT id FROM products WHERE code=$1", p.Code).Scan(&id); err != nil {
				return err
			}
			if err := storeCatalogRoute(ctx, tx, id, input.CapturedAt, p.BusinessRoutes[0]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("fill missing source product codes in existing routes: %v", err)
	}
	periods := map[string]int64{}
	for _, p := range input.Products {
		var id int64
		if err := s.db.QueryRow(ctx, "SELECT id FROM products WHERE code=$1", p.Code).Scan(&id); err != nil {
			t.Fatal(err)
		}
		result, err := s.createPeriod(ctx, createPeriodInput{ProductID: id, Name: "八月", SettlementMonth: "2026-08"})
		if err != nil {
			t.Fatal(err)
		}
		periods[p.Code] = result.(map[string]int64)["id"]
	}
	wire := func(order, code, kind, amounts string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"fact_schema_version":"source.order-fact/1.0","fact_type":%q,"occurred_at":"2026-08-02T00:00:00Z",
 "order":{"source_order_id":%q},"source_record":{"source_table":"ai_health_ambassador_order","record_id":%q,"record_version":"1","updated_at":"2026-08-02T00:00:00Z"},
 "business":{"source_domain_code":"HEALTH_IDENTITY","source_entity_type_code":"HEALTH_IDENTITY_ORDER","source_product_code":%q},
 "payment":{"source_payment_id":%q},"refund":{"source_refund_id":%q},"amounts":%s,
 "relations":[{"relation":"REFUNDS_PAYMENT","target_source_order_id":%q,"target_source_record_id":%q}]}`,
			kind, order, order, code, order, order+"-refund", amounts, order, order))
	}
	if _, err := normalizeDCAIOrderRow(dcaiHealthStream, input.CapturedAt.AddDate(0, 1, 0), orderWireRow{Facts: []json.RawMessage{
		wire("ambassador", "HEALTH_AMBASSADOR", "ORDER_PAID", `{"paid_minor":"3980000"}`),
		wire("ambassador", "HEALTH_CREATOR", "REFUND_SUCCEEDED", `{"refund_minor":"10000"}`),
	}, Gates: []dcaiGate{}}); err == nil {
		t.Error("a health refund with a different product code from its payment was admitted")
	}
	fact := func(order, code, kind, amounts string) sourceFact {
		t.Helper()
		f, err := normalizeFact("DCAI", order, wire(order, code, kind, amounts))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	store := func(facts ...sourceFact) int64 {
		t.Helper()
		var inserted int64
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var err error
			inserted, err = storeSourceFacts(ctx, tx, facts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return inserted
	}
	ambassador := fact("ambassador", "HEALTH_AMBASSADOR", "ORDER_PAID", `{"paid_minor":"3980000"}`)
	previous := ambassador
	previous.Classification = sourceClassification{}
	store(previous)
	if _, err := s.db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
		t.Fatal(err)
	}
	if store(ambassador) != 0 {
		t.Fatal("a reread should classify the stored payment without adding another version")
	}
	var queued []int64
	if err := s.db.QueryRow(ctx, "SELECT array_agg(period_id ORDER BY period_id) FROM draft_refresh_queue").Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0] != periods["FIXTURE_DCAI_HEALTH_AMBASSADOR"] {
		t.Fatalf("the reread enqueued the wrong periods: %v", queued)
	}
	store(
		fact("creator", "HEALTH_CREATOR", "ORDER_PAID", `{"paid_minor":"1680000"}`),
		fact("unknown", "HEALTH_UNKNOWN", "ORDER_PAID", `{"paid_minor":"1000"}`),
		fact("ambassador", "HEALTH_AMBASSADOR", "REFUND_SUCCEEDED", `{"refund_minor":"10000"}`),
	)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for code, expected := range map[string]string{"FIXTURE_DCAI_HEALTH_AMBASSADOR": "ambassador", "FIXTURE_DCAI_HEALTH_CREATOR": "creator"} {
			period := periods[code]
			report, err := readCatalogMatches(ctx, tx, period, 0, 25)
			if err != nil {
				return err
			}
			if report.OrderCount != 1 || len(report.Items) != 1 || report.Items[0].OrderID != expected || report.Items[0].RuleVersion != 1 {
				t.Fatalf("%s selected orders outside its catalog scenario: %+v", code, report)
			}
			status := "MATCHED"
			if expected == "creator" {
				status = "COMPOUND_RECOGNITION_UNSUPPORTED"
			}
			if report.Items[0].Status != status {
				t.Fatalf("%s payment status %s, want %s", code, report.Items[0].Status, status)
			}
			refunds, err := readRefundEvents(ctx, tx, period, catalogRefundInputsSQL)
			if err != nil {
				return err
			}
			if len(refunds) == 0 && expected == "ambassador" || len(refunds) != 0 && expected == "creator" {
				t.Fatalf("%s selected refunds outside its source product: %+v", code, refunds)
			}
		}
		ambassadorPeriod := periods["FIXTURE_DCAI_HEALTH_AMBASSADOR"]
		if _, err := tx.Exec(ctx, `INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
		 VALUES($1,1,'\x01'::bytea,0,0,0,now())`, ambassadorPeriod); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO calculation_draft_performance(period_id,source_version_id)
		 SELECT $1,id FROM source_order_versions WHERE order_id='ambassador' AND kind='ORDER_PAID'`, ambassadorPeriod); err != nil {
			return err
		}
		if err := storeDCAIBlockers(ctx, tx, dcaiHealthStream, []orderPageRow{{
			Cursor: orderCursor{ID: "1", UpdatedAt: input.CapturedAt}, OrderID: "ambassador", Blockers: []string{"REFUND_EVIDENCE_MISSING"},
		}}); err != nil {
			return err
		}
		for period, gaps := range map[int64]int{ambassadorPeriod: 1, periods["FIXTURE_DCAI_HEALTH_CREATOR"]: 0} {
			streams, err := s.settlementSources(ctx, tx, period, settlementReview{})
			if err != nil {
				return err
			}
			if len(streams) != 1 || streams[0].Stream != dcaiHealthStream || streams[0].RefundGap != gaps {
				t.Fatalf("period %d expected %d health refund blockers, got %+v", period, gaps, streams)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnimplementedCompoundRecognitionDoesNotCreditPerformance(t *testing.T) {
	amount := int64(1000)
	row := catalogPaymentMatch{SourceVersionID: 1, Status: "COMPOUND_RECOGNITION_UNSUPPORTED", PaidMinor: &amount}
	var result paymentDraft
	if err := addVIPDraftPayment(&result, row, vipRuntimeData{}, reportAttribution{}); err != nil {
		t.Fatal(err)
	}
	if len(result.Performance) != 0 || len(result.Lines) != 0 || len(result.Issues) != 1 || result.Issues[0].Code != row.Status {
		t.Fatalf("an unsupported rule must only produce a pending issue: %+v", result)
	}
}
