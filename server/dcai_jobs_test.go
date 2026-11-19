package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDCAISyncJobDeduplicatesAndResumesDurably(t *testing.T) {
	ctx := context.Background()
	var reads atomic.Int32
	var failNext atomic.Bool
	var failReadiness atomic.Bool
	var stopOnce sync.Once
	blocked := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cutoff := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		write := func(result any) { _ = json.NewEncoder(w).Encode(result) }
		switch r.URL.Path {
		case "/internal/v1/finance/source-orders/readiness":
			if failReadiness.Swap(false) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			write(map[string]any{"status": "READY", "contract_version": dcaiOrderPageContract, "source_fact_schema_version": sourceSchema,
				"source_fact_schema_sha256": dcaiFactSchemaHash, "pull_page_schema_sha256": dcaiPageSchemaHash,
				"streams": map[string]string{dcaiVIPStream: "READY"}})
		case "/internal/v1/finance/source-orders/cutoff":
			write(map[string]any{"contract_version": dcaiOrderPageContract, "through": cutoff})
		case "/internal/v1/finance/source-orders":
			if reads.Add(1) == 1 {
				stopOnce.Do(func() { close(blocked) })
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return
			}
			if failNext.Swap(false) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			q := r.URL.Query()
			write(map[string]any{"contract_version": dcaiOrderPageContract, "source_fact_schema_version": sourceSchema,
				"through": cutoff, "stream": dcaiVIPStream, "items": []any{}, "has_more": false,
				"next_cursor": map[string]string{"updated_at": q.Get("after_updated_at"), "id": q.Get("after_id")}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	s := workspace{db: integrationDB(t), sources: map[string]sourceConfig{"DCAI": {BaseURL: provider.URL, Token: "fixture"}}}
	input := dcaiSyncRequest{Stream: dcaiVIPStream, FromDate: "2026-08-01"}
	queued, err := s.enqueueDCAISync(ctx, input)
	if err != nil || queued.ID == 0 || queued.Status != "QUEUED" || reads.Load() != 0 {
		t.Fatalf("request must be saved before contacting the provider: %+v %v", queued, err)
	}
	var requesters sync.WaitGroup
	for range 4 {
		requesters.Go(func() {
			again, err := s.enqueueDCAISync(ctx, input)
			if err != nil || again.ID != queued.ID {
				t.Errorf("same pending stream must return one task: %+v %v", again, err)
			}
		})
	}
	requesters.Wait()
	attempt, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.runDCAISyncJob(attempt, dcaiVIPStream, 0, 0) }()
	select {
	case <-blocked:
	case <-time.After(6 * time.Second):
		t.Fatal("worker never requested the first page")
	}
	if err := s.runDCAISyncJob(ctx, dcaiVIPStream, 0, 0); err != nil || reads.Load() != 1 {
		t.Fatalf("second worker must skip the running stream: %v, source reads %d", err, reads.Load())
	}
	if _, err := s.enqueueDCAISync(ctx, dcaiSyncRequest{Stream: dcaiVIPStream, FromDate: "2026-07-01"}); !errors.Is(err, errSyncInProgress) {
		t.Fatalf("an earlier backfill cannot be silently swallowed by a running task: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker should leave a canceled job recoverable: %v", err)
	}
	if err := s.runDCAISyncJob(ctx, dcaiVIPStream, 0, 0); err != nil {
		t.Fatal(err)
	}
	var id int64
	var state string
	var through *time.Time
	if err := s.db.QueryRow(ctx, `SELECT j.id,j.status,d.completed_through FROM dcai_sync_jobs j JOIN dcai_order_sync d USING(stream)
	 WHERE stream=$1`, dcaiVIPStream).Scan(&id, &state, &through); err != nil || id != queued.ID || state != "SUCCEEDED" || through == nil {
		t.Fatalf("resumed job and completed watermark should agree: %d %s %v %v", id, state, through, err)
	}
	if err := s.runDCAISyncJob(ctx, dcaiVIPStream, time.Hour, 0); err != nil || reads.Load() != 2 {
		t.Fatalf("recent completion must prevent redundant automatic work: %v, reads=%d", err, reads.Load())
	}
	if err := s.runDCAISyncJob(ctx, dcaiHealthStream, time.Hour, 0); err != nil || reads.Load() != 2 {
		t.Fatalf("automatic scheduling must not choose a first historical start: %v, reads=%d", err, reads.Load())
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_sync_jobs SET finished_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_order_sync SET last_success_at=clock_timestamp()-interval '2 hours' WHERE stream=$1`, dcaiVIPStream); err != nil {
		t.Fatal(err)
	}
	failNext.Store(true)
	if err := s.runDCAISyncJob(ctx, dcaiVIPStream, time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	failed, err := readDCAISyncJob(ctx, s.db, dcaiVIPStream)
	if err != nil || failed.ID == id || failed.Status != "FAILED" || reads.Load() != 3 {
		t.Fatalf("scheduled job should record the failure: %+v %v", failed, err)
	}
	if err := s.runDCAISyncJob(ctx, dcaiVIPStream, time.Hour, 0); err != nil || reads.Load() != 3 {
		t.Fatalf("automatic retries should wait for the interval: %v, reads=%d", err, reads.Load())
	}
	retry, err := s.enqueueDCAISync(ctx, dcaiSyncRequest{Stream: dcaiVIPStream})
	if err != nil || retry.ID == failed.ID {
		t.Fatalf("manual retry must enqueue immediately: %+v %v", retry, err)
	}
	if err := s.runDCAISyncJob(ctx, dcaiVIPStream, 0, failed.ID); !errors.Is(err, errSyncJobReplaced) || reads.Load() != 3 {
		t.Fatalf("a replaced CLI request must not run its successor: %v, reads=%d", err, reads.Load())
	}
	if _, err := s.syncDCAIOrders(ctx, dcaiVIPStream, nil); err != nil {
		t.Fatal(err)
	}
	completed, err := readDCAISyncJob(ctx, s.db, dcaiVIPStream)
	if err != nil || completed.ID != retry.ID || completed.Status != "SUCCEEDED" || reads.Load() != 4 {
		t.Fatalf("manual retry did not resume the paused sync: %+v %v", completed, err)
	}

	backfillDate := "2026-07-01"
	backfill, err := s.enqueueDCAISync(ctx, dcaiSyncRequest{Stream: dcaiVIPStream, FromDate: backfillDate})
	if err != nil {
		t.Fatal(err)
	}
	failReadiness.Store(true)
	if err := s.runDCAISyncJob(ctx, dcaiVIPStream, 0, backfill.ID); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := readDCAISync(ctx, s.db, dcaiVIPStream)
	if err != nil || stateBefore.Target != nil || stateBefore.From.In(shanghai).Format(time.DateOnly) != input.FromDate {
		t.Fatalf("preflight failure must leave the previous watermark intact: %+v %v", stateBefore, err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_sync_jobs SET finished_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, backfill.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE dcai_order_sync SET last_success_at=clock_timestamp()-interval '2 hours' WHERE stream=$1`, dcaiVIPStream); err != nil {
		t.Fatal(err)
	}
	if err := s.runDCAISyncJob(ctx, dcaiVIPStream, time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	stateAfter, err := readDCAISync(ctx, s.db, dcaiVIPStream)
	if err != nil || stateAfter.Target != nil || stateAfter.From.In(shanghai).Format(time.DateOnly) != backfillDate {
		t.Fatalf("scheduled retry must preserve the failed backfill date: %+v %v", stateAfter, err)
	}
}
