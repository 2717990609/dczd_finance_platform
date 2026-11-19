package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestFinancialInputChangesRefreshOnlyAffectedDrafts(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	day := 1
	product := id(s.createProduct(ctx, createProductInput{Code: "AUTO", Name: "自动试算样本", PeriodMode: "calendar", CycleStartDay: &day}))
	other := id(s.createProduct(ctx, createProductInput{Code: "OTHER", Name: "无关产品", PeriodMode: "calendar", CycleStartDay: &day}))
	august := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月", SettlementMonth: "2026-08"}))
	september := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月", SettlementMonth: "2026-09"}))
	unrelated := id(s.createPeriod(ctx, createPeriodInput{ProductID: other, Name: "八月其他", SettlementMonth: "2026-08"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "DEMO", SourceKey: "PRODUCT"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "财务样本"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-01-01T00:00:00+08:00"}))
	publish := func(rate int64, only *int64) {
		t.Helper()
		if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "销售分润", PeriodID: only, Components: []component{{Type: "sales", Mode: "rate", Value: rate, SourceRole: "LOCK_SALES_USER"}}}); err != nil {
			t.Fatal(err)
		}
	}
	publish(1000, nil)
	if _, err := s.db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, shanghai)
	store := func(rev, display string, amount int64, occurs time.Time, updated time.Time) {
		t.Helper()
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Domain: "DEMO", SourceKey: "PRODUCT", Kind: "ORDER_PAID", RecordKey: "demo:1", OrderID: "DEMO-1", OrderNo: display, PaymentID: "PAY-1", Version: rev, AmountMinor: &amount, OccurredAt: occurs, UpdatedAt: updated, Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "seller"}}}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	queued := func(ids ...int64) {
		t.Helper()
		rows, err := s.db.Query(ctx, "SELECT period_id FROM draft_refresh_queue ORDER BY period_id")
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil || len(got) != len(ids) {
			t.Fatalf("queued periods %v, want %v, error %v", got, ids, err)
		}
		for i, id := range ids {
			if got[i] != id {
				t.Fatalf("queued periods %v, want %v", got, ids)
			}
		}
	}
	value := func(period, version, total int64) {
		t.Helper()
		var v, n int64
		if err := s.db.QueryRow(ctx, `SELECT version,total_minor FROM calculation_drafts WHERE period_id=$1`, period).Scan(&v, &n); err != nil || v != version || n != total {
			t.Fatalf("draft %d version %d total %d, want %d %d: %v", period, v, n, version, total, err)
		}
	}
	store("1", "单据", 10000, at, at)
	queued(august)
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	value(august, 1, 1000)
	queued()
	store("2", "仅显示号码更正", 10000, at, at.Add(time.Hour))
	queued()
	store("3", "调整支付日期", 20000, at.AddDate(0, 1, 0), at.Add(2*time.Hour))
	queued(august, september)
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	value(august, 2, 0)
	value(september, 1, 2000)
	queued()
	publish(2000, &september)
	queued(september)
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	value(september, 2, 4000)
	queued()
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calculation_drafts WHERE period_id=$1)`, unrelated).Scan(&exists); err != nil || exists {
		t.Fatalf("unrelated product was calculated: %v %v", exists, err)
	}
}
