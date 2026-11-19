package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDCZDOrderSyncResumesPracticePaymentsAndRefunds(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	eventAt := from.Add(time.Hour)
	through := from.Add(2 * time.Hour)
	const stream = "ORDER_FACTS"
	const contract = "dczd.finance-order-pull-page/1.0"
	var cutoffs atomic.Int32
	var permitSecondPage atomic.Bool
	var conflictOnLastPage atomic.Bool
	var conflicts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-dczd-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch r.URL.Path {
		case "/internal/v1/finance/source-orders/readiness":
			status := "READY"
			if conflicts.Load() != 0 {
				status = "BLOCKED"
			}
			write(map[string]any{"status": status, "contract_version": contract, "source_fact_schema_version": sourceSchema,
				"streams": map[string]string{stream: status}, "max_page_size": 40, "conflict_count": conflicts.Load()})
		case "/internal/v1/finance/source-orders/cutoff":
			if r.Header.Get("X-DCZD-Finance-Contract") != contract {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			cutoffs.Add(1)
			write(map[string]any{"contract_version": contract, "through": through})
		case "/internal/v1/finance/source-orders":
			query := r.URL.Query()
			if r.Header.Get("X-DCZD-Finance-Contract") != contract || query.Get("stream") != stream ||
				query.Get("limit") != "40" || query.Get("through") != through.Format(time.RFC3339) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			id, kind, table, more := "9", "ORDER_PAID", "dc_practice_total_order_t", true
			size := map[string]string{"paid_minor": "10000"}
			switch query.Get("after_id") {
			case "0":
				if query.Get("after_updated_at") != from.Format(time.RFC3339) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			case "9":
				if !permitSecondPage.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if conflictOnLastPage.Swap(false) {
					conflicts.Store(1)
				}
				id, kind, table, more = "10", "REFUND_SUCCEEDED", "dc_payrefund_t", false
				size = map[string]string{"refund_minor": "2000"}
			default:
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			cursor := map[string]any{"updated_at": eventAt, "id": id}
			fact := map[string]any{
				"fact_schema_version": sourceSchema, "fact_type": kind, "occurred_at": from.Add(-time.Hour),
				"source_record": map[string]any{"source_table": table, "record_id": id, "record_version": "1", "updated_at": from},
				"order":         map[string]any{"source_order_id": "100"}, "amounts": size,
				"business": map[string]any{"source_domain_code": "PRACTICE", "source_product_code": "PRACTICE:2:1"},
			}
			if more {
				fact["payment"] = map[string]string{"source_payment_id": "1"}
			} else {
				fact["refund"] = map[string]any{"source_refund_id": "2", "refunded_at": eventAt}
				fact["relations"] = []map[string]string{{"relation": "REFUNDS_PAYMENT", "target_source_order_id": "100"}}
			}
			write(map[string]any{"contract_version": contract, "source_fact_schema_version": sourceSchema, "stream": stream,
				"through": through, "has_more": more, "next_cursor": cursor,
				"items": []any{map[string]any{"cursor": cursor, "facts": []any{fact}, "gates": []any{}}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	service := workspace{db: db, sources: map[string]sourceConfig{"DCZD": {BaseURL: provider.URL, Token: "test-dczd-token"}}}
	job, err := service.enqueueOrderSyncAt(ctx, stream, &from)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.runOrderSyncJob(ctx, stream, 0, job.ID); err != nil {
		t.Fatal(err)
	}
	state, err := readOrderSync(ctx, db, stream)
	if err != nil || !state.Failed || state.Completed != nil || state.next.ID != "9" || state.Versions != 1 {
		t.Fatalf("partial sync must save the payment, exact cursor and no coverage: %+v %v", state, err)
	}
	permitSecondPage.Store(true)
	conflictOnLastPage.Store(true)
	job, err = service.enqueueOrderSyncAt(ctx, stream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.runOrderSyncJob(ctx, stream, 0, job.ID); err != nil {
		t.Fatal(err)
	}
	state, err = readOrderSync(ctx, db, stream)
	if err != nil || !state.Failed || state.Completed != nil || state.next.ID != "9" || state.Versions != 1 {
		t.Fatalf("provider conflict before promotion must retain the previous cursor and no coverage: %+v %v", state, err)
	}
	conflicts.Store(0)
	job, err = service.enqueueOrderSyncAt(ctx, stream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.runOrderSyncJob(ctx, stream, 0, job.ID); err != nil {
		t.Fatal(err)
	}
	state, err = readOrderSync(ctx, db, stream)
	if err != nil || state.Failed || state.Completed == nil || !state.Completed.Equal(through) || state.Versions != 2 || cutoffs.Load() != 1 {
		t.Fatalf("completed sync must resume at the previous cutoff and persist both versions: %+v %v", state, err)
	}
	var paid, refunds int
	err = db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='ORDER_PAID' AND (fact->>'amountMinor')::int=10000),
	 count(*) FILTER(WHERE kind='REFUND_SUCCEEDED' AND occurred_at=$1 AND fact->'refundsPayment'->0->>'orderId'='100')
	 FROM source_order_versions WHERE source='DCZD'`, eventAt).Scan(&paid, &refunds)
	if err != nil || paid != 1 || refunds != 1 {
		t.Fatalf("wrong DCZD payment/refund facts: %d %d %v", paid, refunds, err)
	}
}
