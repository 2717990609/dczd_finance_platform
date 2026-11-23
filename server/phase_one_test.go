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

func TestPeriodEndpointCreatesAccountingMonthsOnly(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	day := 25
	addProduct := func(code, mode string, startDay *int) int64 {
		t.Helper()
		result, err := s.createProduct(ctx, createProductInput{Code: code, Name: code, PeriodMode: mode, CycleStartDay: startDay})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	class := addProduct("PERIOD_TEST_CLASS", "class", nil)
	calendar := addProduct("PERIOD_TEST_CALENDAR", "calendar", &day)
	const path = "/api/v1/periods"
	create := func(payload string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		for _, route := range s.routes() {
			if route.Path == path && route.Method == request.Method {
				route.Handler(response, request)
				return response
			}
		}
		t.Fatal("period route missing")
		return nil
	}
	manual := create(fmt.Sprintf(`{"productId":%d,"name":"手填开班","ordinal":156,"startsOn":"2026-10-08","endsOn":"2026-10-09"}`, class))
	if manual.Code != http.StatusBadRequest || !strings.Contains(manual.Body.String(), "开班期次") {
		t.Fatalf("the accounting endpoint must reject a manually invented class: %d %s", manual.Code, manual.Body.String())
	}
	for _, product := range []int64{class, calendar} {
		payload := fmt.Sprintf(`{"productId":%d,"name":"2026-10 账期","settlementMonth":"2026-10"}`, product)
		response := create(payload)
		if response.Code != http.StatusOK {
			t.Fatalf("configured accounting month should be created: %d %s", response.Code, response.Body.String())
		}
		var from, to, month string
		if err := s.db.QueryRow(ctx, `SELECT starts_on::text,ends_on::text,calendar_month::text
			FROM periods WHERE product_id=$1`, product).Scan(&from, &to, &month); err != nil {
			t.Fatal(err)
		}
		if from != "2026-09-25" || to != "2026-10-24" || month != "2026-10-01" {
			t.Fatalf("accounting month uses the configured 25–24 cycle: got %s through %s (%s)", from, to, month)
		}
		if repeated := create(payload); repeated.Code != http.StatusConflict {
			t.Fatalf("duplicate accounting month accepted: %d %s", repeated.Code, repeated.Body.String())
		}
	}
}

func TestRuleRejectsInvalidOrRepeatedComponents(t *testing.T) {
	rules := []component{{Type: "sales", Mode: "rate", Value: 100}, {Type: "sales", Mode: "fixed", Value: 10}}
	if validateComponents(rules) == nil {
		t.Fatal("duplicate component accepted")
	}
	rules = []component{{Type: "delivery", Mode: "rate", Value: 10001}}
	if validateComponents(rules) == nil {
		t.Fatal("percentage over 100 accepted")
	}
	if err := validateComponents([]component{{Type: "sales", Mode: "rate", Value: 1500}, {Type: "delivery", Mode: "fixed", Value: 12000}}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceFactNormalizesOrderAndRejectsOtherOrder(t *testing.T) {
	payload := json.RawMessage(`{"fact_schema_version":"source.order-fact/1.0","fact_type":"ORDER_PAID","occurred_at":"2026-10-01T00:00:00Z","order":{"source_order_id":"o1","order_no":"sample","source_status":{"code":"PAID"}},"source_record":{"source_table":"orders","record_id":"r1","record_version":"v2","updated_at":"2026-10-02T00:00:00Z"},"business":{"source_domain_code":"CAMP","source_product_code":"p1"},"amounts":{"paid_minor":"9000"}}`)
	fact, err := normalizeFact("DCZD", "o1", payload)
	if err != nil || fact.AmountMinor == nil || *fact.AmountMinor != 9000 || fact.Version != "v2" || fact.OrderNo != "sample" {
		t.Fatalf("unexpected fact: %#v %v", fact, err)
	}
	if _, err = normalizeFact("DCZD", "other", payload); err == nil {
		t.Fatal("order mismatch accepted")
	}
}
