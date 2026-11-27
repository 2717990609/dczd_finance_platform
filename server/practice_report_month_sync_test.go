package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPracticeRegistrationSyncSelectsEffectiveReportMonth(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	s := workspace{db: db}
	now := time.Now().In(shanghai)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, shanghai)
	prior := month.AddDate(0, -1, 0)
	next := month.AddDate(0, 1, 0)
	for _, strategy := range []int{1, 4} {
		product, err := s.createProduct(ctx, createProductInput{Code: fmt.Sprintf("REPORT_MONTH_%d", strategy), Name: fmt.Sprint(strategy), PeriodMode: "class"})
		if err != nil {
			t.Fatal(err)
		}
		productID := product.(map[string]int64)["id"]
		if _, err := s.createMapping(ctx, createMappingInput{ProductID: productID, Source: "DCZD", Domain: "PRACTICE", SourceKey: strconv.Itoa(strategy)}); err != nil {
			t.Fatal(err)
		}
		for i, dates := range [][2]string{
			{prior.Format(time.DateOnly), ""},
			{month.Format(time.DateOnly), ""},
			{next.Format(time.DateOnly), ""},
			{month.Format(time.DateOnly), prior.Format("2006-01")},
			{prior.Format(time.DateOnly), month.Format("2006-01")},
		} {
			if _, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: fmt.Sprint(i + 1), Ordinal: i + 1,
				StartsOn: dates[0], EndsOn: dates[0], ReportMonth: dates[1], SourcePeriodKey: fmt.Sprintf("PRACTICE:2:%d:%d", strategy, i+1)}); err != nil {
				t.Fatal(err)
			}
		}
	}

	requested := make(chan string, 10)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("period_key")
		parts := strings.Split(key, ":")
		if r.URL.Path != "/internal/v1/finance/source-orders/practice-registrations" || len(parts) != 4 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requested <- key
		strategy, _ := strconv.Atoi(parts[2])
		activity, _ := strconv.Atoi(parts[3])
		signed := []practiceEnrollmentWire{}
		_ = json.NewEncoder(w).Encode(map[string]any{"contract_version": practiceRegistrationsContract, "source_system": "DCZD",
			"period_key": key, "activity_type": 2, "strategy_id": strategy, "activity_id": activity,
			"period_start_at": month.Format(time.RFC3339), "enrollments": signed, "roster_hash": practiceRosterHash(signed),
			"valid_payment_links": true, "registrations": []any{}})
	}))
	defer provider.Close()
	client, err := newOrderFeedClient("DCZD", sourceConfig{BaseURL: provider.URL, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err := syncPracticeRegistrations(ctx, conn, client, 0); err != nil {
		t.Fatal(err)
	}
	close(requested)
	var keys []string
	for key := range requested {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	want := []string{"PRACTICE:2:1:2", "PRACTICE:2:1:3", "PRACTICE:2:1:5", "PRACTICE:2:4:2", "PRACTICE:2:4:3", "PRACTICE:2:4:5"}
	if !slices.Equal(keys, want) {
		t.Fatalf("requested periods = %v, want %v", keys, want)
	}
	var jobID int64
	if err := conn.QueryRow(ctx, `INSERT INTO dcai_sync_jobs(stream,source_key,status,started_at) VALUES($1,$2,'RUNNING',$3) RETURNING id`,
		dczdOrderStream, client.sourceKey(), month.Add(-time.Second).UTC()).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	resumed, err := readPracticeSyncPeriods(ctx, conn, jobID, 0)
	if err != nil || len(resumed) != 10 {
		t.Fatalf("batch started before Shanghai midnight must keep the earlier report month: %v, %v", resumed, err)
	}
	if _, err := conn.Exec(ctx, `UPDATE dcai_sync_jobs SET started_at=$2 WHERE id=$1`, jobID, month.UTC()); err != nil {
		t.Fatal(err)
	}
	resumed, err = readPracticeSyncPeriods(ctx, conn, jobID, 0)
	if err != nil || len(resumed) != len(want) {
		t.Fatalf("batch started at Shanghai midnight must use the new report month: %v, %v", resumed, err)
	}
}
