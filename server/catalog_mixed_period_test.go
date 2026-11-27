package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMixedPeriodGroupsOrderSellerAndDeliveryInOneMonth(t *testing.T) {
	const definition = `{
 "amount_basis":"ACTUAL_RECEIVED","recognition_trigger":"PAID","refund_policy":"PROPORTIONAL","stacking_policy":"SUM",
 "rounding_policy":{"mode":"TRUNCATE","scale":2},"mixed_arrangements":true,
 "policy_scope":{"sales_systems":["DCAI"],"business_sku_codes":["VIP"],"financial_event_types":["order.paid"]},
 "settlement_policy":{"cycle":"MONTHLY_25_TO_24","negative_balance_policy":"CARRY_FORWARD"},
 "benefit_actions":[
 {"item_key":"sale","label":"成交销售","type":"TIER_RATE","metric_code":"MONTHLY_NET_RECEIPTS","metric_unit":"AMOUNT_MINOR",
 "per_order":true,"beneficiary_mode":"ORDER_SELLER","tier_table":[{"rate_bps":1000}]},
 {"item_key":"delivery","label":"交付","type":"TIER_RATE","metric_code":"MONTHLY_NET_RECEIPTS","metric_unit":"AMOUNT_MINOR",
 "beneficiary_mode":"PARTY","designated_party_id":"person:31","tier_table":[{"rate_bps":1000}]}]
}`
	parsed, err := parseCatalogPeriodRule(json.RawMessage(definition))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 25, 0, 0, 0, 0, shanghai)
	rules := []catalogAggregateRule{{ID: "v1", Version: 1, From: start, Definition: parsed}}
	directory := catalogAggregateDirectory{People: map[int64]string{31: "交付", 42: "成交人"}}
	performance := catalogPerformanceDirectory{
		people: map[int64]string{42: "成交人"},
		claims: map[int64]vipClaim{1: {User: "seller"}, 2: {User: "seller"}},
		users:  [2]map[string][]vipLink{{"seller": {{Key: "seller", Value: "42"}}}},
	}
	events := []catalogActivityEvent{
		{EventID: 1, PaymentID: 1, Scenario: "VIP", PaidAt: start, Amount: 10009},
		{EventID: 2, PaymentID: 2, Scenario: "VIP", PaidAt: start.AddDate(0, 0, 12), Amount: 10009},
	}
	report, err := groupCatalogPeriodEvents(events, rules, directory, nil, performance, start.AddDate(0, 1, 0))
	if err != nil || len(report.Issues) != 0 {
		t.Fatalf("mixed calculation failed: %+v %v", report.Issues, err)
	}
	total, counts := map[int64]int64{}, map[int64]int{}
	for _, group := range report.Groups {
		person := group.posting.Targets[0].Person
		total[person] += group.quote().Total
		counts[person]++
	}
	if total[42] != 2000 || counts[42] != 2 || total[31] != 2001 || counts[31] != 1 {
		t.Fatalf("expected two per-order postings totaling 2000 and one whole-period allocation of 2001, got totals %v groups %v", total, counts)
	}
}

