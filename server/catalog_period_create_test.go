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

func TestCreateCatalogPeriodRuleFromEmptyConfiguration(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	for _, sql := range []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE VALUES(980,'DCAI_WHOLE','整期验收','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		 VALUES(980,gen_random_uuid(),'test','DCAI','DCAI','new','2026-11-01',sha256('fixture'::bytea))`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,
		 sku_code,sku_label,mapping_version,effective_from,observed_at) VALUES
		 (gen_random_uuid(),980,'DCAI','VIP','VIP_ORDER','ai_vip_order','first','vip','FIRST','会员',1,'2026-09-01','2026-11-01'),
		 (gen_random_uuid(),980,'DCAI','VIP','VIP_ORDER','ai_vip_order','second','vip','SECOND','另一个SKU',1,'2026-09-01','2026-11-01')`,
		`INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(9801,'整期个人收款')`,
		`INSERT INTO organization_units(id,kind,name,status) VALUES ('10000000-0000-4000-8000-000000000981','COMPANY','整期公司收款','ACTIVE')`,
		`WITH payment AS (INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
		 VALUES('DCAI','101','','ai_vip_order:101','1','ORDER_PAID','2026-10-02T00:00Z','2026-10-02T00:00Z',
		 '{"domain":"VIP","sourceKey":"new","paymentId":"101","amountMinor":10000}'),
		 ('DCAI','102','','ai_vip_order:102','1','ORDER_PAID','2026-10-03T00:00Z','2026-10-03T00:00Z',
		 '{"domain":"VIP","sourceKey":"new","paymentId":"102","amountMinor":30000}') RETURNING id,order_id)
		 INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
		 SELECT id,'VIP_ORDER',CASE order_id WHEN '101' THEN 'first' ELSE 'second' END,'vip' FROM payment`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	s := workspace{db: db, development: true, ruleAuth: catalogRuleAuth{issuer: "finance-test", key: []byte("isolated-finance-aggregate-test-key")}}
	submit := func(grant int64, path string, handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
		t.Helper()
		claims := catalogRuleClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: s.ruleAuth.issuer, Subject: "fixture",
			Audience: jwt.ClaimStrings{"finance-platform"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))},
			Permissions: []string{catalogRulePublishPermission}, ProductIDs: []int64{grant}}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.ruleAuth.key)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	const path = "/api/v1/catalog-period-rules"
	const input = `{"create":{"productId":980,"label":"新整期规则","cycle":"MONTHLY_25_TO_24"},
	 "effectiveFrom":"2026-09-25T00:00:00+08:00","scenarioCodes":["FIRST","SECOND"],"components":[
	 {"label":"公司安排","targetKind":"company","organizationKey":"unit:10000000-0000-4000-8000-000000000981","tiers":[{"rate_bps":1500}]},
	 {"label":"个人安排","targetKind":"person","personId":9801,"tiers":[{"rate_bps":500}]}]}`
	quoteInput := strings.TrimSuffix(input, "}") + `,"receiptsMinor":40000}`
	preview := submit(980, path+"/preview", s.postCatalogPeriodQuote, quoteInput)
	var quote catalogPeriodQuote
	if err := json.Unmarshal(preview.Body.Bytes(), &quote); err != nil || preview.Code != 200 || quote.Total != 8000 {
		t.Fatalf("unsaved aggregate quote should price both benefits: %d %s %v", preview.Code, preview.Body.String(), err)
	}
	var packages int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM product_rule_packages WHERE product_id=980`).Scan(&packages); err != nil || packages != 0 {
		t.Fatalf("preview should not publish a package: %d %v", packages, err)
	}
	if bad := submit(980, path, s.postCatalogPeriodRule, strings.Replace(input, "2026-09-25", "2026-09-24", 1)); bad.Code != 400 {
		t.Fatalf("creation accepted a date outside the selected cycle: %d %s", bad.Code, bad.Body.String())
	}
	if unmapped := submit(980, path, s.postCatalogPeriodRule, strings.Replace(input, "SECOND", "UNMAPPED", 1)); unmapped.Code != 409 {
		t.Fatalf("creation accepted an unmapped SKU: %d %s", unmapped.Code, unmapped.Body.String())
	}
	w := submit(980, path, s.postCatalogPeriodRule, input)
	var published catalogPublication
	if err := json.Unmarshal(w.Body.Bytes(), &published); err != nil || w.Code != 200 || published.ID == "" || published.Version != 1 {
		t.Fatalf("first aggregate rule did not publish: %d %s %v", w.Code, w.Body.String(), err)
	}
	trial, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: published.ID, Month: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	if got := trial.(draftSummary); got.Total != 8000 || got.LineCount != 2 || got.IssueCount != 0 {
		t.Fatalf("first version should trial both mapped SKUs as one accounting period: %+v", got)
	}
	if denied := submit(979, path, s.postCatalogPeriodRule, input); denied.Code != 403 {
		t.Fatalf("out-of-scope creation: %d %s", denied.Code, denied.Body.String())
	}
	if duplicate := submit(980, path, s.postCatalogPeriodRule, input); duplicate.Code != 409 {
		t.Fatalf("duplicate aggregate package: %d %s", duplicate.Code, duplicate.Body.String())
	}
}
