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

func TestProductSettlementSettingPersistsWithProductAuthorization(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	newProduct := func(code string) int64 {
		t.Helper()
		result, err := s.createProduct(ctx, createProductInput{Code: code, Name: code, PeriodMode: "class"})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	allowed, other := newProduct("SWITCH-ALLOWED"), newProduct("SWITCH-OTHER")
	if _, err := s.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", other); err != nil {
		t.Fatal(err)
	}

	router := func(s workspace) *http.ServeMux {
		mux := http.NewServeMux()
		for _, route := range s.routes() {
			mux.HandleFunc(route.Method+" "+route.Path, route.Handler)
		}
		return mux
	}
	managed := router(s)
	call := func(mux *http.ServeMux, method, path, body, origin string, cookie *http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	const origin = "http://127.0.0.1"
	const password = "isolated-product-fixture!42"
	login := func(config financeAccountConfig) *http.Cookie {
		t.Helper()
		person, err := s.createPerson(ctx, createPersonInput{Name: config.Login})
		if err != nil {
			t.Fatal(err)
		}
		config.PersonID, config.Enabled = person.(map[string]int64)["id"], true
		if err := s.saveFinanceAccount(ctx, config, password); err != nil {
			t.Fatal(err)
		}
		w := call(managed, http.MethodPost, "/api/v1/session",
			fmt.Sprintf(`{"login":%q,"password":%q}`, config.Login, password), origin, nil, http.StatusOK)
		return w.Result().Cookies()[0]
	}
	operator := login(financeAccountConfig{Login: "fixture.operator", Workspace: true, Settlements: []int64{allowed}})
	workspaceReader := login(financeAccountConfig{Login: "fixture.workspace", Workspace: true})
	rulePublisher := login(financeAccountConfig{Login: "fixture.publisher", Rules: []financeRuleGrant{{ProductID: allowed, Publish: true}}})

	const setting = "/api/v1/products/settlement"
	update := func(productID int64, enabled bool) string {
		return fmt.Sprintf(`{"productId":%d,"settlementEnabled":%t}`, productID, enabled)
	}
	check := func(expected bool) {
		t.Helper()
		w := call(managed, http.MethodGet, "/api/v1/products", "", origin, operator, http.StatusOK)
		var products []struct {
			ID                int64
			SettlementEnabled *bool
		}
		if err := json.Unmarshal(w.Body.Bytes(), &products); err != nil {
			t.Fatal(err)
		}
		if len(products) != 2 || products[0].ID != other || products[0].SettlementEnabled == nil ||
			!*products[0].SettlementEnabled || products[1].ID != allowed || products[1].SettlementEnabled == nil ||
			*products[1].SettlementEnabled != expected {
			t.Fatalf("products do not reflect stored switch values: %s", w.Body)
		}
	}

	call(managed, http.MethodPost, setting, update(allowed, true), origin, nil, http.StatusUnauthorized)
	check(false)
	call(managed, http.MethodPost, setting, update(allowed, true), origin, workspaceReader, http.StatusForbidden)
	call(managed, http.MethodPost, setting, update(allowed, true), origin, rulePublisher, http.StatusForbidden)
	call(managed, http.MethodPost, setting, update(other, false), origin, operator, http.StatusForbidden)
	call(managed, http.MethodPost, setting, update(allowed, true), "https://cross-origin.invalid", operator, http.StatusForbidden)
	for _, invalid := range []string{
		fmt.Sprintf(`{"productId":%d}`, allowed),
		fmt.Sprintf(`{"productId":%d,"settlementEnabled":null}`, allowed),
		update(0, true),
	} {
		call(managed, http.MethodPost, setting, invalid, origin, operator, http.StatusBadRequest)
	}
	check(false)
	call(managed, http.MethodPost, setting, update(allowed, true), origin, operator, http.StatusOK)
	check(true)
	call(managed, http.MethodPost, setting, update(allowed, false), origin, operator, http.StatusOK)
	call(managed, http.MethodPost, setting, update(allowed, false), origin, operator, http.StatusOK)
	check(false)

	s.development = true
	local := router(s)
	call(local, http.MethodPost, setting, update(allowed, true), "https://cross-origin.invalid", nil, http.StatusForbidden)
	call(local, http.MethodPost, setting, update(allowed, true), origin, &http.Cookie{Name: financeAccountCookie, Value: "expired"}, http.StatusUnauthorized)
	call(local, http.MethodPost, setting, update(other+1, true), origin, nil, http.StatusNotFound)
	check(false)
	call(local, http.MethodPost, setting, update(allowed, true), origin, nil, http.StatusOK)
	check(true)
}
