package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const twoTierPeriodRule = `{
 "amount_basis":"ACTUAL_RECEIVED","recognition_trigger":"PAID","refund_policy":"PROPORTIONAL","stacking_policy":"SUM",
 "rounding_policy":{"mode":"HALF_UP","scale":2},
 "policy_scope":{"sales_systems":["DCAI"],"business_sku_codes":["FIRST","SECOND"],"financial_event_types":["order.paid"]},
 "settlement_policy":{"cycle":"MONTHLY","negative_balance_policy":"CARRY_FORWARD"},
 "benefit_actions":[{"item_key":"operations","label":"运营","type":"TIER_RATE","metric_code":"MONTHLY_NET_RECEIPTS","metric_unit":"AMOUNT_MINOR",
 "beneficiary_mode":"TEAM_POOL","pool_name":"运营池","pool_code":"operations","pool_party_id":"party","pool_organization_unit_id":"unit","allocation_owner_role":"owner",
 "tier_table":[{"id":"original-1","rate_bps":0,"max_exclusive":10001},{"id":"original-2","rate_bps":100,"min_inclusive":10001}]}]
}`

func TestCatalogPeriodRuleQuotesWholeNetAtCentBoundary(t *testing.T) {
	rule, err := parseCatalogPeriodRule(json.RawMessage(twoTierPeriodRule))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ received, refunds, carry, net, reward, nextCarry int64 }{
		{15000, 2000, -3000, 10000, 0, 0},
		{15001, 2000, -3000, 10001, 100, 0},
		{500, 1400, -100, 0, 0, -1000},
	} {
		result, err := quoteCatalogPeriod(rule.Items, test.received, test.refunds, test.carry)
		if err != nil || result.Basis != test.net || result.Total != test.reward || result.CarryForward != test.nextCarry {
			t.Errorf("actual=%+v err=%v test=%+v", result, err, test)
		}
	}
	if _, err := quoteCatalogPeriod(rule.Items, 200, 100, 1); err == nil {
		t.Fatal("positive carry was accepted")
	}
}

func TestCatalogPeriodPublicationIsScopedVersionedAndKeepsNextRule(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	const product = 801
	const pack = "10000000-0000-4000-8000-000000000001"
	const baseline = "10000000-0000-4000-8000-000000000002"
	const future = "10000000-0000-4000-8000-000000000003"
	for _, statement := range []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE
		VALUES(801,'DCAI_FIXTURE','整期配置样本','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		VALUES(801,'10000000-0000-4000-8000-000000000011','test','DCAI','DCAI','fixture','2026-10-01','\x1234567890123456789012345678901234567890123456789012345678901234')`,
		`INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
		VALUES('10000000-0000-4000-8000-000000000001',801,'fixture','月度运营','DCAI','COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE','aggregate',true)`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO product_rule_versions
	 (source_id,package_id,version_no,source_status,effective_from,effective_to,content_hash,definition,observed_at)
	 VALUES($1,$3,1,'PUBLISHED','2026-08-01 00:00+08','2026-11-01 00:00+08','fixture',$4,'2026-10-01'),
	 ($2,$3,2,'PUBLISHED','2026-11-01 00:00+08',NULL,'fixture',$4,'2026-10-01')`, baseline, future, pack, json.RawMessage(twoTierPeriodRule)); err != nil {
		t.Fatal(err)
	}
	post := func(actor financeRuleActor, input catalogPeriodPublishInput) (catalogPublication, error) {
		var result catalogPublication
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			var err error
			result, err = writeCatalogPeriodRule(ctx, tx, input, actor)
			return err
		})
		return result, err
	}
	actor := financeRuleActor{subject: "fixture", permits: func(id int64, publish bool) bool { return id == product && publish }}
	input := catalogPeriodPublishInput{BaseID: baseline, EffectiveFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, shanghai),
		Edits: []catalogPeriodTierEdit{{ItemKey: "operations", Tiers: []catalogPeriodTier{{From: 0, Rate: 120}}}}}
	reader := financeRuleActor{permits: func(int64, bool) bool { return false }}
	if _, err := post(reader, input); !errors.Is(err, errRulePermission) {
		t.Fatalf("unauthorized publication: %v", err)
	}
	invalidStart := input
	invalidStart.EffectiveFrom = input.EffectiveFrom.Add(time.Hour)
	if _, err := post(actor, invalidStart); !errors.Is(err, errCatalogPeriodStart) {
		t.Fatalf("mid-period publication: %v", err)
	}
	published, err := post(actor, input)
	if err != nil || published.Version != 3 {
		t.Fatalf("publish result=%+v err=%v", published, err)
	}
	for id, count := range map[string]int{baseline: 2, future: 2, published.ID: 1} {
		version, err := readCatalogPeriodVersion(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		rule, err := parseCatalogPeriodRule(version.raw)
		if err != nil || len(rule.Items[0].Tiers) != count || !version.Paused {
			t.Fatalf("rule %s changed unexpectedly: %v %v", id, rule.Items, err)
		}
	}
	if _, err := post(actor, input); err == nil {
		t.Fatal("reused superseded version")
	}
}

func TestCatalogPeriodTierEditingPreservesOriginalFields(t *testing.T) {
	base := json.RawMessage(twoTierPeriodRule)
	updated, err := editCatalogPeriodTiers(base, []catalogPeriodTierEdit{
		{ItemKey: "operations", Tiers: []catalogPeriodTier{{From: 0, Rate: 70}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := parseCatalogPeriodRule(updated)
	if err != nil || len(rule.Items) != 1 || len(rule.Items[0].Tiers) != 1 || rule.Items[0].PoolName != "运营池" || len(rule.Scenarios) != 2 {
		t.Fatalf("updated rule=%+v err=%v", rule, err)
	}
	old, err := parseCatalogPeriodRule(base)
	if err != nil || len(old.Items[0].Tiers) != 2 {
		t.Fatal("base version changed")
	}
	max := int64(500)
	if _, err := editCatalogPeriodTiers(base, []catalogPeriodTierEdit{
		{ItemKey: "operations", Tiers: []catalogPeriodTier{{From: 0, Until: &max}, {From: 501, Rate: 100}}},
	}); err == nil {
		t.Fatal("gap accepted")
	}
}
