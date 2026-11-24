package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestFinanceAccountLoginScopesRulesAndRevokesSession(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	person, err := s.createPerson(ctx, createPersonInput{Name: "规则配置员"})
	if err != nil {
		t.Fatal(err)
	}
	newProduct := func(code string) int64 {
		result, err := s.createProduct(ctx, createProductInput{Code: code, Name: code, PeriodMode: "class"})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	allowed, denied := newProduct("ALLOWED"), newProduct("DENIED")
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,kind,name,status)
	 VALUES ('44444444-0000-4000-8000-000000000001','DEPARTMENT','池配置部门','ACTIVE'),
	 ('44444444-0000-4000-8000-000000000002','DEPARTMENT','已停用部门','INACTIVE')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO practice_pools(product_id,unit_id,name)
	 VALUES ($1,'44444444-0000-4000-8000-000000000002','历史团队池')`, allowed); err != nil {
		t.Fatal(err)
	}
	grant := financeAccountConfig{Login: "rule.operator", PersonID: person.(map[string]int64)["id"], Enabled: true,
		Rules: []financeRuleGrant{{ProductID: allowed, Publish: true}}}
	const password = "isolated-fixture-pass!42"
	if err := s.saveFinanceAccount(ctx, grant, password); err != nil {
		t.Fatal(err)
	}
	router := http.NewServeMux()
	for _, route := range s.routes() {
		router.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	call := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := call(http.MethodGet, "/api/v1/products", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous product list: %d", w.Code)
	}
	if w := call(http.MethodPost, "/api/v1/session", `{"login":"rule.operator","password":"wrong"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
	w := call(http.MethodPost, "/api/v1/session", `{"login":"RULE.OPERATOR","password":"`+password+`"}`, nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].HttpOnly {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	w = call(http.MethodGet, "/api/v1/products", "", cookie)
	var products []struct{ ID int64 }
	if err := json.Unmarshal(w.Body.Bytes(), &products); err != nil || w.Code != 200 || len(products) != 1 || products[0].ID != allowed {
		t.Fatalf("scoped products: %d %s", w.Code, w.Body)
	}
	if w := call(http.MethodGet, "/api/v1/orders", "", cookie); w.Code != http.StatusForbidden {
		t.Fatalf("scoped rules account saw unrelated order data: %d", w.Code)
	}
	poolDirectory := func(product int64) *httptest.ResponseRecorder {
		return call(http.MethodGet, "/api/v1/practice-pools?productId="+strconv.FormatInt(product, 10), "", cookie)
	}
	if w := poolDirectory(allowed); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "历史团队池") ||
		!strings.Contains(w.Body.String(), "池配置部门") {
		t.Fatalf("publisher did not receive historical pools and active unit choices: %d %s", w.Code, w.Body)
	}
	if w := poolDirectory(denied); w.Code != http.StatusForbidden {
		t.Fatalf("directory outside product scope: %d %s", w.Code, w.Body)
	}
	postRule := func(product int64) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(publishRuleInput{ProductID: product, Label: "样本", Components: []component{
			{Type: "sales", ClassMember: "new", Mode: "rate", Value: 250, SourceRole: "DIRECT_REFERRER"},
		}})
		return call(http.MethodPost, "/api/v1/rules", string(payload), cookie)
	}
	if w := postRule(denied); w.Code != http.StatusForbidden {
		t.Fatalf("rule outside product scope: %d %s", w.Code, w.Body)
	}
	if w := postRule(allowed); w.Code != 200 {
		t.Fatalf("authorized product rule: %d %s", w.Code, w.Body)
	}
	var stamped bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rule_versions r JOIN finance_accounts a ON a.id=r.published_by
	 WHERE r.product_id=$1 AND a.login='rule.operator')`, allowed).Scan(&stamped); err != nil || !stamped {
		t.Fatalf("missing finance publisher reference: %t %v", stamped, err)
	}
	grant.Rules[0].Publish = false
	if err := s.saveFinanceAccount(ctx, grant, ""); err != nil {
		t.Fatal(err)
	}
	if w := postRule(allowed); w.Code != http.StatusUnauthorized {
		t.Fatalf("old session survived a privilege change: %d", w.Code)
	}
	w = call(http.MethodPost, "/api/v1/session", `{"login":"rule.operator","password":"`+password+`"}`, nil)
	if w.Code != 200 {
		t.Fatalf("relogin: %d", w.Code)
	}
	cookie = w.Result().Cookies()[0]
	if w := poolDirectory(allowed); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "历史团队池") ||
		!strings.Contains(w.Body.String(), `"units":[]`) {
		t.Fatalf("reader should see history without write choices: %d %s", w.Code, w.Body)
	}
	if w := postRule(allowed); w.Code != http.StatusForbidden {
		t.Fatalf("read-only role published a rule: %d", w.Code)
	}
	if w := call(http.MethodDelete, "/api/v1/session", "", cookie); w.Code != 200 {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	if w := call(http.MethodGet, "/api/v1/products", "", cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("old cookie retained access after logout: %d", w.Code)
	}
	grant.Workspace, grant.Rules = true, nil
	if err := s.saveFinanceAccount(ctx, grant, ""); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodPost, "/api/v1/session", `{"login":"rule.operator","password":"`+password+`"}`, nil)
	if w.Code != 200 {
		t.Fatalf("workspace login: %d", w.Code)
	}
	cookie = w.Result().Cookies()[0]
	if w := call(http.MethodGet, "/api/v1/orders", "", cookie); w.Code != http.StatusOK {
		t.Fatalf("full workspace account could not read orders: %d", w.Code)
	}
	if w := call(http.MethodGet, "/api/v1/rules", "", cookie); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("workspace access leaked configuration outside rule grants: %d %s", w.Code, w.Body)
	}
}