func TestMixed499RuleSettlesOneMonthAndRefundsOriginalSeller(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	s := workspace{db: db, development: true, sources: map[string]sourceConfig{
		"DCAI": {BaseURL: "https://fixture.invalid", Token: "fixture"},
	}}
	_, err := db.Exec(ctx, `
 INSERT INTO products(id,code,name,period_mode,cycle_start_day,settlement_enabled) OVERRIDING SYSTEM VALUE
 VALUES(501,'DCAI_VIP','AI软件499会员','calendar',25,true),
 (502,'DCAI_HEALTH_AMBASSADOR','健康大使','calendar',25,true);
 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
 VALUES(501,gen_random_uuid(),'test','DCAI','DCAI','vip','2025-11-01',sha256('fixture'::bytea)),
 (502,gen_random_uuid(),'test','DCAI','DCAI','ambassador','2025-11-01',sha256('other'::bytea));
 INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,
  sku_code,sku_label,mapping_version,effective_from,observed_at,source_product_code)
 VALUES(gen_random_uuid(),501,'DCAI','VIP','VIP_ORDER','ai_vip_order','vip','vip','VIP499','会员',1,'2025-09-01','2025-11-01',NULL),
 (gen_random_uuid(),502,'DCAI','HEALTH_IDENTITY','HEALTH_IDENTITY','ai_health_ambassador_order','ambassador','ambassador',
  'AMBASSADOR','健康大使',1,'2025-09-01','2025-11-01','HEALTH_AMBASSADOR');
 INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES (5011,'成交销售'),(5012,'交付'),(5013,'期中到岗负责人');
 INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES (5011,'DCAI','seller','2025-09-01');
 INSERT INTO organization_units(id,kind,name,status) VALUES ('51000499-0000-4000-8000-000000000001','COMPANY','验收组织','ACTIVE');
 INSERT INTO organization_units(id,kind,name,status) VALUES ('51000499-0000-4000-8000-000000000002','COMPANY','销售公司','ACTIVE');
 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from) VALUES
  (gen_random_uuid(),5011,'51000499-0000-4000-8000-000000000002','PRIMARY','2025-09-01'),
  (gen_random_uuid(),5012,'51000499-0000-4000-8000-000000000001','PRIMARY','2025-09-01');
 INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest) VALUES('DCAI','2025-11-01',sha256('fixture'::bytea));
 INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
 VALUES(gen_random_uuid(),'DCAI',5013,'TEAM_LEAD','ORGANIZATION_UNIT','51000499-0000-4000-8000-000000000001','2025-10-03T00:00Z','2025-11-01');
 WITH payment AS (
  INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
  VALUES('DCAI','101','','ai_vip_order:101','1','ORDER_PAID','2025-10-02T00:00Z','2025-10-02T00:00Z',
   '{"domain":"VIP","sourceKey":"vip","paymentId":"101","amountMinor":10000}'),
   ('DCAI','102','','ai_vip_order:102','1','ORDER_PAID','2025-10-03T00:00Z','2025-10-03T00:00Z',
    '{"domain":"VIP","sourceKey":"vip","paymentId":"102","amountMinor":20000}')
  RETURNING id)
 INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier,sales_claims)
 SELECT id,'VIP_ORDER','vip','vip','[{"role":"PID_USER","user":"seller","relationship":"DIRECT"}]'::jsonb FROM payment;`)
	if err != nil {
		t.Fatal(err)
	}
	options, err := readCatalogPeriodScenarioOptions(ctx, db, 501, nil, nil)
	if err != nil || len(options) != 2 {
		t.Fatalf("fixture must offer both project SKUs to the legacy editor: %+v (%v)", options, err)
	}
	wSetup := httptest.NewRecorder()
	s.getCatalogPeriodRule(wSetup, httptest.NewRequest(http.MethodGet,
		"/api/v1/catalog-period-rules?productId=501&mixedArrangements=1", nil))
	var setup catalogPeriodVersion
	if err := json.Unmarshal(wSetup.Body.Bytes(), &setup); err != nil || wSetup.Code != http.StatusOK ||
		!setup.Mixed || setup.Options == nil || len(setup.Options.Scenarios) != 1 || setup.Options.Scenarios[0].Code != "VIP499" {
		t.Fatalf("499 editor must only offer the product's membership SKU: %d %s (%v)", wSetup.Code, wSetup.Body, err)
	}
	if _, err := db.Exec(ctx, `UPDATE source_write_state SET provider_key=$1 WHERE source='DCAI'`, s.dcaiSourceKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,next_updated_at,next_id,completed_through,last_success_at)
 VALUES('VIP_ORDERS',$1,'2025-09-01','2025-09-01','0',now(),now())`, s.dcaiSourceKey()); err != nil {
		t.Fatal(err)
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/catalog-period-rules", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		s.postCatalogPeriodRule(w, req)
		return w
	}
	const input = `{
 "create":{"productId":501,"label":"499统一分润","cycle":"MONTHLY_25_TO_24","mixedArrangements":true},
 "effectiveFrom":"2025-09-25T00:00:00+08:00","scenarioCodes":["VIP499"],"components":[
  {"label":"销售","targetKind":"order_seller","perOrder":true,"tiers":[{"rate_bps":1000}]},
  {"label":"交付","targetKind":"person","personId":5012,"tiers":[{"rate_bps":500}]},
  {"label":"负责人","targetKind":"person","personId":5013,"tiers":[{"rate_bps":300}],
   "appointment":{"personId":5013,"unitId":"51000499-0000-4000-8000-000000000001","role":"TEAM_LEAD"}}
 ]}`
	w := post(input)
	var publication catalogPublication
	if err := json.Unmarshal(w.Body.Bytes(), &publication); err != nil || w.Code != 200 || publication.ID == "" {
		t.Fatalf("publish mixed rule: %d %s (%v)", w.Code, w.Body, err)
	}
	if duplicate := post(strings.Replace(input, "499统一分润", "另一条规则", 1)); duplicate.Code != http.StatusConflict {
		t.Fatalf("second rule could duplicate payouts: %d %s", duplicate.Code, duplicate.Body)
	}
	backdate := post(fmt.Sprintf(`{"baseId":%q,"effectiveFrom":"2025-10-25T00:00:00+08:00",
	  "components":[{"label":"变更","targetKind":"order_seller","perOrder":true,"tiers":[{"rate_bps":1500}]}]}`, publication.ID))
	if backdate.Code != http.StatusConflict {
		t.Fatalf("started period allowed a new version: %d %s", backdate.Code, backdate.Body)
	}

	open := func(month string) draftSummary {
		t.Helper()
		result, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: publication.ID, Month: month})
		if err != nil {
			t.Fatal(err)
		}
		return result.(draftSummary)
	}
	draft := open("2025-10")
	if draft.Total != 5100 || draft.LineCount != 4 || draft.IssueCount != 0 {
		t.Fatalf("unexpected combined draft %+v", draft)
	}
	var sales, delivery, leader int64
	err = db.QueryRow(ctx, `SELECT sum(amount_minor) FILTER(WHERE person_id=5011),
	  sum(amount_minor) FILTER(WHERE person_id=5012),sum(amount_minor) FILTER(WHERE person_id=5013)
	  FROM calculation_draft_lines WHERE period_id=$1`, draft.PeriodID).Scan(&sales, &delivery, &leader)
	if err != nil || sales != 3000 || delivery != 1500 || leader != 600 {
		t.Fatalf("seller, delivery and time-limited leader: %d %d %d (%v)", sales, delivery, leader, err)
	}
	var salesOrganizationLines, otherOrganizationLines int
	err = db.QueryRow(ctx, `SELECT
	 count(*) FILTER(WHERE person_id=5011 AND company_id='51000499-0000-4000-8000-000000000002'),
	 count(*) FILTER(WHERE person_id IN (5012,5013) AND company_id='51000499-0000-4000-8000-000000000001')
	 FROM calculation_draft_lines WHERE period_id=$1`, draft.PeriodID).Scan(&salesOrganizationLines, &otherOrganizationLines)
	if err != nil || salesOrganizationLines != 2 || otherOrganizationLines != 2 {
		t.Errorf("seller and designated recipients need company attribution: %d, %d (%v)", salesOrganizationLines, otherOrganizationLines, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
	 VALUES('DCAI','102','','ai_vip_order:refund-102','1','REFUND_SUCCEEDED','2025-10-18','2025-10-18',
	  '{"domain":"VIP","sourceKey":"vip","refundId":"refund-102","amountMinor":2000,
	   "refundsPayment":[{"orderId":"102","recordId":"102"}]}')`); err != nil {
		t.Fatal(err)
	}
	draft, err = s.calculateCurrentDraft(ctx, draft.PeriodID, 0)
	if err != nil || draft.Total != 4740 || draft.RefundCount != 0 {
		t.Fatalf("same-period refund should net into current commissions: %+v (%v)", draft, err)
	}
	var performanceCount int
	var netPerformance int64
	err = db.QueryRow(ctx, `SELECT count(*),coalesce(sum(`+performancePostingMinorSQL+`),0)
	 FROM calculation_draft_performance a JOIN source_order_versions v ON v.id=a.source_version_id
	 LEFT JOIN source_order_versions prior ON prior.id=a.prior_refund_version_id
	 WHERE a.period_id=$1 AND a.person_id=5011 AND a.company_id='51000499-0000-4000-8000-000000000002'`,
		draft.PeriodID).Scan(&performanceCount, &netPerformance)
	if err != nil || performanceCount != 3 || netPerformance != 28000 {
		t.Errorf("payment/refund performance must post once per event: %d rows, %d net (%v)", performanceCount, netPerformance, err)
	}
	if retry, err := s.calculateCurrentDraft(ctx, draft.PeriodID, 0); err != nil || retry.Version != draft.Version {
		t.Errorf("retry changed the unchanged draft version from %d to %d", draft.Version, retry.Version)
	}
	w = httptest.NewRecorder()
	s.getSettlementReview(w, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", draft.PeriodID, draft.Version), nil))
	var review settlementReview
	if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil || w.Code != 200 || review.Token == "" || review.Total != 4740 {
		t.Fatalf("combined settlement review: %d %s (%v)", w.Code, w.Body, err)
	}
	inputConfirm := confirmSettlementInput{PeriodID: draft.PeriodID, ExpectedVersion: draft.Version, ReviewToken: review.Token}
	confirmed, err := s.confirmSettlement(ctx, inputConfirm, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.confirmSettlement(ctx, inputConfirm, "fixture")
	if err != nil || repeated.ID != confirmed.ID {
		t.Fatalf("confirmation retry: %+v %v", repeated, err)
	}
	// Correcting an assignment after confirmation must not move a later refund.
	if _, err := db.Exec(ctx, `UPDATE organization_assignments SET unit_id='51000499-0000-4000-8000-000000000001'
	 WHERE person_id=5011`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
 VALUES('DCAI','101','','refund:101','1','REFUND_SUCCEEDED','2025-10-28','2025-10-28',
 '{"domain":"VIP","sourceKey":"vip","refundId":"refund-101","amountMinor":5000,
   "refundsPayment":[{"orderId":"101","recordId":"101"}]}')`); err != nil {
		t.Fatal(err)
	}
	next := open("2025-11")
	if next.Total != -500 || next.RefundCount != 1 || next.IssueCount != 0 {
		t.Fatalf("later refund did not recover the original seller exactly once: %+v", next)
	}
	var refundPerformanceCompany string
	var refundPerformancePerson int64
	err = db.QueryRow(ctx, `SELECT company_id::text,person_id FROM calculation_draft_performance
	 WHERE period_id=$1 AND payment_version_id IS NOT NULL`, next.PeriodID).Scan(&refundPerformanceCompany, &refundPerformancePerson)
	if err != nil || refundPerformanceCompany != "51000499-0000-4000-8000-000000000002" || refundPerformancePerson != 5011 {
		t.Errorf("cross-period refund performance should use the original seller and company: %q, %d (%v)",
			refundPerformanceCompany, refundPerformancePerson, err)
	}
	var originalPeriod, originalSeller, paymentID, adjustment int64
	err = db.QueryRow(ctx, `SELECT l.period_id,l.person_id,l.source_version_id,r.amount_minor
 FROM calculation_draft_refunds r JOIN calculation_draft_lines l ON l.id=r.original_line_id
 WHERE r.period_id=$1`, next.PeriodID).Scan(&originalPeriod, &originalSeller, &paymentID, &adjustment)
	if err != nil || originalPeriod != draft.PeriodID || originalSeller != 5011 || paymentID == 0 || adjustment != -500 {
		t.Fatalf("refund did not reference the seller's original order: %d %d %d %d (%v)", originalPeriod, originalSeller, paymentID, adjustment, err)
	}
}
