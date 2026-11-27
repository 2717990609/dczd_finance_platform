package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestCatalogPoolOwnerAndMembersUseIndependentRoles(t *testing.T) {
	options := catalogStructureOptions{
		Organizations: []catalogStructureOption{{Value: "pool-party", UnitID: "team-unit", Kind: "DEPARTMENT"}},
		Roles:         []catalogStructureOption{{Value: "PROJECT_OWNER", Personal: true}, {Value: "DELIVERY_TEAM", Organization: true}},
	}
	action := vipBenefitAction{Item: "delivery", Label: "交付", Mode: "TEAM_POOL", PoolParty: "pool-party", PoolUnit: "team-unit", PoolName: "交付", PoolCode: "delivery"}
	target := catalogBenefitTarget{Target: "pool", Organization: "pool-party", Role: "PROJECT_OWNER", MemberRole: "DELIVERY_TEAM"}
	if err := configureCatalogBenefitTarget(&action, target, options, nil); err != nil {
		t.Fatal(err)
	}
	if action.OwnerRole != "PROJECT_OWNER" || action.Role != "DELIVERY_TEAM" {
		t.Fatalf("owner and member eligibility must be distinct: %+v", action)
	}
	target.MemberRole = "PROJECT_OWNER"
	if err := configureCatalogBenefitTarget(&action, target, options, nil); err == nil {
		t.Fatal("a project-only appointment cannot become the required role of distribution members")
	}
}

