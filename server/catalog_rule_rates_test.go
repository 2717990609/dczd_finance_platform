package main

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRemoveCatalogEntriesKeepsTheBaseDefinitionAndAtLeastOneArrangement(t *testing.T) {
	base := json.RawMessage(`{
		"amount_basis":"ACTUAL_RECEIVED","stacking_policy":"SUM","rounding_policy":{"mode":"HALF_UP","scale":2},
		"benefit_actions":[
			{"item_key":"sales","label":"销售提成","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY",
			 "rate_table":[{"sales_organization_party_id":"company","sales_relationship":"ANY","rate_bps":500},
			 {"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":200}]},
			{"item_key":"owner","label":"项目负责人","role":"PROJECT_OWNER","type":"RATE","beneficiary_mode":"PARTY","designated_party_id":"person:8","rate_bps":100}
		]
	}`)
	updated, err := removeCatalogEntries(base, []string{"owner"}, []catalogRateRowRemoval{{ItemKey: "sales", Row: 1}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := catalogRateComponents(base, nil, nil, nil, nil)
	if err != nil || len(before) != 2 || len(before[0].Rows) != 2 {
		t.Fatalf("base rule must retain existing items: %v %v", before, err)
	}
	after, err := catalogRateComponents(updated, nil, nil, nil, nil)
	if err != nil || len(after) != 1 || len(after[0].Rows) != 1 || after[0].Rows[0].Rate != 200 {
		t.Fatalf("new version should retain only the default sales row: %v %v", after, err)
	}
	ownerOnly, err := removeCatalogEntries(base, []string{"sales"}, nil)
	if err != nil {
		t.Fatalf("independent seller arrangement could not be removed: %v", err)
	}
	if items, err := catalogRateComponents(ownerOnly, nil, nil, nil, nil); err != nil || len(items) != 1 || items[0].ItemKey != "owner" {
		t.Fatalf("the remaining arrangement must be editable: %+v %v", items, err)
	}
	if _, err := removeCatalogEntries(base, []string{"sales", "owner"}, nil); !errors.Is(err, errCatalogRequiredEntry) {
		t.Fatalf("a rule must preserve at least one arrangement: %v", err)
	}
	if _, err := removeCatalogEntries(base, nil, []catalogRateRowRemoval{{ItemKey: "sales", Row: 1}, {ItemKey: "sales", Row: 2}}); !errors.Is(err, errCatalogRequiredEntry) {
		t.Fatalf("empty sales table was not rejected: %v", err)
	}
}

func TestCatalogRateComponentsDescribeActualRecipients(t *testing.T) {
	definition := json.RawMessage(`{
		"amount_basis":"ACTUAL_RECEIVED","stacking_policy":"SUM","rounding_policy":{"mode":"HALF_UP","scale":2},
		"benefit_actions":[
			{"item_key":"sales","label":"销售提成","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":500},{"sales_organization_party_id":"ANY","sales_relationship":"DIRECT","rate_bps":700}]},
			{"item_key":"owner","label":"项目负责人","role":"PROJECT_OWNER","type":"RATE","beneficiary_mode":"PARTY","designated_party_id":"person:8","rate_bps":100},
			{"item_key":"company","label":"技术服务","role":"TECHNOLOGY","type":"RATE","beneficiary_mode":"PARTY","company_beneficiary_party_id":"company","rate_bps":100},
			{"item_key":"pool","label":"交付","role":"DELIVERY_TEAM","type":"RATE","beneficiary_mode":"TEAM_POOL","pool_code":"delivery","pool_organization_unit_id":"delivery-unit","allocation_owner_role":"TEAM_LEAD","pool_name":"交付团队池","pool_party_id":"delivery","rate_bps":300}
		]
	}`)
	items, err := catalogRateComponents(definition, nil, nil, nil, map[string]string{"company": "技术公司", "delivery": "交付部"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("want four configured items, got %d", len(items))
	}
	if items[3].TargetRole != "TEAM_LEAD" || items[3].MemberRole != "DELIVERY_TEAM" {
		t.Fatalf("the pool must show its owner role and allocation member role separately: %+v", items[3])
	}
	if rows := items[0].Rows; len(rows) != 2 || rows[0].LimitsRelationship || !rows[1].LimitsRelationship {
		t.Fatalf("only the direct-sales row should show a relationship condition: %+v", rows)
	}
	want := []struct{ kind, label string }{
		{"seller", ""},
		{"person", ""},
		{"company", "技术公司"},
		{"pool", "交付团队池 · 交付部"},
	}
	for i, expected := range want {
		raw, err := json.Marshal(items[i])
		if err != nil {
			t.Fatal(err)
		}
		var item struct{ TargetKind, TargetLabel string }
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		if item.TargetKind != expected.kind || item.TargetLabel != expected.label {
			t.Errorf("item %q: got target %+v, want %+v", items[i].ItemKey, item, expected)
		}
	}
}
