package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestLegacyCatalogImportPreservesVersionAndRejectsStaleOrChangedDefinition(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	service := workspace{db: db}
	at := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	day := 25
	input := legacyCatalogFile{Environment: "legacy-test", CapturedAt: at.Add(time.Hour), Products: []legacyCatalogProduct{{
		SourceID: "7225e54e-ab18-4fd6-8c4d-02fb7eec1000", Code: "LEGACY_VIP", Name: "Legacy VIP", Project: "TEST_AI",
		Source: "DCAI", SourceKey: "test.vip", PeriodMode: "calendar", CycleStartDay: &day,
		Packages: []legacyCatalogPackage{{SourceID: "7225e54e-ab18-4fd6-8c4d-02fb7eec1001", Code: "TEST-VIP", Name: "VIP scenario",
			Project: "TEST_AI", Kind: "LEGACY_BUNDLE", ScopeType: "BUSINESS_SCENARIO", ScopeID: "STANDARD",
			Versions: []legacyCatalogVersion{{SourceID: "7225e54e-ab18-4fd6-8c4d-02fb7eec1002", Number: 3, Status: "PUBLISHED",
				From: at, Hash: "example-sealed", Definition: json.RawMessage(`{"cycle":"MONTHLY_25_TO_24","payment":{"rate":1200}}`)}}}},
	}}}
	store := func() error {
		return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return storeLegacyCatalog(ctx, tx, input) })
	}
	if err := store(); err != nil {
		t.Fatal(err)
	}
	if err := store(); err != nil {
		t.Fatal(err)
	}
	input.CapturedAt = input.CapturedAt.Add(time.Hour)
	input.Products[0].Packages[0].Paused = true
	if err := store(); err != nil {
		t.Fatal(err)
	}
	input.CapturedAt = input.CapturedAt.Add(-time.Hour)
	input.Products[0].Packages[0].Paused = false
	if err := store(); !errors.Is(err, errLegacyCatalogStale) {
		t.Fatalf("stale import returned %v", err)
	}
	input.CapturedAt = input.CapturedAt.Add(2 * time.Hour)
	input.Products[0].Packages[0].Versions[0].Definition = json.RawMessage(`{"cycle":"MONTHLY_25_TO_24","payment":{"rate":1900}}`)
	if err := store(); !errors.Is(err, errLegacyCatalogConflict) {
		t.Fatalf("altered published version returned %v", err)
	}
	var product int64
	var total int
	var paused bool
	if err := db.QueryRow(ctx, `SELECT product_id,paused,(SELECT count(*) FROM legacy_rule_versions)
	 FROM legacy_rule_packages`).Scan(&product, &paused, &total); err != nil || !paused || total != 1 {
		t.Fatalf("product=%d paused=%t versions=%d err=%v", product, paused, total, err)
	}
	var list json.RawMessage
	if err := db.QueryRow(ctx, catalogRulesSQL, nil).Scan(&list); err != nil {
		t.Fatal(err)
	}
	var rules []struct {
		ScopeType  string
		PolicyKind *string
	}
	if err := json.Unmarshal(list, &rules); err != nil || len(rules) != 1 ||
		rules[0].ScopeType != "BUSINESS_SCENARIO" || rules[0].PolicyKind != nil {
		t.Fatalf("unexpected product rule representation %s: %v", list, err)
	}
	if _, err := db.Exec(ctx, `UPDATE legacy_rule_packages SET scope_type='LEGACY_SCOPE'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, catalogRulesSQL, nil).Scan(&list); err != nil {
		t.Fatal(err)
	}
	rules = nil
	if err := json.Unmarshal(list, &rules); err != nil || len(rules) != 1 || rules[0].ScopeType != "" {
		t.Fatalf("unexpected scope in product rule list %s: %v", list, err)
	}
	result, err := service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "Check", SettlementMonth: "2026-08"})
	if err != nil {
		t.Fatal(err)
	}
	period := result.(map[string]int64)["id"]
	_, err = service.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if !errors.Is(err, errLegacyRuleNotAdapted) {
		t.Fatalf("trial returned %v", err)
	}
	_, err = service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Ad hoc", Components: []component{{Type: "sales", Mode: "rate", Value: 500}}})
	if !errors.Is(err, errLegacyRuleNotAdapted) {
		t.Fatalf("publishing returned %v", err)
	}
}
