package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPracticeRuleSettingsSaveOnlyTheSelectedConfiguration(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	s := workspace{db: db}
	var productID, ruleID, personID, poolID int64
	scan := func(target *int64, sql string, args ...any) {
		t.Helper()
		if err := db.QueryRow(ctx, sql, args...).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	scan(&productID, `INSERT INTO products(code,name,period_mode) VALUES('P_SETTINGS','配置验收','class') RETURNING id`)
	scan(&personID, `INSERT INTO people(name) VALUES('配置验收人员') RETURNING id`)
	scan(&poolID, `WITH company AS (
	 INSERT INTO organization_units(id,name,kind,status) VALUES(gen_random_uuid(),'配置验收公司','COMPANY','ACTIVE') RETURNING id
	) INSERT INTO practice_pools(product_id,unit_id,name) SELECT $1,id,'复训资金池' FROM company RETURNING id`, productID)
	if _, err := db.Exec(ctx, `INSERT INTO configuration_options(group_key,option_key,label,display_order)
	 VALUES('practiceSalesCompany','settings-company','配置验收销售公司',90)`); err != nil {
		t.Fatal(err)
	}
	base := json.RawMessage(`[
	 {"type":"sales","mode":"tiered","value":0,"classMember":"new","sourceRole":"DIRECT_REFERRER",
	  "tiers":{"countFromOrdinal":1,"count":[{"from":0,"mode":"fixed","value":1500}]}},
	 {"type":"sales","mode":"fixed","value":12000,"classMember":"retrain","sourceRole":"DIRECT_REFERRER"}]`)
	scan(&ruleID, `INSERT INTO rule_versions(product_id,version,label,components) VALUES($1,1,'验收规则',$2) RETURNING id`, productID, base)
	otherOrdinal := 200
	var scopedRuleID int64
	scan(&scopedRuleID, `INSERT INTO rule_versions(product_id,version,label,from_ordinal,components)
	 VALUES($1,2,'后续期次',$2,$3) RETURNING id`, productID, otherOrdinal, base)
	start := practicePersonCountStart{PersonID: personID, practiceCountStart: practiceCountStart{FromOrdinal: 171}}
	input := savePracticeRuleSettingsInput{ProductID: productID, RuleID: ruleID, CountStart: &start}
	read := func() []practiceRuleSettings {
		t.Helper()
		items, err := readPracticeRuleSettings(ctx, db, productID)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	publishedID := func() int64 {
		t.Helper()
		var id int64
		scan(&id, `SELECT id FROM rule_versions WHERE product_id=$1 ORDER BY version DESC LIMIT 1`, productID)
		return id
	}
	if _, err := s.savePracticeRuleSettings(ctx, input); err != nil {
		t.Fatal(err)
	}
	if list := read(); len(list) != 2 || list[0].ID != publishedID() || len(list[0].PersonCountStarts) != 1 ||
		list[0].PersonCountStarts[0] != start || list[1].ID != scopedRuleID || len(list[1].PersonCountStarts) != 0 {
		t.Fatalf("expected latest settings for each rule scope: %+v", list)
	}
	if _, err := s.savePracticeRuleSettings(ctx, input); !errors.Is(err, errRuleEditStale) {
		t.Fatalf("stale scope version should not save: %v", err)
	}
	input.RuleID, input.ExistingPersonID = publishedID(), personID
	start.FromOrdinal = 175
	if _, err := s.savePracticeRuleSettings(ctx, input); err != nil {
		t.Fatal(err)
	}
	input.RuleID, input.ExistingPersonID = publishedID(), 0
	if _, err := s.savePracticeRuleSettings(ctx, input); !errors.Is(err, conflict) {
		t.Fatalf("add must not replace an existing person's start: %v", err)
	}
	split := practiceRetrainSplit{CompanyID: "settings-company", PersonalMinor: 5000,
		ServicePersonID: personID, ServiceMinor: 600, Pool: practicePoolTarget{PoolID: poolID, OwnerID: personID}}
	input.CountStart, input.RetrainSplit = nil, &split
	if _, err := s.savePracticeRuleSettings(ctx, input); err != nil {
		t.Fatal(err)
	}
	list := read()
	if list[0].PersonCountStarts[0].FromOrdinal != 175 || len(list[0].RetrainSplits) != 1 ||
		list[0].RetrainSplits[0] != split || list[0].RetrainTotal != 12000 {
		t.Fatalf("saving a split must keep the start and its existing total: %+v", list[0])
	}
	input.RuleID, input.ExistingCompanyID = publishedID(), split.CompanyID
	split.ServiceMinor = 8000
	if _, err := s.savePracticeRuleSettings(ctx, input); !errors.Is(err, invalid) {
		t.Fatalf("split must fit the original per-enrollment total: %v", err)
	}
	input.RetrainSplit = nil
	if _, err := s.savePracticeRuleSettings(ctx, input); err != nil {
		t.Fatal(err)
	}
	if list := read(); len(list[0].RetrainSplits) != 0 || list[0].PersonCountStarts[0].FromOrdinal != 175 {
		t.Fatalf("removing a split must keep other configurations: %+v", list)
	}
	var unchanged int64
	scan(&unchanged, `SELECT count(*) FROM rule_versions WHERE id=$1 AND components=$2::jsonb`, ruleID, base)
	if unchanged != 1 {
		t.Fatal("earlier published versions must remain intact")
	}
}
