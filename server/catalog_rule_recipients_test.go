package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogVIPRuleAllowsLaterRecipientAppointment(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	for _, sql := range []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE
		 VALUES(950,'DCAI_VIP','499 会员','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		 VALUES(950,gen_random_uuid(),'test','DCAI','DCAI','vip','2026-11-01',sha256('fixture'::bytea))`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,
		 sku_code,sku_label,mapping_version,effective_from,observed_at)
		 VALUES(gen_random_uuid(),950,'DCAI','VIP','VIP_ORDER','ai_vip_order','dcai:vip:new','dcai.vip-lifecycle.v1',
		 'VIP_TEST','499 会员',1,'2026-05-01T00:00:00+08:00','2026-11-01')`,
		`INSERT INTO periods(id,product_id,name,ordinal,starts_on,ends_on,calendar_month) OVERRIDING SYSTEM VALUE
		 VALUES(951,950,'九月',202609,'2026-08-25','2026-09-24','2026-09-01')`,
		`INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(9501,'九月起任职'),(9502,'已结束任职')`,
		`INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest,benefit_ready)
		 VALUES('DCAI','2026-11-01',sha256('fixture'::bytea),true)`,
		`INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,valid_to,observed_at)
		 VALUES(gen_random_uuid(),'DCAI',9501,'PROJECT_OWNER','PROJECT','DCAI','2026-09-01T00:00:00+08:00',NULL,'2026-11-01'),
		 (gen_random_uuid(),'DCAI',9502,'PROJECT_OWNER','PROJECT','DCAI','2026-05-01T00:00:00+08:00','2026-06-01T00:00:00+08:00','2026-11-01')`,
		`WITH times(order_id,paid_at) AS (VALUES
		 ('101',TIMESTAMPTZ '2026-08-31T23:59:59+08:00'),('102',TIMESTAMPTZ '2026-09-01T00:00:00+08:00')),
		 payment AS (INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
		 SELECT 'DCAI',order_id,'','ai_vip_order:'||order_id,'1','ORDER_PAID',paid_at,paid_at,
		 jsonb_build_object('domain','VIP','sourceKey','vip','paymentId',order_id,'amountMinor',49900)
		 FROM times RETURNING id)
		 INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
		 SELECT id,'VIP_ORDER','dcai:vip:new','dcai.vip-lifecycle.v1' FROM payment`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	service := workspace{db: db, development: true}
	preview := func(person int) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"periodId":951,"matchedOrders":true,"rule":{
		 "effectiveFrom":"2026-06-01T00:00:00+08:00",
		 "create":{"productId":950,"sku":"VIP_TEST","label":"499 分润"},
		 "newItems":[{"label":"项目负责人提成","target":"person","personId":%d,"role":"PROJECT_OWNER","rateBps":200}]}}`, person)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/catalog-rule-impact", strings.NewReader(body))
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		service.postCatalogRuleImpact(response, request)
		return response
	}
	response := preview(9501)
	var report ruleImpactReport
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &report) != nil {
		t.Fatalf("the June rule must be able to use a September appointment: %d %s", response.Code, response.Body)
	}
	if len(report.Items) != 1 || report.Items[0].OrderID != "102" || report.Items[0].AfterMinor != 998 {
		t.Fatalf("only the payment at the appointment boundary earns 2%%: %+v", report.Items)
	}
	if expired := preview(9502); expired.Code != http.StatusConflict {
		t.Fatalf("an appointment ending at the rule boundary cannot be used: %d %s", expired.Code, expired.Body)
	}
}
