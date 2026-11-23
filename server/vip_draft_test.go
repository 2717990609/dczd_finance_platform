package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestVIPDraftReportsMissingSourceSellerOncePerPayment(t *testing.T) {
	amount := int64(49900)
	row := catalogPaymentMatch{SourceVersionID: 19, PaidMinor: &amount, PaidInWindow: true, Status: "MATCHED",
		RuleID: "60000000-0000-4000-8000-000000000004", SalesStatus: "SALES_PERSON_MISSING",
		BenefitParts: []vipBenefitPart{
			{Label: "团队跟单", Status: "SALES_PERSON_MISSING"},
			{Label: "销售本人", Status: "SALES_PERSON_MISSING"},
			{Label: "另一个岗位", Status: "BENEFIT_APPOINTMENT_CONFLICT"},
			{Item: "fixed", Party: "independent", Status: "CALCULATED", Amount: 500},
		}}
	data := vipRuntimeData{Claims: map[int64][]vipClaim{19: {}}, PersonIDs: map[string]int64{"independent": 9}}
	var result paymentDraft
	if err := addVIPDraftPayment(&result, row, data, reportAttribution{}); err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 2 || result.Issues[0].Code != "SALES_ORDER_SELLER_MISSING" ||
		result.Issues[0].FactID != 19 || result.Issues[0].Component != "" || result.Issues[1].Code != "BENEFIT_APPOINTMENT_CONFLICT" {
		t.Fatalf("expected one source issue and the distinct appointment issue: %+v", result.Issues)
	}
	if len(result.Performance) != 1 || result.Performance[0].Principal != amount ||
		len(result.Lines) != 1 || result.Lines[0].PersonID != 9 || result.Total != 500 {
		t.Fatalf("missing source data should retain receipts and unrelated arrangements: %+v", result)
	}

	data.Claims[19] = []vipClaim{{Role: "PID_USER", User: "unmapped"}}
	result = paymentDraft{}
	if err := addVIPDraftPayment(&result, row, data, reportAttribution{}); err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 4 || result.Issues[0].Code != "SALES_PERSON_MISSING" {
		t.Fatalf("a supplied but unmapped seller must keep its distinct issue: %+v", result.Issues)
	}
}

