package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManualDraftGenerationCoalescesRequestsAndRunsWithoutAutomaticScan(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	period, product := createDraftGenerationPeriod(t, s)
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "销售分润", Components: []component{
		{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "LOCK_SALES_USER"},
	}}); err != nil {
		t.Fatal(err)
	}

	// A worker can hold the period lock for the entire calculation. Submissions
	// still complete immediately and share the existing queued request.
	lock, err := s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, "SELECT id FROM periods WHERE id=$1 FOR NO KEY UPDATE", period); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for range cap(responses) {
		wg.Go(func() { responses <- requestManualDraft(s, period) })
	}
	wg.Wait()
	close(responses)
	for reply := range responses {
		if reply.Code != http.StatusAccepted {
			t.Fatalf("POST /drafts = %d %s", reply.Code, reply.Body)
		}
	}
	var generation int64
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*), min(generation) FROM draft_refresh_queue WHERE period_id=$1", period).Scan(&count, &generation); err != nil || count != 1 {
		t.Fatalf("expected one queued request: count=%d, err=%v", count, err)
	}
	if reply := requestManualDraft(s, period); reply.Code != http.StatusAccepted {
		t.Fatalf("duplicate POST /drafts = %d %s", reply.Code, reply.Body)
	}
	var same int64
	if err := s.db.QueryRow(ctx, "SELECT generation FROM draft_refresh_queue WHERE period_id=$1", period).Scan(&same); err != nil || same != generation {
		t.Fatalf("duplicate request changed generation: %d -> %d, err=%v", generation, same, err)
	}
	if status := readManualDraftStatus(t, s, period); status["generating"] != true || status["version"] != float64(0) {
		t.Fatalf("before running: %v", status)
	}

	// Start two workers after the request has already been persisted to model
	// a restart or multiple service instances reading the same queue.
	stop := s.startDraftRefreshWorker(ctx, 0)
	defer stop()
	stopSecond := s.startDraftRefreshWorker(ctx, 0)
	defer stopSecond()
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	waitForManualDraft(t, s, period, func(status map[string]any) bool {
		return status["generating"] == false && status["version"] == float64(1) && status["pending"] == false
	})
}

func TestManualDraftGenerationReportsFailureAndCanBeRetried(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	period, product := createDraftGenerationPeriod(t, s)
	if reply := requestManualDraft(s, period); reply.Code != http.StatusAccepted {
		t.Fatalf("POST /drafts = %d %s", reply.Code, reply.Body)
	}
	stop := s.startDraftRefreshWorker(ctx, 0)
	defer stop()
	waitForManualDraft(t, s, period, func(status map[string]any) bool {
		return status["generating"] == false && status["pending"] == true && status["message"] == errDraftRule.Error()
	})
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "销售分润", Components: []component{
		{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "LOCK_SALES_USER"},
	}}); err != nil {
		t.Fatal(err)
	}
	if reply := requestManualDraft(s, period); reply.Code != http.StatusAccepted {
		t.Fatalf("retry POST /drafts = %d %s", reply.Code, reply.Body)
	}
	waitForManualDraft(t, s, period, func(status map[string]any) bool {
		return status["generating"] == false && status["version"] == float64(1) && status["message"] == ""
	})
}

func createDraftGenerationPeriod(t *testing.T, s workspace) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	day := 1
	product, err := s.createProduct(ctx, createProductInput{Code: "ASYNC", Name: "异步试算样本", PeriodMode: "calendar", CycleStartDay: &day})
	if err != nil {
		t.Fatal(err)
	}
	productID := product.(map[string]int64)["id"]
	period, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: "十月", SettlementMonth: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	return period.(map[string]int64)["id"], productID
}

func requestManualDraft(s workspace, period int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drafts", strings.NewReader(fmt.Sprintf(`{"periodId":%d}`, period)))
	req.Header.Set("Content-Type", "application/json")
	reply := httptest.NewRecorder()
	for _, route := range s.routes() {
		if route.Method == req.Method && route.Path == req.URL.Path {
			route.Handler(reply, req)
			return reply
		}
	}
	panic("draft route missing")
}

func readManualDraftStatus(t *testing.T, s workspace, period int64) map[string]any {
	t.Helper()
	reply := httptest.NewRecorder()
	s.draftRefreshStatus(reply, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts/status?periodId=%d", period), nil))
	var result map[string]any
	if err := json.Unmarshal(reply.Body.Bytes(), &result); err != nil || reply.Code != http.StatusOK {
		t.Fatalf("GET /drafts/status = %d %s (%v)", reply.Code, reply.Body, err)
	}
	return result
}

func waitForManualDraft(t *testing.T, s workspace, period int64, ready func(map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for {
		status := readManualDraftStatus(t, s, period)
		if ready(status) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("unexpected status: %v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
