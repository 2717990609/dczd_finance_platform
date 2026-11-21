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

	"github.com/golang-jwt/jwt/v4"
	"github.com/jackc/pgx/v5"
)

func TestCreateFirstCatalogScenarioRuleMatchesAndQueuesPayment(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	for _, sql := range []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE VALUES(950,'DCAI_FIRST','新产品','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		 VALUES(950,gen_random_uuid(),'test','DCAI','DCAI','new','2026-11-01',sha256('fixture'::bytea))`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,
		 sku_code,sku_label,mapping_version,effective_from,observed_at)
		 VALUES(gen_random_uuid(),950,'DCAI','VIP','VIP_ORDER','ai_vip_order','dcai:vip:new','dcai.vip-lifecycle.v1','FIRST','新报 SKU',1,'2026-09-01','2026-11-01')`,
		`INSERT INTO periods(id,product_id,name,ordinal,starts_on,ends_on,calendar_month) OVERRIDING SYSTEM VALUE
		 VALUES(951,950,'十月',202610,'2026-09-25','2026-10-24','2026-10-01')`,
		`INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(9501,'成交销售样本'),(9502,'原推荐人'),(9503,'更正推荐人')`,
		`INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES
		 (9501,'DCAI','seller','2026-09-01'),(9502,'DCAI','referrer','2026-09-01'),(9503,'DCAI','replacement','2026-09-01')`,
		`INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest,benefit_ready)
		 VALUES('DCAI','2026-11-01',sha256('fixture'::bytea),true)`,
		`INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
		 VALUES(gen_random_uuid(),'DCAI',9501,'SALES','PROJECT','DCAI','2026-09-01','2026-11-01')`,
		`INSERT INTO organization_units(id,kind,name,status) VALUES
		 ('10000000-0000-4000-8000-000000000953','COMPANY','收款公司样本','ACTIVE'),
		 ('10000000-0000-4000-8000-000000000955','COMPANY','特例乙','ACTIVE'),
		 ('10000000-0000-4000-8000-000000000956','COMPANY','类别默认公司','ACTIVE')`,
		`INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
		 VALUES('DCAI','seller-company','10000000-0000-4000-8000-000000000953','2026-09-01'),
		 ('DCAI','special-company','10000000-0000-4000-8000-000000000955','2026-09-01'),
		 ('DCAI','standard-company','10000000-0000-4000-8000-000000000956','2026-09-01')`,
		`INSERT INTO organization_sales_categories(id,name,active) VALUES
		 ('10000000-0000-4000-8000-000000000957','类别甲',true),('10000000-0000-4000-8000-000000000958','类别乙',true)`,
		`INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from) VALUES
		 ('10000000-0000-4000-8000-000000000953','10000000-0000-4000-8000-000000000957','2026-09-01'),
		 ('10000000-0000-4000-8000-000000000956','10000000-0000-4000-8000-000000000958','2026-09-01')`,
		`INSERT INTO catalog_benefit_organizations(source,party_uuid,unit_id,observed_at)
		 VALUES('DCAI','10000000-0000-4000-8000-000000000954','10000000-0000-4000-8000-000000000953','2026-11-01')`,
		`WITH payment AS (INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
		 VALUES('DCAI','101','','ai_vip_order:101','1','ORDER_PAID','2026-10-02T00:00Z','2026-10-02T00:00Z',
		 '{"domain":"VIP","sourceKey":"new","orderId":"101","paymentId":"101","amountMinor":10000,"occurredAt":"2026-10-02T00:00:00Z",
		 "participants":[{"role":"DIRECT_PID_USER","userId":"referrer"}]}'),
		 ('DCAI','102','','ai_vip_order:102','1','ORDER_PAID','2026-10-02T00:00Z','2026-10-02T00:00Z',
		 '{"domain":"VIP","sourceKey":"new","paymentId":"102","amountMinor":10000}') RETURNING id,order_id)
		 INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier,sales_claims)
		 SELECT id,'VIP_ORDER','dcai:vip:new','dcai.vip-lifecycle.v1',jsonb_build_array(jsonb_build_object(
		 'role','PID_USER','user','seller','relationship','DIRECT','company',CASE order_id WHEN '101' THEN 'seller-company' ELSE 'standard-company' END)) FROM payment`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	s := workspace{db: db, development: true, ruleAuth: catalogRuleAuth{issuer: "finance-test", key: []byte("isolated-finance-scenario-test-key")},
		sources: map[string]sourceConfig{"DCAI": {BaseURL: "https://fixture.invalid", Token: "fixture"}}}
	post := func(productGrant int64, preview bool) *httptest.ResponseRecorder {
		t.Helper()
		claims := catalogRuleClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: s.ruleAuth.issuer, Subject: "fixture",
			Audience: jwt.ClaimStrings{"finance-platform"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))},
			Permissions: []string{catalogRulePublishPermission}, ProductIDs: []int64{productGrant}}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.ruleAuth.key)
		if err != nil {
			t.Fatal(err)
		}
		body := `{
		 "effectiveFrom":"2026-10-01T00:00:00+08:00","create":{"productId":950,"sku":"FIRST","label":"首版分润",
		 "salesRates":[{"company":"ANY","categoryIds":["10000000-0000-4000-8000-000000000957","10000000-0000-4000-8000-000000000958"],"relationship":"ANY","rateBps":1000,"payee":"sales_company"},
		 {"companyIds":["10000000-0000-4000-8000-000000000953","10000000-0000-4000-8000-000000000955"],"relationship":"ANY","rateBps":1250}]},
		 "newItems":[{"label":"公司安排","target":"company","organization":"10000000-0000-4000-8000-000000000954","rateBps":250},
		 {"label":"固定交付","target":"company","organization":"10000000-0000-4000-8000-000000000954","fixedMinor":501},
		 {"label":"推荐提成","target":"referrer","rateBps":1000}]}`
		path := "/api/v1/catalog-rule-rates"
		if preview {
			path = "/api/v1/catalog-rule-impact"
			body = `{"periodId":951,"rule":` + body + `}`
		}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		if preview {
			s.postCatalogRuleImpact(w, r)
		} else {
			s.postCatalogRuleRates(w, r)
		}
		return w
	}
	if denied := post(999, true); denied.Code != http.StatusForbidden {
		t.Errorf("another product's grant authorized the first-rule preview: %d %s", denied.Code, denied.Body)
	}
	w := post(950, true)
	var preview ruleImpactReport
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || w.Code != http.StatusOK ||
		preview.CurrentRuleAvailable || preview.MatchedPaymentCount == nil || *preview.MatchedPaymentCount != 2 ||
		preview.BeforeMinor != 0 || preview.AfterMinor != 4752 || preview.DeltaMinor != 4752 ||
		preview.BeforeIssueCount != 2 || preview.AfterIssueCount != 0 || len(preview.Items) != 7 {
		t.Errorf("first rule should be compared with the previously unmatched payments: %d %s %v", w.Code, w.Body, err)
	}
	if len(preview.IssueReasons) != 1 || preview.IssueReasons[0] != (ruleImpactIssueReason{Code: "RULE_MISSING", BeforeCount: 2}) {
		t.Errorf("preview must show why the existing payments were blocked: %+v", preview.IssueReasons)
	}
	wantRecipients := map[string]int64{"成交销售样本": 1250, "原推荐人": 1000, "收款公司样本": 1502, "类别默认公司": 1000}
	for _, item := range preview.Items {
		wantRecipients[item.Recipient] -= item.AfterMinor
		if item.Recipient == "收款公司样本" {
			want, named := map[string]int64{"公司安排": 250, "固定交付": 501}[item.Arrangement]
			if !named || item.AfterMinor != want {
				t.Errorf("company's two arrangements must be itemized: %+v", item)
			}
		}
	}
	if w.Code == http.StatusOK {
		for recipient, outstanding := range wantRecipients {
			if outstanding != 0 {
				t.Errorf("preview attributed the wrong amount to %s, difference %d", recipient, outstanding)
			}
		}
	}
	var versions, drafts, queuedDrafts int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM product_rule_versions),
	 (SELECT count(*) FROM calculation_drafts), (SELECT count(*) FROM draft_refresh_queue)`).Scan(&versions, &drafts, &queuedDrafts); err != nil ||
		versions != 0 || drafts != 0 || queuedDrafts != 0 {
		t.Fatalf("preview persisted rules, drafts or jobs: %d %d %d %v", versions, drafts, queuedDrafts, err)
	}
	w = post(950, false)
	var published catalogPublication
	if err := json.Unmarshal(w.Body.Bytes(), &published); err != nil || w.Code != 200 || published.Version != 1 || published.ID == "" {
		t.Fatalf("could not create first rule: %d %s %v", w.Code, w.Body.String(), err)
	}
	if occupied := post(950, true); occupied.Code != http.StatusConflict {
		t.Errorf("preview must enforce the same occupied-SKU check as publication: %d %s", occupied.Code, occupied.Body)
	}
	var queued, supported bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=951),`+catalogDraftAvailableSQL+`
	 FROM products pro WHERE pro.id=950`).Scan(&queued, &supported); err != nil || !queued || !supported {
		t.Fatalf("the new SKU must reach regular trial calculation: queued=%t supported=%t err=%v", queued, supported, err)
	}
	sheet, err := s.catalogRuleRateSheet(ctx, published.ID)
	if err != nil || len(sheet.Components) != 4 || len(sheet.Components[0].Rows) != 2 ||
		sheet.Components[0].Rows[1].Rate != 1250 || sheet.Components[3].TargetKind != "referrer" ||
		sheet.Components[0].Rows[0].Category != "类别甲、类别乙" || sheet.Components[0].Rows[1].Company != "收款公司样本、特例乙" {
		t.Fatalf("the first rule must be editable: %+v %v", sheet, err)
	}
	fixedKey := sheet.Components[2].ItemKey
	var details map[string]any
	body, err := json.Marshal(sheet.Components[2])
	if err != nil || json.Unmarshal(body, &details) != nil || details["fixedMinor"] != float64(501) {
		t.Fatalf("fixed award must round trip through the editor: %s %v", body, err)
	}
	trial, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: 951})
	if err != nil {
		t.Fatal(err)
	}
	if result := trial.(draftSummary); result.Total != 4752 || result.LineCount != 7 || result.IssueCount != 0 {
		t.Fatalf("trial must apply the company exception for one order and category default for the other: %+v", result)
	}
	var exceptionToSeller, defaultToCompany int
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE src.order_id='101' AND l.person_id=9501 AND l.amount_minor=1250),
	 count(*) FILTER(WHERE src.order_id='102' AND l.person_id IS NULL AND l.amount_minor=1000
	  AND l.beneficiary_unit_id='10000000-0000-4000-8000-000000000956' AND perf.person_id=9501)
	 FROM calculation_draft_lines l JOIN source_order_versions src ON src.id=l.source_version_id
	 JOIN calculation_draft_performance perf ON (perf.period_id,perf.source_version_id)=(l.period_id,l.source_version_id)
	 WHERE l.period_id=951 AND l.component_type='sales'`).Scan(&exceptionToSeller, &defaultToCompany); err != nil ||
		exceptionToSeller != 1 || defaultToCompany != 1 {
		t.Fatalf("winner must choose the payee and keep actual seller performance: %d %d %v", exceptionToSeller, defaultToCompany, err)
	}
	w = httptest.NewRecorder()
	s.catalogMatches(w, httptest.NewRequest(http.MethodGet, "/api/v1/period-rule-matches?periodId=951", nil))
	var orderDetails catalogMatchReport
	if err := json.Unmarshal(w.Body.Bytes(), &orderDetails); err != nil || w.Code != 200 || len(orderDetails.Items) != 2 {
		t.Fatalf("order match response should contain the two test orders: %d %s %v", w.Code, w.Body, err)
	}
	for _, match := range orderDetails.Items {
		if match.OrderID == "102" && (len(match.SalesParts) != 1 || match.SalesParts[0].CompanyName != "类别默认公司" ||
			match.SalesParts[0].Name != "成交销售样本") {
			t.Fatalf("order detail must show both company and actual seller: %+v", match.SalesParts)
		}
	}
	cut := time.Date(2026, 10, 1, 12, 0, 0, 0, shanghai)
	revisedRate := int64(1250)
	var revision publishCatalogRatesInput
	if err := json.Unmarshal([]byte(`{"changes":[{"itemKey":"`+fixedKey+`","fixedMinor":603}]}`), &revision); err != nil {
		t.Fatal(err)
	}
	revision.BaseID, revision.EffectiveFrom = published.ID, cut
	revision.RemovedItems = []string{sheet.Components[1].ItemKey}
	revision.NewRateRows = []catalogNewRateRow{{ItemKey: sheet.Components[0].ItemKey, Row: 2,
		catalogConditionInput: catalogConditionInput{CompanyIDs: []string{"10000000-0000-4000-8000-000000000955"}, Relationship: "ANY", Rate: &revisedRate}}}
	updated, err := s.publishCatalogRates(ctx, revision, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	trial, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: 951})
	if err != nil {
		t.Fatal(err)
	}
	result := trial.(draftSummary)
	if result.Total != 4206 || result.LineCount != 5 || result.IssueCount != 0 {
		t.Fatalf("editing the special scope must let the removed company inherit its category rate: %+v", result)
	}
	if _, err := db.Exec(ctx, `UPDATE products SET settlement_enabled=true WHERE id=950`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE source_write_state SET provider_key=$1 WHERE source='DCAI'`, s.dcaiSourceKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,next_updated_at,next_id,completed_through,last_success_at)
	 VALUES('VIP_ORDERS',$1,'2026-09-01','2026-09-01','0',now(),now())`, s.dcaiSourceKey()); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.getSettlementReview(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/settlement-review?periodId=951&version=%d", result.Version), nil))
	var review settlementReview
	if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil || w.Code != 200 || review.Token == "" {
		t.Fatalf("original draft must be available for settlement: %d %s %v", w.Code, w.Body.String(), err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: 951, ExpectedVersion: result.Version, ReviewToken: review.Token}, "fixture"); err != nil {
		t.Fatal(err)
	}
	var replacement publishCatalogRatesInput
	if err := json.Unmarshal([]byte(`{"calculationChanges":[{"itemKey":"`+fixedKey+`",
	 "rows":[{"company":"ANY","relationship":"ANY","rateBps":350}]}]}`), &replacement); err != nil {
		t.Fatal(err)
	}
	replacement.BaseID, replacement.EffectiveFrom = updated.ID, cut.AddDate(0, 1, 0)
	tableVersion, err := s.publishCatalogRates(ctx, replacement, "fixture")
	if err != nil {
		t.Fatalf("could not change the original fixed award to conditional rates: %v", err)
	}
	tableSheet, err := s.catalogRuleRateSheet(ctx, tableVersion.ID)
	if err != nil || len(tableSheet.Components) != 3 {
		t.Fatalf("could not reopen the changed arrangement: %+v %v", tableSheet, err)
	}
	table := tableSheet.Components[1]
	if table.ItemKey != fixedKey || table.Fixed != nil || table.Rate != nil || len(table.Rows) != 1 ||
		table.Rows[0].Rate != 350 || table.TargetOrg != sheet.Components[2].TargetOrg {
		t.Fatalf("changing the calculation must retain the arrangement and its company: %+v", table)
	}
	replacement = publishCatalogRatesInput{}
	if err := json.Unmarshal([]byte(`{"calculationChanges":[{"itemKey":"`+fixedKey+`","fixedMinor":802}]}`), &replacement); err != nil {
		t.Fatal(err)
	}
	replacement.BaseID, replacement.EffectiveFrom = tableVersion.ID, cut.AddDate(0, 2, 0)
	fixedVersion, err := s.publishCatalogRates(ctx, replacement, "fixture")
	if err != nil {
		t.Fatalf("could not switch back to fixed amount: %v", err)
	}
	fixedSheet, err := s.catalogRuleRateSheet(ctx, fixedVersion.ID)
	if err != nil || len(fixedSheet.Components) != 3 || fixedSheet.Components[1].ItemKey != fixedKey ||
		fixedSheet.Components[1].Fixed == nil || *fixedSheet.Components[1].Fixed != 802 || len(fixedSheet.Components[1].Rows) != 0 {
		t.Fatalf("fixed method did not replace the table on the same arrangement: %+v %v", fixedSheet, err)
	}
	if _, err := s.publishCatalogRates(ctx, publishCatalogRatesInput{BaseID: fixedVersion.ID,
		EffectiveFrom: cut.AddDate(0, 3, 0), RemovedItems: []string{fixedKey}}, "fixture"); err != nil {
		t.Fatal(err)
	}
	next, err := s.createPeriod(ctx, createPeriodInput{ProductID: 950, SettlementMonth: "2026-11", Name: "退款账期"})
	if err != nil {
		t.Fatal(err)
	}
	refundAt := time.Date(2026, 11, 3, 0, 0, 0, 0, shanghai)
	amount := int64(5000)
	refund := sourceFact{Source: "DCAI", Domain: "VIP", SourceKey: "new", Kind: "REFUND_SUCCEEDED",
		OrderID: "101", RecordKey: "ai_vip_order:partial-101", Version: "1", RefundID: "partial-101",
		AmountMinor: &amount, OccurredAt: refundAt, UpdatedAt: refundAt,
		RefundsPayment: []sourcePaymentReference{{OrderID: "101", RecordID: "101"}}}
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{refund}, s.dcaiSourceKey())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	postingPeriod := next.(map[string]int64)["id"]
	refunded, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: postingPeriod})
	if err != nil {
		t.Fatal(err)
	}
	if got := refunded.(draftSummary); got.RefundTotal != -1302 || got.RefundCount != 3 || got.IssueCount != 0 {
		t.Fatalf("a half refund must recover 302 cents of the original 603-cent award in the refund period: %+v", got)
	}
	var companyDeduction int64
	if err := db.QueryRow(ctx, `SELECT r.amount_minor FROM calculation_draft_refunds r
	 JOIN calculation_draft_lines l ON l.id=r.original_line_id
	 WHERE r.period_id=$1 AND l.component_type='sales' AND l.person_id IS NULL
	  AND l.beneficiary_unit_id='10000000-0000-4000-8000-000000000953'`, postingPeriod).Scan(&companyDeduction); err != nil || companyDeduction != -500 {
		t.Fatalf("the refund must recover half of the settled company payout: %d %v", companyDeduction, err)
	}
	originalAmount := int64(10000)
	saveReplacement := func(version, memberType string, observed time.Time) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{{
				Source: "DCAI", Domain: "VIP", SourceKey: "new", Kind: "ORDER_PAID", OrderID: "101", PaymentID: "101",
				RecordKey: "ai_vip_order:101", Version: version, AmountMinor: &originalAmount, TransactionType: memberType,
				OccurredAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), UpdatedAt: observed,
				Classification: sourceClassification{"VIP_ORDER", "dcai:vip:new", "dcai.vip-lifecycle.v1"},
				VIPClaims:      []vipClaim{{Role: "PID_USER", User: "seller", Company: "seller-company", Relationship: "DIRECT"}},
				Participants:   []sourceParticipant{{Role: "DIRECT_PID_USER", UserID: "replacement"}},
			}}, s.dcaiSourceKey())
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE source_order_versions SET received_at=$1 WHERE source='DCAI' AND order_id='101' AND version=$2`,
				observed, version)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	saveReplacement("2", "", refundAt.Add(time.Hour))
	corrected, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: postingPeriod})
	if err != nil || corrected.(draftSummary).IssueCount != 1 || corrected.(draftSummary).CorrectionTotal != 500 {
		var issue string
		_ = db.QueryRow(ctx, "SELECT code FROM calculation_draft_issues WHERE period_id=$1 LIMIT 1", postingPeriod).Scan(&issue)
		t.Fatalf("referrer-only correction may not disturb the other parts: %+v %s %v", corrected, issue, err)
	}
	correctionVersion := corrected.(draftSummary).Version
	saveReplacement("3", "RENEWAL", refundAt.Add(2*time.Hour))
	corrected, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: postingPeriod})
	if err != nil || corrected.(draftSummary).IssueCount != 1 || corrected.(draftSummary).CorrectionTotal != 500 ||
		corrected.(draftSummary).Version != correctionVersion {
		t.Fatalf("adding the missing member type cannot change the pending referrer correction: %+v %v", corrected, err)
	}
	var recoveryLine int64
	if err := db.QueryRow(ctx, `SELECT correction_line_id FROM calculation_draft_issues
	 WHERE period_id=$1 AND code='CORRECTION_RECOVERY_PENDING' AND recovery_person_id=9502 AND recovery_minor=-500`,
		postingPeriod).Scan(&recoveryLine); err != nil {
		t.Fatalf("expected recovery of the original referrer's unrefunded balance: %v", err)
	}
	if _, err := db.Exec(ctx, storeCorrectionRecoverySQL, recoveryLine, 9502, "recover", "fixture"); err != nil {
		t.Fatal(err)
	}
	corrected, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: postingPeriod})
	if err != nil || corrected.(draftSummary).IssueCount != 0 || corrected.(draftSummary).CorrectionCount != 2 {
		t.Fatalf("confirmation of recovery should post both sides of the referrer change: %+v %v", corrected, err)
	}
	var debit, credit int64
	if err := db.QueryRow(ctx, `SELECT coalesce(sum(amount_minor) FILTER(WHERE person_id=9502),0),
	 coalesce(sum(amount_minor) FILTER(WHERE person_id=9503),0)
	 FROM calculation_draft_corrections WHERE period_id=$1`, postingPeriod).Scan(&debit, &credit); err != nil ||
		debit != -500 || credit != 500 {
		t.Fatalf("expected the original referrer's unrefunded 500 cents to move to the corrected referrer: %d %d %v", debit, credit, err)
	}
	if duplicate := post(950, false); duplicate.Code != 409 {
		t.Fatalf("duplicate rule must be rejected: %d %s", duplicate.Code, duplicate.Body)
	}
	if denied := post(949, false); denied.Code != 403 {
		t.Fatalf("publication must remain product-scoped: %d %s", denied.Code, denied.Body)
	}

	if _, err := db.Exec(ctx, `INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,
	 business_key,classifier,sku_code,sku_label,mapping_version,effective_from,observed_at)
	 SELECT gen_random_uuid(),product_id,source,domain,entity_type,record_table,'dcai:vip:other','dcai.vip-lifecycle.v1',
	 'OTHER','非成交安排',1,'2026-07-01',observed_at FROM catalog_business_routes WHERE sku_code='FIRST';
	 UPDATE identity_mappings SET valid_from='2026-07-01' WHERE source='DCAI' AND person_id IN (9501,9502);
	 UPDATE organization_company_sources SET valid_from='2026-07-01' WHERE source='DCAI' AND source_company_id='seller-company';
	 UPDATE catalog_sales_appointments SET valid_from='2026-07-01' WHERE source='DCAI' AND person_id=9501`); err != nil {
		t.Fatal(err)
	}
	firstAt := time.Date(2026, 7, 25, 0, 0, 0, 0, shanghai)
	standalone := publishCatalogRatesInput{EffectiveFrom: firstAt, Create: &catalogScenarioCreate{ProductID: 950,
		catalogScenarioCopy: catalogScenarioCopy{SKU: "OTHER", Label: "无成交分润"}}, NewItems: []catalogNewBenefitItem{
		{Label: "公司独立固定额", catalogBenefitTarget: catalogBenefitTarget{Target: "company", Organization: "10000000-0000-4000-8000-000000000954"},
			catalogBenefitCalculation: catalogBenefitCalculation{Fixed: &revisedRate}},
		{Label: "直接推荐", catalogBenefitTarget: catalogBenefitTarget{Target: "referrer"},
			catalogBenefitCalculation: catalogBenefitCalculation{Rate: &revisedRate}},
	}}
	onlyBenefits, err := s.publishCatalogRates(ctx, standalone, "fixture")
	if err != nil {
		t.Fatalf("could not publish without a sales arrangement: %v", err)
	}
	sheet, err = s.catalogRuleRateSheet(ctx, onlyBenefits.ID)
	if err != nil || len(sheet.Components) != 2 || sheet.Components[0].TargetKind != "company" ||
		sheet.Components[1].TargetKind != "referrer" {
		t.Fatalf("unexpected first version: %+v %v", sheet, err)
	}
	salesRate := int64(1500)
	withSales, err := s.publishCatalogRates(ctx, publishCatalogRatesInput{
		BaseID: onlyBenefits.ID, EffectiveFrom: firstAt.AddDate(0, 0, 3),
		NewSalesRates: []catalogConditionInput{{Company: "ANY", Relationship: "ANY", Rate: &salesRate}},
		NewSalesLabel: "  成交提成 ARR-01  ",
	}, "fixture")
	if err != nil {
		t.Fatalf("could not add sales to an existing rule: %v", err)
	}
	withSalesSheet, err := s.catalogRuleRateSheet(ctx, withSales.ID)
	if err != nil || len(withSalesSheet.Components) != 3 || withSalesSheet.Components[2].TargetKind != "seller" ||
		withSalesSheet.Components[2].Label != "成交提成 ARR-01" {
		t.Fatalf("could not reopen added sales arrangement: %+v %v", withSalesSheet, err)
	}
	withoutSales, err := s.publishCatalogRates(ctx, publishCatalogRatesInput{
		BaseID: withSales.ID, EffectiveFrom: firstAt.AddDate(0, 0, 6),
		RemovedItems: []string{withSalesSheet.Components[2].ItemKey},
	}, "fixture")
	if err != nil {
		t.Fatalf("could not remove independent sales arrangement: %v", err)
	}
	withoutSalesSheet, err := s.catalogRuleRateSheet(ctx, withoutSales.ID)
	if err != nil || len(withoutSalesSheet.Components) != 2 || withoutSalesSheet.Components[0].ItemKey != sheet.Components[0].ItemKey {
		t.Fatalf("removing sales must retain the other arrangements: %+v %v", withoutSalesSheet, err)
	}
	if _, err := s.publishCatalogRates(ctx, publishCatalogRatesInput{
		BaseID: withoutSales.ID, EffectiveFrom: firstAt.AddDate(0, 0, 7),
		RemovedItems: []string{sheet.Components[0].ItemKey, sheet.Components[1].ItemKey},
	}, "fixture"); err == nil {
		t.Fatal("a rule with no arrangements may not be published")
	}
	benefitPeriod, err := s.createPeriod(ctx, createPeriodInput{ProductID: 950, SettlementMonth: "2026-08", Name: "独立收款账期"})
	if err != nil {
		t.Fatal(err)
	}
	for i, day := range []int{1, 4, 7} {
		at := firstAt.AddDate(0, 0, day)
		external := fmt.Sprintf("20%d", i)
		fact := sourceFact{Source: "DCAI", Domain: "VIP", SourceKey: "new", Kind: "ORDER_PAID",
			OrderID: external, PaymentID: external, RecordKey: "ai_vip_order:" + external, Version: "1",
			AmountMinor: &originalAmount, OccurredAt: at, UpdatedAt: at,
			Classification: sourceClassification{"VIP_ORDER", "dcai:vip:other", "dcai.vip-lifecycle.v1"},
			Participants:   []sourceParticipant{{Role: "DIRECT_PID_USER", UserID: "referrer"}}}
		if i == 1 {
			fact.VIPClaims = []vipClaim{{Role: "PID_USER", User: "seller", Company: "seller-company", Relationship: "DIRECT"}}
		}
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{fact}, s.dcaiSourceKey())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	newPeriod := benefitPeriod.(map[string]int64)["id"]
	tested, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: newPeriod})
	if err != nil || tested.(draftSummary).Total != 9000 || tested.(draftSummary).LineCount != 7 || tested.(draftSummary).IssueCount != 0 {
		var issues []string
		_ = db.QueryRow(ctx, `SELECT array_agg(i.code ORDER BY i.id) FROM calculation_draft_issues i WHERE i.period_id=$1`, newPeriod).Scan(&issues)
		t.Fatalf("the three versions must generate only their configured benefits: %+v issues=%v %v", tested, issues, err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')",
		newPeriod, tested.(draftSummary).Version); err != nil {
		t.Fatal(err)
	}
	refundPeriod, err := s.createPeriod(ctx, createPeriodInput{ProductID: 950, SettlementMonth: "2026-09", Name: "独立收款退款账期"})
	if err != nil {
		t.Fatal(err)
	}
	at := firstAt.AddDate(0, 1, 1)
	for _, external := range []string{"200", "201"} {
		adjustment := sourceFact{Source: "DCAI", Domain: "VIP", SourceKey: "new", Kind: "REFUND_SUCCEEDED", OrderID: external,
			RecordKey: "ai_vip_order:partial-" + external, RefundID: "partial-" + external, Version: "1", AmountMinor: &amount,
			OccurredAt: at, UpdatedAt: at, RefundsPayment: []sourcePaymentReference{{OrderID: external, RecordID: external}}}
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{adjustment}, s.dcaiSourceKey())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	refunds, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: refundPeriod.(map[string]int64)["id"]})
	if err != nil || refunds.(draftSummary).RefundTotal != -3250 || refunds.(draftSummary).RefundCount != 5 || refunds.(draftSummary).IssueCount != 0 {
		t.Fatalf("refunds must use the original sales and independent benefit arrangements: %+v %v", refunds, err)
	}
}