func TestVIPDraftReusesVersionAndReconcilesBeneficiaries(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), draftQuotes: &draftCalculationCaches{},
		sources: map[string]sourceConfig{"DCAI": {BaseURL: "https://fixture.invalid", Token: "fixture"}}}
	id := func(n int) string { return fmt.Sprintf("60000000-0000-4000-8000-%012d", n) }
	start := time.Date(2026, 7, 25, 0, 0, 0, 0, shanghai)
	previousStart := start.AddDate(0, -1, 0)
	boundary := start.AddDate(0, 0, 5)
	policy := func(ownerRate string) json.RawMessage {
		return json.RawMessage(strings.NewReplacer(
			`"amount_basis"`, `"refund_policy":"PROPORTIONAL","schema_version":"1.1","recognition_trigger":"PAID","settlement_policy":{"cycle":"MONTHLY_25_TO_24"},"policy_scope":{"business_sku_codes":["test"],"sales_systems":["DCAI"],"financial_event_types":["order.paid"]},"amount_basis"`,
			`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`, fmt.Sprintf(`"item_key":"sales","label":"销售","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":1000},{"sales_organization_party_id":"ANY","sales_relationship":"ANY","sales_category_id":%q,"rate_bps":1500}]`, id(29)),
			`"alice"`, fmt.Sprintf("%q", id(11)), `"tech"`, fmt.Sprintf("%q", id(15)), `"team"`, fmt.Sprintf("%q", id(16)),
			`"dept"`, fmt.Sprintf("%q", id(18)), `"company"`, fmt.Sprintf("%q", id(14)), `"rate_bps":500`, `"rate_bps":`+ownerRate,
		).Replace(testVIPBenefits))
	}
	day := 25
	input := productConfigFile{Environment: "test", CapturedAt: start, Products: []productConfigEntry{{
		SourceID: id(1), Code: "VIP_TEST", Name: "会员样本", Project: "AI", Source: "DCAI", SourceKey: "vip-test", PeriodMode: "calendar", CycleStartDay: &day,
		Packages: []productRulePackageConfig{{SourceID: id(2), Code: "test", Name: "规则样本", Project: "AI", Kind: "COMPONENT_RULE", ScopeType: "BUSINESS_SCENARIO", ScopeID: "test",
			Versions: []productRuleVersionConfig{{SourceID: id(3), Number: 1, Status: "PUBLISHED", From: previousStart, Through: &boundary, Hash: "first", Definition: policy("500")},
				{SourceID: id(4), Number: 2, Status: "PUBLISHED", From: boundary, Hash: "second", Definition: policy("700")}},
		}},
		BusinessRoutes: []catalogBusinessRoute{{SourceID: id(5), Source: "DCAI", Domain: "VIP", Entity: "VIP_ORDER", RecordTable: "ai_vip_order",
			BusinessKey: "dcai:vip:new", Classifier: "dcai.vip-lifecycle.v1", SKU: "test", Label: "会员下单", Version: 1, From: previousStart}},
	}}}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, input) }); err != nil {
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
	 "companyLinks":[{"id":%q,"externalId":"seller-company","companyId":%q,"unitId":%q,"from":"2026-01-01T00:00:00Z"}],
	 "categories":[{"companyId":%q,"categoryId":null,"from":"2026-01-01T00:00:00Z"}],
	 "appointments":[{"id":%q,"partyId":%q,"role":"PROJECT_OWNER","scope":"PROJECT","key":"AI","from":"2026-01-01T00:00:00Z"},
	 {"id":%q,"partyId":%q,"role":"SALES","scope":"PROJECT","key":"AI","from":"2026-01-01T00:00:00Z"},
	 {"id":%q,"partyId":%q,"role":"TEAM_LEAD","scope":"ORGANIZATION_UNIT","key":%q,"from":"2026-01-01T00:00:00Z"},
	 {"id":%q,"partyId":%q,"role":"DELIVERY","scope":"ORGANIZATION_UNIT","key":%q,"from":"2026-01-01T00:00:00Z"},
	 {"id":%q,"partyId":%q,"role":"DELIVERY","scope":"ORGANIZATION_UNIT","key":%q,"from":%q}],
	 "organizations":[{"partyId":%q,"unitId":%q},{"partyId":%q,"unitId":%q}]}`,
		id(21), id(12), id(22), id(14), id(17), id(14), id(23), id(11), id(24), id(12), id(25), id(13), id(18), id(26), id(11), id(18), id(27), id(12), id(18), boundary.Format(time.RFC3339), id(15), id(17), id(16), id(18))
	if err := storeVIPSalesContext(ctx, s.db, json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	saveWithSeller := func(order, version, seller string, amount int64, at, updated time.Time, entity string, line ...string) {
		t.Helper()
		f := sourceFact{Source: "DCAI", Kind: "ORDER_PAID", OrderID: order, OrderNo: version, PaymentID: order, RecordKey: "ai_vip_order:" + order,
			Domain: "VIP", SourceKey: "vip-test", Version: version, AmountMinor: &amount, OccurredAt: at, UpdatedAt: updated,
			Classification: classifySourcePayment("DCAI", "VIP", entity, "ai_vip_order", "vip-test"),
			VIPClaims:      []vipClaim{{Role: "PID_USER", User: seller, Company: "seller-company", Relationship: "DIRECT"}}}
		if len(line) != 0 {
			f.LineID = line[0]
		}
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{f}, s.dcaiSourceKey())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	save := func(order, version string, amount int64, at, updated time.Time, entity string, line ...string) {
		t.Helper()
		saveWithSeller(order, version, "seller", amount, at, updated, entity, line...)
	}
	recalc := func() draftSummary {
		t.Helper()
		result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: periodID})
		if err != nil {
			t.Fatal(err)
		}
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			scope := draftSummary{PeriodID: periodID}
			window, rules, err := readDraftScope(ctx, tx, &scope)
			if err != nil {
				return err
			}
			_, full, err := computeDraft(ctx, tx, scope, window, rules, nil)
			if err != nil {
				return err
			}
			var saved []byte
			if err := tx.QueryRow(ctx, `SELECT input_digest FROM calculation_drafts WHERE period_id=$1`, periodID).Scan(&saved); err != nil {
				return err
			}
			if !bytes.Equal(saved, full) {
				return errors.New("saved trial differs from an independent full computation")
			}
			return nil
		}); err != nil {
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
	readPools := func(summary draftSummary) poolDraftReport {
		t.Helper()
		w := httptest.NewRecorder()
		s.draftPools(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/draft-pools?periodId=%d&version=%d", summary.PeriodID, summary.Version), nil))
		var out poolDraftReport
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
			t.Fatalf("pool summary %d: %v %s", w.Code, err, w.Body)
		}
		return out
	}
	firstPools := readPools(got)
	if len(firstPools.Items) != 1 || firstPools.Items[0].Balance != 600 || len(firstPools.Items[0].Members) != 2 {
		t.Fatalf("qualified pool snapshot = %+v", firstPools)
	}
	people := map[string]poolMember{}
	for _, p := range firstPools.Items[0].Members {
		people[p.Name] = p
	}
	alice, seller := people["负责人甲"], people["销售乙"]
	if alice.Available != 600 || seller.Available != 300 {
		t.Fatalf("payment-time member limits: %+v", people)
	}
	plan := savePoolAllocationInput{PeriodID: periodID, Version: got.Version, Basis: firstPools.Basis, poolScope: firstPools.Items[0].poolScope}
	plan.Shares = []poolAllocationShare{{PersonID: seller.ID, Amount: 301}}
	if _, err := s.savePoolAllocation(ctx, plan); !errors.Is(err, errPoolAmount) {
		t.Fatalf("seller cannot use the earlier lot: %v", err)
	}
	plan.Shares = []poolAllocationShare{{PersonID: alice.ID, Amount: 350}}
	plan.Retained = 100
	if _, err := s.savePoolAllocation(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := s.savePoolAllocation(ctx, plan); !errors.Is(err, errPoolStale) {
		t.Fatalf("same revision must avoid overwrite: %v", err)
	}
	if saved := readPools(got); !saved.Items[0].PlanCurrent || saved.Items[0].Revision != 1 || saved.Items[0].Balance != 600 {
		t.Fatalf("saved pool draft = %+v", saved)
	}
	assertAllocationReport := func(expected int64) {
		t.Helper()
		for _, dimension := range []string{"person", "company", "department"} {
			w := httptest.NewRecorder()
			s.draftReport(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/draft-report?periodId=%d&version=%d&dimension=%s", periodID, got.Version, dimension), nil))
			var body struct {
				Items []struct {
					Name                        string
					TotalMinor, AllocationMinor int64
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
				t.Fatalf("report: %d %v %s", w.Code, err, w.Body)
			}
			var total, allocated int64
			for _, row := range body.Items {
				total += row.TotalMinor
				if row.Name == "负责人甲" {
					allocated = row.AllocationMinor
				}
			}
			if total != 4400 || dimension == "person" && allocated != expected {
				t.Fatalf("allocation should reclassify but not increase %s: %+v", dimension, body)
			}
		}
	}
	assertAllocationReport(350)
	editor := s
	editor.development = true
	if _, err := editor.endCatalogAppointment(ctx, endAppointmentInput{AssignmentID: id(26), At: start.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if stale := readPools(got).Items[0]; stale.PlanCurrent || len(stale.Shares) != 2 {
		t.Fatalf("expired delivery appointment must invalidate the open pool plan: %+v", stale)
	}
	assertAllocationReport(0)
	if _, err := s.db.Exec(ctx, `UPDATE catalog_sales_appointments SET valid_to=NULL WHERE id=$1::uuid`, id(26)); err != nil {
		t.Fatal(err)
	}
	if restored := recalc(); restored.Version != got.Version {
		t.Fatalf("restored appointment changed the draft: %+v", restored)
	}
	assertAllocationReport(350)
	if _, err := s.db.Exec(ctx, "INSERT INTO organization_import_state(singleton,captured_at,digest) VALUES(true,now(),decode(repeat('aa',32),'hex'))"); err != nil {
		t.Fatal(err)
	}
	if stale := readPools(got).Items[0]; stale.PlanCurrent || len(stale.Shares) != 2 {
		t.Fatalf("org revision should preserve and invalidate entered amounts: %+v", stale)
	}
	assertAllocationReport(0)

	if _, err := s.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", productID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: periodID, ExpectedVersion: got.Version}, "fixture"); !errors.Is(err, errSettlementSourceCoverage) {
		t.Fatalf("a draft alone must not prove complete source coverage: %v", err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,target_through,next_updated_at,next_id)
	 VALUES('VIP_ORDERS',$1,$2,now(),$2,'0')`, s.dcaiSourceKey(), previousStart); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: periodID, ExpectedVersion: got.Version}, "fixture"); !errors.Is(err, errSettlementSourceCoverage) {
		t.Fatalf("a partial sync must not provide coverage: %v", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_order_sync SET completed_through=target_through-interval '2 hours',target_through=NULL,last_success_at=now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: periodID, ExpectedVersion: got.Version}, "fixture"); !errors.Is(err, errSettlementSourceStale) {
		t.Fatalf("a recently completed job with an old source watermark must not authorize a close: %v", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_order_sync SET completed_through=now()+interval '2 hours',last_success_at=now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: periodID, ExpectedVersion: got.Version}, "fixture"); !errors.Is(err, errSettlementSourceClock) {
		t.Fatalf("an untrustworthy future watermark cannot cover a close: %v", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_order_sync SET completed_through=now(),last_success_at=now()`); err != nil {
		t.Fatal(err)
	}
	job, err := s.enqueueOrderSync(ctx, orderSyncRequest{Stream: dcaiVIPStream})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: periodID, ExpectedVersion: got.Version}, "fixture"); !errors.Is(err, errSettlementSourceCoverage) {
		t.Fatalf("a newly queued sync must block confirmation before it even obtains a target watermark: %v", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_sync_jobs SET status='SUCCEEDED',finished_at=now() WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO dcai_order_blockers(stream,source_row_id,order_id,cursor_updated_at,reasons)
	 VALUES('VIP_ORDERS','unlinked','no-local-payment',$1,ARRAY['REFUND_EVIDENCE_MISSING'])`, start); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: periodID, ExpectedVersion: got.Version}, "fixture"); !errors.Is(err, errSettlementSourceRefund) {
		t.Fatalf("a refund with no locally linked payment must prevent a close: %v", err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM dcai_order_blockers WHERE source_row_id='unlinked'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO dcai_order_blockers(stream,source_row_id,order_id,cursor_updated_at,reasons)
	 VALUES('VIP_ORDERS','1','one',$1,ARRAY['REFUND_EVIDENCE_MISSING'])`, start); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: periodID, ExpectedVersion: got.Version}, "fixture"); !errors.Is(err, errSettlementSourceRefund) {
		t.Fatalf("unresolved completion of a source refund must remain visible: %v", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_order_blockers SET reasons=ARRAY['REFUND_PROCESSING']`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: periodID, ExpectedVersion: got.Version}, "fixture"); err == nil || !strings.Contains(err.Error(), "团队池") {
		t.Fatalf("unallocated pool balance must prevent confirmation: %v", err)
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
	saveRefund := func(order, id, version string, amount int64, occurred time.Time) {
		t.Helper()
		refund := sourceFact{Source: "DCAI", Domain: "VIP", SourceKey: "vip-test", Kind: "REFUND_SUCCEEDED",
			RecordKey: "ai_vip_order:" + id, Version: version, OrderID: order, RefundID: id,
			AmountMinor: &amount, OccurredAt: occurred, UpdatedAt: occurred,
			RefundsPayment: []sourcePaymentReference{{OrderID: order, RecordID: order}}}
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{refund}, s.dcaiSourceKey())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	saveRefund("two", "partial", "1", 5050, boundary.AddDate(0, 0, 1))
	partial := recalc()
	if partial.Version != 3 || partial.Accrued != 6700 || partial.RefundTotal != -1163 || partial.Total != 5537 || partial.RefundCount != 5 || partial.IssueCount != 1 {
		t.Fatalf("same-period partial refund = %+v", partial)
	}
	if recalc().Version != partial.Version {
		t.Fatal("same refund generated another draft")
	}
	w = httptest.NewRecorder()
	s.getDraft(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d", periodID), nil))
	var refundDetail struct {
		Refunds []struct {
			BeneficiaryKind, PersonName, RefundPeriod, PostingPeriod string
			RuleVersion                                              int
			AmountMinor                                              int64
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &refundDetail); err != nil || w.Code != 200 || len(refundDetail.Refunds) != 5 {
		t.Fatalf("refund line detail: %d %v %s", w.Code, err, w.Body)
	}
	counts := map[string]int{}
	var debits []int64
	for _, line := range refundDetail.Refunds {
		counts[line.BeneficiaryKind]++
		debits = append(debits, line.AmountMinor)
		if line.PersonName == "" || line.RuleVersion != 2 || line.RefundPeriod != "8 月" || line.PostingPeriod != "8 月" {
			t.Fatalf("refund identity or period has drifted: %+v", line)
		}
	}
	if counts["person"] != 3 || counts["company"] != 1 || counts["pool"] != 1 {
		t.Fatalf("refund recipients=%+v", counts)
	}
	// 5050 / 20000 of the original 2000, 1400, 600, 400 and 200 cent awards.
	slices.Sort(debits)
	if !slices.Equal(debits, []int64{-505, -354, -152, -101, -51}) {
		t.Fatalf("refund debits did not match the original splits: %v", debits)
	}

	save("unclassified", "classified", 1100, boundary, boundary.Add(3*time.Minute), "VIP_ORDER")
	finished := recalc()
	if finished.IssueCount != 0 {
		t.Fatalf("expected review to be resolved before closing: %+v", finished)
	}
	review := func(result draftSummary) settlementReview {
		t.Helper()
		w := httptest.NewRecorder()
		s.getSettlementReview(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", result.PeriodID, result.Version), nil))
		var out settlementReview
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("settlement review: %d %v %s", w.Code, err, w.Body)
		}
		return out
	}
	closeReviewed := func(result draftSummary, token string) settlement {
		t.Helper()
		request := confirmSettlementInput{PeriodID: result.PeriodID, ExpectedVersion: result.Version, ReviewToken: token}
		closed, err := s.confirmSettlement(ctx, request, "fixture")
		if err != nil || closed.ID == 0 {
			t.Fatalf("confirmation: %+v %v", closed, err)
		}
		if repeated, err := s.confirmSettlement(ctx, request, "fixture retry"); err != nil || repeated != closed {
			t.Fatalf("same review must return the original confirmation: %+v %v", repeated, err)
		}
		return closed
	}
	createPeriod := func(month, label string) int64 {
		t.Helper()
		out, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: label, SettlementMonth: month})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]int64)["id"]
	}
	draftAt := func(period int64) draftSummary {
		t.Helper()
		out, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		return out.(draftSummary)
	}
	plan.Version, plan.Revision = finished.Version, 1
	readyPools := readPools(finished)
	plan.Basis = readyPools.Basis
	plan.Shares = []poolAllocationShare{{PersonID: alice.ID, Amount: 600}}
	plan.Retained = 0
	if _, err := s.savePoolAllocation(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if partialReview := review(finished); partialReview.Token != "" || len(partialReview.Pools) != 1 || partialReview.Pools[0].Unallocated != 181 {
		t.Fatalf("review should expose the unallocated portion: %+v", partialReview)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO draft_refresh_queue(period_id) VALUES($1)`, periodID); err != nil {
		t.Fatal(err)
	}
	if pending := review(finished); !pending.DraftStale || len(pending.Pools) != 1 || pending.Token != "" {
		t.Fatalf("review must show the pending draft and pool gap together: %+v", pending)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM draft_refresh_queue WHERE period_id=$1`, periodID); err != nil {
		t.Fatal(err)
	}
	plan.Revision, plan.Retained = 2, 181
	if _, err := s.savePoolAllocation(ctx, plan); err != nil {
		t.Fatal(err)
	}
	firstReview := review(finished)
	if firstReview.Token == "" || len(firstReview.Pools) != 0 {
		t.Fatalf("fully funded review: %+v", firstReview)
	}
	request := confirmSettlementInput{PeriodID: periodID, ExpectedVersion: finished.Version, ReviewToken: firstReview.Token}
	save("one", "changed-after-review", 10100, boundary.Add(-time.Second), boundary.Add(3*time.Minute), "VIP_ORDER")
	if revised := review(finished); !revised.DraftStale || revised.Token != "" {
		t.Fatalf("review should detect the changed payment before confirmation: %+v", revised)
	}
	if _, err := s.confirmSettlement(ctx, request, "fixture"); !errors.Is(err, errSettlementStale) {
		t.Fatalf("changed payment must invalidate confirmation: %v", err)
	}
	save("one", "restored-after-review", 10000, boundary.Add(-time.Second), boundary.Add(4*time.Minute), "VIP_ORDER")
	if rerun := recalc(); rerun.Version != finished.Version {
		t.Fatalf("restored amount should clear pending work without publishing a new draft: %+v", rerun)
	}
	plan.Revision, plan.Retained = 3, 182
	plan.Shares[0].Amount = 599
	if _, err := s.savePoolAllocation(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, request, "fixture"); !errors.Is(err, errSettlementStale) {
		t.Fatalf("a different allocation must invalidate a reviewed draft even when the total is unchanged: %v", err)
	}
	checkRetention := func(month string, opening, income int64) {
		t.Helper()
		w := httptest.NewRecorder()
		s.departmentRetention(w, httptest.NewRequest(http.MethodGet,
			"/api/v1/department-retention?month="+month+"&accountId="+id(18), nil))
		var ledger struct {
			Items   []struct{ OpeningMinor, IncomeMinor, BalanceMinor int64 }
			Entries []struct{ PeriodID, AmountMinor int64 }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &ledger); err != nil || w.Code != http.StatusOK {
			t.Fatalf("retention ledger HTTP %d: %s %v", w.Code, w.Body.String(), err)
		}
		if opening+income == 0 {
			if len(ledger.Items) != 0 || len(ledger.Entries) != 0 {
				t.Fatalf("open pool plan must not enter the ledger: %+v", ledger)
			}
		} else if len(ledger.Items) != 1 || len(ledger.Entries) != 1 || ledger.Entries[0].PeriodID != periodID ||
			ledger.Entries[0].AmountMinor != 182 || ledger.Items[0].OpeningMinor != opening ||
			ledger.Items[0].IncomeMinor != income || ledger.Items[0].BalanceMinor != opening+income {
			t.Fatalf("the only retention credit should be the confirmed August share: %+v", ledger)
		}
	}
	checkRetention("2026-08", 0, 0)
	secondReview := review(finished)
	closed := verifySettlementHTTP(t, ctx, s, alice.ID, productID, finished, secondReview.Token)
	checkRetention("2026-08", 0, 182)
	if after := review(finished); after.Settlement == nil || after.Settlement.ID != closed.ID || after.Token != "" {
		t.Fatalf("closed review should reference the existing settlement: %+v", after)
	}
	if _, err := editor.endCatalogAppointment(ctx, endAppointmentInput{AssignmentID: id(26), At: start.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	postedPool := readPools(finished).Items[0]
	if !postedPool.PlanCurrent || !slices.ContainsFunc(postedPool.Shares, func(share poolAllocationShare) bool {
		return share.PersonID == alice.ID && share.Amount == 599
	}) {
		t.Fatalf("later appointment changes rewrote a confirmed pool allocation: %+v", postedPool)
	}
	if _, err := s.db.Exec(ctx, `UPDATE catalog_sales_appointments SET valid_to=NULL WHERE id=$1::uuid`, id(26)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmSettlement(ctx, request, "fixture"); !errors.Is(err, errSettlementClosed) {
		t.Fatalf("a different review cannot replace the finalized result: %v", err)
	}

	if _, err := s.db.Exec(ctx, "UPDATE monthly_pool_allocation_shares SET amount_minor=400"); err == nil {
		t.Fatal("confirmed recipient amount can change")
	}
	if _, err := s.db.Exec(ctx, "UPDATE product_rule_versions SET definition='{}'::jsonb WHERE source_id=$1", id(4)); err == nil {
		t.Fatal("confirmed rule content can change")
	}
	if _, err := s.db.Exec(ctx, "UPDATE product_rule_packages SET scope_id='other' WHERE source_id=$1", id(2)); err == nil {
		t.Fatal("confirmed rule scope can change")
	}
	if _, err := s.db.Exec(ctx, "UPDATE periods SET ordinal=ordinal+1 WHERE id=$1", periodID); err == nil {
		t.Fatal("confirmed period ordering can change")
	}
	if _, err := s.savePoolAllocation(ctx, plan); !errors.Is(err, errSettlementClosed) {
		t.Fatalf("closed pool plan accepted writes: %v", err)
	}

	october := createPeriod("2026-10", "10 月")
	emptyOctober := draftAt(october)
	closeReviewed(emptyOctober, review(emptyOctober).Token)
	beforeBackfill := readPools(emptyOctober)
	if !beforeBackfill.Closed || len(beforeBackfill.Items) != 0 {
		t.Fatalf("empty confirmed October should have no carried pool balance: %+v", beforeBackfill)
	}

	sept := createPeriod("2026-09", "9 月")
	lateAt := start.AddDate(0, 1, 2)
	now := time.Now().In(shanghai)
	activeMonth := now
	if now.Day() >= 25 {
		activeMonth = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, shanghai).AddDate(0, 1, 0)
	}
	current := createPeriod(activeMonth.Format("2006-01"), "本次办理期")
	saveRefund("one", "in-september", "1", 2000, lateAt)
	if outside := draftAt(current); outside.RefundCount != 0 {
		t.Fatalf("open September must receive its own refund: %+v", outside)
	}
	september := draftAt(sept)
	if september.Total != -420 || september.RefundCount != 5 || september.IssueCount != 0 {
		t.Fatalf("September refund = %+v", september)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_order_blockers SET reasons=ARRAY['REFUND_EVIDENCE_MISSING'],cursor_updated_at=$1`, lateAt); err != nil {
		t.Fatal(err)
	}
	if gap := review(september); len(gap.Sources) != 1 || gap.Sources[0].RefundGap != 1 || gap.Token != "" {
		t.Fatalf("source refund of a historically posted order must appear in the next settlement review: %+v", gap)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: sept, ExpectedVersion: september.Version}, "fixture"); !errors.Is(err, errSettlementSourceRefund) {
		t.Fatalf("a historical missing refund cannot be bypassed: %v", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_order_blockers SET reasons='{}'`); err != nil {
		t.Fatal(err)
	}
	closeReviewed(september, review(september).Token)
	checkRetention("2026-09", 182, 0)
	if got := readPools(emptyOctober); !got.Closed || len(got.Items) != 0 || got.Basis != beforeBackfill.Basis {
		t.Fatalf("confirming an earlier refund must not rewrite October's confirmed carry: %+v", got)
	}
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", october); err != nil {
		t.Fatal(err)
	}
	if got := readPools(emptyOctober); got.Closed || len(got.Items) != 1 || got.Items[0].Opening != -60 {
		t.Fatalf("a reopened October must see the newly confirmed September refund: %+v", got)
	}
	emptyOctober = draftAt(october)
	closeReviewed(emptyOctober, review(emptyOctober).Token)
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", sept); err == nil || !strings.Contains(err.Error(), "booked adjustments") {
		t.Fatalf("September removal would change the now-confirmed October opening, even with no October movement: %v", err)
	}
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", october); err != nil {
		t.Fatal(err)
	}

	saveRefund("two", "after-close", "1", 14950, lateAt.Add(time.Hour))
	late := draftAt(current)
	if late.RefundTotal != -3437 || late.Total != -3437 || late.RefundCount != 5 || late.IssueCount != 0 {
		t.Fatalf("the current period should take only the unbooked remainder: %+v", late)
	}
	if latePool := readPools(late).Items[0]; latePool.Opening != -60 || latePool.Refund != -448 || latePool.Balance != -508 {
		t.Fatalf("refund after distribution must leave original distribution fixed and charge the pool: %+v", latePool)
	}

	saveRefund("one", "in-september", "2", 2100, lateAt.Add(time.Minute))
	bookedChanged := draftAt(current)
	if bookedChanged.RefundTotal != -3437 || bookedChanged.IssueCount != 1 {
		t.Fatalf("booked refund revision should need review without changing original debit: %+v", bookedChanged)
	}
	for _, dimension := range []string{"person", "company", "department"} {
		w := httptest.NewRecorder()
		s.draftReport(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/draft-report?periodId=%d&version=%d&dimension=%s", current, bookedChanged.Version, dimension), nil))
		var report struct {
			Items []struct{ TotalMinor, PerformanceMinor int64 }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != 200 {
			t.Fatalf("%s report: %d %v", dimension, w.Code, err)
		}
		var total, performance int64
		for _, row := range report.Items {
			total += row.TotalMinor
			performance += row.PerformanceMinor
		}
		if performance != -14950 || total != -3437 {
			t.Fatalf("%s refund report: %d %d", dimension, performance, total)
		}
	}
	w = httptest.NewRecorder()
	s.getDraft(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d", current), nil))
	var lateDetail struct {
		Refunds []struct{ OriginalPeriod, RefundPeriod, PostingPeriod string }
		Issues  []struct{ Code string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &lateDetail); err != nil || w.Code != 200 || len(lateDetail.Refunds) != 5 || len(lateDetail.Issues) != 1 || lateDetail.Issues[0].Code != "REFUND_BOOKED_CHANGED" {
		t.Fatalf("late detail: %d %v %s", w.Code, err, w.Body)
	}
	for _, line := range lateDetail.Refunds {
		if line.OriginalPeriod != "8 月" || line.RefundPeriod != "9 月" || line.PostingPeriod != "本次办理期" {
			t.Fatalf("refund period labels drifted: %+v", line)
		}
	}
	// The closed payment and earlier refund stays unchanged after the late run.
	var confirmedTotal int64
	if err := s.db.QueryRow(ctx, "SELECT total_minor+refund_minor FROM calculation_drafts WHERE period_id=$1", periodID).Scan(&confirmedTotal); err != nil || confirmedTotal != finished.Total {
		t.Fatalf("closed payment period changed: %d %v", confirmedTotal, err)
	}

	octoberPayment := lateAt.AddDate(0, 1, 0)
	save("offset-pool-deficit", "1", 2000, octoberPayment, octoberPayment, "VIP_ORDER")
	octoberDraft := draftAt(october)
	closeReviewed(octoberDraft, review(octoberDraft).Token)
	if balance := readPools(octoberDraft).Items[0]; balance.Opening != -60 || balance.Income != 60 || balance.Balance != 0 {
		t.Fatalf("October payment should only offset prior deficit: %+v", balance)
	}
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", sept); err == nil || !strings.Contains(err.Error(), "booked adjustments") {
		t.Fatalf("September removal would change the confirmed October pool: %v", err)
	}

	july := createPeriod("2026-07", "7 月")
	before := start.AddDate(0, 0, -1)
	save("backdated-payment", "1", 10000, before, before, "VIP_ORDER")
	earlier := draftAt(july)
	earlierPools := readPools(earlier)
	if earlier.IssueCount != 0 || len(earlierPools.Items) != 1 || earlierPools.Items[0].Balance != 300 {
		t.Fatalf("backdated payment: %+v %+v", earlier, earlierPools)
	}
	if _, err := s.savePoolAllocation(ctx, savePoolAllocationInput{PeriodID: july, Version: earlier.Version,
		Basis: earlierPools.Basis, poolScope: earlierPools.Items[0].poolScope, Retained: 300}); err != nil {
		t.Fatal(err)
	}
	if blocked := review(earlier); blocked.Token != "" || !blocked.LaterPoolActivity {
		t.Fatalf("later confirmation should block changing the same pool history: %+v", blocked)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: july, ExpectedVersion: earlier.Version}, "fixture"); !errors.Is(err, errSettlementPoolHistory) {
		t.Fatalf("backdated confirmation changed an account with later postings: %v", err)
	}

	save("one", "moved-after-settlement", 10000, before, boundary.Add(5*time.Minute), "VIP_ORDER", "line-backfilled")
	moved := draftAt(july)
	if moved.Total != earlier.Total || moved.IssueCount != 1 {
		t.Fatalf("a date correction cannot post a settled payment again: before %+v after %+v", earlier, moved)
	}
	var issue string
	if err := s.db.QueryRow(ctx, "SELECT code FROM calculation_draft_issues WHERE period_id=$1", july).Scan(&issue); err != nil || issue != "PAYMENT_ALREADY_SETTLED" {
		t.Fatalf("expected already-settled payment review, got %q: %v", issue, err)
	}

	next := createPeriod("2032-02", "费率调整验证")
	newAt := time.Date(2032, 2, 5, 12, 0, 0, 0, shanghai)
	save("before-new-rate", "1", 10000, newAt.Add(-time.Second), newAt, "VIP_ORDER")
	save("new-rate", "1", 10000, newAt, newAt, "VIP_ORDER")
	prior := draftAt(next)
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,kind,name,status) VALUES($1,'COMPANY','规则新收款公司','ACTIVE')`, id(71)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO catalog_benefit_organizations(source,party_uuid,unit_id,observed_at)
	 SELECT source,$1,$2,captured_at FROM catalog_sales_context WHERE source='DCAI'`, id(72), id(71)); err != nil {
		t.Fatal(err)
	}
	newRate, newSalesRate, salesRow := int64(900), int64(1100), 1
	inputRates := publishCatalogRatesInput{BaseID: id(4), EffectiveFrom: newAt, Changes: []catalogRateChange{
		{ItemKey: "owner", Rate: &newRate}, {ItemKey: "sales", Row: &salesRow, Rate: &newSalesRate},
	}, TargetChanges: []catalogBenefitTargetChange{
		{ItemKey: id(14), catalogBenefitTarget: catalogBenefitTarget{Target: "company", Organization: id(72)}},
	}}
	preview := func(period int64, input publishCatalogRatesInput) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(catalogRuleImpactInput{PeriodID: period, Rule: input})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/catalog-rule-impact", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		local := s
		local.development = true
		local.postCatalogRuleImpact(w, r)
		return w
	}
	verifyPreview := func(input publishCatalogRatesInput, matched int, delta int64) {
		t.Helper()
		w := preview(next, input)
		var result ruleImpactReport
		if err := json.Unmarshal(w.Body.Bytes(), &result); w.Code != http.StatusOK || err != nil || result.MatchedPaymentCount == nil ||
			*result.MatchedPaymentCount != matched || result.BeforeMinor != prior.Total || result.DeltaMinor != delta {
			t.Errorf("unexpected preview for %d applicable payments: %d %s", matched, w.Code, w.Body)
		}
		for _, row := range result.Items {
			if row.OrderID != "new-rate" {
				t.Errorf("forecast changed a payment outside the candidate's effective time: %+v", row)
			}
		}
	}
	verifyPreview(inputRates, 1, 300)
	later := inputRates
	later.EffectiveFrom = newAt.AddDate(0, 0, 1)
	verifyPreview(later, 0, 0)
	renamed := "负责人新版名称"
	verifyPreview(publishCatalogRatesInput{BaseID: id(4), EffectiveFrom: newAt, Changes: []catalogRateChange{
		{ItemKey: "owner", Label: &renamed},
	}}, 1, 0)
	unchangedRate := int64(700)
	noChange := preview(next, publishCatalogRatesInput{BaseID: id(4), EffectiveFrom: newAt, Changes: []catalogRateChange{
		{ItemKey: "owner", Rate: &unchangedRate},
	}})
	var noCandidate ruleImpactReport
	if err := json.Unmarshal(noChange.Body.Bytes(), &noCandidate); noChange.Code != http.StatusOK || err != nil ||
		noCandidate.MatchedPaymentCount != nil || noCandidate.DeltaMinor != 0 {
		t.Fatalf("an unchanged definition must not report the old rule's payments as a new match: %d %s", noChange.Code, noChange.Body)
	}
	ownerPreview := preview(next, publishCatalogRatesInput{
		BaseID: id(4), EffectiveFrom: newAt,
		TargetChanges: []catalogBenefitTargetChange{{ItemKey: "delivery",
			catalogBenefitTarget: catalogBenefitTarget{Target: "pool", Role: "PROJECT_OWNER", Organization: id(16)}}},
	})
	var ownership ruleImpactReport
	if err := json.Unmarshal(ownerPreview.Body.Bytes(), &ownership); ownerPreview.Code != http.StatusOK || err != nil ||
		ownership.BeforeMinor != prior.Total || ownership.DeltaMinor != 0 || len(ownership.Items) != 2 {
		t.Fatalf("changing only a pool owner should produce two attributed rows with zero net change: %d %s", ownerPreview.Code, ownerPreview.Body)
	}
	for _, row := range ownership.Items {
		if row.Recipient != "交付池" || row.OrderID != "new-rate" ||
			!(row.AllocationOwner == "分配负责人" && row.BeforeMinor == 300 && row.AfterMinor == 0 ||
				row.AllocationOwner == "负责人甲" && row.BeforeMinor == 0 && row.AfterMinor == 300) {
			t.Fatalf("wrong pool or owner affected by the rule: %+v", row)
		}
	}
	var aggregatePeriod int64
	if err := s.db.QueryRow(ctx, `WITH pkg AS (
	 INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
	 VALUES($2,$1,'impact-aggregate','整期试算','AI','COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE','impact-aggregate',false) RETURNING source_id
	 ) INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,report_month,aggregate_package_id)
	 SELECT $1,'整期试算',203202,'2032-01-25','2032-02-24','2032-02-01',source_id FROM pkg RETURNING id`,
		productID, id(73)).Scan(&aggregatePeriod); err != nil {
		t.Fatal(err)
	}
	if blocked := preview(aggregatePeriod, inputRates); blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "请选择逐单账期") {
		t.Errorf("per-payment preview accepted an aggregate period: %d %s", blocked.Code, blocked.Body)
	}
	if after := draftAt(next); after.Version != prior.Version || after.Total != prior.Total {
		t.Fatalf("preview changed the saved draft: %+v", after)
	}
	published, err := s.publishCatalogRates(ctx, inputRates, "finance-fixture")
	if err != nil || published.ID == id(4) || published.Version != 3 {
		t.Fatalf("publication failed: %+v, %v", published, err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE product_rule_versions SET published_by='other' WHERE source_id=$1::uuid`, published.ID); err == nil {
		t.Fatal("a published version accepted an audit change")
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM product_rule_versions WHERE source_id=$1::uuid`, published.ID); err == nil {
		t.Fatal("a published version was removable before settlement")
	}
	if _, err := s.publishCatalogRates(ctx, inputRates, "finance-fixture"); !errors.Is(err, errCatalogRateStale) {
		t.Fatalf("stale form should fail: %v", err)
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var openVersion, openTotal int64
	if err := s.db.QueryRow(ctx, `SELECT version,total_minor FROM calculation_drafts WHERE period_id=$1`, next).Scan(&openVersion, &openTotal); err != nil || openVersion != prior.Version+1 || openTotal != prior.Total+300 {
		t.Fatalf("updated draft: %d %d; previous %+v; %v", openVersion, openTotal, prior, err)
	}
	if err := s.db.QueryRow(ctx, `SELECT total_minor+refund_minor FROM calculation_drafts WHERE period_id=$1`, periodID).Scan(&confirmedTotal); err != nil || confirmedTotal != finished.Total {
		t.Fatalf("publish changed the confirmed period: %d %v", confirmedTotal, err)
	}
	var matched int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM calculation_draft_lines WHERE period_id=$1 AND catalog_rule_id=$2::uuid`, next, published.ID).Scan(&matched); err != nil || matched != 5 {
		t.Fatalf("only payments at or after effective time should use the new rule: %d %v", matched, err)
	}
	var oldPayee, newPayee string
	if err := s.db.QueryRow(ctx, `SELECT min(l.beneficiary_unit_id::text) FILTER(WHERE o.order_id='before-new-rate'),
	 min(l.beneficiary_unit_id::text) FILTER(WHERE o.order_id='new-rate')
	 FROM calculation_draft_lines l JOIN source_order_versions o ON o.id=l.source_version_id
	 WHERE l.period_id=$1 AND l.component_type=$2 AND l.amount_minor=200`, next, id(14)).
		Scan(&oldPayee, &newPayee); err != nil || oldPayee != id(17) || newPayee != id(71) {
		t.Fatalf("same rate must pay the configured company only after its effective time: %s %s %v", oldPayee, newPayee, err)
	}
	march := createPeriod("2032-03", "后续规则验证")
	marchAt := newAt.AddDate(0, 1, 0)
	save("later-rule", "1", 10000, marchAt, marchAt, "VIP_ORDER")
	if _, err := s.publishCatalogRates(ctx, publishCatalogRatesInput{BaseID: published.ID, EffectiveFrom: marchAt,
		Changes: []catalogRateChange{{ItemKey: "owner", Rate: &newSalesRate}}}, "finance-fixture"); err != nil {
		t.Fatal(err)
	}
	marchDraft := draftAt(march)
	inputRates.EffectiveFrom = newAt.AddDate(0, 0, -1)
	if _, err := s.publishCatalogRates(ctx, inputRates, "finance-fixture"); err != nil {
		t.Fatal(err)
	}
	var affected, untouched bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1),
	 EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$2)`, next, march).Scan(&affected, &untouched); err != nil || !affected || untouched {
		t.Fatalf("inserting before a scheduled successor queued the wrong periods: %t %t %v", affected, untouched, err)
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	if currentMarch := draftAt(march); currentMarch.Version != marchDraft.Version || currentMarch.Total != marchDraft.Total {
		t.Fatalf("schedule and result of the next period changed: %+v -> %+v", marchDraft, currentMarch)
	}

	t.Run("指定收款人的生效边界", func(t *testing.T) {
		cut := marchAt.AddDate(0, 1, 0)
		april := createPeriod("2032-04", "收款人变更验证")
		var base string
		if err := s.db.QueryRow(ctx, `SELECT source_id::text FROM product_rule_versions WHERE effective_from=$1`, marchAt).Scan(&base); err != nil {
			t.Fatal(err)
		}
		result, err := s.createPerson(ctx, createPersonInput{Name: "新任负责人"})
		if err != nil {
			t.Fatal(err)
		}
		person := result.(map[string]int64)["id"]
		save("before-payee", "1", 10000, cut.Add(-time.Second), cut, "VIP_ORDER")
		save("after-payee", "1", 10000, cut, cut, "VIP_ORDER")
		previous := draftAt(april)
		if err := s.saveFinanceAccount(ctx, financeAccountConfig{PersonID: person, Login: "payee.operator", Enabled: true,
			Rules: []financeRuleGrant{{ProductID: productID, Publish: true}}}, "isolated-payee-test"); err != nil {
			t.Fatal(err)
		}
		session, err := s.loginFinanceAccount(ctx, financeLoginInput{Login: "payee.operator", Password: "isolated-payee-test"})
		if err != nil {
			t.Fatal(err)
		}
		cookie := &http.Cookie{Name: financeAccountCookie, Value: session}
		submit := func(item string) *httptest.ResponseRecorder {
			t.Helper()
			payload := fmt.Sprintf(`{"baseId":%q,"effectiveFrom":%q,"recipientChanges":[{"itemKey":%q,"personId":%d}]}`, base, cut.Format(time.RFC3339), item, person)
			r := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/catalog-rule-rates", strings.NewReader(payload))
			r.Header.Set("Origin", "http://localhost")
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			s.postCatalogRuleRates(w, r)
			return w
		}
		if blocked := submit("company"); blocked.Code != 400 {
			t.Fatalf("company beneficiary accepted an individual: %d %s", blocked.Code, blocked.Body)
		}
		if blocked := submit("owner"); blocked.Code != 409 {
			t.Fatalf("payee without an effective appointment was accepted: %d %s", blocked.Code, blocked.Body)
		}
		if _, err := editor.addCatalogAppointment(ctx, addCatalogAppointmentInput{
			PersonID: person, StartsAt: cut, catalogAppointmentTarget: catalogAppointmentTarget{Role: "PROJECT_OWNER", Scope: "PROJECT", ScopeKey: "AI"},
		}); err != nil {
			t.Fatal(err)
		}
		w := submit("owner")
		var revision catalogPublication
		if err := json.Unmarshal(w.Body.Bytes(), &revision); err != nil || w.Code != 200 || revision.ID == "" || revision.ID == base {
			t.Fatalf("payee publication: %d %s %v", w.Code, w.Body, err)
		}
		if err := s.refreshPendingDrafts(ctx); err != nil {
			t.Fatal(err)
		}
		now := draftAt(april)
		if now.Version != previous.Version+1 || now.Total != previous.Total || now.IssueCount != 0 {
			t.Fatalf("payee-only change: before=%+v after=%+v", previous, now)
		}
		for _, expected := range []struct {
			order, rule string
			person      int64
		}{{"before-payee", base, alice.ID}, {"after-payee", revision.ID, person}} {
			var actualPerson int64
			var actualRule string
			err := s.db.QueryRow(ctx, `SELECT l.person_id,l.catalog_rule_id::text FROM calculation_draft_lines l
			 JOIN source_order_versions source ON source.id=l.source_version_id
			 WHERE l.period_id=$1 AND l.component_type='owner' AND source.order_id=$2`, april, expected.order).Scan(&actualPerson, &actualRule)
			if err != nil || actualPerson != expected.person || actualRule != expected.rule {
				t.Fatalf("order %s person=%d rule=%s error=%v", expected.order, actualPerson, actualRule, err)
			}
		}
	})

	t.Run("已结算订单更正销售归属", func(t *testing.T) {
		if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", october); err != nil {
			t.Fatal(err)
		}
		var seller, replacement, lineID, remaining int64
		if err := s.db.QueryRow(ctx, `SELECT id FROM people WHERE legacy_party_id=$1::uuid`, id(13)).Scan(&replacement); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO catalog_sales_people_sources(id,source,external_id,person_id,valid_from,observed_at)
		 SELECT $1,'DCAI','replacement',$2,'2026-01-01',captured_at FROM catalog_sales_context WHERE source='DCAI'`, id(30), replacement); err != nil {
			t.Fatal(err)
		}
		if _, err := editor.addCatalogAppointment(ctx, addCatalogAppointmentInput{PersonID: replacement, StartsAt: previousStart,
			catalogAppointmentTarget: catalogAppointmentTarget{Role: "SALES", Scope: "PROJECT", ScopeKey: "AI"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(ctx, `SELECT l.id,l.person_id,l.amount_minor+coalesce(sum(r.amount_minor),0)
		 FROM calculation_draft_lines l JOIN source_order_versions v ON v.id=l.source_version_id
		 LEFT JOIN calculation_draft_refunds r ON r.original_line_id=l.id AND r.period_id=$2
		 WHERE l.period_id=$1 AND l.component_type='sales' AND v.order_id='one' GROUP BY l.id`, periodID, sept).Scan(&lineID, &seller, &remaining); err != nil || remaining <= 0 {
			t.Fatalf("expected settled sales with a remaining balance: %d %v", remaining, err)
		}
		before := draftAt(october)
		baselineRefunds := draftAt(current)
		updated := time.Now().UTC().Truncate(time.Microsecond)
		saveWithSeller("one", "sales-reassigned", "replacement", 10000, boundary.Add(-time.Second), updated, "VIP_ORDER")
		var queued bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, current).Scan(&queued); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `UPDATE source_order_versions SET received_at=$1 WHERE version='sales-reassigned'`, octoberPayment); err != nil {
			t.Fatal(err)
		}
		saveRefund("one", "after-sales-adjustment", "1", 3000, time.Now().Add(-time.Second))
		tooEarly := draftAt(current)
		if tooEarly.CorrectionCount != 0 || tooEarly.IssueCount != baselineRefunds.IssueCount+1 {
			t.Fatalf("the later refund must wait before the earlier draft is first opened: %+v", tooEarly)
		}
		changed := draftAt(october)
		if changed.CorrectionCount != 1 || changed.CorrectionTotal != remaining || changed.IssueCount != before.IssueCount+1 || !queued {
			t.Fatalf("expected a %d credit and a pending recovery after sync: queued=%t, before=%+v after=%+v", remaining, queued, before, changed)
		}
		w := httptest.NewRecorder()
		s.getDraft(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d", october), nil))
		var detail struct {
			Corrections []struct {
				AmountMinor int64
				RuleVersion int
				PersonName  string
			}
			Issues []struct {
				ID       int64
				Recovery *struct{ PersonID, AmountMinor, RuleVersion int64 }
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != http.StatusOK || len(detail.Corrections) != 1 ||
			detail.Corrections[0].AmountMinor != remaining || detail.Corrections[0].RuleVersion != 1 || detail.Corrections[0].PersonName != "分配负责人" {
			t.Fatalf("correction detail did not retain rule v1: %d %v %s", w.Code, err, w.Body)
		}
		var issueID int64
		for _, issue := range detail.Issues {
			if issue.Recovery != nil && issue.Recovery.PersonID == seller && issue.Recovery.AmountMinor == -remaining && issue.Recovery.RuleVersion == 1 {
				issueID = issue.ID
			}
		}
		if issueID == 0 {
			t.Fatalf("missing pending recovery for the original person: %s", w.Body)
		}
		saveWithSeller("one", "sales-reassigned-display", "replacement", 10000, boundary.Add(-time.Second), updated.Add(time.Second), "VIP_ORDER")
		if repeat := draftAt(october); repeat.Version != changed.Version {
			t.Fatalf("display-only change published another correction draft: %+v", repeat)
		}
		token, err := s.loginFinanceAccount(ctx, financeLoginInput{Login: "settlement.fixture", Password: "isolated-close-fixture"})
		if err != nil {
			t.Fatal(err)
		}
		payload := fmt.Sprintf(`{"periodId":%d,"version":%d,"issueId":%d,"outcome":"recover"}`, october, changed.Version, issueID)
		request := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/correction-recovery", strings.NewReader(payload))
		request.Header.Set("Origin", "http://localhost")
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: financeAccountCookie, Value: token})
		w = httptest.NewRecorder()
		for _, route := range s.routes() {
			if route.Method == request.Method && route.Path == request.URL.Path {
				route.Handler.ServeHTTP(w, request)
				break
			}
		}
		if w.Code != http.StatusOK {
			t.Fatalf("authorized recovery not accepted: %d %s", w.Code, w.Body)
		}
		reviewed := draftAt(october)
		if reviewed.CorrectionCount != 2 || reviewed.CorrectionTotal != 0 || reviewed.IssueCount != before.IssueCount {
			t.Fatalf("two-sided correction expected after review: %+v", reviewed)
		}
		w = httptest.NewRecorder()
		s.draftReport(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/draft-report?periodId=%d&version=%d&dimension=person", october, reviewed.Version), nil))
		var report struct {
			Items []struct {
				Name            string
				CorrectionMinor int64
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != http.StatusOK {
			t.Fatalf("correction report: %d %v %s", w.Code, err, w.Body)
		}
		var credit, debit int64
		for _, row := range report.Items {
			switch row.Name {
			case "分配负责人":
				credit = row.CorrectionMinor
			case "销售乙":
				debit = row.CorrectionMinor
			}
		}
		if credit != remaining || debit != -remaining {
			t.Fatalf("report lost the two signed person entries: %d, %d", credit, debit)
		}
		if err := s.db.QueryRow(ctx, `SELECT person_id FROM calculation_draft_lines WHERE id=$1`, lineID).Scan(&seller); err != nil || seller == replacement {
			t.Fatalf("frozen recipient changed: %d %v", seller, err)
		}

		saveWithSeller("unclassified", "other-order-reassigned", "replacement", 1100, boundary, updated, "VIP_ORDER")
		saveRefund("unclassified", "earlier-refund-after-reassignment", "1", 100, octoberPayment.Add(time.Second))
		earlier := draftAt(october)
		if earlier.RefundCount != reviewed.RefundCount+5 || earlier.IssueCount != reviewed.IssueCount || earlier.CorrectionCount != reviewed.CorrectionCount {
			t.Fatalf("an earlier refund must not pick up a revision received in a later period before its draft exists: %+v", earlier)
		}
		if later := draftAt(current); later.CorrectionCount != 1 || later.IssueCount != baselineRefunds.IssueCount+2 {
			t.Fatalf("another order should reserve its own correction in the later period: %+v", later)
		}
		if repeat := draftAt(october); repeat.Version != earlier.Version {
			t.Fatalf("the later draft cannot change the earlier refund: %+v", repeat)
		}
		save("unclassified", "other-order-reassignment-reverted", 1100, boundary, updated.Add(time.Second), "VIP_ORDER")
		reviewed = draftAt(october)
		waiting := draftAt(current)
		var waitingFound bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calculation_draft_issues WHERE period_id=$1 AND code='CORRECTION_REFUND_WAITING')`, current).Scan(&waitingFound); err != nil ||
			!waitingFound || waiting.CorrectionCount != 0 || waiting.RefundCount != baselineRefunds.RefundCount+5 {
			t.Fatalf("refund must wait for the other open correction: %+v %t %v", waiting, waitingFound, err)
		}
		closeReviewed(reviewed, review(reviewed).Token)
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, current).Scan(&queued); err != nil || !queued {
			t.Fatalf("closing the correction must queue the waiting refund: %t %v", queued, err)
		}
		refunded := draftAt(current)
		if refunded.IssueCount != baselineRefunds.IssueCount+1 || refunded.RefundCount != waiting.RefundCount ||
			refunded.CorrectionCount != 1 || refunded.CorrectionTotal != 300 {
			t.Fatalf("refund after a booked correction must offset the frozen payee and review the new payee: %+v", refunded)
		}
		var payee, refundDebit int64
		if err := s.db.QueryRow(ctx, `SELECT recovery_person_id,recovery_minor FROM calculation_draft_issues
		 WHERE period_id=$1 AND code='CORRECTION_RECOVERY_PENDING'`, current).Scan(&payee, &refundDebit); err != nil || payee != replacement || refundDebit != -300 {
			t.Fatalf("wrong refund recipient or amount: %d %d %v", payee, refundDebit, err)
		}
		saveWithSeller("one", "sales-reassigned-amount-changed", "replacement", 9000, boundary.Add(-time.Second), updated.Add(2*time.Second), "VIP_ORDER")
		mismatch := draftAt(current)
		var conflictFound bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calculation_draft_issues WHERE period_id=$1 AND code='CORRECTION_SCOPE_CHANGED')`, current).Scan(&conflictFound); err != nil ||
			!conflictFound || mismatch.CorrectionCount != 0 || mismatch.IssueCount != baselineRefunds.IssueCount+1 {
			t.Fatalf("a payment amount change cannot be confirmed as an equivalent recipient change: %+v %t %v", mismatch, conflictFound, err)
		}
		// Isolate the database's deletion rule from pool-carry dependencies.
		onlyCorrection := createPeriod("2036-01", "仅更正")
		onlyRefund := createPeriod("2036-02", "仅退款")
		correctionDraft, refundDraft := draftAt(onlyCorrection), draftAt(onlyRefund)
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO calculation_draft_corrections(period_id,original_line_id,source_version_id,person_id,amount_minor)
			 SELECT $1,original_line_id,source_version_id,person_id,amount_minor FROM calculation_draft_corrections WHERE period_id=$2`, onlyCorrection, october); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO calculation_draft_refunds(period_id,original_line_id,refund_period_id,refund_source_version_id,amount_minor)
			 SELECT $1,original_line_id,refund_period_id,refund_source_version_id,amount_minor FROM calculation_draft_refunds
			 WHERE period_id=$2 AND original_line_id=$3`, onlyRefund, current, lineID); err != nil {
				return err
			}
			if _, err := insertSettlement(ctx, tx, onlyCorrection, correctionDraft.Version, "fixture", "", nil); err != nil {
				return err
			}
			_, err := insertSettlement(ctx, tx, onlyRefund, refundDraft.Version, "fixture", "", nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", onlyCorrection); err == nil || !strings.Contains(err.Error(), "booked adjustments") {
			t.Fatalf("cannot reopen a correction needed by a later settled refund: %v", err)
		}
		if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", onlyRefund); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", onlyCorrection); err != nil {
			t.Fatalf("reopening in reverse order should succeed: %v", err)
		}
	})

	t.Run("新增公司和销售套用默认比例", func(t *testing.T) {
		var person int64
		if err := s.db.QueryRow(ctx, "INSERT INTO people(name) VALUES('新销售') RETURNING id").Scan(&person); err != nil {
			t.Fatal(err)
		}
		if _, err := s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCAI",
			ExternalID: "new-seller", ValidFrom: boundary.Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
		if _, err := editor.addCatalogAppointment(ctx, addCatalogAppointmentInput{PersonID: person, StartsAt: boundary,
			catalogAppointmentTarget: catalogAppointmentTarget{Role: "SALES", Scope: "PROJECT", ScopeKey: "AI"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,kind,name,status)
		 VALUES($1,'COMPANY','新渠道公司','ACTIVE')`, id(60)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO organization_sales_categories(id,name,active) VALUES($1,'渠道类别',true)`, id(29)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
		 VALUES('DCAI','new-company',$1,$2)`, id(60), boundary); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from)
		 VALUES($1,$2,$3)`, id(60), id(29), boundary); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,kind,name,status)
		 VALUES($1,$2,'TEAM','新渠道小组','ACTIVE')`, id(65), id(60)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
		 VALUES($1,$2,$3,'PRIMARY',$4)`, id(66), person, id(65), boundary); err != nil {
			t.Fatal(err)
		}
		change := boundary.Add(time.Hour)
		if _, err := s.db.Exec(ctx, `INSERT INTO catalog_sales_company_aliases(id,source,external_id,company_uuid,company_unit_id,valid_from,valid_to,observed_at)
		 SELECT $1::uuid,source,'previous-alias',$3::uuid,$5::uuid,'2026-01-01'::timestamptz,$6::timestamptz,captured_at FROM catalog_sales_context WHERE source='DCAI'
		 UNION ALL SELECT $2::uuid,source,'next-alias',$4::uuid,$5::uuid,$6::timestamptz,NULL,captured_at FROM catalog_sales_context WHERE source='DCAI'`,
			id(61), id(62), id(63), id(64), id(60), change); err != nil {
			t.Fatal(err)
		}
		saveWithSeller("member-from-directory", "1", "new-seller", 10000, boundary, boundary, "VIP_ORDER")
		item := catalogPaymentMatch{OrderID: "member-from-directory", PaidAt: boundary, Status: "MATCHED", RuleID: id(4)}
		amount := int64(10000)
		item.PaidMinor = &amount
		if err := s.db.QueryRow(ctx, `UPDATE source_order_classifications SET sales_claims=jsonb_set(sales_claims,'{0,company}','"new-company"')
		 WHERE source_version_id=(SELECT id FROM source_order_versions WHERE order_id=$1) RETURNING source_version_id`, item.OrderID).
			Scan(&item.SourceVersionID); err != nil {
			t.Fatal(err)
		}
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			orders := []catalogPaymentMatch{item}
			data, err := loadVIPRuntimeData(ctx, tx, periodID, orders, true)
			if err != nil {
				return err
			}
			applyVIPQuotes(orders, data)
			quote := orders[0]
			if len(quote.SalesParts) != 1 || quote.SalesParts[0].Name != "新销售" || quote.SalesParts[0].Amount != 1500 {
				return fmt.Errorf("expected 15%% paid to newly appointed seller: %s %+v", quote.SalesStatus, quote.SalesParts)
			}
			req, issue := vipPaymentInputs(item, data)
			if issue != "" || req.Company != id(63) {
				return fmt.Errorf("company alias at the original payment: %q %q", req.Company, issue)
			}
			later := item
			later.PaidAt = change
			req, issue = vipPaymentInputs(later, data)
			if issue != "" || req.Company != id(64) {
				return fmt.Errorf("company alias at the change boundary: %q %q", req.Company, issue)
			}
			before := item
			before.PaidAt = boundary.Add(-time.Second)
			req, _ = vipPaymentInputs(before, data)
			if req.Seller != "" || req.Company != "" {
				return fmt.Errorf("directory links matched before their effective time: %+v", req)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		checkScope := func(unit string, count int) {
			t.Helper()
			w := httptest.NewRecorder()
			s.orders(w, httptest.NewRequest(http.MethodGet, "/api/v1/orders?source=DCAI&orderId="+item.OrderID+"&unitId="+unit, nil))
			var orders []sourceFact
			if err := json.Unmarshal(w.Body.Bytes(), &orders); w.Code != http.StatusOK || err != nil || len(orders) != count || count > 0 && orders[0].OrderID != item.OrderID {
				t.Fatalf("expected %d uncalculated orders under %s: %d, %s, %v", count, unit, w.Code, w.Body, err)
			}
		}
		checkScope(id(60), 1)
		checkScope(id(65), 1)
		checkScope(id(17), 0)
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO catalog_sales_people_sources(id,source,external_id,person_id,valid_from,observed_at)
			 SELECT $1,'DCAI','new-seller',p.id,$3,c.captured_at FROM people p CROSS JOIN catalog_sales_context c
			 WHERE p.legacy_party_id=$2 AND c.source='DCAI'`, id(67), id(12), boundary); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO catalog_sales_company_aliases(id,source,external_id,company_uuid,company_unit_id,valid_from,observed_at)
			 SELECT $1,'DCAI','new-company',$2,$3,$4,captured_at FROM catalog_sales_context WHERE source='DCAI'`, id(68), id(69), id(17), boundary)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		checkScope(id(17), 1)
		checkScope(id(18), 1)
		checkScope(id(60), 0)
		checkScope(id(65), 0)
	})

	t.Run("外部公司兜底按付款时的公司类型", func(t *testing.T) {
		may := createPeriod("2032-05", "外部公司默认标准验证")
		at := time.Date(2032, 5, 5, 12, 0, 0, 0, shanghai)
		var base string
		if err := s.db.QueryRow(ctx, `SELECT source_id::text FROM product_rule_versions WHERE source_status='PUBLISHED'
		 AND effective_from<$1 ORDER BY effective_from DESC LIMIT 1`, at).Scan(&base); err != nil {
			t.Fatal(err)
		}
		save("external-default", "1", 10000, at, at, "VIP_ORDER")
		save("internal-general", "1", 10000, at.Add(time.Second), at.Add(time.Second), "VIP_ORDER")
		if _, err := s.db.Exec(ctx, `UPDATE organization_units SET sales_company_type='INTERNAL',sales_company_type_version=2 WHERE id=$1`, id(17)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO organization_sales_company_type_changes(unit_id,version,previous_type,company_type,changed_at)
		 VALUES($1,2,'EXTERNAL','INTERNAL',$2)`, id(17), at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		rate := int64(425)
		input := publishCatalogRatesInput{BaseID: base, EffectiveFrom: at,
			NewRateRows: []catalogNewRateRow{{ItemKey: "sales", catalogConditionInput: catalogConditionInput{
				Company: "ANY", Relationship: "ANY", Rate: &rate, Payee: vipPayeeSalesCompany, ExternalDefault: true,
			}}}}
		w := preview(may, input)
		var result ruleImpactReport
		if err := json.Unmarshal(w.Body.Bytes(), &result); w.Code != http.StatusOK || err != nil || result.DeltaMinor != 425-newSalesRate {
			t.Fatalf("expected preview to replace the general rate only for the external sale: %d %s", w.Code, w.Body)
		}
		published, err := s.publishCatalogRates(ctx, input, "external-default-fixture")
		if err != nil {
			t.Fatal(err)
		}
		sheet, err := s.catalogRuleRateSheet(ctx, published.ID)
		if err != nil || !slices.ContainsFunc(sheet.Components, func(item catalogRateComponent) bool {
			return item.ItemKey == "sales" && slices.ContainsFunc(item.Rows, func(row catalogRateRow) bool {
				return row.Scope == "external" && row.Payee == vipPayeeSalesCompany && row.Rate == rate
			})
		}) {
			t.Fatalf("published company default could not be loaded: %+v %v", sheet, err)
		}
		got := draftAt(may)
		if got.IssueCount != 0 || got.Total != result.AfterMinor {
			t.Fatalf("saved calculation differs from preview: %+v", got)
		}
		for _, want := range []struct {
			order, company string
			amount         int64
		}{{"external-default", id(17), rate}, {"internal-general", "", newSalesRate}} {
			var company string
			var amount int64
			err := s.db.QueryRow(ctx, `SELECT coalesce(l.beneficiary_unit_id::text,''),l.amount_minor
			 FROM calculation_draft_lines l JOIN source_order_versions source ON source.id=l.source_version_id
			 WHERE l.period_id=$1 AND l.component_type='sales' AND source.order_id=$2`, may, want.order).Scan(&company, &amount)
			if err != nil || company != want.company || amount != want.amount {
				t.Fatalf("%+v: got company=%s amount=%d (%v)", want, company, amount, err)
			}
		}
	})
}
