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
	"github.com/jackc/pgx/v5/pgtype"
)

func TestVIPCompanyCorrectionMovesRemainingAwardAndKeepsPool(t *testing.T) {
	const rule = "60000000-0000-4000-8000-000000000001"
	const originalCompany = "60000000-0000-4000-8000-000000000002"
	const replacementCompany = "60000000-0000-4000-8000-000000000003"
	const poolUnit = "60000000-0000-4000-8000-000000000004"
	unit := func(id string) pgtype.UUID {
		t.Helper()
		var value pgtype.UUID
		if err := value.Scan(id); err != nil {
			t.Fatal(err)
		}
		return value
	}
	paid := int64(100000)
	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	fact := sourceFact{OrderID: "order", PaymentID: "payment", Domain: "VIP", SourceKey: "vip", AmountMinor: &paid, OccurredAt: at}
	payment := vipCorrectionPayment{Anchor: 1, OriginalID: 1, LatestID: 8, SameClassification: true, Original: fact, Latest: fact,
		Frozen: []vipCorrectionPosting{
			{ID: 1, Line: draftLine{Amount: 10000, Base: paid, Component: "company",
				Catalog: &catalogDraftBenefit{Rule: unit(rule), Unit: unit(originalCompany), Rate: 1000}}},
			{ID: 2, Line: draftLine{Amount: 1000, Base: paid, Component: "pool",
				Catalog: &catalogDraftBenefit{Rule: unit(rule), Unit: unit(poolUnit), PoolID: 3, Rate: 100, AllocationOwnerID: 11}}},
		}}
	match := func(id int64, company string) catalogPaymentMatch {
		row := payment.match(id)
		row.SalesStatus = vipSalesNotConfigured
		row.BenefitParts = []vipBenefitPart{
			{Item: "company", Kind: "company", Unit: company, Amount: 10000, Rate: 1000, Status: "CALCULATED"},
			{Item: "pool", Kind: "pool", Unit: poolUnit, Pool: "team-pool:3", Owner: "owner", Amount: 1000, Rate: 100, Status: "CALCULATED"},
		}
		return row
	}
	data := vipRuntimeData{PersonIDs: map[string]int64{"owner": 11}}
	balances := map[int64]*correctionBalance{
		1: {organizations: map[pgtype.UUID]int64{unit(originalCompany): 7500}, refundDebit: 2500},
		2: {organizations: map[pgtype.UUID]int64{unit(poolUnit): 750}, refundDebit: 250},
	}
	old, latest := match(1, originalCompany), match(8, replacementCompany)
	var first paymentDraft
	if err := payment.append(&first, old, latest, balances, data); err != nil || len(first.Issues) != 0 ||
		len(first.Corrections) != 2 || first.CorrectionTotal != 0 {
		t.Fatalf("expected a balanced company transfer after a partial refund: %+v %v", first, err)
	}
	if first.Corrections[0].Unit != unit(originalCompany) || first.Corrections[0].Amount != -7500 ||
		first.Corrections[1].Unit != unit(replacementCompany) || first.Corrections[1].Amount != 7500 {
		t.Fatalf("wrong company transfer: %+v", first.Corrections)
	}
	// The generic refund still debits its frozen line. A later draft offsets
	// that provisional debit and moves it to the new company exactly once.
	balances[1].organizations = map[pgtype.UUID]int64{unit(originalCompany): -2500, unit(replacementCompany): 7500}
	balances[1].refundDebit = 5000
	var later paymentDraft
	if err := payment.append(&later, old, latest, balances, data); err != nil || len(later.Issues) != 0 ||
		len(later.Corrections) != 2 || later.CorrectionTotal != 0 ||
		later.Corrections[0].Unit != unit(originalCompany) || later.Corrections[0].Amount != 2500 ||
		later.Corrections[1].Unit != unit(replacementCompany) || later.Corrections[1].Amount != -2500 {
		t.Fatalf("later refund must follow the corrected company: %+v %v", later, err)
	}
	latest.BenefitParts[1].Pool = "team-pool:9"
	var blocked paymentDraft
	if err := payment.append(&blocked, old, latest, balances, data); err != nil || len(blocked.Corrections) != 0 ||
		len(blocked.Issues) != 1 || blocked.Issues[0].Code != "CORRECTION_LINKED_REVIEW" {
		t.Fatalf("a change to the pool must require review before any adjustment: %+v %v", blocked, err)
	}
}

