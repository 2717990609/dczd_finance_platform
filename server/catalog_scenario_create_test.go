package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
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
		`INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(9501,'成交销售样本')`,
		`INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(9501,'DCAI','seller','2026-09-01')`,
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
		 '{"domain":"VIP","sourceKey":"new","paymentId":"101","amountMinor":10000}'),
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
	s := workspace{db: db, development: true, ruleAuth: catalogRuleAuth{issuer: "finance-test", key: []byte("isolated-finance-scenario-test-key")}}
	post := func(productGrant int64) *httptest.ResponseRecorder {
		t.Helper()
		claims := catalogRuleClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: s.ruleAuth.issuer, Subject: "fixture",
			Audience: jwt.ClaimStrings{"finance-platform"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))},
			Permissions: []string{catalogRulePublishPermission}, ProductIDs: []int64{productGrant}}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.ruleAuth.key)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/catalog-rule-rates", strings.NewReader(`{
		 "effectiveFrom":"2026-10-01T00:00:00+08:00","create":{"productId":950,"sku":"FIRST","label":"首版分润",
		 "salesRates":[{"company":"ANY","categoryIds":["10000000-0000-4000-8000-000000000957","10000000-0000-4000-8000-000000000958"],"relationship":"ANY","rateBps":1000},
		 {"companyIds":["10000000-0000-4000-8000-000000000953","10000000-0000-4000-8000-000000000955"],"relationship":"ANY","rateBps":1250}]},
		 "newItems":[{"label":"公司安排","target":"company","organization":"10000000-0000-4000-8000-000000000954","rateBps":250}]}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.postCatalogRuleRates(w, r)
		return w
	}
	w := post(950)
	var published catalogPublication
	if err := json.Unmarshal(w.Body.Bytes(), &published); err != nil || w.Code != 200 || published.Version != 1 || published.ID == "" {
		t.Fatalf("could not create first rule: %d %s %v", w.Code, w.Body.String(), err)
	}
	var queued, supported bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=951),`+catalogDraftAvailableSQL+`
	 FROM products pro WHERE pro.id=950`).Scan(&queued, &supported); err != nil || !queued || !supported {
		t.Fatalf("the new SKU must reach regular trial calculation: queued=%t supported=%t err=%v", queued, supported, err)
	}
	sheet, err := s.catalogRuleRateSheet(ctx, published.ID)
	if err != nil || len(sheet.Components) != 2 || len(sheet.Components[0].Rows) != 2 || sheet.Components[0].Rows[1].Rate != 1250 ||
		sheet.Components[0].Rows[0].Category != "类别甲、类别乙" || sheet.Components[0].Rows[1].Company != "收款公司样本、特例乙" {
		t.Fatalf("the first rule must be editable: %+v %v", sheet, err)
	}
	trial, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: 951})
	if err != nil {
		t.Fatal(err)
	}
	if result := trial.(draftSummary); result.Total != 2750 || result.LineCount != 4 || result.IssueCount != 0 {
		t.Fatalf("trial must apply the company exception for one order and category default for the other: %+v", result)
	}
	cut := time.Date(2026, 10, 1, 12, 0, 0, 0, shanghai)
	revisedRate := int64(1250)
	if _, err := s.publishCatalogRates(ctx, publishCatalogRatesInput{BaseID: published.ID, EffectiveFrom: cut,
		RemovedItems: []string{sheet.Components[1].ItemKey}, NewRateRows: []catalogNewRateRow{{ItemKey: sheet.Components[0].ItemKey, Row: 2,
			catalogConditionInput: catalogConditionInput{CompanyIDs: []string{"10000000-0000-4000-8000-000000000955"}, Relationship: "ANY", Rate: &revisedRate}}}}, "fixture"); err != nil {
		t.Fatal(err)
	}
	trial, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: 951})
	if err != nil {
		t.Fatal(err)
	}
	if result := trial.(draftSummary); result.Total != 2000 || result.LineCount != 2 || result.IssueCount != 0 {
		t.Fatalf("editing the special scope must let the removed company inherit its category rate: %+v", result)
	}
	if duplicate := post(950); duplicate.Code != 409 {
		t.Fatalf("duplicate rule must be rejected: %d %s", duplicate.Code, duplicate.Body)
	}
	if denied := post(949); denied.Code != 403 {
		t.Fatalf("publication must remain product-scoped: %d %s", denied.Code, denied.Body)
	}
}