func TestCatalogIndependentOrderSalesTargetsResolveExplicitPayees(t *testing.T) {
	fixed, rate := int64(800), int64(500)
	paid := vipSalesRequest{Paid: 10000, OriginalPaid: 20000, Seller: "person:1",
		Company: "source-company", CompanyUnit: "40000000-0000-4000-8000-000000000001", CompanyName: "销售所属公司",
		RecommendationCompany: vipRecommendationCompany{Unit: "40000000-0000-4000-8000-000000000002"}}
	options := catalogStructureOptions{Duties: []catalogStructureOption{{Value: "SALES_DUTY"}}}
	seller, err := newCatalogBenefitAction("sales-person", catalogNewBenefitItem{Label: "成交销售",
		catalogBenefitTarget:      catalogBenefitTarget{Target: "order_seller", RecipientDuty: "SALES_DUTY"},
		catalogBenefitCalculation: catalogBenefitCalculation{Fixed: &fixed}}, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	company, err := newCatalogBenefitAction("sales-company", catalogNewBenefitItem{Label: "销售公司",
		catalogBenefitTarget:      catalogBenefitTarget{Target: "order_recommendation_company"},
		catalogBenefitCalculation: catalogBenefitCalculation{Rate: &rate}}, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := json.Marshal(map[string]any{
		"amount_basis": "ACTUAL_RECEIVED", "stacking_policy": "SUM", "sales_attribution": "ORDER_SELLER",
		"rounding_policy": map[string]any{"mode": "HALF_UP", "scale": 2},
		"benefit_actions": []map[string]json.RawMessage{seller, company},
	})
	if err != nil {
		t.Fatal(err)
	}
	cards, err := catalogRateComponents(definition, nil, nil, nil, nil)
	if err != nil || len(cards) != 2 || cards[0].TargetKind != "order_seller" || cards[0].RecipientDuty != "SALES_DUTY" ||
		cards[1].TargetKind != "order_recommendation_company" {
		t.Fatalf("independent dynamic recipients must be editable after saving: %+v %v", cards, err)
	}
	policy, ok := readVIPBenefits(definition)
	if !ok {
		t.Fatal("independent sales recipients must work without the dedicated sales table")
	}
	// These two recipients are already resolved from the order's seller and company.
	data := vipRuntimeData{CompanyNames: map[string]string{paid.RecommendationCompany.Unit: "推荐公司"}, vipSalesData: vipSalesData{People: map[string]string{"person:1": "成交销售"}}}
	result := quoteVIPBenefits(paid, "", policy, data)
	if len(result) != 2 || result[0].Status != "CALCULATED" || result[0].Kind != "person" ||
		result[0].Party != paid.Seller || result[0].Amount != 400 || result[1].Status != "CALCULATED" ||
		result[1].Kind != "company" || result[1].Unit != paid.RecommendationCompany.Unit || result[1].Amount != 500 {
		t.Fatalf("expected 400 cents to the seller and 500 cents to the sale company: %+v", result)
	}
	paid.RecommendationCompany = vipRecommendationCompany{Issue: "RECOMMENDATION_COMPANY_CONFLICT"}
	result = quoteVIPBenefits(paid, "SALES_COMPANY_CONFLICT", policy, data)
	if result[0].Amount != 400 || result[1].Status != "RECOMMENDATION_COMPANY_CONFLICT" || result[1].Amount != 0 {
		t.Fatalf("company conflict must block that share alone: %+v", result)
	}
	paid.Seller = ""
	result = quoteVIPBenefits(paid, "SALES_PERSON_CONFLICT", policy, data)
	if result[0].Amount != 0 || result[0].Status != "SALES_PERSON_CONFLICT" {
		t.Fatalf("conflicting seller must not receive a commission: %+v", result)
	}
}

func TestCatalogIndependentCompanyArrangementCanUseExternalDefault(t *testing.T) {
	const exception = "40000000-0000-4000-8000-000000000001"
	const category = "40000000-0000-4000-8000-000000000002"
	const other = "40000000-0000-4000-8000-000000000003"
	zero, categoryRate, externalRate := int64(0), int64(500), int64(300)
	rows := []catalogConditionInput{
		{Company: exception, Relationship: "ANY", Rate: &zero},
		{Company: "ANY", Relationship: "ANY", Category: category, Rate: &categoryRate},
		{Company: "ANY", Relationship: "ANY", Rate: &externalRate, ExternalDefault: true},
	}
	option := catalogStructureOptions{Companies: []catalogStructureOption{{Value: "ANY"}, {Value: exception}},
		Relationships: []catalogStructureOption{{Value: "ANY"}}, Categories: []catalogStructureOption{{Value: category}}}
	fields, err := newCatalogBenefitAction("company", catalogNewBenefitItem{Label: "销售所属公司提成",
		catalogBenefitTarget:      catalogBenefitTarget{Target: "order_recommendation_company"},
		catalogBenefitCalculation: catalogBenefitCalculation{Rows: rows}}, option, nil)
	if err != nil {
		t.Fatalf("an independent company payee should accept an external default rate: %v", err)
	}
	definition, err := json.Marshal(map[string]any{
		"amount_basis": "ACTUAL_RECEIVED", "stacking_policy": "SUM", "sales_attribution": "ORDER_SELLER",
		"rounding_policy": map[string]any{"mode": "HALF_UP", "scale": 2},
		"benefit_actions": []map[string]json.RawMessage{fields},
	})
	if err != nil {
		t.Fatal(err)
	}
	cards, err := catalogRateComponents(definition, nil, nil, nil, nil)
	if err != nil || len(cards) != 1 || len(cards[0].Rows) != 3 || cards[0].Rows[2].Scope != "external" || cards[0].Rows[2].Payee != "" {
		t.Fatalf("expected an external order condition with the arrangement-level recipient: %+v %v", cards, err)
	}
	policy, ok := readVIPBenefits(definition)
	if !ok {
		t.Fatal("published external company fallback must be supported at runtime")
	}
	for _, tc := range []struct {
		company, category, kind, status string
		amount                          int64
	}{
		{exception, category, "EXTERNAL", "ZERO", 0},
		{other, category, "EXTERNAL", "CALCULATED", 500},
		{other, "unlisted", "EXTERNAL", "CALCULATED", 300},
		{other, "unlisted", "INTERNAL", vipCommissionOutsideScope, 0},
		{other, "unlisted", "", "SALES_COMPANY_TYPE_MISSING", 0},
	} {
		req := vipSalesRequest{Paid: 10000, Company: tc.company, CompanyUnit: tc.company, CompanyName: "销售公司",
			CompanyType: tc.kind, Category: &tc.category, RecommendationCompany: vipRecommendationCompany{Unit: other}}
		got := quoteVIPBenefits(req, "", policy, vipRuntimeData{CompanyNames: map[string]string{other: "推荐公司"}})[0]
		if got.Status != tc.status || got.Amount != tc.amount || tc.amount > 0 && (got.Unit != other || got.Kind != "company") {
			t.Errorf("unexpected company payout for %+v: %+v", tc, got)
		}
	}
}

func TestCatalogPoolCanAssignNamedOwner(t *testing.T) {
	options := catalogStructureOptions{
		Organizations: []catalogStructureOption{{Value: "pool-party", UnitID: "team-unit", Kind: "DEPARTMENT"}},
		Roles:         []catalogStructureOption{{Value: "POOL_MEMBERS", Organization: true}},
	}
	target := catalogBenefitTarget{Target: "pool", Organization: "pool-party", OwnerPersonID: 7, MemberRole: "POOL_MEMBERS"}
	action := vipBenefitAction{Item: "delivery", Label: "交付", Mode: "TEAM_POOL", PoolParty: "pool-party", PoolUnit: "team-unit", PoolName: "交付", PoolCode: "delivery"}
	if err := configureCatalogBenefitTarget(&action, target, options, map[int64]string{7: "person:7"}); err != nil {
		t.Fatal(err)
	}
	var line vipBenefitPart
	bindVIPBenefit(&line, action, vipSalesRequest{}, vipRuntimeData{
		Organizations: []vipBenefitOrganization{{Party: "pool-party", Unit: "team-unit", Name: "交付部"}},
		vipSalesData:  vipSalesData{People: map[string]string{"person:7": "分配负责人"}}, BenefitReady: true,
	})
	if !action.validTarget() || line.Pool != "delivery" || line.Owner != "person:7" || line.OwnerName != "分配负责人" {
		t.Fatalf("selected owner must be resolved for the same pool: %+v %+v", action, line)
	}
}

func TestCatalogPoolSelectionResolvesRegisteredUnitWithoutRuleOwner(t *testing.T) {
	unit := "40000000-0000-4000-8000-000000000011"
	options := catalogStructureOptions{
		TeamPools: []teamPool{{ID: 9, UnitID: unit, Name: "交付池"}},
		Roles:     []catalogStructureOption{{Value: "MEMBER", Organization: true}},
	}
	target := catalogBenefitTarget{Target: "pool", PoolID: 9, MemberRole: "MEMBER"}
	action := vipBenefitAction{Item: "service", Label: "交付服务"}
	if err := configureCatalogBenefitTarget(&action, target, options, map[int64]string{7: "person:7"}); err != nil {
		t.Fatal(err)
	}
	if !action.validTarget() || action.OwnerRole != "" || action.OwnerParty != "" || action.PoolCode != "team-pool:9" || action.PoolParty != "unit:"+unit ||
		action.PoolUnit != unit || action.PoolName != "交付池" {
		t.Fatalf("registered pool must supply its own identity and organization: %+v", action)
	}
	target.PoolID = 10
	if err := configureCatalogBenefitTarget(&action, target, options, map[int64]string{7: "person:7"}); err == nil {
		t.Fatal("pool IDs outside the authorized product directory must be rejected")
	}
}

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
	zeroException := row("company", "ANY", "", 0, false)
	override = append(slices.Clone(withCategory), zeroException)
	if err := validateCatalogNewRateConditions(withCategory, override); err != nil {
		t.Fatalf("company exception and company fallback can share the same company and relationship: %v", err)
	}
	parsed, _ = parseVIPRateTable(override)
	if rate, issue := resolveVIPRate(parsed, "company", "DIRECT", &category); issue != "" || rate != 0 {
		t.Fatalf("the explicit zero rate must win over both the category and company fallback: rate=%d issue=%s", rate, issue)
	}
	if err := validateCatalogNewRateConditions(override, append(slices.Clone(override), zeroException)); !errors.Is(err, errCatalogRateConditions) {
		t.Fatalf("the same company exception cannot be configured twice: %v", err)
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
