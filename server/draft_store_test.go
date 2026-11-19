package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := emptyIntegrationDB(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func emptyIntegrationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("FINANCE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("FINANCE_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasSuffix(cfg.ConnConfig.Database, "_dev") {
		t.Fatal("integration tests must point to a dedicated development database")
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schemaName := pgx.Identifier{fmt.Sprintf("finance_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schemaName+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg = cfg.Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestDraftStoreReusesUnchangedVersionAndAtomicallyReplacesChangedLines(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t)}
	mustID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	day := 25
	product := mustID(service.createProduct(ctx, createProductInput{Code: "test", Name: "Test", PeriodMode: "calendar", CycleStartDay: &day}))
	period := mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "August", SettlementMonth: "2026-08"}))
	if _, err := service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "Duplicate", SettlementMonth: "2026-08"}); !errors.Is(err, conflict) {
		t.Fatalf("duplicate calendar period: %v", err)
	}
	person := mustID(service.createPerson(ctx, createPersonInput{Name: "Alice"}))
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "VIP", SourceKey: "VIP499"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCAI", ExternalID: "test-a", ValidFrom: "2026-01-01T00:00:00+08:00"}))
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "2026", Components: []component{{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "LOCK_SALES_USER"}}}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	fact := sourceFact{Source: "DCAI", Kind: "ORDER_PAID", OrderID: "O1", PaymentID: "P1", RecordKey: "orders:1", Domain: "VIP", SourceKey: "VIP499",
		OccurredAt: at, Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "test-a"}}}
	insert := func(version string, amount int64, updated time.Time) {
		t.Helper()
		fact.Version, fact.AmountMinor, fact.UpdatedAt = version, &amount, updated
		raw, err := json.Marshal(fact)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
			VALUES($1,$2,'',$3,$4,$5,$6,$7,$8)`, fact.Source, fact.OrderID, fact.RecordKey, version, fact.Kind, updated, raw, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(version, total int64) {
		t.Helper()
		data, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		head := data.(draftSummary)
		if head.Version != version || head.Total != total || head.IssueCount != 0 || head.LineCount != 1 {
			t.Fatalf("unexpected draft: %+v", head)
		}
	}
	insert("1", 990, at)
	check(1, 99)
	fact.OrderNo = "display-only change"
	insert("2", 990, at.Add(time.Minute))
	check(1, 99)
	insert("3", 105, at.Add(2*time.Minute))
	check(2, 11)
	var count int
	var amount int64
	err := service.db.QueryRow(ctx, "SELECT count(*), max(amount_minor) FROM calculation_draft_lines WHERE period_id=$1", period).Scan(&count, &amount)
	if err != nil || count != 1 || amount != 11 {
		t.Fatalf("stored lines: count=%d amount=%d error=%v", count, amount, err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d&version=1", period), nil)
	service.getDraft(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale draft pagination returned HTTP %d", response.Code)
	}
}

func TestCalendarDraftDigestRemainsCompatible(t *testing.T) {
	result := paymentDraft{Lines: []draftLine{{FactID: 42, PersonID: 3, IdentityID: 4, RuleID: 17, Component: "sales", Base: 990, Amount: 99}}}
	facts := []paymentInput{{ID: 42, Fact: sourceFact{Source: "DCAI", OrderID: "O1", PaymentID: "P1", RecordKey: "orders:1",
		OccurredAt: time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)}}}
	digest, err := digestDraft(17, result, facts)
	const previous = "4d531cd1e84d9fb81bdb5789bb4d9599d98c7732f3190db8fd1322a1249162a1"
	if err != nil || fmt.Sprintf("%x", digest) != previous {
		t.Fatalf("calendar digest = %x, error = %v", digest, err)
	}
}

func TestClassDraftKeepsNewCompanyVersionEvenWhenAmountIsSame(t *testing.T) {
	fact := paymentInput{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "1", PaymentID: "1", SalesCompanyID: "first"}}
	draft := paymentDraft{Lines: []draftLine{{FactID: 1, RuleID: 1, EnrollmentID: 1, PersonID: 1, Amount: 99}}}
	first, err := digestDraft(1, draft, []paymentInput{fact})
	if err != nil {
		t.Fatal(err)
	}
	fact.Fact.SalesCompanyID = "second"
	next, err := digestDraft(1, draft, []paymentInput{fact})
	if err != nil || string(first) == string(next) {
		t.Fatalf("company change did not invalidate the draft, error=%v", err)
	}
}
