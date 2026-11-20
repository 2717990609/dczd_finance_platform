package main

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

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
	for _, conflicting := range []json.RawMessage{specific, row("ANY", "DIRECT", "", 1200, false)} {
		if err := validateCatalogNewRateConditions(before, append(slices.Clone(before), conflicting)); !errors.Is(err, errCatalogRateConditions) {
			t.Fatalf("condition must be rejected: %v", err)
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
}
