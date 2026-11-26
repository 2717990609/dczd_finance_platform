package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOrderSyncSettingsPersistAndScheduleTheFirstRun(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	requested := make(chan struct{}, 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/finance/source-orders/readiness" {
			requested <- struct{}{}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer source.Close()
	s := workspace{db: db, sources: map[string]sourceConfig{"DCZD": {BaseURL: source.URL, Token: "test"}}}
	if err := s.runOrderSyncJob(ctx, dczdOrderStream, time.Minute, 0); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 0 {
		t.Fatal("uninitialized streams must wait for a historical start")
	}
	historical := "2026-01-01"
	settings := orderSyncSettings{Stream: dczdOrderStream, Auto: true, IntervalMinutes: 30, FromDate: historical}
	if _, err := s.saveOrderSyncSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := s.runOrderSyncJob(ctx, dczdOrderStream, 0, 0); err != nil {
		t.Fatal(err)
	}
	job, err := readOrderSyncJob(ctx, db, dczdOrderStream)
	if err != nil || job.Status != "FAILED" || job.from == nil || job.from.In(shanghai).Format(time.DateOnly) != historical || len(requested) != 1 {
		t.Fatalf("saved schedule did not initialize the stream: %+v %v", job, err)
	}
	settings.Auto = false
	if _, err := s.saveOrderSyncSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := s.runOrderSyncJob(ctx, dczdOrderStream, time.Minute, 0); err != nil || len(requested) != 1 {
		t.Fatalf("disabled schedule must not run: %v", err)
	}
}
