package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCatalogAppointmentsTransferAndPreserveLocalMaintenance(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	if _, err := s.db.Exec(ctx, `
	 INSERT INTO people(name,legacy_party_id) VALUES('在任负责人','a1000000-0000-0000-0000-000000000001'),
	 ('新负责人',NULL),('核算维护员',NULL),('普通财务',NULL);
	 INSERT INTO products(code,name,period_mode,cycle_start_day) VALUES('AI_MEMBER','大春 AI 会员','calendar',25);
	 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
	 VALUES(1,'a1000000-0000-0000-0000-000000000005','test','AI','DCAI','vip','2026-09-01',decode(repeat('01',32),'hex'));
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month) VALUES
	 (1,'9 月',202609,'2026-08-25','2026-09-24','2026-09-01'),(1,'10 月',202610,'2026-09-25','2026-10-24','2026-10-01');
	 INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest) VALUES('DCAI','2026-09-01','\x01');
	 INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
	 VALUES('a1000000-0000-0000-0000-000000000008',1,'AI','会员代理规则','AI','COMPONENT_RULE','BUSINESS_SCENARIO','NEW',false);
	 INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at)
	 VALUES('a1000000-0000-0000-0000-000000000009','a1000000-0000-0000-0000-000000000008',1,'PUBLISHED','2026-01-01','rule',
	 '{"schema_version":"1.1","recognition_trigger":"PAID","policy_scope":{"financial_event_types":["order.paid"]},
	   "internal_sales_direct_split":{"mode":"DCAI_INTERNAL_DIRECT"},
	   "benefit_actions":[{"beneficiary_mode":"TEAM_POOL","allocation_owner_role":"PROJECT_OWNER"}]}','2026-09-01');
	 INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,valid_to,observed_at)
	 VALUES('a1000000-0000-0000-0000-000000000006','DCAI',1,'PROJECT_OWNER','PROJECT','AI','2026-01-01','2026-10-01T00:00:00+08:00','2026-09-01'),
	 ('a1000000-0000-0000-0000-000000000007','DCAI',1,'SALES','PROJECT','AI','2026-01-01',NULL,'2026-08-01');
	 INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	 VALUES(1,1,'\x01',0,0,0,now());
	 INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(1,1,'fixture');`); err != nil {
		t.Fatal(err)
	}
	const pass = "roles-test-password"
	for _, a := range []financeAccountConfig{
		{PersonID: 3, Login: "role.operator", Enabled: true, Workspace: true, OrganizationWrite: true},
		{PersonID: 4, Login: "role.reader", Enabled: true, Workspace: true},
	} {
		if err := s.saveFinanceAccount(ctx, a, pass); err != nil {
			t.Fatal(err)
		}
	}
	router := http.NewServeMux()
	for _, route := range s.routes() {
		router.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	call := func(method, path, body string, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, status, w.Body)
		}
		return w
	}
	login := func(name string) *http.Cookie {
		return call("POST", "/api/v1/session", `{"login":"`+name+`","password":"`+pass+`"}`, nil, 200).Result().Cookies()[0]
	}
	operator, reader := login("role.operator"), login("role.reader")
	const path = "/api/v1/catalog-appointments"
	const transfer = `{"assignmentId":"a1000000-0000-0000-0000-000000000006","personId":2,"at":"2026-09-25T00:00:00+08:00","expectedThroughAt":"2026-10-01T00:00:00+08:00"}`
	call("POST", path+"/transfer", transfer, reader, 403)
	call("POST", path+"/transfer", transfer, nil, 401)
	if view := call("GET", path+"/scopes", "", reader, 200).Body.String(); !strings.Contains(view, "大春 AI 会员") {
		t.Fatal(view)
	}
	if before := call("GET", path+"?history=true", "", reader, 200).Body.String(); !strings.Contains(before, `"total":1`) {
		t.Fatalf("superseded initialization rows became active: %s", before)
	}
	probe := s
	probe.development = true
	if _, err := probe.addCatalogAppointment(ctx, addCatalogAppointmentInput{
		PersonID: 2, StartsAt: time.Date(2026, 9, 25, 0, 0, 0, 0, shanghai),
		catalogAppointmentTarget: catalogAppointmentTarget{Role: "PROJECT_OWNER", Scope: "PROJECT", ScopeKey: "AI"},
	}); err != errCatalogAppointmentHolder {
		t.Fatalf("single holder registration returned %v", err)
	}
	call("POST", path, `{"personId":2,"role":"PROJECT_OWNER","scope":"PROJECT","scopeKey":"AI","startsAt":"2026-09-25T00:00:00+08:00"}`, operator, 409)
	first := call("POST", path+"/transfer", transfer, operator, 200).Body.String()
	if retry := call("POST", path+"/transfer", transfer, operator, 200).Body.String(); retry != first {
		t.Fatalf("transfer retry created a new row: %s / %s", first, retry)
	}
	if dated := call("GET", path+"?on=2026-09-25&role=PROJECT_OWNER", "", reader, 200).Body.String(); !strings.Contains(dated, "新负责人") ||
		strings.Contains(dated, "在任负责人") || !strings.Contains(dated, `"total":1`) {
		t.Fatal(dated)
	}
	var queued []int64
	if err := s.db.QueryRow(ctx, "SELECT array_agg(period_id ORDER BY period_id) FROM draft_refresh_queue").Scan(&queued); err != nil ||
		len(queued) != 1 || queued[0] != 2 {
		t.Fatalf("open queue = %v: %v", queued, err)
	}
	const reload = `{"source":"DCAI","capturedAt":"2026-09-09T00:00:00Z","people":[],"personLinks":[],"companyLinks":[],"categories":[],
	"appointments":[{"id":"a1000000-0000-0000-0000-000000000006","partyId":"a1000000-0000-0000-0000-000000000001","role":"PROJECT_OWNER","scope":"PROJECT","key":"AI","from":"2026-01-01T00:00:00Z"}]}`
	if err := storeVIPSalesContext(ctx, s.db, json.RawMessage(reload)); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		paid := int64(10000)
		data, err := loadVIPRuntimeData(ctx, tx, 2, []catalogPaymentMatch{{SourceVersionID: 1, PaidMinor: &paid}})
		if err != nil {
			return err
		}
		start := time.Date(2026, 9, 25, 0, 0, 0, 0, shanghai)
		old, _ := vipRoleHolder(data.Roles, "PROJECT_OWNER", "PROJECT", "AI", start.Add(-time.Second))
		newHolder, ambiguous := vipRoleHolder(data.Roles, "PROJECT_OWNER", "PROJECT", "AI", start)
		if ambiguous || data.PersonIDs[old] != 1 || data.PersonIDs[newHolder] != 2 || data.People[newHolder] != "新负责人" {
			t.Fatalf("runtime stopped using locally maintained holders: %q / %q, ambiguous=%t", old, newHolder, ambiguous)
		}
		data.BenefitReady = true
		data.Organizations = []vipBenefitOrganization{{Party: "pool-party", Unit: "dept", Name: "交付部"}}
		rate := int64(500)
		policy := vipBenefitPolicy{actions: []vipBenefitAction{
			{Item: "pool", Role: "DELIVERY", Mode: "TEAM_POOL", PoolParty: "pool-party", PoolUnit: "dept", PoolCode: "delivery", PoolName: "交付池",
				OwnerRole: "PROJECT_OWNER", Rate: &rate},
			{Item: "fixed", Role: "PROJECT_OWNER", Mode: "PARTY", Designated: old, Rate: &rate},
		}}
		before := quoteVIPBenefits(catalogPaymentMatch{PaidAt: start.Add(-time.Second), PaidMinor: &paid}, policy, data)
		after := quoteVIPBenefits(catalogPaymentMatch{PaidAt: start, PaidMinor: &paid}, policy, data)
		if before[0].Owner != old || before[1].Party != old || after[0].Owner != newHolder || after[0].Amount != 500 ||
			after[1].Status != "INACTIVE" || after[1].Party != "" {
			t.Fatalf("handover should change the pool owner and respect the named payee: before=%+v after=%+v", before, after)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sales := `{"personId":1,"role":"SALES","scope":"PROJECT","scopeKey":"AI","startsAt":"2026-09-25T00:00:00+08:00"}`
	call("POST", path, sales, operator, 200)
	call("POST", path, strings.Replace(sales, `"personId":1`, `"personId":2`, 1), operator, 200)
}
