package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeFactSelectionIncludesUnsignedPaymentAndSignedDepositAndBalance(t *testing.T) {
	first, balance := "11", "22"
	legs := []string{"21", balance}
	data := practiceRegistrationSnapshot{
		Registrations: []practiceRegistrationSnapshotRow{{ID: "51", OrderID: &first}, {ID: "52", OrderID: &balance}},
		practiceRosterWire: practiceRosterWire{Enrollments: []practiceEnrollmentWire{
			{SourceEnrollmentID: "52", SourceOrderID: &balance, PaidOrderIDs: &legs, CommissionEligible: true},
			{SourceEnrollmentID: "53", CommissionEligible: false},
		}},
	}
	want := []practiceFactSelection{{Kind: "DELIVERY_COMPLETED", ID: 52}, {Kind: "ORDER_PAID", ID: 11},
		{Kind: "ORDER_PAID", ID: 21}, {Kind: "ORDER_PAID", ID: 22}}
	selection, required := selectPracticeFacts(data)
	if !slices.Equal(selection, want) || required["dc_practice_total_order_t:11"] || !required["dc_practice_total_order_t:21"] ||
		!required["dc_practice_total_order_t:22"] || !required["dc_offline_order_log_t:52"] {
		t.Fatalf("unexpected scoped selections %v or required facts %v", selection, required)
	}
}

