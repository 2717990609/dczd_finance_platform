package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestChangingSalesRatesPreservesInternalTeamSplit(t *testing.T) {
	definition := json.RawMessage(strings.NewReplacer(
		`"sales_item_key":"sales"`, `"sales_item_key":"sales","total_rate_bps":1000,"teams":[{"sales_company_party_id":"internal"}]`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`,
		`"item_key":"sales","label":"销售","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[
			{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":1000},
			{"sales_organization_party_id":"outside","sales_relationship":"DIRECT","rate_bps":700}]`,
	).Replace(testVIPBenefits))
	one, two, rate := 1, 2, int64(850)
	if _, _, err := editCatalogRates(definition, []catalogRateChange{{ItemKey: "sales", Row: &one, Rate: &rate}}); !errors.Is(err, errCatalogTeamRate) {
		t.Fatalf("changing the fallback broke the internal split: %v", err)
	}
	if _, changed, err := editCatalogRates(definition, []catalogRateChange{{ItemKey: "sales", Row: &two, Rate: &rate}}); err != nil || !changed {
		t.Fatalf("independent sales row should remain editable: %t %v", changed, err)
	}
}
