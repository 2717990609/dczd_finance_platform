package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestVIPDraftReusesVersionAndReconcilesBeneficiaries(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(n int) string { return fmt.Sprintf("60000000-0000-4000-8000-%012d", n) }
	start := time.Date(2026, 7, 25, 0, 0, 0, 0, shanghai)
	boundary := start.AddDate(0, 0, 5)
	policy := func(ownerRate string) json.RawMessage {
		return json.RawMessage(strings.NewReplacer(
			`"amount_basis"`, `"schema_version":"1.1","recognition_trigger":"PAID","settlement_policy":{"cycle":"MONTHLY_25_TO_24"},"policy_scope":{"business_sku_codes":["test"],"sales_systems":["DCAI"],"financial_event_types":["order.paid"]},"amount_basis"`,
			`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`, fmt.Sprintf(`"item_key":"sales","label":"销售","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[{"sales_organization_party_id":%q,"sales_relationship":"ANY","rate_bps":1000}]`, id(14)),
			`"alice"`, fmt.Sprintf("%q", id(11)), `"tech"`, fmt.Sprintf("%q", id(15)), `"team"`, fmt.Sprintf("%q", id(16)),
			`"dept"`, fmt.Sprintf("%q", id(18)), `"company"`, fmt.Sprintf("%q", id(14)), `"rate_bps":500`, `"rate_bps":`+ownerRate,
		).Replace(testVIPBenefits))
	}
	day := 25
	input := legacyCatalogFile{Environment: "test", CapturedAt: start, Products: []legacyCatalogProduct{{
		SourceID: id(1), Code: "VIP_TEST", Name: "会员样本", Project: "AI", Source: "DCAI", SourceKey: "vip-test", PeriodMode: "calendar", CycleStartDay: &day,
		Packages: []legacyCatalogPackage{{SourceID: id(2), Code: "test", Name: "规则样本", Project: "AI", Kind: "LEGACY_BUNDLE", ScopeType: "BUSINESS_SCENARIO", ScopeID: "test",
			Versions: []legacyCatalogVersion{{SourceID: id(3), Number: 1, Status: "PUBLISHED", From: start, Through: &boundary, Hash: "first", Definition: policy("500")},
				{SourceID: id(4), Number: 2, Status: "PUBLISHED", From: boundary, Hash: "second", Definition: policy("700")}},
		}},
		BusinessRoutes: []catalogBusinessRoute{{SourceID: id(5), Source: "DCAI", Domain: "VIP", Entity: "VIP_ORDER", RecordTable: "ai_vip_order",
			BusinessKey: "dcai:vip:new", Classifier: "dcai.vip-lifecycle.v1", SKU: "test", Label: "会员下单", Version: 1, From: start}},
	}}}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return storeLegacyCatalog(ctx, tx, input) }); err != nil {
		t.Fatal(err)
	}
	var productID int64
	if err := s.db.QueryRow(ctx, "SELECT id FROM products").Scan(&productID); err != nil {
		t.Fatal(err)
	}
	created, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: "8 月", SettlementMonth: "2026-08"})
	if err != nil {
		t.Fatal(err)
	}
	periodID := created.(map[string]int64)["id"]
	orgSQL := fmt.Sprintf(`
	 INSERT INTO organization_units (id,parent_id,kind,name,status) VALUES ('%[1]s',NULL,'COMPANY','甲公司','ACTIVE'),('%[2]s','%[1]s','DEPARTMENT','交付部','ACTIVE');
	 INSERT INTO people(name,legacy_party_id) VALUES ('负责人甲','%[3]s'),('销售乙','%[4]s'),('分配负责人','%[5]s');
	 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
	 SELECT (substr(legacy_party_id::text,1,24) || '0000000001' || right(legacy_party_id::text,2))::uuid,id,'%[2]s','PRIMARY','2026-01-01' FROM people;`,
		id(17), id(18), id(11), id(12), id(13))
	if _, err := s.db.Exec(ctx, orgSQL); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"source":"DCAI","capturedAt":"2026-11-01T00:00:00Z","people":[],"personLinks":[{"id":%q,"externalId":"seller","partyId":%q,"from":"2026-01-01T00:00:00Z"}],
	 "companyLinks":[{"id":%q,"externalId":"seller-company","companyId":%q,"unitId":%q,"from":"2026-01-01T00:00:00Z"}],"categories":[],
	 "appointments":[{"id":%q,"partyId":%q,"role":"PROJECT_OWNER","scope":"PROJECT","key":"AI","from":"2026-01-01T00:00:00Z"},
	 {"id":%q,"partyId":%q,"role":"SALES","scope":"PROJECT","key":"AI","from":"2026-01-01T00:00:00Z"},
	 {"id":%q,"partyId":%q,"role":"TEAM_LEAD","scope":"ORGANIZATION_UNIT","key":%q,"from":"2026-01-01T00:00:00Z"}],
	 "organizations":[{"partyId":%q,"unitId":%q},{"partyId":%q,"unitId":%q}]}`,
		id(21), id(12), id(22), id(14), id(17), id(23), id(11), id(24), id(12), id(25), id(13), id(18), id(15), id(17), id(16), id(18))
	if err := storeVIPSalesContext(ctx, s.db, json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	save := func(order, version string, amount int64, at, updated time.Time, entity string) {
		t.Helper()
		f := sourceFact{Source: "DCAI", Kind: "ORDER_PAID", OrderID: order, OrderNo: version, PaymentID: order, RecordKey: "ai_vip_order:" + order,
			Domain: "VIP", SourceKey: "vip-test", Version: version, AmountMinor: &amount, OccurredAt: at, UpdatedAt: updated,
			Classification: classifySourcePayment("DCAI", "VIP", entity, "ai_vip_order"),
			VIPClaims:      []vipClaim{{Role: "PID_USER", User: "seller", Company: "seller-company", Relationship: "DIRECT"}}}
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { _, err := storeSourceFacts(ctx, tx, []sourceFact{f}); return err })
		if err != nil {
			t.Fatal(err)
		}
	}
	recalc := func() draftSummary {
		t.Helper()
		result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: periodID})
		if err != nil {
			t.Fatal(err)
		}
		return result.(draftSummary)
	}
	save("one", "1", 10000, boundary.Add(-time.Second), start, "VIP_ORDER")
	save("two", "1", 10000, boundary, boundary, "VIP_ORDER")
	got := recalc()
	if got.Version != 1 || got.Total != 4400 || got.LineCount != 10 || got.IssueCount != 0 {
		t.Fatalf("first draft = %+v", got)
	}
	if recalc().Version != got.Version {
		t.Fatal("unchanged input unexpectedly published a new draft")
	}
	w := httptest.NewRecorder()
	s.getDraft(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d", periodID), nil))
	var detail struct {
		Lines []struct {
			OrderID, PersonName, ComponentLabel, BeneficiaryKind string
			RuleVersion                                          int
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 || len(detail.Lines) != 10 {
		t.Fatalf("draft detail did not return all calculated benefits: %d %s", w.Code, w.Body)
	}
	kinds := map[string]int{}
	for _, line := range detail.Lines {
		kinds[line.BeneficiaryKind]++
		expected := 1
		if line.OrderID == "two" {
			expected = 2
		}
		if line.RuleVersion != expected || line.PersonName == "" || line.ComponentLabel == "" {
			t.Fatalf("line not linked to its rule and recipient: %+v", line)
		}
	}
	if kinds["person"] != 6 || kinds["company"] != 2 || kinds["pool"] != 2 {
		t.Fatalf("beneficiaries = %+v", kinds)
	}
	for _, dimension := range []string{"person", "company", "department"} {
		w = httptest.NewRecorder()
		s.draftReport(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/draft-report?periodId=%d&version=1&dimension=%s", periodID, dimension), nil))
		var report struct {
			Items []struct {
				Name                         string
				PerformanceMinor, TotalMinor int64
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != 200 {
			t.Fatalf("%s has no report: %d %s", dimension, w.Code, w.Body)
		}
		var performance, commission int64
		for _, row := range report.Items {
			performance += row.PerformanceMinor
			commission += row.TotalMinor
		}
		if performance != 20000 || commission != 4400 {
			t.Fatalf("%s totals = %d %d", dimension, performance, commission)
		}
		if dimension == "person" && len(report.Items) != 4 {
			t.Fatalf("company and pool should have their own rows: %+v", report.Items)
		}
	}
	if _, err := s.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", productID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: periodID, ExpectedVersion: got.Version}, "fixture"); !errors.Is(err, errSettlementDisabled) {
		t.Fatalf("unfinished refund and pool settlement should remain disabled: %v", err)
	}
	save("two", "display", 10000, boundary, boundary.Add(time.Minute), "VIP_ORDER")
	if recalc().Version != got.Version {
		t.Fatal("display-only amendment made a new draft")
	}
	save("two", "financial", 20000, boundary, boundary.Add(2*time.Minute), "VIP_ORDER")
	save("unclassified", "1", 1100, boundary, boundary, "UNCLASSIFIED")
	changed := recalc()
	if changed.Version != 2 || changed.Total != 6700 || changed.IssueCount != 1 {
		t.Fatalf("changed draft = %+v", changed)
	}
	var rows int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM calculation_drafts WHERE period_id=$1", periodID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("draft row count = %d: %v", rows, err)
	}
	if err := migrate(ctx, s.db); err != nil {
		t.Fatalf("schema initialization must accept existing drafts: %v", err)
	}
	if reopened := recalc(); reopened.Version != changed.Version || reopened.Total != changed.Total {
		t.Fatalf("schema reinitialization changed the current draft: %+v", reopened)
	}
}
