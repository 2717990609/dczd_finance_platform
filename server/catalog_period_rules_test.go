package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestCatalogPeriodRuleRejectsIgnoredPerArrangementScopes(t *testing.T) {
	for _, field := range []string{
		`"sales_scope_unit_ids":["41000000-0000-4000-8000-000000000111"]`,
		`"leader_scope":{"orders":"all"}`,
	} {
		raw := strings.Replace(twoTierPeriodRule, `"tier_table":`, field+`,"tier_table":`, 1)
		if _, err := parseCatalogPeriodRule(json.RawMessage(raw)); !errors.Is(err, errCatalogRateUnavailable) {
			t.Fatalf("must not accept an unexecuted scope %s: %v", field, err)
		}
	}
}

func TestCatalogPeriodRulesUseOrganizationDirectoryWithoutImportedAliases(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	const company = "41000000-0000-4000-8000-000000000111"
	const team = "41000000-0000-4000-8000-000000000112"
	const disabled = "41000000-0000-4000-8000-000000000113"
	if _, err := db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,name,kind,status) VALUES
	 ($1,NULL,'新公司','COMPANY','ACTIVE'),($2,$1,'新交付组','TEAM','ACTIVE'),($3,$1,'旧组','TEAM','INACTIVE')`, company, team, disabled); err != nil {
		t.Fatal(err)
	}
	base := json.RawMessage(twoTierPeriodRule)
	edits := []catalogPeriodComponentEdit{
		{Label: "公司收款", TargetKind: "company", OrganizationKey: "unit:" + company, Tiers: []catalogPeriodTier{{Rate: 250}}},
		{Label: "团队收款", TargetKind: "pool", OrganizationKey: "unit:" + team, PoolName: "交付池",
			OwnerRole: "SERVICE_DELIVERY", MemberRole: "DELIVERY_TEAM", Tiers: []catalogPeriodTier{{Rate: 125}}},
	}
	updated, err := applyCatalogPeriodChanges(ctx, db, base, nil, edits)
	if err != nil {
		t.Fatalf("active organizations from the directory must be configurable: %v", err)
	}
	rule, err := parseCatalogPeriodRule(updated)
	if err != nil || len(rule.Items) != 2 || rule.Items[0].unit != company || rule.Items[1].unit != team {
		t.Fatalf("published recipients must keep both selected organization IDs: %+v %v", rule.Items, err)
	}
	if err := nameCatalogPeriodTargets(ctx, db, rule.Items); err != nil || rule.Items[0].TargetName != "新公司" || rule.Items[1].TargetName != "新交付组" {
		t.Fatalf("the rule must display the referenced organizations: %+v %v", rule.Items, err)
	}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		directory, err := readCatalogAggregateDirectory(ctx, tx, []catalogAggregateRule{{Definition: rule}}, at, at.Add(time.Hour))
		if err != nil {
			return err
		}
		if _, key, issue := directory.groupTarget(rule.Items[0], at); key.Unit != company || issue != "" {
			t.Fatalf("wrong company payout: %+v %s", key, issue)
		}
		if _, key, issue := directory.groupTarget(rule.Items[1], at); key.Unit != team || issue != "BENEFIT_POOL_OWNER_MISSING" {
			t.Fatalf("the team resolves by ID and still needs a dated owner: %+v %s", key, issue)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest,benefit_ready)
	 VALUES('DCAI','2026-09-01',sha256('fixture'::bytea),false);
	 INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(9101,'交付负责人'),(9102,'交付成员')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `WITH memberships AS (
	 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
	 SELECT gen_random_uuid(),id,$1::uuid,'PRIMARY','2026-09-01' FROM people WHERE id IN (9101,9102)
	 RETURNING person_id
	 ) INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
	 SELECT gen_random_uuid(),'DCAI',person_id,CASE person_id WHEN 9101 THEN 'SERVICE_DELIVERY' ELSE 'DELIVERY_TEAM' END,
	 'ORGANIZATION_UNIT',$1::uuid::text,'2026-09-01','2026-09-01' FROM memberships`, team); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		directory, err := readCatalogAggregateDirectory(ctx, tx, []catalogAggregateRule{{Definition: rule}}, at, at.Add(time.Hour))
		if err != nil {
			return err
		}
		_, key, issue := directory.groupTarget(rule.Items[1], at)
		if key.Unit != team || key.Owner != 9101 || len(key.Members) != 1 || key.Members[0] != 9102 || issue != "" {
			t.Fatalf("the selected team uses payment-date membership even without a source alias: %+v %s", key, issue)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	edits[1].OrganizationKey = "unit:" + disabled
	if _, err := applyCatalogPeriodChanges(ctx, db, base, nil, edits); !errors.Is(err, errCatalogPeriodRecipient) {
		t.Fatalf("accepted a disabled organization: %v", err)
	}
}

func TestCatalogPeriodComponentChangesPreserveOriginalAndUseSelectedRecipient(t *testing.T) {
	choices := catalogStructureOptions{
		Organizations: []catalogStructureOption{{Value: "company", UnitID: "company-unit", Kind: "COMPANY"}, {Value: "pool", UnitID: "pool-unit", Kind: "TEAM"}},
		Roles:         []catalogStructureOption{{Value: "OWNER", Organization: true}, {Value: "MEMBER", Organization: true}},
	}
	raw := json.RawMessage(twoTierPeriodRule)
	edits := []catalogPeriodComponentEdit{
		{ItemKey: "operations", Label: "交付", TargetKind: "pool", OrganizationKey: "pool", PoolName: "交付池",
			OwnerRole: "OWNER", MemberRole: "MEMBER", Tiers: []catalogPeriodTier{{Rate: 125}}},
		{Label: "技术公司", TargetKind: "company", OrganizationKey: "company", Tiers: []catalogPeriodTier{{Rate: 250}}},
		{Label: "个人分润", TargetKind: "person", PersonID: 22, Tiers: []catalogPeriodTier{{Rate: 210}}},
	}
	body, err := editCatalogPeriodComponents(raw, edits, choices)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseCatalogPeriodRule(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Items) != 3 || parsed.Items[0].ItemKey != "operations" || parsed.Items[0].unit != "pool-unit" ||
		parsed.Items[0].PoolName != "交付池" || parsed.Items[0].OwnerRole != "OWNER" || parsed.Items[0].MemberRole != "MEMBER" ||
		parsed.Items[1].OrganizationKey != "company" || parsed.Items[1].TargetKind != "company" ||
		parsed.Items[2].PersonID != 22 || parsed.Items[2].TargetKind != "person" {
		t.Fatalf("wrong version contents: %+v", parsed.Items)
	}
	original, err := parseCatalogPeriodRule(raw)
	if err != nil || len(original.Items) != 1 || original.Items[0].Label != "运营" || len(original.Items[0].Tiers) != 2 {
		t.Fatalf("original changed: %+v, %v", original, err)
	}
	edits = edits[1:2]
	edits[0].ItemKey = parsed.Items[1].ItemKey
	body, err = editCatalogPeriodComponents(body, edits, choices)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err = parseCatalogPeriodRule(body)
	if err != nil || len(parsed.Items) != 1 || parsed.Items[0].ItemKey != edits[0].ItemKey {
		t.Fatalf("removal did not reach execution: %+v, %v", parsed, err)
	}
	quote, err := quoteCatalogPeriod(parsed.Items, 20000, 0, 0)
	if err != nil || quote.Total != 500 || len(quote.Awards) != 1 {
		t.Fatalf("wrong edited award: %+v, %v", quote, err)
	}
	edits[0].OrganizationKey = "pool"
	if _, err := editCatalogPeriodComponents(body, edits, choices); !errors.Is(err, errCatalogPeriodRecipient) {
		t.Fatalf("accepted non-company for company payout: %v", err)
	}
}

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
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE VALUES
		(802,'DCAI_RENEWAL','续费样本','calendar',25),(803,'ANOTHER_PROJECT','其他项目','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest) VALUES
		(802,gen_random_uuid(),'test','DCAI','DCAI','renewal','2026-10-01',sha256('renewal'::bytea)),
		(803,gen_random_uuid(),'test','ANOTHER','DCAI','foreign','2026-10-01',sha256('foreign'::bytea))`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,
		 classifier,sku_code,sku_label,mapping_version,effective_from,effective_to,observed_at,source_product_code) VALUES
		(gen_random_uuid(),802,'DCAI','VIP','ORDER','vip','new','VIP','NEW','新增场景',1,'2026-08-31 16:00Z',NULL,'2026-10-01','renewal'),
		(gen_random_uuid(),802,'DCAI','VIP','ORDER','vip','old','VIP','ENDED','已截止场景',1,'2026-01-01','2026-08-31 16:00Z','2026-10-01','renewal'),
		(gen_random_uuid(),803,'DCAI','VIP','ORDER','vip','foreign','VIP','FOREIGN','其他项目场景',1,'2026-01-01',NULL,'2026-10-01','foreign')`,
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
	actor := financeRuleActor{subject: "fixture", permits: func(id int64, publish bool) bool { return (id == product || id == 802) && publish }}
	anchorOnly := financeRuleActor{subject: "anchor", permits: func(id int64, publish bool) bool { return id == product && publish }}
	input := catalogPeriodPublishInput{BaseID: baseline, EffectiveFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, shanghai),
		Edits:         []catalogPeriodTierEdit{{ItemKey: "operations", Tiers: []catalogPeriodTier{{From: 0, Rate: 120}}}},
		ScenarioCodes: []string{"FIRST", "NEW"}}
	reader := financeRuleActor{permits: func(int64, bool) bool { return false }}
	if _, err := post(reader, input); !errors.Is(err, errRulePermission) {
		t.Fatalf("unauthorized publication: %v", err)
	}
	if _, err := post(anchorOnly, input); !errors.Is(err, errRulePermission) {
		t.Fatalf("the anchor grant must not allow adding another product: %v", err)
	}
	invalidStart := input
	invalidStart.EffectiveFrom = input.EffectiveFrom.Add(time.Hour)
	if _, err := post(actor, invalidStart); !errors.Is(err, errCatalogPeriodStart) {
		t.Fatalf("mid-period publication: %v", err)
	}
	for _, choices := range [][]string{{}, {"FIRST", "FIRST"}, {"FIRST", "FOREIGN"}, {"FIRST", "ENDED"}} {
		bad := input
		bad.ScenarioCodes = choices
		if _, err := post(actor, bad); !errors.Is(err, errCatalogPeriodScenarios) {
			t.Fatalf("invalid scenario selection %v accepted: %v", choices, err)
		}
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
		expected := "SECOND"
		if id == published.ID {
			expected = "NEW"
		}
		if len(rule.Scenarios) != 2 || rule.Scenarios[0] != "FIRST" || rule.Scenarios[1] != expected {
			t.Fatalf("unexpected scenario scope for %s: %v", id, rule.Scenarios)
		}
	}
	if _, err := post(actor, input); err == nil {
		t.Fatal("reused superseded version")
	}
	remove := catalogPeriodPublishInput{BaseID: published.ID, ScenarioCodes: []string{"FIRST"},
		EffectiveFrom: time.Date(2026, 10, 1, 0, 0, 0, 0, shanghai)}
	if _, err := post(anchorOnly, remove); !errors.Is(err, errRulePermission) {
		t.Fatalf("the anchor grant must not allow removing another product: %v", err)
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
