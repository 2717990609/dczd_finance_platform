package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPersonDetailUsesStableIDAndExistingWorkspaceAccess(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	const seed = `
INSERT INTO people(name) VALUES('同名人员'),('同名人员'),('只读财务'),('规则用户');
INSERT INTO products(code,name,period_mode) VALUES('TEST','测试产品','class');
INSERT INTO identity_mappings(person_id,source,external_id,valid_from,valid_to)
 VALUES(1,'DCZD','original','2020-01-01','2021-01-01'),
 (2,'DCZD','original','2021-01-01',NULL),(2,'DCJK','other-only','2026-01-01',NULL);
INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest) VALUES('DCAI','2026-01-01','\x01');
INSERT INTO catalog_sales_people_sources(id,source,external_id,person_id,valid_from,observed_at)
 VALUES(gen_random_uuid(),'DCAI','imported-only',1,'2025-01-01','2026-01-01');
INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
 VALUES(gen_random_uuid(),'DCAI',1,'SALES','PROJECT','exact-person','2025-01-01','2026-01-01'),
 (gen_random_uuid(),'DCAI',2,'SALES','PROJECT','other-person','2025-01-01','2026-01-01');`
	if _, err := s.db.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	const password = "isolated-test-password!42"
	for _, account := range []financeAccountConfig{
		{Login: "org.operator", PersonID: 1, Enabled: true, Workspace: true, OrganizationWrite: true},
		{Login: "other.person", PersonID: 2, Workspace: true},
		{Login: "work.reader", PersonID: 3, Enabled: true, Workspace: true},
		{Login: "rules.reader", PersonID: 4, Enabled: true, Rules: []financeRuleGrant{{ProductID: 1}}},
	} {
		if err := s.saveFinanceAccount(ctx, account, password); err != nil {
			t.Fatal(err)
		}
	}
	router := http.NewServeMux()
	for _, route := range s.routes() {
		router.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	call := func(method, path, body string, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d, expected %d: %s", path, w.Code, status, w.Body)
		}
		return w
	}
	login := func(name string) *http.Cookie {
		body, _ := json.Marshal(financeLoginInput{Login: name, Password: password})
		return call("POST", "/api/v1/session", string(body), nil, 200).Result().Cookies()[0]
	}
	const personPath = "/api/v1/person-detail?personId="
	call("GET", personPath+"1", "", nil, 401)
	call("GET", personPath+"1", "", login("rules.reader"), 403)
	reader := login("work.reader")
	operator := login("org.operator")
	call("GET", personPath+"-1", "", operator, 400)
	call("GET", personPath+"999", "", operator, 404)
	if body := call("GET", personPath+"1", "", reader, 200).Body.String(); !strings.Contains(body, `"id":1`) ||
		strings.Contains(body, "accountStatus") || strings.Contains(body, "org.operator") {
		t.Fatal(body)
	}
	if body := call("GET", personPath+"2", "", operator, 200).Body.String(); !strings.Contains(body, `"accountStatus":"DISABLED"`) ||
		strings.Contains(body, "other.person") || strings.Contains(body, "password") {
		t.Fatal(body)
	}
	accounts := call("GET", "/api/v1/person-source-accounts?personId=1", "", reader, 200).Body.String()
	if !strings.Contains(accounts, `"total":2`) || !strings.Contains(accounts, "imported-only") ||
		!strings.Contains(accounts, "original") || strings.Contains(accounts, "other-only") {
		t.Fatal(accounts)
	}
	currentAccounts := call("GET", "/api/v1/person-source-accounts?personId=1&current=true", "", reader, 200).Body.String()
	if !strings.Contains(currentAccounts, `"total":1`) || !strings.Contains(currentAccounts, "imported-only") || strings.Contains(currentAccounts, "original") {
		t.Fatal(currentAccounts)
	}
	call("GET", "/api/v1/person-source-accounts?personId=1&current=unknown", "", reader, 400)
	positions := call("GET", "/api/v1/catalog-appointments?history=true&personId=1", "", reader, 200).Body.String()
	if !strings.Contains(positions, `"total":1`) || !strings.Contains(positions, "exact-person") || strings.Contains(positions, "other-person") {
		t.Fatal(positions)
	}
}
