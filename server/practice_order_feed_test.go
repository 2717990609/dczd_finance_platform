package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPracticeOrderFeedUsesIndependentRegistrationStream(t *testing.T) {
	ctx := context.Background()
	through := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	var legacyFact atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const path = "/internal/v1/finance/source-orders"
		const stream = "PRACTICE_ORDER_FACTS"
		if r.URL.Path != path+"/readiness" && r.URL.Query().Get("stream") != stream {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case path + "/readiness":
			fmt.Fprint(w, `{"status":"BLOCKED","streams":{"ORDER_FACTS":"BLOCKED","PRACTICE_ORDER_FACTS":"READY"},"contract_version":"dczd.finance-order-pull-page/1.0","source_fact_schema_version":"source.order-fact/1.0","max_page_size":200,"conflict_count":1,"practice_conflict_count":0}`)
		case path + "/cutoff":
			json.NewEncoder(w).Encode(map[string]any{"contract_version": dczdOrderPageContract, "through": through, "source_through": through.Add(-time.Hour)})
		case path, path + "/lookup":
			classification := map[string]string{"registration_attribution_basis": "BUSINESS_ENROLLMENT"}
			if legacyFact.Load() {
				classification["registration_attribution_basis"] = "PERMISSION"
			}
			fact := map[string]any{
				"fact_schema_version": sourceSchema, "fact_type": "ORDER_PAID", "occurred_at": through,
				"source_record": map[string]any{"source_table": "dc_practice_total_order_t", "record_id": "8", "record_version": "practice-registration/v2/100", "updated_at": through},
				"order":         map[string]string{"source_order_id": "8"}, "amounts": map[string]string{"paid_minor": "100"},
				"business": map[string]any{"source_domain_code": "PRACTICE", "source_product_code": "4", "classification_fields": classification},
			}
			cursor := map[string]any{"id": "1", "updated_at": through}
			item := map[string]any{"facts": []any{fact}, "cursor": cursor, "gates": []any{}}
			if r.URL.Path == path {
				json.NewEncoder(w).Encode(map[string]any{"contract_version": dczdOrderPageContract, "source_fact_schema_version": sourceSchema,
					"stream": stream, "through": through, "items": []any{item}, "next_cursor": cursor, "has_more": false})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"contract_version": "dczd.finance-order-lookup/1.0", "source_fact_schema_version": sourceSchema,
					"stream": stream, "source_order_id": "8", "item": item})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer source.Close()
	t.Setenv("FINANCE_DCZD_URL", source.URL)
	t.Setenv("FINANCE_DCZD_TOKEN", "test")
	cfg := readSources()["DCZD"]
	client, err := newOrderFeedClient("DCZD", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.readiness(ctx, dczdOrderStream); err != nil {
		t.Fatal(err)
	}
	if _, err := client.cutoff(ctx); err != nil {
		t.Fatal(err)
	}
	after := orderCursor{ID: "0", UpdatedAt: through.Add(-time.Hour)}
	page, err := client.page(ctx, dczdOrderStream, after, through, 100)
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Facts[0].ReferrerID != "" {
		t.Fatalf("could not read registration-only fact without guessing its referrer: %+v %v", page, err)
	}
	req := pullInput{Source: "DCZD", Stream: dczdOrderStream, OrderID: "8"}
	if _, err := lookupOrder(ctx, cfg, req); err != nil {
		t.Fatal(err)
	}
	legacyFact.Store(true)
	if _, err := client.page(ctx, dczdOrderStream, after, through, 100); err == nil {
		t.Fatal("page accepted a legacy permission fact")
	}
	if _, err := lookupOrder(ctx, cfg, req); err == nil {
		t.Fatal("single lookup accepted a legacy permission fact")
	}
}

func TestPracticeOrderSyncKeepsOutboxAndBusinessWatermarksSeparate(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	through := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	coverage := through.Add(-time.Hour)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const path = "/internal/v1/finance/source-orders"
		switch r.URL.Path {
		case path + "/readiness":
			fmt.Fprint(w, `{"streams":{"PRACTICE_ORDER_FACTS":"READY"},"contract_version":"dczd.finance-order-pull-page/1.0","source_fact_schema_version":"source.order-fact/1.0","max_page_size":200,"practice_conflict_count":0}`)
		case path + "/cutoff":
			json.NewEncoder(w).Encode(map[string]any{"contract_version": dczdOrderPageContract, "through": through, "source_through": coverage})
		case path:
			query := r.URL.Query()
			json.NewEncoder(w).Encode(map[string]any{"contract_version": dczdOrderPageContract, "source_fact_schema_version": sourceSchema,
				"stream": "PRACTICE_ORDER_FACTS", "through": query.Get("through"), "items": []any{}, "has_more": false,
				"next_cursor": map[string]string{"updated_at": query.Get("after_updated_at"), "id": query.Get("after_id")}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer source.Close()
	s.sources = map[string]sourceConfig{"DCZD": {BaseURL: source.URL, Token: "test", PracticeOnly: true}}
	start := coverage.Add(-72 * time.Hour)
	state, err := s.syncOrders(ctx, dczdOrderStream, &start)
	if err != nil || state.Completed == nil || !state.Completed.Equal(coverage) {
		t.Fatalf("closing coverage must come from the completed source sweep: %+v %v", state, err)
	}
	var pageThrough time.Time
	if err := s.db.QueryRow(ctx, "SELECT completed_outbox_through FROM dcai_order_sync WHERE stream=$1", dczdOrderStream).Scan(&pageThrough); err != nil || !pageThrough.Equal(through) {
		t.Fatalf("paging checkpoint must use outbox bound: %v %v", pageThrough, err)
	}
	state, err = s.syncOrders(ctx, dczdOrderStream, nil)
	if err != nil || state.Completed == nil || !state.Completed.Equal(coverage) || !state.next.UpdatedAt.Equal(through.Add(-24*time.Hour)) {
		t.Fatalf("repeated source coverage must be accepted without rewinding outbox: %+v %v", state, err)
	}
}
