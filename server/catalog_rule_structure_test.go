package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestCatalogGroupedRateConditionsKeepOneRowAndCompanyPriority(t *testing.T) {
	base := json.RawMessage(`{"sales_organization_party_id":"ANY","sales_category_ids":["partner","regional"],"sales_relationship":"ANY","rate_bps":1200}`)
	special := json.RawMessage(`{"sales_organization_party_ids":["alpha","beta"],"sales_relationship":"ANY","rate_bps":2000}`)
	table := []json.RawMessage{base, special}
	if err := validateCatalogNewRateConditions(nil, table); err != nil {
		t.Fatalf("the grouped table should be accepted: %v", err)
	}
	rows, valid := parseVIPRateTable(table)
	if !valid || len(rows) != 2 {
		t.Fatalf("expected two editable rate rows, got %+v", rows)
	}
	for _, tc := range []struct {
		company, category string
		want              int64
	}{
		{"ordinary", "partner", 1200}, {"ordinary", "regional", 1200},
		{"alpha", "partner", 2000}, {"beta", "regional", 2000},
	} {
		if got, issue := resolveVIPRate(rows, tc.company, "DIRECT", &tc.category); got != tc.want || issue != "" {
			t.Fatalf("%s/%s rate=%d issue=%s", tc.company, tc.category, got, issue)
		}
	}
	for _, overlapping := range []json.RawMessage{
		json.RawMessage(`{"sales_organization_party_id":"alpha","sales_relationship":"ANY","rate_bps":4000}`),
		json.RawMessage(`{"sales_organization_party_id":"ANY","sales_category_id":"regional","sales_relationship":"ANY","rate_bps":4000}`),
	} {
		if err := validateCatalogNewRateConditions(table, append(slices.Clone(table), overlapping)); !errors.Is(err, errCatalogRateConditions) {
			t.Fatalf("a group overlap must be rejected: %v", err)
		}
	}
}

func TestCatalogNewRateConditionsPreserveMatcherPrecedence(t *testing.T) {
	row := func(company, relation, category string, rate int64, fallback bool) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(vipRateRow{Company: company, Relationship: relation, Category: category, Rate: rate, SnapshotDefault: fallback})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	specific := row("company", "ANY", "", 1000, false)
	before := []json.RawMessage{specific}
	if err := validateCatalogNewRateConditions(before, append(slices.Clone(before), specific)); !errors.Is(err, errCatalogRateConditions) {
		t.Fatalf("duplicate company condition must be rejected: %v", err)
	}
	withGeneral := append(slices.Clone(before), row("ANY", "DIRECT", "", 1200, false))
	if err := validateCatalogNewRateConditions(before, withGeneral); err != nil {
		t.Fatalf("company exception must take priority over a general relationship rate: %v", err)
	}
	grouped, ok := parseVIPRateTable(withGeneral)
	if !ok {
		t.Fatal("cannot read company exception and general rate")
	}
	for company, want := range map[string]int64{"company": 1000, "other": 1200} {
		if rate, issue := resolveVIPRate(grouped, company, "DIRECT", nil); issue != "" || rate != want {
			t.Fatalf("%s rate=%d issue=%s, want %d", company, rate, issue, want)
		}
	}
	after := append(slices.Clone(before), row("company", "DIRECT", "", 1200, false))
	if err := validateCatalogNewRateConditions(before, after); err != nil {
		t.Fatalf("more specific condition: %v", err)
	}
	parsed, ok := parseVIPRateTable(after)
	if rate, issue := resolveVIPRate(parsed, "company", "DIRECT", nil); !ok || issue != "" || rate != 1200 {
		t.Fatalf("direct rate=%d issue=%s", rate, issue)
	}
	category := "category"
	withCategory := []json.RawMessage{row("company", "ANY", "", 1000, true), row("ANY", "ANY", category, 1200, false)}
	override := append(slices.Clone(withCategory), row("company", "ANY", category, 1500, false))
	if err := validateCatalogNewRateConditions(withCategory, override); err != nil {
		t.Fatalf("specific category override: %v", err)
	}
	parsed, _ = parseVIPRateTable(override)
	if rate, issue := resolveVIPRate(parsed, "company", "DIRECT", &category); issue != "" || rate != 1500 {
		t.Fatalf("category rate=%d issue=%s", rate, issue)
	}
}

func TestCatalogStructureCannotChangeInternalSplitViaNewCategory(t *testing.T) {
	compared := func(rows []vipRateRow) vipSalesPolicy {
		return vipSalesPolicy{Sales: rows, Split: vipInternalSplit{Teams: []vipInternalTeam{{Company: "internal"}}}}
	}
	base := []vipRateRow{{Company: "internal", Relationship: "ANY", Rate: 1000, SnapshotDefault: true}}
	next := append(slices.Clone(base), vipRateRow{Company: "ANY", Relationship: "ANY", Category: "new-category", Rate: 1200})
	if !internalTeamRatesChanged(compared(base), compared(next)) {
		t.Fatal("new category overrides the internal team fallback")
	}
	next[1].Rate, next[1].Payee = 1000, vipPayeeSalesCompany
	if !internalTeamRatesChanged(compared(base), compared(next)) {
		t.Fatal("the same percentage cannot redirect an internal team split to its company")
	}
	next[1].Company, next[1].Category = "outside", ""
	if internalTeamRatesChanged(compared(base), compared(next)) {
		t.Fatal("a different company may settle directly without changing the internal team split")
	}
}

func TestCatalogPayeeEditPreservesRateTableAndOriginalRule(t *testing.T) {
	raw := json.RawMessage(strings.NewReplacer(
		`"internal_sales_direct_split":{"mode":"DCAI_INTERNAL_DIRECT","sales_item_key":"sales"},`, "",
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":1000}]`,
	).Replace(testVIPBenefits))
	root, actions, err := catalogRateActions(raw)
	if err != nil {
		t.Fatal(err)
	}
	sales, _ := readVIPSalesPolicy(raw)
	options := catalogStructureOptions{
		Organizations: []catalogStructureOption{{Value: "new-company", Kind: "COMPANY", UnitID: "new-unit"}},
		Roles:         []catalogStructureOption{{Value: "PROJECT_OWNER", Personal: true}},
	}
	edit := catalogBenefitTargetChange{ItemKey: "corp", catalogBenefitTarget: catalogBenefitTarget{Target: "company", Organization: "new-company"}}
	beforeTable := slices.Clone(actions[4]["rate_table"])
	if err := changeCatalogBenefitTargets(actions, sales.Item, []catalogBenefitTargetChange{edit}, options, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(beforeTable, actions[4]["rate_table"]) {
		t.Fatal("changing the payee must preserve the existing rate table")
	}
	root["benefit_actions"], err = json.Marshal(actions)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(root)
	updated, valid := readVIPBenefits(body)
	original, _ := readVIPBenefits(raw)
	if err != nil || !valid || updated.actions[3].Company != "new-company" ||
		updated.actions[3].Designated != "" || original.actions[3].Designated != "alice" {
		t.Fatalf("unexpected recipient or original rule: %+v %+v %v", updated, original, err)
	}
	edit.ItemKey = sales.Item
	if err := changeCatalogBenefitTargets(actions, sales.Item, []catalogBenefitTargetChange{edit}, options, nil); !errors.Is(err, invalid) {
		t.Fatalf("direct sales component cannot be rewritten as a company benefit: %v", err)
	}
}