func TestPracticeScopedFeedUsesIndependentRefundCursorAndBatchedFactVersions(t *testing.T) {
	at := time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encode := func(body any) { _ = json.NewEncoder(w).Encode(body) }
		const prefix = "/internal/v1/finance/source-orders/"
		switch r.URL.Path {
		case prefix + "practice-facts":
			if r.Method != http.MethodPost || r.Header.Get("X-DCZD-Finance-Contract") != "dczd.finance-practice-facts/1.0" {
				t.Errorf("unexpected practice batch request: %s %s", r.Method, r.URL)
			}
			body, _ := io.ReadAll(r.Body)
			if got := string(body); got != `[{"kind":"ORDER_PAID","id":17,"known_version":"effect-v1"}]` {
				t.Errorf("batch request body = %s", got)
			}
			encode(map[string]any{"contract_version": "dczd.finance-practice-facts/1.0", "source_fact_schema_version": sourceSchema,
				"facts": []any{}, "missing": []any{}})
		case prefix + "practice-refunds":
			q := r.URL.Query()
			if r.Method != http.MethodGet || r.Header.Get("X-DCZD-Finance-Contract") != "dczd.finance-practice-refunds/1.0" ||
				q.Get("after_success_at") != at.Format(time.RFC3339) || q.Get("after_id") != "601" || q.Get("after_code") != "refund-601" {
				t.Errorf("unexpected practice refund request: %v", r.URL)
			}
			cursor := map[string]any{"success_at": at, "id": "602", "code": "refund-602"}
			fact := map[string]any{"fact_schema_version": sourceSchema, "fact_type": "REFUND_SUCCEEDED", "occurred_at": at.Add(-time.Hour),
				"source_record": map[string]any{"source_table": "dc_payrefund_t", "record_id": "602", "record_version": "1", "updated_at": at.Add(-time.Hour)},
				"order":         map[string]string{"source_order_id": "17"}, "amounts": map[string]string{"refund_minor": "5000"},
				"business": map[string]any{"source_domain_code": "PRACTICE", "source_product_code": "1",
					"classification_fields": map[string]string{"registration_attribution_basis": "BUSINESS_ENROLLMENT"}},
				"refund":    map[string]any{"source_refund_id": "602", "refunded_at": at.Add(-time.Hour)},
				"relations": []map[string]string{{"relation": "REFUNDS_PAYMENT", "target_source_order_id": "17"}}}
			encode(map[string]any{"contract_version": "dczd.finance-practice-refunds/1.0", "source_fact_schema_version": sourceSchema,
				"through": at, "next_cursor": cursor, "has_more": false, "items": []any{map[string]any{"cursor": cursor, "fact": fact}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()
	client, err := newOrderFeedClient("DCZD", sourceConfig{BaseURL: provider.URL, Token: "fixture", PracticeOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	facts, missing, err := client.practiceScopedFacts(ctx, []practiceFactSelection{{Kind: "ORDER_PAID", ID: 17, KnownVersion: "effect-v1"}})
	if err != nil || len(facts) != 0 || len(missing) != 0 {
		t.Fatalf("unchanged selected payment was returned: %v, %v, %v", facts, missing, err)
	}
	page, err := client.practiceRefunds(ctx, practiceRefundCursor{SuccessAt: at, ID: "601", Code: "refund-601"}, nil, 100)
	if err != nil || page.Next.ID != "602" || len(page.Facts) != 1 || page.Facts[0].OrderID != "17" {
		t.Fatalf("refund must resume by refund-table cursor and retain original payment: %+v, %v", page, err)
	}
}

func TestPracticeRefundSyncResumesCommittedPageAfterSourceFailure(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	at := time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC)
	through := at.Add(time.Hour)
	var failSecond atomic.Bool
	var originalReads atomic.Int32
	failSecond.Store(true)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/finance/source-orders/practice-facts" {
			var selected []practiceFactSelection
			if err := json.NewDecoder(r.Body).Decode(&selected); err != nil || !slices.Equal(selected, []practiceFactSelection{{Kind: "ORDER_PAID", ID: 17}}) {
				t.Errorf("unexpected original payment selection %v: %v", selected, err)
			}
			originalReads.Add(1)
			fact := practicePaidFixture(at.AddDate(0, -3, 0))
			_ = json.NewEncoder(w).Encode(map[string]any{"contract_version": practiceScopedFactsContract,
				"source_fact_schema_version": sourceSchema, "facts": []any{fact}, "missing": []any{}})
			return
		}
		if r.URL.Path != "/internal/v1/finance/source-orders/practice-refunds" {
			t.Errorf("refund sync requested unexpected URL %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		second := r.URL.Query().Get("after_id") == "601"
		if second && failSecond.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		id := "601"
		if second {
			id = "602"
			if r.URL.Query().Get("through") != through.Format(time.RFC3339) {
				t.Errorf("resumed page lost the original refund cutoff: %s", r.URL)
			}
		}
		cursor := map[string]any{"success_at": at, "id": id, "code": "refund-" + id}
		fact := map[string]any{"fact_schema_version": sourceSchema, "fact_type": "REFUND_SUCCEEDED", "occurred_at": at,
			"source_record": map[string]any{"source_table": "dc_payrefund_t", "record_id": id, "record_version": "1", "updated_at": at},
			"order":         map[string]string{"source_order_id": "17"}, "amounts": map[string]string{"refund_minor": "5000"},
			"business": map[string]any{"source_domain_code": "PRACTICE", "source_product_code": "1",
				"classification_fields": map[string]string{"registration_attribution_basis": "BUSINESS_ENROLLMENT"}},
			"refund":    map[string]any{"source_refund_id": id, "refunded_at": at},
			"relations": []map[string]string{{"relation": "REFUNDS_PAYMENT", "target_source_order_id": "17"}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"contract_version": "dczd.finance-practice-refunds/1.0", "source_fact_schema_version": sourceSchema,
			"through": through, "next_cursor": cursor, "has_more": !second, "items": []any{map[string]any{"cursor": cursor, "fact": fact}}})
	}))
	defer provider.Close()
	client, err := newOrderFeedClient("DCZD", sourceConfig{BaseURL: provider.URL, Token: "fixture", PracticeOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	from := at.Add(-time.Minute)
	if _, err := runPracticeRefundSync(ctx, conn, client, from, 100); err == nil {
		t.Fatal("interrupted second refund page should report failure")
	}
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM source_order_versions WHERE source='DCZD' AND kind='REFUND_SUCCEEDED'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("only the first refund page should have committed: count=%d err=%v", count, err)
	}
	failSecond.Store(false)
	result, err := runPracticeRefundSync(ctx, conn, client, from, 100)
	if err != nil || result != 1 {
		t.Fatalf("resume should ingest exactly one more refund: count=%d err=%v", result, err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM source_order_versions WHERE source='DCZD' AND kind='REFUND_SUCCEEDED'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("both refunds must be preserved: count=%d err=%v", count, err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM source_order_versions WHERE source='DCZD' AND kind='ORDER_PAID' AND order_id='17'`).Scan(&count); err != nil ||
		count != 1 || originalReads.Load() != 1 {
		t.Fatalf("both refunds should use one targeted read of their historical payment: count=%d reads=%d err=%v", count, originalReads.Load(), err)
	}
}

func TestPracticeOrderJobUsesReportMonthFactsAndIndependentRefundCoverage(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	reference := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	previous := reference.Add(-time.Hour)
	local := time.Now().In(shanghai)
	monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, shanghai)
	curr := monthStart.Format("2006-01")
	prevMonth := monthStart.AddDate(0, -1, 0).Format("2006-01")
	s := workspace{db: db}
	product, err := s.createProduct(ctx, createProductInput{Code: "SCOPED_PRACTICE", Name: "实战班", PeriodMode: "class"})
	if err != nil {
		t.Fatal(err)
	}
	productID := product.(map[string]int64)["id"]
	if _, err := s.createMapping(ctx, createMappingInput{ProductID: productID, Source: "DCZD", Domain: "PRACTICE", SourceKey: "1"}); err != nil {
		t.Fatal(err)
	}
	const currentKey = "PRACTICE:2:1:21"
	const oldKey = "PRACTICE:2:1:20"
	var oldPeriodID int64
	for i, fixture := range [][2]string{{prevMonth, oldKey}, {curr, currentKey}} {
		created, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: fixture[1], Ordinal: i + 20,
			StartsOn: fixture[0] + "-01", EndsOn: fixture[0] + "-01", SourcePeriodKey: fixture[1]})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldPeriodID = created.(map[string]int64)["id"]
		}
	}
	var practiceFactBatches atomic.Int32
	var manual atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encode := func(body any) { _ = json.NewEncoder(w).Encode(body) }
		switch r.URL.Path {
		case "/internal/v1/finance/source-orders/readiness":
			if got := r.URL.Query().Get("stream"); got != dczdPracticeStream {
				t.Errorf("practice readiness must opt in for a source shared with legacy finance: %s", got)
			}
			encode(map[string]any{"status": "READY", "contract_version": dczdOrderPageContract, "source_fact_schema_version": sourceSchema,
				"streams": map[string]string{dczdPracticeStream: "READY"}, "max_page_size": 200, "practice_conflict_count": 0,
				"practice_registrations_contract": practiceRegistrationsContract, "practice_facts_contract": practiceScopedFactsContract,
				"practice_refunds_contract": practiceRefundsContract, "practice_orders_through": reference})
		case "/internal/v1/finance/source-orders/practice-refunds":
			after := previous
			if manual.Load() {
				after = reference
			}
			if r.URL.Query().Get("after_success_at") != after.Format(time.RFC3339) {
				t.Errorf("refunds began outside the legacy verified boundary: %s", r.URL)
			}
			encode(map[string]any{"contract_version": practiceRefundsContract, "source_fact_schema_version": sourceSchema,
				"through": reference, "next_cursor": practiceRefundCursor{SuccessAt: after, ID: "0"}, "has_more": false, "items": []any{}})
		case "/internal/v1/finance/source-orders/practice-registrations":
			want, sourceID := currentKey, 21
			if manual.Load() {
				want, sourceID = oldKey, 20
			}
			if got := r.URL.Query().Get("period_key"); got != want {
				t.Errorf("unexpected period selected: %s", got)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			signed := []practiceEnrollmentWire{}
			encode(map[string]any{"contract_version": practiceRegistrationsContract, "source_system": "DCZD", "period_key": want,
				"strategy_id": 1, "activity_type": 2, "activity_id": sourceID, "period_start_at": reference.Format(time.RFC3339),
				"enrollments": signed, "roster_hash": practiceRosterHash(signed), "valid_payment_links": true,
				"registrations": []any{map[string]any{"source_enrollment_id": "31", "source_participant_id": "11",
					"registration_order_id": "17", "membership_type": 1, "enabled": true, "state": 1}}})
		case "/internal/v1/finance/source-orders/practice-facts":
			practiceFactBatches.Add(1)
			var selected []practiceFactSelection
			selection := practiceFactSelection{Kind: "ORDER_PAID", ID: 17}
			if manual.Load() {
				selection.KnownVersion = "1"
			}
			if err := json.NewDecoder(r.Body).Decode(&selected); err != nil || !slices.Equal(selected, []practiceFactSelection{selection}) {
				t.Errorf("unexpected scope %v: %v", selected, err)
			}
			facts := []any{}
			if !manual.Load() {
				facts = append(facts, practicePaidFixture(reference))
			}
			encode(map[string]any{"contract_version": practiceScopedFactsContract, "source_fact_schema_version": sourceSchema,
				"facts": facts, "missing": []any{}})
		default:
			t.Errorf("legacy/unexpected request sent in scoped mode: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()
	s.sources = map[string]sourceConfig{"DCZD": {BaseURL: provider.URL, Token: "fixture", PracticeOnly: true}}
	key := s.orderSourceKey("DCZD")
	if _, err := db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,completed_through,next_updated_at,next_id)
	 VALUES($1,$2,$3,$4,$4,'0')`, dczdOrderStream, key, previous.Add(-time.Hour), previous); err != nil {
		t.Fatal(err)
	}
	job, err := s.enqueueOrderSyncAt(ctx, dczdOrderStream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runOrderSyncJob(ctx, dczdOrderStream, 0, job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = readOrderSyncJob(ctx, db, dczdOrderStream)
	if err != nil || job.Status != "SUCCEEDED" || practiceFactBatches.Load() != 1 {
		t.Fatalf("scoped job: %+v, batches=%d, %v", job, practiceFactBatches.Load(), err)
	}
	var periodCoverage, refundCoverage, legacyCoverage time.Time
	if err := db.QueryRow(ctx, `SELECT s.covered_through,r.completed_through,d.completed_through FROM practice_period_sync s
	 JOIN periods p ON p.id=s.period_id JOIN practice_refund_sync r USING(source_key) JOIN dcai_order_sync d ON d.source_key=s.source_key
	 WHERE p.source_period_key=$1`, currentKey).Scan(&periodCoverage, &refundCoverage, &legacyCoverage); err != nil ||
		!periodCoverage.Equal(reference) || !refundCoverage.Equal(reference) || !legacyCoverage.Equal(previous) {
		t.Fatalf("scoped coverage must not claim progress for the legacy full feed: %v %v %v %v", periodCoverage, refundCoverage, legacyCoverage, err)
	}
	checkOldPeriodCoverage := func(want string) {
		t.Helper()
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			sources, err := readSettlementSourceCoverage(ctx, tx, oldPeriodID, key, time.Hour,
				`WITH scope AS (SELECT clock_timestamp()-interval '90 minutes' AS from_time,
				 clock_timestamp()-interval '2 minutes' AS through_time)`+practiceSettlementCoverageSQL)
			if err != nil {
				return err
			}
			statuses := make(map[string]string, len(sources))
			for _, source := range sources {
				statuses[source.Stream] = source.Status
			}
			if len(statuses) != 2 || statuses["PRACTICE_PERIOD_ORDERS"] != want || statuses["PRACTICE_REFUNDS"] != "READY" {
				t.Errorf("settlement coverage for untouched/targeted period: %v", statuses)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkOldPeriodCoverage("NOT_STARTED")
	manual.Store(true)
	job, err = s.enqueueOrderSync(ctx, orderSyncRequest{Stream: dczdOrderStream, PeriodID: oldPeriodID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runOrderSyncJob(ctx, dczdOrderStream, 0, job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = readOrderSyncJob(ctx, db, dczdOrderStream)
	if err != nil || job.Status != "SUCCEEDED" || practiceFactBatches.Load() != 2 {
		t.Fatalf("historical period must be synchronized on demand: %+v, batches=%d, %v", job, practiceFactBatches.Load(), err)
	}
	if err := db.QueryRow(ctx, `SELECT covered_through FROM practice_period_sync WHERE period_id=$1`, oldPeriodID).
		Scan(&periodCoverage); err != nil || !periodCoverage.Equal(reference) {
		t.Fatalf("historical period coverage: %v %v", periodCoverage, err)
	}
	checkOldPeriodCoverage("READY")
}

func practicePaidFixture(at time.Time) map[string]any {
	return map[string]any{"fact_schema_version": sourceSchema, "fact_type": "ORDER_PAID", "occurred_at": at,
		"source_record": map[string]any{"source_table": "dc_practice_total_order_t", "record_id": "17", "record_version": "1", "updated_at": at},
		"order":         map[string]string{"source_order_id": "17"}, "amounts": map[string]string{"paid_minor": "39900"},
		"business": map[string]any{"source_domain_code": "PRACTICE", "source_product_code": "1",
			"classification_fields": map[string]string{"registration_attribution_basis": "BUSINESS_ENROLLMENT"}},
		"payment": map[string]string{"source_payment_id": "17"}}
}
