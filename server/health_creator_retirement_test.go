package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetiredHealthCreatorIsHiddenFromProductAndPeriodWorkflows(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, `
	 INSERT INTO products(code,name,period_mode,cycle_start_day)
	 VALUES ('DCAI_HEALTH_CREATOR','AI健康创客','calendar',25),('DCAI_HEALTH_AMBASSADOR','AI健康大使','calendar',25);
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month)
	 VALUES (1,'创客历史期',202608,'2026-07-25','2026-08-24','2026-08-01'),
	        (2,'大使八月',202608,'2026-07-25','2026-08-24','2026-08-01');
	 INSERT INTO draft_refresh_queue(period_id) VALUES(1);`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, route := range s.routes() {
		mux.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	for _, route := range []struct{ path, visible string }{
		{"/products", "AI健康大使"}, {"/periods", "大使八月"},
		{"/periods/page?limit=1", "大使八月"}, {"/periods?month=2026-08", "大使八月"},
		{"/monthly-report?month=2026-08&view=current&dimension=person", "大使八月"},
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1"+route.path, nil))
		if body := response.Body.String(); response.Code != http.StatusOK || !strings.Contains(body, route.visible) || strings.Contains(body, "创客") {
			t.Errorf("%s: HTTP %d %s", route.path, response.Code, body)
		}
	}
	if _, err := s.createPeriod(ctx, createPeriodInput{ProductID: 1, Name: "新创客期", SettlementMonth: "2026-09"}); err == nil {
		t.Fatal("retired product accepted a new period")
	}
	w := httptest.NewRecorder()
	pull := httptest.NewRequest(http.MethodPost, "/api/v1/periods/pull", strings.NewReader(`{"productId":1}`))
	pull.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(w, pull)
	if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "已下架") {
		t.Errorf("period pull was not explicitly refused: %d %s", w.Code, w.Body)
	}
	if _, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: 1}); err == nil {
		t.Fatal("retired period was recalculated")
	}
	draft := httptest.NewRequest(http.MethodPost, "/api/v1/drafts", strings.NewReader(`{"periodId":1}`))
	draft.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, draft)
	if w.Code != http.StatusGone {
		t.Errorf("retired draft submission returned HTTP %d: %s", w.Code, w.Body)
	}
	var periods int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM periods WHERE product_id=1").Scan(&periods); err != nil || periods != 1 {
		t.Fatalf("retired product history: %d, %v", periods, err)
	}
}

func TestHealthFeedSkipsCreatorWithoutBlockingAmbassadorOrWatermark(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	observed := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	from, through := observed.AddDate(0, -1, 0), observed.Add(time.Hour)
	creator := healthCompletionReply("HEALTH_CREATOR", observed, time.Time{}, "42")
	ambassador := healthCompletionReply("HEALTH_AMBASSADOR", observed, time.Time{}, "42")
	ambassador.Cursor.ID = "43"
	ambassador.Gates[0].OrderID = "order-43"
	ambassador.Facts[0] = json.RawMessage(strings.ReplaceAll(strings.ReplaceAll(string(ambassador.Facts[0]), "order-42", "order-43"), `"42"`, `"43"`))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		switch r.URL.Path {
		case "/internal/v1/finance/source-orders/readiness":
			body = map[string]any{"status": "READY", "contract_version": dcaiOrderPageContract, "source_fact_schema_version": sourceSchema,
				"source_fact_schema_sha256": dcaiFactSchemaHash, "pull_page_schema_sha256": dcaiPageSchemaHash, "streams": map[string]string{dcaiHealthStream: "READY"}}
		case "/internal/v1/finance/source-orders/cutoff":
			body = map[string]any{"contract_version": dcaiOrderPageContract, "through": through}
		case "/internal/v1/finance/source-orders/lookup":
			body = map[string]any{"contract_version": "dcai.finance-order-lookup/1.0", "source_fact_schema_version": sourceSchema, "source_order_id": "order-42", "stream": dcaiHealthStream, "item": creator}
		case "/internal/v1/finance/source-orders":
			var row orderWireRow
			var more bool
			switch r.URL.Query().Get("after_id") {
			case "0":
				row, more = creator, true
			case "42":
				row = ambassador
			default:
				http.Error(w, "unexpected cursor", 400)
				return
			}
			body = map[string]any{"contract_version": dcaiOrderPageContract, "source_fact_schema_version": sourceSchema, "stream": dcaiHealthStream,
				"through": through, "has_more": more, "next_cursor": row.Cursor, "items": []orderWireRow{row}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(provider.Close)
	s := workspace{db: db, sources: map[string]sourceConfig{"DCAI": {BaseURL: provider.URL, Token: "fixture"}}}
	client, err := newOrderFeedClient("DCAI", s.sources["DCAI"])
	if err != nil {
		t.Fatal(err)
	}
	conn, release, err := acquireOrderSyncLock(ctx, db, dcaiHealthStream)
	if err != nil {
		t.Fatal(err)
	}
	state, err := syncOrdersLocked(ctx, conn, client, dcaiHealthStream, &from, 0)
	release()
	if err != nil || state.Completed == nil || !state.Completed.Equal(through) || state.Scanned != 2 || state.Versions != 1 {
		t.Errorf("health stream cursor or versions: %+v %v", state, err)
	}
	if _, err := s.pullOrder(ctx, pullInput{Source: "DCAI", Stream: dcaiHealthStream, OrderID: "order-42"}); err == nil || !strings.Contains(err.Error(), "已下架") {
		t.Errorf("single creator lookup not refused: %v", err)
	}
	var facts, gates string
	if err := db.QueryRow(ctx, `SELECT (SELECT string_agg(order_id,',' ORDER BY order_id) FROM source_order_versions),
	 (SELECT string_agg(source_row_id,',' ORDER BY source_row_id) FROM dcai_order_blockers)`).Scan(&facts, &gates); err != nil || facts != "order-43" || gates != "43" {
		t.Fatalf("facts %s or gates %s should contain only the active order: %v", facts, gates, err)
	}
}
