package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPracticePeriodPullImportsBothActivityTypes(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	var productID int64
	if err := db.QueryRow(ctx, `INSERT INTO products(code,name,period_mode)
	 VALUES('test-practice','实战班','class') RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO source_mappings(source,domain,source_key,product_id)
	 VALUES('DCZD','PRACTICE','4',$1)`, productID); err != nil {
		t.Fatal(err)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activity := r.URL.Query().Get("activity_type")
		ordinal := map[string]int{"1": 11, "2": 20}[activity]
		if r.URL.Query().Get("strategy_id") != "4" || ordinal == 0 || r.Header.Get("X-DCZD-Finance-Contract") != "dczd.finance-practice-period-read/1.0" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"contract_version": "dczd.finance-practice-period-read/1.0", "strategy_id": 4, "activity_type": ordinal / 10,
			"next_after_id": 7, "through_id": 7, "has_more": false, "skipped": 0,
			"periods": []practicePeriodMapping{{PeriodKey: fmt.Sprintf("PRACTICE:%s:4:7", activity), Name: "实战班",
				Ordinal: ordinal, StartsOn: "2026-06-08", EndsOn: "2026-06-10"}},
		})
	}))
	defer source.Close()
	s := workspace{db: db, sources: map[string]sourceConfig{"DCZD": {BaseURL: source.URL, Token: "test"}}}
	first, err := s.pullPracticePeriods(ctx, practicePeriodPullInput{ProductID: productID})
	if err != nil || first.Added != 2 {
		t.Fatalf("first pull: %+v %v", first, err)
	}
	second, err := s.pullPracticePeriods(ctx, practicePeriodPullInput{ProductID: productID})
	if err != nil || second.Added != 0 || second.Existing != 2 {
		t.Fatalf("repeat pull: %+v %v", second, err)
	}
}
