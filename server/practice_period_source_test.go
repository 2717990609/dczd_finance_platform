package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPullPracticePeriodsUsesMappedProductAndIsIdempotent(t *testing.T) {
	db := integrationDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO products(id,code,name,period_mode) OVERRIDING SYSTEM VALUE
	 VALUES (11,'HEALTH','Health','class'), (12,'FAMILY','Family','class'), (13,'VIP','VIP','calendar');
	 INSERT INTO source_mappings(product_id,source,domain,source_key) VALUES
	 (11,'DCZD','PRACTICE','1'),(11,'DCZD','PRACTICE','test-practice-historical'),(12,'DCZD','PRACTICE','4');`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var conflictingDates atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/internal/v1/finance/source-orders/practice-periods" ||
			r.Header.Get("Authorization") != "Bearer fixture" ||
			r.Header.Get("X-DCZD-Finance-Contract") != practicePeriodReadContract ||
			r.URL.Query().Get("strategy_id") != "1" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		out := practicePeriodSourcePage{Contract: practicePeriodReadContract, StrategyID: 1, ThroughID: 102}
		more := r.URL.Query().Get("after_id") == "0"
		out.HasMore = &more
		if more {
			out.NextAfterID, out.Skipped = 98, 1
			out.Periods = []practicePeriodMapping{}
		} else {
			if r.URL.Query().Get("through_id") != "102" || r.URL.Query().Get("after_id") != "98" {
				t.Errorf("the second page must retain the original source boundary: %s", r.URL)
			}
			out.NextAfterID = 102
			out.Periods = []practicePeriodMapping{{PeriodKey: "PRACTICE:2:1:102", Name: "158期实战班",
				Ordinal: 158, StartsOn: "2026-11-20", EndsOn: "2026-11-23"}}
			if conflictingDates.Load() {
				out.Periods[0].EndsOn = "2026-11-24"
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer source.Close()
	s := workspace{db: db, sources: map[string]sourceConfig{"DCZD": {BaseURL: source.URL, Token: "fixture"}}}
	for _, want := range []int{1, 0} {
		result, err := s.pullPracticePeriods(ctx, practicePeriodPullInput{ProductID: 11})
		if err != nil || result.Added != want || result.Existing != 1-want || result.Skipped != 1 {
			t.Fatalf("pull = %+v, %v", result, err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("expected two requests per pull: %d", calls.Load())
	}
	var product int64
	if err := db.QueryRow(ctx, `SELECT product_id FROM periods WHERE source_period_key='PRACTICE:2:1:102'`).Scan(&product); err != nil || product != 11 {
		t.Fatalf("period stored for product %d: %v", product, err)
	}
	if _, err := s.pullPracticePeriods(ctx, practicePeriodPullInput{ProductID: 13}); !errors.Is(err, errPracticePeriodMappingMissing) || calls.Load() != 4 {
		t.Fatalf("calendar products must not call the practice period source: %v", err)
	}
	conflictingDates.Store(true)
	result, err := s.pullPracticePeriods(ctx, practicePeriodPullInput{ProductID: 11})
	if err != nil || result.Added != 0 || len(result.Conflicts) != 1 || result.Conflicts[0] != "PRACTICE:2:1:102" {
		t.Fatalf("source dates must not overwrite an existing accounting period: %+v, %v", result, err)
	}
	var end string
	if err := db.QueryRow(ctx, `SELECT ends_on::text FROM periods WHERE source_period_key='PRACTICE:2:1:102'`).Scan(&end); err != nil || end != "2026-11-23" {
		t.Fatalf("original period changed: %q, %v", end, err)
	}
}

func TestPracticePeriodSourceRejectsCrossProductRows(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		more := false
		_ = json.NewEncoder(w).Encode(practicePeriodSourcePage{Contract: practicePeriodReadContract,
			StrategyID: 1, ThroughID: 95, NextAfterID: 95, HasMore: &more,
			Periods: []practicePeriodMapping{{PeriodKey: "PRACTICE:2:4:95", Name: "第20届家庭教育",
				Ordinal: 20, StartsOn: "2026-11-20", EndsOn: "2026-11-23"}}})
	}))
	defer source.Close()
	client, err := newOrderFeedClient("DCZD", sourceConfig{BaseURL: source.URL, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.practicePeriods(context.Background(), 1); !errors.Is(err, errPracticePeriodSource) {
		t.Fatalf("must reject a foreign product: %v", err)
	}
}
