package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFinanceInitializationIsAtomicAndSafeToRetry(t *testing.T) {
	ctx := context.Background()
	cfg := emptyIntegrationDB(t).Config()
	cfg.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var schema string
	if err := db.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	input := func(database, targetSchema, openingPerson string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{
 "database":%q,
 "schema":%q,
 "organization":{"capturedAt":"2026-11-01T00:00:00Z",
  "people":[{"id":"10000000-0000-4000-8000-000000000001","name":"财务员"}],
  "units":[{"id":"10000000-0000-4000-8000-000000000002","name":"示例公司","kind":"COMPANY","status":"ACTIVE"}],
  "assignments":[{"id":"10000000-0000-4000-8000-000000000003","personId":"10000000-0000-4000-8000-000000000001",
   "unitId":"10000000-0000-4000-8000-000000000002","kind":"PRIMARY","from":"2026-01-01T00:00:00Z"}]},
 "productConfig":{"environment":"acceptance","capturedAt":"2026-11-01T00:00:00Z",
  "products":[{"sourceId":"10000000-0000-4000-8000-000000000004","code":"PRACTICE","name":"实战班",
   "project":"PRACTICE","source":"DCZD","sourceKey":"practice","periodMode":"class",
   "packages":[{"sourceId":"10000000-0000-4000-8000-000000000005","code":"practice","name":"实战班规则",
    "project":"PRACTICE","kind":"COMPONENT_RULE","scopeType":"BUSINESS_SCENARIO","scopeId":"class",
    "versions":[{"sourceId":"10000000-0000-4000-8000-000000000006","version":1,"status":"PUBLISHED","from":"2026-01-01T00:00:00Z",
     "hash":"test-rule","definition":{}}]}]}]},
 "salesContext":{"source":"DCAI","capturedAt":"2026-11-01T00:00:00Z","people":[],"personLinks":[],
  "companyLinks":[],"categories":[],"appointments":[{"id":"10000000-0000-4000-8000-000000000007",
   "partyId":"10000000-0000-4000-8000-000000000001","role":"SALES","scope":"PROJECT","key":"DCAI","from":"2026-01-01T00:00:00Z"}]},
 "practiceOpenings":[{"productCode":"PRACTICE","sourceSettlementId":"10000000-0000-4000-8000-000000000008",
  "throughAt":"2026-10-31T00:00:00Z","people":[{"legacyPartyId":%q,"newCount":3}]}]
}`, database, targetSchema, openingPerson))
	}
	assertNoBusinessSchema := func() {
		t.Helper()
		var exists bool
		if err := db.QueryRow(ctx, "SELECT to_regclass('products') IS NOT NULL").Scan(&exists); err != nil || exists {
			t.Fatalf("trial or failed initialization persisted its schema: %t %v", exists, err)
		}
	}
	name := db.Config().ConnConfig.Database
	person := "10000000-0000-4000-8000-000000000001"
	if _, err := initializeFinance(ctx, db, input("another_database", schema, person), true); !errors.Is(err, errFinanceInitializationTarget) {
		t.Fatalf("wrong target: %v", err)
	}
	if _, err := initializeFinance(ctx, db, input(name, "public", person), true); !errors.Is(err, errFinanceInitializationTarget) {
		t.Fatalf("wrong schema: %v", err)
	}
	assertNoBusinessSchema()
	if _, err := initializeFinance(ctx, db, input(name, schema, "10000000-0000-4000-8000-000000000009"), true); !errors.Is(err, errPracticeCountPerson) {
		t.Fatalf("missing opening person: %v", err)
	}
	assertNoBusinessSchema()
	raw := input(name, schema, person)
	if same, err := initializeFinance(ctx, db, raw, false); err != nil || same {
		t.Fatalf("trial: %t %v", same, err)
	}
	assertNoBusinessSchema()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	failures := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			same, err := initializeFinance(ctx, db, raw, true)
			results <- same
			failures <- err
		})
	}
	wg.Wait()
	if first, second := <-results, <-results; first == second {
		t.Error("concurrent identical files must apply once and return an idempotent response once")
	}
	for range 2 {
		if err := <-failures; err != nil {
			t.Error(err)
		}
	}
	var opened, rules, assignments, appointments, accounts, enabled int
	if err := db.QueryRow(ctx, `SELECT COALESCE((SELECT sum(new_count) FROM practice_count_opening_people),0),
	 (SELECT count(*) FROM product_rule_versions), (SELECT count(*) FROM organization_assignments),
	 (SELECT count(*) FROM catalog_current_sales_appointments), (SELECT count(*) FROM finance_accounts),
	 (SELECT count(*) FROM products WHERE settlement_enabled)`).Scan(&opened, &rules, &assignments, &appointments, &accounts, &enabled); err != nil ||
		opened != 3 || rules != 1 || assignments != 1 || appointments != 1 || accounts != 0 || enabled != 0 {
		t.Fatalf("unexpected initialized data: opening=%d rules=%d assignments=%d appointments=%d accounts=%d enabled=%d err=%v",
			opened, rules, assignments, appointments, accounts, enabled, err)
	}
	if _, err := initializeFinance(ctx, db, input(name, schema, "10000000-0000-4000-8000-000000000009"), true); !errors.Is(err, errFinanceInitializationExisting) {
		t.Fatalf("different file after initialization: %v", err)
	}
	file := filepath.Join(t.TempDir(), "initialize.json")
	if err := os.WriteFile(file, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initializeFinanceFromFile(ctx, config{database: db.Config()}, file, true); !errors.Is(err, errFinanceInitializationExisting) {
		t.Fatalf("edited input bytes were accepted as the same file: %v", err)
	}
}

func TestFinanceInitializationRejectsExistingSchema(t *testing.T) {
	db := emptyIntegrationDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `CREATE COLLATION existing_configuration FROM "C"`); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := db.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"database":%q,"schema":%q}`, db.Config().ConnConfig.Database, schema))
	if _, err := initializeFinance(ctx, db, raw, true); !errors.Is(err, errFinanceInitializationExisting) {
		t.Fatalf("occupied schema: %v", err)
	}
	var exists bool
	if err := db.QueryRow(ctx, "SELECT to_regclass('products') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("occupied schema was changed: %t %v", exists, err)
	}
}
