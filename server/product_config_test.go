package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestProductConfigImportPreservesVersionAndRejectsStaleOrChangedDefinition(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	service := workspace{db: db}
	at := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	day := 25
	input := productConfigFile{Environment: "legacy-test", CapturedAt: at.Add(time.Hour), Products: []productConfigEntry{{
		SourceID: "7225e54e-ab18-4fd6-8c4d-02fb7eec1000", Code: "LEGACY_VIP", Name: "Legacy VIP", Project: "TEST_AI",
		Source: "DCAI", SourceKey: "test.vip", PeriodMode: "calendar", CycleStartDay: &day,
		Packages: []productRulePackageConfig{{SourceID: "7225e54e-ab18-4fd6-8c4d-02fb7eec1001", Code: "TEST-VIP", Name: "VIP scenario",
			Project: "TEST_AI", Kind: "LEGACY_BUNDLE", ScopeType: "BUSINESS_SCENARIO", ScopeID: "STANDARD",
			Versions: []productRuleVersionConfig{{SourceID: "7225e54e-ab18-4fd6-8c4d-02fb7eec1002", Number: 3, Status: "PUBLISHED",
				From: at, Hash: "example-sealed", Definition: json.RawMessage(`{"cycle":"MONTHLY_25_TO_24","payment":{"rate":1200}}`)}}}},
	}}}
	store := func() error {
		return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, input) })
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
	if err := store(); !errors.Is(err, errProductConfigStale) {
		t.Fatalf("stale import returned %v", err)
	}
	input.CapturedAt = input.CapturedAt.Add(2 * time.Hour)
	input.Products[0].Packages[0].Versions[0].Definition = json.RawMessage(`{"cycle":"MONTHLY_25_TO_24","payment":{"rate":1900}}`)
	if err := store(); !errors.Is(err, errProductConfigConflict) {
		t.Fatalf("altered published version returned %v", err)
	}
	var product int64
	var total int
	var paused bool
	if err := db.QueryRow(ctx, `SELECT product_id,paused,(SELECT count(*) FROM product_rule_versions)
	 FROM product_rule_packages`).Scan(&product, &paused, &total); err != nil || !paused || total != 1 {
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
	if _, err := db.Exec(ctx, `UPDATE product_rule_packages SET scope_type='LEGACY_SCOPE'`); err != nil {
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
	if !errors.Is(err, errProductRuleUnavailable) {
		t.Fatalf("trial returned %v", err)
	}
	_, err = service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Ad hoc", Components: []component{{Type: "sales", Mode: "rate", Value: 500}}})
	if !errors.Is(err, errProductRuleUnavailable) {
		t.Fatalf("publishing returned %v", err)
	}
}

func TestProductRuleListShowsApplicableBusinessScenarioName(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cutoff := at.AddDate(0, 1, 0)
	day := 25
	input := productConfigFile{Environment: "test", CapturedAt: cutoff, Products: []productConfigEntry{{
		SourceID: "11000000-0000-4000-8000-000000000001", Code: "MEMBER", Name: "会员", Project: "AI",
		Source: "DCAI", SourceKey: "member", PeriodMode: "calendar", CycleStartDay: &day,
		Packages: []productRulePackageConfig{{SourceID: "11000000-0000-4000-8000-000000000002", Code: "INTERNAL_MEMBER", Name: "会员提成",
			Project: "AI", Kind: "LEGACY_BUNDLE", ScopeType: "BUSINESS_SCENARIO", ScopeID: "MEMBER_NEW",
			Versions: []productRuleVersionConfig{
				{SourceID: "11000000-0000-4000-8000-000000000003", Number: 1, Status: "PUBLISHED", From: at, Through: &cutoff, Hash: "first", Definition: json.RawMessage(`{}`)},
				{SourceID: "11000000-0000-4000-8000-000000000004", Number: 2, Status: "PUBLISHED", From: cutoff, Hash: "second", Definition: json.RawMessage(`{}`)},
			}}},
		BusinessRoutes: []catalogBusinessRoute{
			{SourceID: "11000000-0000-4000-8000-000000000005", Source: "DCAI", Domain: "VIP", Entity: "VIP_ORDER", RecordTable: "vip_order",
				BusinessKey: "vip:new", Classifier: "test", SKU: "MEMBER_NEW", Label: "会员新购", Version: 1, From: at, Through: &cutoff},
			{SourceID: "11000000-0000-4000-8000-000000000006", Source: "DCAI", Domain: "VIP", Entity: "VIP_ORDER", RecordTable: "vip_order",
				BusinessKey: "vip:new", Classifier: "test", SKU: "MEMBER_NEW", Label: "公司普通新报", Version: 2, From: cutoff},
		},
	}}}
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, input) }); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := db.QueryRow(ctx, catalogRulesSQL, nil).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Version    int
		ScopeLabel string
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Version != 2 || rows[0].ScopeLabel != "公司普通新报" ||
		rows[1].Version != 1 || rows[1].ScopeLabel != "会员新购" {
		t.Fatalf("expected business scenario names from each rule's effective time, got %+v", rows)
	}
}
