package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDCAISyncResumesTheSameWatermarkAndCommitsOnlyCompleteCoverage(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	changed := from.Add(time.Hour)
	through := from.Add(2 * time.Hour)
	var cutoffs atomic.Int32
	var failSecondPage atomic.Bool
	var scanStart atomic.Value
	failSecondPage.Store(true)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-service-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch r.URL.Path {
		case "/internal/v1/finance/source-orders/readiness":
			write(map[string]any{"status": "READY", "contract_version": "dcai.finance-order-pull-page/1.0",
				"source_fact_schema_version": "source.order-fact/1.0", "source_fact_schema_sha256": "d4981ce54a1018f5e6bd39c80e64fafdff6b0d58187120c8b05df298c8966c09",
				"pull_page_schema_sha256": "5dfe80ac6c41077fa7912c38eb9892d542b1627b4e57f4aae4fdbbfec2752277", "streams": map[string]string{"VIP_ORDERS": "READY"}})
		case "/internal/v1/finance/source-orders/cutoff":
			cutoffs.Add(1)
			write(map[string]any{"contract_version": "dcai.finance-order-pull-page/1.0", "through": through})
		case "/internal/v1/finance/source-orders/lookup":
			if r.Header.Get("X-DCAI-Finance-Contract") != "dcai.finance-order-lookup/1.0" {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			order := r.URL.Query().Get("source_order_id")
			lookupAt, id, status := changed.Add(time.Minute), "9", "PASSED"
			var reason any
			if order == "VIP-8" {
				lookupAt, id, status, reason = from.Add(-time.Minute), "8", "BLOCKED", "REFUND_EVIDENCE_MISSING"
			}
			gate := map[string]any{"code": "REFUND_FINALITY", "status": status, "reason_code": reason, "source_order_id": order, "effective_at": lookupAt}
			write(map[string]any{"contract_version": "dcai.finance-order-lookup/1.0", "source_fact_schema_version": "source.order-fact/1.0",
				"source_order_id": order, "stream": "VIP_ORDERS",
				"item": map[string]any{"cursor": dcaiCursor{UpdatedAt: lookupAt, ID: id}, "facts": []any{}, "gates": []any{gate}}})
		case "/internal/v1/finance/source-orders":
			q := r.URL.Query()
			if q.Get("through") != through.Format(time.RFC3339) || q.Get("stream") != "VIP_ORDERS" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			id := "9"
			more := true
			switch q.Get("after_id") {
			case "0":
				scanStart.Store(q.Get("after_updated_at"))
			case "9":
				if failSecondPage.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				id, more = "10", false
			default:
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			order := "VIP-" + id
			cursor := map[string]any{"updated_at": changed, "id": id}
			fact := map[string]any{"fact_schema_version": "source.order-fact/1.0", "fact_type": "ORDER_PAID", "occurred_at": changed,
				"source_record": map[string]any{"source_table": "ai_vip_order", "record_id": id, "record_version": "v1", "updated_at": changed.Add(time.Minute)},
				"order":         map[string]any{"source_order_id": order}, "payment": map[string]any{"source_payment_id": id},
				"business": map[string]any{"source_domain_code": "VIP", "source_entity_type_code": "VIP_ORDER", "source_product_code": "VIP"},
				"amounts":  map[string]any{"paid_minor": "49900"}}
			gate := map[string]any{"code": "REFUND_FINALITY", "status": "BLOCKED", "reason_code": "REFUND_PROCESSING", "source_order_id": order, "effective_at": changed}
			write(map[string]any{"contract_version": "dcai.finance-order-pull-page/1.0", "source_fact_schema_version": "source.order-fact/1.0",
				"stream": "VIP_ORDERS", "through": through, "has_more": more, "next_cursor": cursor,
				"items": []any{map[string]any{"cursor": cursor, "facts": []any{fact}, "gates": []any{gate}}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	service := workspace{db: db, sources: map[string]sourceConfig{"DCAI": {BaseURL: provider.URL, Token: "synthetic-service-token"}}}
	if _, err := service.pullOrder(ctx, pullInput{Source: "DCAI", Stream: "VIP_ORDERS", OrderID: "VIP-8"}); err != nil {
		t.Fatalf("single-order gate before the first sync: %v", err)
	}
	var beforeSync int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM dcai_order_blockers WHERE source_row_id='8' AND cardinality(reasons)>0`).Scan(&beforeSync); err != nil || beforeSync != 1 {
		t.Fatalf("known refund blocker should survive before any bulk checkpoint: %d %v", beforeSync, err)
	}
	read := func() (string, bool, int) {
		t.Helper()
		var cursor string
		var complete bool
		var versions int
		if err := db.QueryRow(ctx, `SELECT next_id,completed_through IS NOT NULL,(SELECT count(*) FROM source_order_versions)
		 FROM dcai_order_sync WHERE stream='VIP_ORDERS'`).Scan(&cursor, &complete, &versions); err != nil {
			t.Fatal(err)
		}
		return cursor, complete, versions
	}
	if _, err := service.syncDCAIOrders(ctx, "VIP_ORDERS", &from); !errors.Is(err, errSourceRead) {
		t.Fatalf("fault should fail the run: %v", err)
	}
	if cursor, complete, versions := read(); cursor != "9" || complete || versions != 1 {
		t.Fatalf("partial state = cursor %s, complete %t, versions %d", cursor, complete, versions)
	}
	failSecondPage.Store(false)
	if _, err := service.syncDCAIOrders(ctx, "VIP_ORDERS", nil); err != nil {
		t.Fatal(err)
	}
	if cursor, complete, versions := read(); cursor != "10" || !complete || versions != 2 || cutoffs.Load() != 1 {
		t.Fatalf("resumed state = cursor %s, complete %t, versions %d, cutoffs %d", cursor, complete, versions, cutoffs.Load())
	}
	lookup := pullInput{Source: "DCAI", Stream: "VIP_ORDERS", OrderID: "VIP-9"}
	if _, err := service.pullOrder(ctx, lookup); err != nil {
		t.Fatalf("single-order gate refresh: %v", err)
	}
	if cursor, complete, versions := read(); cursor != "10" || !complete || versions != 2 {
		t.Fatalf("lookup must not advance the completed stream: %s %t %d", cursor, complete, versions)
	}
	other := httptest.NewServer(provider.Config.Handler)
	t.Cleanup(other.Close)
	mismatch := workspace{db: db, sources: map[string]sourceConfig{"DCAI": {BaseURL: other.URL, Token: "synthetic-service-token"}}}
	if _, err := mismatch.pullOrder(ctx, lookup); !errors.Is(err, errSyncOrigin) {
		t.Fatalf("direct refresh used the wrong endpoint: %v", err)
	}
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCAI"}})
		return err
	}); !errors.Is(err, errDCAIFileImport) {
		t.Fatalf("a local file must not mix into a bound provider: %v", err)
	}
	if _, err := service.syncDCAIOrders(ctx, "VIP_ORDERS", nil); err != nil {
		t.Fatalf("24-hour replay failed: %v", err)
	}
	if _, _, versions := read(); versions != 2 {
		t.Fatalf("duplicate source versions after replay: %d", versions)
	}
	var blocked int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM dcai_order_blockers WHERE cardinality(reasons)>0`).Scan(&blocked); err != nil || blocked != 2 {
		t.Fatalf("the older page replay must not overwrite the single-order update: %d %v", blocked, err)
	}
	earlier := from.AddDate(0, 0, -3)
	backfill, err := service.syncDCAIOrders(ctx, "VIP_ORDERS", &earlier)
	if err != nil || !backfill.From.Equal(earlier) || scanStart.Load() != earlier.Format(time.RFC3339) || backfill.Versions != 0 {
		t.Fatalf("explicit earlier backfill ran at the wrong cursor: %+v %v", backfill, err)
	}
}

func TestSettlementSourceLockOrdersSyncCommitsWithTheFinanceSnapshot(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	day := 25
	product, err := s.createProduct(ctx, createProductInput{Code: "SOURCE", Name: "Source sample", PeriodMode: "calendar", CycleStartDay: &day})
	if err != nil {
		t.Fatal(err)
	}
	id := product.(map[string]int64)["id"]
	if _, err := s.createMapping(ctx, createMappingInput{ProductID: id, Source: "DCAI", Domain: "VIP", SourceKey: "VIP"}); err != nil {
		t.Fatal(err)
	}
	result, err := s.createPeriod(ctx, createPeriodInput{ProductID: id, Name: "August", SettlementMonth: "2026-08"})
	if err != nil {
		t.Fatal(err)
	}
	periodID := result.(map[string]int64)["id"]
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	amount := int64(49900)
	write := func(ctx context.Context, version string) error {
		return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCAI", Domain: "VIP", SourceKey: "VIP", OrderID: "O1", RecordKey: "order:1",
				Version: version, Kind: "ORDER_PAID", PaymentID: "PAY-1", AmountMinor: &amount, UpdatedAt: at, OccurredAt: at}})
			return err
		})
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := readSettlementScope(ctx, tx, periodID, true); err != nil {
		t.Fatal(err)
	}
	if err := write(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	var pgError *pgconn.PgError
	if err := lockSettlementSources(ctx, tx, periodID); !errors.As(err, &pgError) || pgError.Code != "40001" {
		t.Fatalf("an older snapshot must retry after a concurrent source commit: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if err := lockSettlementSources(ctx, tx, periodID); err != nil {
			return err
		}
		blocked, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		if err := write(blocked, "2"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("source write should wait until confirmation releases its lock: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := write(ctx, "2"); err != nil {
		t.Fatal(err)
	}
}
