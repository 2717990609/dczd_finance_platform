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

func TestPersonDetailUsesStableIDAndExistingWorkspaceAccess(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	const seed = `
INSERT INTO people(name) VALUES('同名人员'),('同名人员'),('只读财务'),('规则用户');
UPDATE people SET legacy_party_id='ae05bffd-1971-4f20-88c5-f6c9837cb2a1' WHERE id=1;
INSERT INTO organization_a2_identifiers(origin,id,party_id,source,kind,external_id,valid_from,valid_to)
 VALUES('MAPPING',gen_random_uuid(),'ae05bffd-1971-4f20-88c5-f6c9837cb2a1','DCZD','SOURCE_USER_ID','original','2020-01-01','2021-01-01'),
 ('IDENTIFIER',gen_random_uuid(),'ae05bffd-1971-4f20-88c5-f6c9837cb2a1','DCZD','SOURCE_USER_ID','unmapped-extra','2020-01-01',NULL),
 ('IDENTIFIER',gen_random_uuid(),'ae05bffd-1971-4f20-88c5-f6c9837cb2a1','DCAI','SOURCE_ACCOUNT_ID','distinct-member','2020-01-01',NULL);
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
	historicalAccounts := call("GET", "/api/v1/person-source-accounts?personId=1&history=true", "", reader, 200).Body.String()
	if !strings.Contains(historicalAccounts, `"total":1`) || !strings.Contains(historicalAccounts, "original") || strings.Contains(historicalAccounts, "imported-only") {
		t.Fatal(historicalAccounts)
	}
	call("GET", "/api/v1/person-source-accounts?personId=1&current=true&history=true", "", reader, 400)
	const relationsURL = "/api/v1/organization-dispatch/source-relations?personId=1"
	if records := call("GET", relationsURL, "", reader, 200).Body.String(); !strings.Contains(records, "original") {
		t.Fatal(records)
	}
	if records := call("GET", relationsURL+"&supplementary=true", "", reader, 200).Body.String(); strings.Contains(records, "original") ||
		!strings.Contains(records, "unmapped-extra") || !strings.Contains(records, "distinct-member") {
		t.Fatal(records)
	}

	const accountPath = "/api/v1/person-source-accounts"
	const first = `{"personId":1,"source":"DCJK","externalId":"profile-flow","validFrom":"2026-01-01T10:00:00+08:00","expectedMappingId":0}`
	call("POST", accountPath, first, reader, 403)
	created := call("POST", accountPath, first, operator, 200)
	var mapping struct{ ID int64 }
	if err := json.Unmarshal(created.Body.Bytes(), &mapping); err != nil || mapping.ID < 1 {
		t.Fatalf("new mapping: %s", created.Body)
	}
	if retry := call("POST", accountPath, first, operator, 200); retry.Body.String() != created.Body.String() {
		t.Fatalf("retry changed mapping: %s", retry.Body)
	}
	const next = `{"personId":2,"source":"DCJK","externalId":"profile-flow","validFrom":"2026-02-01T10:00:00+08:00","expectedMappingId":%d}`
	if blocked := call("POST", accountPath, fmt.Sprintf(next, 0), operator, 409); !strings.Contains(blocked.Body.String(), "归属已变化") {
		t.Fatal(blocked.Body)
	}
	call("POST", accountPath, fmt.Sprintf(next, mapping.ID), operator, 200)
	history := call("GET", "/api/v1/identities?source=DCJK&externalId=profile-flow", "", reader, 200)
	var intervals []struct {
		PersonID  int64
		ValidFrom time.Time
		ValidTo   *time.Time
	}
	if err := json.Unmarshal(history.Body.Bytes(), &intervals); err != nil || len(intervals) != 2 ||
		intervals[0].PersonID != 2 || intervals[0].ValidTo != nil || intervals[1].PersonID != 1 ||
		intervals[1].ValidTo == nil || !intervals[1].ValidTo.Equal(intervals[0].ValidFrom) {
		t.Fatalf("mapping history: %s", history.Body)
	}
	const catalogConflict = `{"personId":2,"source":"DCAI","externalId":"imported-only","validFrom":"2026-02-01T10:00:00+08:00","expectedMappingId":0}`
	if blocked := call("POST", accountPath, catalogConflict, operator, 409); !strings.Contains(blocked.Body.String(), "子系统已同步的人员归属冲突") {
		t.Fatal(blocked.Body)
	}
	positions := call("GET", "/api/v1/catalog-appointments?history=true&personId=1", "", reader, 200).Body.String()
	if !strings.Contains(positions, `"total":1`) || !strings.Contains(positions, "exact-person") || strings.Contains(positions, "other-person") {
		t.Fatal(positions)
	}
}
