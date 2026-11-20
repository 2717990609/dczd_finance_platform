package main

import (
	"encoding/json"
	"testing"
)

func TestCatalogRateComponentsDescribeActualRecipients(t *testing.T) {
	definition := json.RawMessage(`{
		"amount_basis":"ACTUAL_RECEIVED","stacking_policy":"SUM","rounding_policy":{"mode":"HALF_UP","scale":2},
		"benefit_actions":[
			{"item_key":"sales","label":"销售提成","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":500}]},
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
