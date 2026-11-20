package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCatalogRuleMatchingUsesOccurrenceMappingAndPriority(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	s := workspace{db: db, development: true}
	z := func(n int) string { return fmt.Sprintf("30000000-0000-4000-8000-%012d", n) }
	from := time.Date(2025, 7, 25, 0, 0, 0, 0, shanghai)
	boundary := from.AddDate(0, 0, 7)
	through := from.AddDate(0, 1, 0)
	day := 25
	policy := func(sku string, priority int, cycle string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"schema_version":"1.1","recognition_trigger":"PAID","amount_basis":"ACTUAL_RECEIVED","stacking_policy":"SUM",
		 "settlement_policy":{"cycle":%q},"rounding_policy":{"mode":"HALF_UP","scale":2},
		 "benefit_actions":[{"type":"RATE","beneficiary_mode":"PARTY","role":"CHAIRMAN","designated_party_id":"30000000-0000-4000-8000-000000000055",
		 "rate_bps":300,"item_key":"FIXED_A","label":"固定分项"}],
		 "policy_scope":{"business_sku_codes":[%q],"sales_systems":["DCAI"],"financial_event_types":["order.paid"],"priority":%d}}`, cycle, sku, priority))
	}
	pack := func(id, version, priority int, sku string, scope string, paused bool) productRulePackageConfig {
		return productRulePackageConfig{SourceID: z(id), Project: "TEST", Code: fmt.Sprint(id), Name: fmt.Sprintf("规则 %d", version), ScopeType: scope, ScopeID: sku,
			Kind: "COMPONENT_RULE", Paused: paused, Versions: []productRuleVersionConfig{{SourceID: z(id + 1), Number: version, Status: "PUBLISHED",
				From: from, Through: &through, Hash: z(id + 1), Definition: policy(sku, priority, "MONTHLY_25_TO_24")}}}
	}
	mapping := func(id, version int, scenario string, start time.Time, end *time.Time) catalogBusinessRoute {
		return catalogBusinessRoute{SourceID: z(id), Source: "DCAI", Domain: "VIP", Entity: "VIP_ORDER", RecordTable: "ai_vip_order",
			BusinessKey: "dcai:vip:new", Classifier: "dcai.vip-lifecycle.v1", SKU: scenario, Label: scenario,
			Version: version, From: start, Through: end}
	}
	input := productConfigFile{Environment: "test", CapturedAt: time.Now(), Products: []productConfigEntry{{
		SourceID: z(1), Code: "CATALOG_VIP", Name: "会员", Project: "TEST", Source: "DCAI", SourceKey: "dcai.vip", PeriodMode: "calendar", CycleStartDay: &day,
		BusinessRoutes: []catalogBusinessRoute{mapping(2, 1, "BEFORE", from, &boundary), mapping(3, 2, "AFTER", boundary, nil)},
		Packages: []productRulePackageConfig{
			pack(10, 11, 10, "BEFORE", "BUSINESS_SCENARIO", false),
			pack(12, 12, 15, "AFTER", "BUSINESS_SCENARIO", false),
			pack(14, 13, 16, "AFTER", "BUSINESS_SCENARIO", false),
			pack(16, 14, 20, "AFTER", "BUSINESS_SCENARIO", true),
			pack(18, 15, 30, "AFTER", "CROSS_SCENARIO_AGGREGATE", false),
		},
	}}}
	saveConfig := func() {
		t.Helper()
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, input) })
		if err != nil {
			t.Fatal(err)
		}
	}
	saveConfig()
	var productID int64
	if err := db.QueryRow(ctx, "SELECT id FROM products").Scan(&productID); err != nil {
		t.Fatal(err)
	}
	p, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: "August", SettlementMonth: "2025-08"})
	if err != nil {
		t.Fatal(err)
	}
	periodID := p.(map[string]int64)["id"]
	insert := func(order, entity, version, transactionType string, at time.Time, amount int64) {
		t.Helper()
		timestamp := at.Format(time.RFC3339)
		payload := fmt.Sprintf(`{"fact_schema_version":"source.order-fact/1.0","fact_type":"ORDER_PAID","occurred_at":%q,
		 "order":{"source_order_id":%q,"order_no":%q},"source_record":{"source_table":"ai_vip_order","record_id":%q,"record_version":%q,"updated_at":%q},
		 "business":{"source_domain_code":"VIP","source_entity_type_code":%q,"source_product_code":"VIP","source_transaction_type_code":%q},
		 "payment":{"source_payment_id":%q},"amounts":{"paid_minor":"%d"}}`, timestamp, order, order, order, version, timestamp, entity, transactionType, order, amount)
		fact, err := normalizeFact("DCAI", order, json.RawMessage(payload))
		if err != nil {
			t.Fatal(err)
		}
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	insert("before", "VIP_ORDER", "1", "NEW", from, 490)
	insert("after", "VIP_ORDER", "1", "RENEWAL", boundary, 580)
	insert("after", "VIP_ORDER", "2", "RENEWAL", boundary.Add(time.Minute), 650)
	insert("new", "VIP_ORDER", "1", "NEW", boundary.Add(20*time.Second), 510)
	insert("outside", "VIP_ORDER", "1", "RENEWAL", through, 120)
	insert("unknown", "", "1", "", boundary, 170)
	refund := int64(100)
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{
			Source: "DCAI", Domain: "VIP", SourceKey: "VIP", OrderID: "after", Version: "1",
			RecordKey: "ai_vip_order:after", Kind: "REFUND_SUCCEEDED", RefundID: "after-refund",
			OccurredAt: boundary.Add(time.Hour), UpdatedAt: boundary.Add(time.Hour), AmountMinor: &refund,
			RefundsPayment: []sourcePaymentReference{{OrderID: "after", RecordID: "after"}},
		}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	get := func(filter string) catalogMatchReport {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/period-rule-matches?periodId=%d%s", periodID, filter), nil)
		w := httptest.NewRecorder()
		for _, route := range s.routes() {
			if route.Method == req.Method && route.Path == req.URL.Path {
				route.Handler(w, req)
				var report catalogMatchReport
				if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != 200 {
					t.Fatalf("rule report HTTP %d: %s", w.Code, w.Body)
				}
				return report
			}
		}
		t.Fatal("missing catalog matching endpoint")
		return catalogMatchReport{}
	}
	generic, err := s.createProduct(ctx, createProductInput{Code: "SAMPLE", Name: "Generic", PeriodMode: "calendar", CycleStartDay: &day})
	if err != nil {
		t.Fatal(err)
	}
	genericID := generic.(map[string]int64)["id"]
	if _, err := s.createMapping(ctx, createMappingInput{ProductID: genericID, Source: "DCAI", Domain: "VIP", SourceKey: "VIP"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: genericID, Label: "Generic rule", Components: []component{{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "PID_USER"}}}); err != nil {
		t.Fatal(err)
	}
	genericPeriod, err := s.createPeriod(ctx, createPeriodInput{ProductID: genericID, Name: "Generic August", SettlementMonth: "2025-08"})
	if err != nil {
		t.Fatal(err)
	}
	genericPeriodID := genericPeriod.(map[string]int64)["id"]
	genericDraft, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: genericPeriodID})
	if err != nil {
		t.Fatal(err)
	}
	var performanceCount int
	if err = db.QueryRow(ctx, "SELECT count(*) FROM calculation_draft_performance WHERE period_id=$1", genericPeriodID).Scan(&performanceCount); err != nil {
		t.Fatal(err)
	}
	if genericDraft.(draftSummary).IssueCount != 0 || performanceCount != 0 {
		t.Fatalf("catalog payments leaked into generic draft: %+v, performance=%d", genericDraft, performanceCount)
	}

	report := get("")
	if report.OrderCount != 4 || report.MatchedCount != 3 || report.MatchedMinor != 1650 || report.PaidMinor != 1820 ||
		report.UnmarkedMembershipCount != 1 {
		t.Fatalf("unexpected summary: %+v", report)
	}
	byOrder := make(map[string]catalogPaymentMatch)
	for _, row := range report.Items {
		byOrder[row.OrderID] = row
	}
	if byOrder["before"].RuleVersion != 11 || byOrder["after"].RuleVersion != 13 || byOrder["after"].MappingVersion != 2 ||
		byOrder["after"].SourceTransactionType != "RENEWAL" || byOrder["unknown"].Status != "UNCLASSIFIED" {
		t.Fatalf("mismatched classification: %+v", byOrder)
	}
	fixed := byOrder["after"].FixedParts
	if len(fixed) != 1 || fixed[0].Label != "固定分项" || fixed[0].AmountMinor != 20 {
		t.Fatalf("incorrect fixed-part preview: %+v", fixed)
	}
	renewal := get("&memberTransactionType=RENEWAL")
	if renewal.OrderCount != 1 || renewal.MatchedCount != 1 || renewal.PaidMinor != 650 || renewal.MatchedMinor != 650 ||
		renewal.UnmarkedMembershipCount != 0 ||
		len(renewal.Items) != 1 || renewal.Items[0].OrderID != "after" {
		t.Fatalf("renewal filter must retain the real source type and recompute totals: %+v", renewal)
	}
	firstPurchases := get("&memberTransactionType=NEW")
	if firstPurchases.OrderCount != 2 || firstPurchases.PaidMinor != 1000 || firstPurchases.MatchedMinor != 1000 ||
		len(firstPurchases.Items) != 2 || firstPurchases.Items[0].OrderID != "new" ||
		firstPurchases.Items[0].RuleID != renewal.Items[0].RuleID || firstPurchases.Items[0].Scenario != renewal.Items[0].Scenario {
		t.Fatalf("new purchase and renewal in the same scope must share one rule: %+v", firstPurchases)
	}
	if secondPage := get("&memberTransactionType=RENEWAL&page=2"); secondPage.OrderCount != 1 || len(secondPage.Items) != 0 {
		t.Fatalf("filter must be applied before pagination: %+v", secondPage)
	}
	if unspecified := get("&memberTransactionType=UNKNOWN"); unspecified.OrderCount != 1 || unspecified.UnmarkedMembershipCount != 1 ||
		unspecified.MatchedCount != 0 || unspecified.PaidMinor != 170 || !unspecified.HasMembershipOrders ||
		len(unspecified.Items) != 1 || unspecified.Items[0].SourceDomain != "VIP" ||
		unspecified.Items[0].SourceTransactionType != "" {
		t.Fatalf("missing transaction type must stay visible without being inferred: %+v", unspecified)
	}
	input.Products[0].Packages = append(input.Products[0].Packages, pack(20, 16, 16, "AFTER", "BUSINESS_SCENARIO", false))
	input.CapturedAt = input.CapturedAt.Add(time.Second)
	saveConfig()
	if got := get(""); got.MatchedCount != 1 || got.Items[0].Status != "RULE_CONFLICT" {
		t.Fatalf("tied rule must surface conflict: %+v", got)
	}
	insert("pre-cutoff", "VIP_ORDER", "1", "NEW", boundary.Add(time.Second), 200)
	if _, err := s.retireRule(ctx, retireRuleInput{ProductID: productID, CatalogVersionID: z(21),
		EffectiveFrom: boundary.Add(time.Second * 10)}); err != nil {
		t.Fatal(err)
	}
	stopped := make(map[string]catalogPaymentMatch)
	for _, row := range get("").Items {
		stopped[row.OrderID] = row
	}
	if stopped["pre-cutoff"].Status != "RULE_CONFLICT" || stopped["after"].Status != "MATCHED" || stopped["after"].RuleVersion != 13 {
		t.Fatalf("catalog retirement did not respect payment times: before=%+v after=%+v", stopped["pre-cutoff"], stopped["after"])
	}
	input.Products[0].BusinessRoutes = nil
	input.CapturedAt = input.CapturedAt.Add(time.Second)
	saveConfig()
	unchanged, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: genericPeriodID})
	if err != nil || unchanged.(draftSummary).Version != genericDraft.(draftSummary).Version {
		t.Fatalf("withdrawn business routes leaked into the generic draft: %+v, %v", unchanged, err)
	}
}
