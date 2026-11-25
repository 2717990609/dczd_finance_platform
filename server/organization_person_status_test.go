package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOrganizationPersonStatus(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, `
  INSERT INTO people(name,a2_status) VALUES('档案验收',NULL),('旧系统已停用','INACTIVE');
  INSERT INTO organization_units(id,kind,name,status)
  VALUES ('fd000000-0000-0000-0000-000000000001','TEAM','测试小组','ACTIVE');
  INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,valid_to) VALUES
  ('fd000000-0000-0000-0000-000000000002',1,'fd000000-0000-0000-0000-000000000001','PRIMARY',now()-interval '1 day',NULL),
  ('fd000000-0000-0000-0000-000000000003',1,'fd000000-0000-0000-0000-000000000001','PRIMARY',now()-interval '3 days',now()-interval '2 days');`); err != nil {
		t.Fatal(err)
	}
	router := http.NewServeMux()
	for _, route := range s.routes() {
		router.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	call := func(method, path, body string, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w.Body.String()
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := s.db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	const status = "/api/v1/organization-dispatch/person-status"
	const disable = `{"personId":1,"status":"INACTIVE","expectedVersion":0}`
	if msg := call("POST", status, disable, 409); !strings.Contains(msg, "组织任职") {
		t.Fatal(msg)
	}
	exec(`UPDATE organization_assignments SET valid_to=now() WHERE valid_to IS NULL;
 INSERT INTO organization_duty_scopes(id,kind,code,name,active) VALUES('fd000000-0000-0000-0000-000000000004','PROJECT','QA','测试职责项目',true);
 INSERT INTO organization_business_duties(id,role,scope_id,person_id,valid_from)
 VALUES('fd000000-0000-0000-0000-000000000005','PRACTICE_AGENT_RECOMMENDATION','fd000000-0000-0000-0000-000000000004',1,now()-interval '1 day');`)
	if msg := call("POST", status, disable, 409); !strings.Contains(msg, "业务职责") {
		t.Fatal(msg)
	}
	exec(`DELETE FROM organization_business_duties;
 INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest) VALUES('DCAI','2026-01-01','\x01');
 INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
 VALUES('fd000000-0000-0000-0000-000000000008','DCAI',1,'SALES','PROJECT','QA',now()-interval '1 day','2026-01-01');`)
	if msg := call("POST", status, disable, 409); !strings.Contains(msg, "分润岗位") {
		t.Fatal(msg)
	}
	exec(`UPDATE catalog_sales_appointments SET valid_to=now();
 INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id,person_id,external_member_id,identity_state)
 VALUES('DCZD','MEMBER','fd000000-0000-0000-0000-000000000006','fixture-user',1,'fixture-member','ACTIVE');`)
	if msg := call("POST", status, disable, 409); !strings.Contains(msg, "销售身份") {
		t.Fatal(msg)
	}
	exec(`UPDATE organization_dispatch_links SET identity_state='INACTIVE';
 INSERT INTO organization_dispatch_outbox(id,target,entity_type,entity_id,message_type,payload)
 VALUES('fd000000-0000-0000-0000-000000000007','DCZD','MEMBER','fd000000-0000-0000-0000-000000000006','sale_company_membership_set','{"operation":"DISABLE"}');`)
	if msg := call("POST", status, disable, 409); !strings.Contains(msg, "待处理") {
		t.Fatal(msg)
	}
	exec(`UPDATE organization_dispatch_outbox SET state='APPLIED'`)
	call("POST", status, disable, 200)
	call("POST", status, disable, 200)
	current := call("GET", "/api/v1/person-detail?personId=1", "", 200)
	if !strings.Contains(current, `"sourceStatus":"INACTIVE"`) || !strings.Contains(current, `"canRestore":true`) {
		t.Fatal(current)
	}
	if archived := call("GET", "/api/v1/organization-dispatch/people?scope=archived&name=1", "", 200); !strings.Contains(archived, "档案验收") {
		t.Fatal(archived)
	}
	if results := call("GET", "/api/v1/organization-people?name=档案验收", "", 200); results != "[]" {
		t.Fatal(results)
	}
	add := `{"personId":1,"unitId":"fd000000-0000-0000-0000-000000000001","kind":"PRIMARY","startsAt":"2030-04-02T00:00:00+08:00"}`
	call("POST", "/api/v1/organization-assignments", add, 409)
	call("POST", status, `{"personId":1,"status":"ACTIVE","expectedVersion":0}`, 409)
	call("POST", status, `{"personId":2,"status":"ACTIVE","expectedVersion":0}`, 409)
	call("POST", status, `{"personId":1,"status":"ACTIVE","expectedVersion":1}`, 200)
	call("POST", "/api/v1/organization-assignments", add, 200)
	if rows := call("GET", "/api/v1/organization-assignments?history=true&personId=1", "", 200); !strings.Contains(rows, "fd000000-0000-0000-0000-000000000003") {
		t.Fatal(rows)
	}
}

func TestOrganizationCompanyStatusDependencies(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	const company = "fd200000-0000-0000-0000-000000000001"
	run := func(sql string) {
		t.Helper()
		if _, err := s.db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	run(`
 INSERT INTO organization_units(id,name,kind,status,sales_company_type)
 VALUES ('fd200000-0000-0000-0000-000000000001','启停验收公司','COMPANY','ACTIVE','INTERNAL');
 INSERT INTO organization_dispatch_company_keys(unit_id,wire_id,targets_managed)
 VALUES ('fd200000-0000-0000-0000-000000000001','fd200000-0000-0000-0000-000000000002',true);
 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
 VALUES ('DCZD','991','fd200000-0000-0000-0000-000000000001','2020-01-01');`)
	input := dispatchProfileInput{ID: company, Name: "启停验收公司", Status: "INACTIVE"}
	if _, err := s.saveDispatchProfile(ctx, input); err == nil || !strings.Contains(err.Error(), "业务系统使用关系") {
		t.Fatalf("unconfirmed source use: %v", err)
	}
	run(`UPDATE organization_company_sources SET valid_to=now()`)
	if _, err := s.saveDispatchProfile(ctx, input); err != nil {
		t.Fatal(err)
	}
	input.ExpectedVersion = 1
	input.Status = "ACTIVE"
	if _, err := s.saveDispatchProfile(ctx, input); err == nil || !strings.Contains(err.Error(), "有效销售类别") {
		t.Fatalf("activation without category: %v", err)
	}
	run(`
 INSERT INTO organization_sales_categories(id,name,active) VALUES ('fd200000-0000-0000-0000-000000000003','有效类别',true);
 INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from)
 VALUES ('fd200000-0000-0000-0000-000000000001','fd200000-0000-0000-0000-000000000003','2020-01-01');`)
	if _, err := s.saveDispatchProfile(ctx, input); err != nil {
		t.Fatal(err)
	}
}