func TestVIPCompanyCorrectionPostsInOpenPeriodAndRoutesLaterRefund(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	s := workspace{db: db, sources: map[string]sourceConfig{"DCAI": {BaseURL: "http://127.0.0.1:9126", Token: "isolated-company-correction"}}}
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const oldCompany = "63000000-0000-4000-8000-000000000001"
	const newCompany = "63000000-0000-4000-8000-000000000002"
	const packageID = "63000000-0000-4000-8000-000000000003"
	const versionID = "63000000-0000-4000-8000-000000000004"
	must(`INSERT INTO products(id,code,name,period_mode,cycle_start_day,settlement_enabled) OVERRIDING SYSTEM VALUE
	 VALUES(900,'COMPANY_CORRECTION','大春 AI 验收','calendar',25,true);
	  INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
	 VALUES(900,gen_random_uuid(),'test','AI','DCAI','vip','2026-08-01',sha256('fixture'::bytea));
	  INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,sku_code,sku_label,mapping_version,effective_from,observed_at)
	 VALUES(gen_random_uuid(),900,'DCAI','VIP','VIP_ORDER','ai_vip_order','dcai:vip:new','dcai.vip-lifecycle.v1','VIP','会员',1,'2026-07-01','2026-08-01');
	  INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest,benefit_ready)
	 VALUES('DCAI','2026-08-01',sha256('fixture'::bytea),true);
	  INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(990,'销售');
	  INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(990,'DCAI','seller','2026-07-01');
	  INSERT INTO periods(id,product_id,name,ordinal,starts_on,ends_on,calendar_month) OVERRIDING SYSTEM VALUE
	 VALUES(901,900,'八月',202608,'2026-07-25','2026-08-24','2026-08-01'),
	       (902,900,'九月',202609,'2026-08-25','2026-09-24','2026-09-01'),
	       (903,900,'十月',202610,'2026-09-25','2026-10-24','2026-10-01');`)
	must(`INSERT INTO organization_units(id,kind,name,status) VALUES($1,'COMPANY','原公司','ACTIVE'),($2,'COMPANY','新公司','ACTIVE')`, oldCompany, newCompany)
	must(`INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
	 VALUES('DCAI','old',$1,'2026-07-01'),('DCAI','new',$2,'2026-07-01')`, oldCompany, newCompany)
	must(`INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
	 VALUES($1,900,'vip','公司提成','AI','COMPONENT_RULE','BUSINESS_SCENARIO','VIP',false)`, packageID)
	must(`INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at,published_at)
	 VALUES($1,$2,1,'PUBLISHED','2026-07-01','fixture',
	 '{"schema_version":"1.1","recognition_trigger":"PAID","settlement_policy":{"cycle":"MONTHLY_25_TO_24"},
	   "policy_scope":{"business_sku_codes":["VIP"],"sales_systems":["DCAI"],"financial_event_types":["order.paid"]},
	   "amount_basis":"ACTUAL_RECEIVED","refund_policy":"PROPORTIONAL","rounding_policy":{"mode":"HALF_UP","scale":2},
	   "stacking_policy":"SUM","sales_attribution":"ORDER_SELLER","benefit_actions":[
	     {"item_key":"company","label":"公司提成","type":"RATE","rate_bps":1000,"beneficiary_mode":"ORDER_SALES_COMPANY","role":"ORDER_SALES_COMPANY"}]}',
	   '2026-08-01','2026-07-01')`, versionID, packageID)
	save := func(f sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{f}, s.dcaiSourceKey())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	calculate := func(period int64) draftSummary {
		t.Helper()
		result, err := s.calculateCurrentDraft(ctx, period, 0)
		if err != nil || result.IssueCount != 0 {
			var codes []string
			if readErr := db.QueryRow(ctx, `SELECT coalesce(array_agg(code ORDER BY id),'{}')
			 FROM calculation_draft_issues WHERE period_id=$1`, period).Scan(&codes); readErr != nil {
				t.Fatal(readErr)
			}
			t.Fatalf("period %d calculated with issues %v: %+v %v", period, codes, result, err)
		}
		return result
	}
	// The isolated fixture has complete provider coverage; use the normal
	// confirmation transaction so the next month reads immutable postings.
	closePeriod := func(period int64, result draftSummary) {
		t.Helper()
		w := httptest.NewRecorder()
		s.getSettlementReview(w, httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", period, result.Version), nil))
		var review settlementReview
		if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil || w.Code != http.StatusOK || review.Token == "" {
			t.Fatalf("period %d must be ready to confirm: %d %s %v", period, w.Code, w.Body, err)
		}
		if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: period, ExpectedVersion: result.Version, ReviewToken: review.Token}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	paid := int64(100000)
	at := time.Date(2026, 8, 5, 10, 0, 0, 0, shanghai)
	order := sourceFact{Source: "DCAI", Kind: "ORDER_PAID", RecordKey: "ai_vip_order:1001", OrderID: "1001", PaymentID: "1001",
		Version: "1", Domain: "VIP", SourceKey: "vip", AmountMinor: &paid, OccurredAt: at, UpdatedAt: at,
		Classification: classifySourcePayment("DCAI", "VIP", "VIP_ORDER", "ai_vip_order", "VIP"),
		VIPClaims:      []vipClaim{{Role: "PID_USER", User: "seller", Company: "old", Relationship: "DIRECT"}}}
	save(order)
	must(`INSERT INTO dcai_order_sync(stream,source_key,initial_from,completed_through,last_success_at,next_updated_at,next_id)
	 VALUES('VIP_ORDERS',$1,'2026-07-01',now()-interval '1 minute',now(),now(),'0')`, s.dcaiSourceKey())
	original := calculate(901)
	if original.Total != 10000 || original.LineCount != 1 {
		t.Fatalf("expected 100.00 to the original company: %+v", original)
	}
	closePeriod(901, original)
	refund := func(id string, date time.Time) {
		t.Helper()
		amount := int64(25000)
		save(sourceFact{Source: "DCAI", Kind: "REFUND_SUCCEEDED", RecordKey: "ai_vip_order:" + id, OrderID: "1001", RefundID: id,
			Domain: "VIP", SourceKey: "vip", Version: "1", AmountMinor: &amount, OccurredAt: date, UpdatedAt: date,
			RefundsPayment: []sourcePaymentReference{{OrderID: "1001", RecordID: "1001"}}})
	}
	september := time.Date(2026, 9, 5, 10, 0, 0, 0, shanghai)
	order.Version, order.UpdatedAt, order.VIPClaims[0].Company = "2", september, "new"
	save(order)
	must("UPDATE source_order_versions SET received_at=$1 WHERE order_id='1001' AND version='2'", september)
	refund("refund-a", september)
	adjusted := calculate(902)
	if adjusted.CorrectionCount != 2 || adjusted.CorrectionTotal != 0 || adjusted.RefundTotal != -2500 || adjusted.Total != -2500 {
		t.Fatalf("correction and first refund must share the open accounting period: %+v", adjusted)
	}
	checkDeltas := func(period, old, new int64) {
		t.Helper()
		var gotOld, gotNew int64
		err := db.QueryRow(ctx, `SELECT coalesce(sum(amount_minor) FILTER(WHERE beneficiary_unit_id=$2),0),
		 coalesce(sum(amount_minor) FILTER(WHERE beneficiary_unit_id=$3),0)
		 FROM calculation_draft_corrections WHERE period_id=$1`, period, oldCompany, newCompany).Scan(&gotOld, &gotNew)
		if err != nil || gotOld != old || gotNew != new {
			t.Fatalf("period %d company adjustments %d/%d, want %d/%d: %v", period, gotOld, gotNew, old, new, err)
		}
	}
	checkDeltas(902, -7500, 7500)
	closePeriod(902, adjusted)
	refund("refund-b", time.Date(2026, 10, 5, 10, 0, 0, 0, shanghai))
	later := calculate(903)
	if later.RefundTotal != -2500 || later.CorrectionCount != 2 || later.CorrectionTotal != 0 || later.Total != -2500 {
		t.Fatalf("later refund should debit the new company only: %+v", later)
	}
	checkDeltas(903, 2500, -2500)
	var beneficiary string
	var amount int64
	if err := db.QueryRow(ctx, `SELECT beneficiary_unit_id::text,amount_minor FROM calculation_draft_lines WHERE period_id=901`).Scan(&beneficiary, &amount); err != nil ||
		beneficiary != oldCompany || amount != 10000 {
		t.Fatalf("frozen result changed: %s %d %v", beneficiary, amount, err)
	}
}
