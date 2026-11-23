package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCatalogPeriodPublishesSalesOrganizationAppointmentTarget(t *testing.T) {
	base := json.RawMessage(twoTierPeriodRule)
	var edit catalogPeriodComponentEdit
	if err := json.Unmarshal([]byte(`{"label":"在任负责人","targetKind":"appointment","appointmentLevel":"COMPANY",
	 "appointmentRole":"TEAM_LEAD","tiers":[{"rate_bps":1000}]}`), &edit); err != nil {
		t.Fatal(err)
	}
	body, err := editCatalogPeriodComponents(base, base, []catalogPeriodComponentEdit{edit},
		catalogStructureOptions{Roles: []catalogStructureOption{{Value: "TEAM_LEAD", Organization: true}}})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := parseCatalogPeriodRule(body)
	if err != nil || definition.Items[0].TargetKind != "appointment" {
		t.Fatalf("appointment did not survive publication: %+v / %v", definition, err)
	}
	open := time.Date(2026, 10, 1, 0, 0, 0, 0, shanghai)
	transfer := open.AddDate(0, 0, 12)
	unit := "10000000-0000-4000-8000-000000000701"
	versions := []catalogAggregateRule{{ID: "rule", Version: 1, From: open, Definition: definition}}
	events := []catalogActivityEvent{{EventID: 1, PaymentID: 1, PaidAt: transfer.Add(-time.Second), Scenario: "FIRST", Amount: 10000},
		{EventID: 2, PaymentID: 2, PaidAt: transfer, Scenario: "FIRST", Amount: 20000}}
	performance := catalogPerformanceDirectory{claims: map[int64]vipClaim{1: {Company: "company"}, 2: {Company: "company"}},
		companies: [2]map[string][]vipLink{{"company": {{Key: "company", Value: unit, vipInterval: vipInterval{From: open}}}}}}
	directory := catalogAggregateDirectory{AppointmentDuties: map[catalogAggregateDutyKey][]catalogAggregateDuty{
		{unit, "TEAM_LEAD"}: {{ID: 10, Name: "前任", vipInterval: vipInterval{From: open, To: &transfer}},
			{ID: 20, Name: "接任", vipInterval: vipInterval{From: transfer}}},
	}}
	report, err := groupCatalogPeriodEvents(events, versions, directory, nil, performance, open.AddDate(0, 1, 0))
	if err != nil || len(report.Groups) != 2 || len(report.Issues) != 0 {
		t.Fatalf("split at appointment transfer: %+v / %v", report, err)
	}
	frozen := map[catalogFrozenGroupReference]catalogPeriodGroup{}
	var original catalogFrozenGroupReference
	for _, g := range report.Groups {
		target := g.posting.Targets[0]
		if target.AppointmentUnit != unit || target.Unit != "" || g.quote().Total != g.Receipts/10 {
			t.Fatalf("appointment payout must stay personal, scoped to its original organization: %+v", g)
		}
		if target.Person == 10 {
			original = catalogFrozenGroupReference{PeriodID: 7, Key: g.ID}
			g.Receipts, g.Payments = 0, 0
			frozen[original] = g
		}
	}
	refund := events[0]
	refund.EventID, refund.Amount, refund.Refund = 3, 1000, true
	refund.frozenPeriod, refund.frozenGroups = 7, []catalogFrozenGroupReference{original}
	report, err = groupCatalogPeriodEvents([]catalogActivityEvent{refund}, versions, catalogAggregateDirectory{}, frozen, catalogPerformanceDirectory{}, open.AddDate(0, 2, 0))
	if err != nil || len(report.Groups) != 1 || report.Groups[0].posting.Targets[0].Person != 10 || report.Groups[0].Refunds != 1000 {
		t.Fatalf("refund must use the original confirmed recipient: %+v / %v", report, err)
	}
	fixed := int64(100000)
	edit.Tiers, edit.FixedMinor = nil, &fixed
	body, err = editCatalogPeriodComponents(base, base, []catalogPeriodComponentEdit{edit},
		catalogStructureOptions{Roles: []catalogStructureOption{{Value: "TEAM_LEAD", Organization: true}}})
	if err != nil {
		t.Fatal(err)
	}
	versions[0].Definition, err = parseCatalogPeriodRule(body)
	if err != nil {
		t.Fatal(err)
	}
	other := "10000000-0000-4000-8000-000000000702"
	fullyRefunded := "10000000-0000-4000-8000-000000000703"
	for _, organization := range []string{other, fullyRefunded} {
		performance.companies[0][organization] = []vipLink{{Key: organization, Value: organization}}
		directory.AppointmentDuties[catalogAggregateDutyKey{organization, "TEAM_LEAD"}] = []catalogAggregateDuty{{ID: 20, Name: "接任"}}
	}
	performance.claims[3], performance.claims[4] = vipClaim{Company: other}, vipClaim{Company: fullyRefunded}
	events = append(events, catalogActivityEvent{EventID: 3, PaymentID: 3, PaidAt: transfer, Scenario: "FIRST", Amount: 10000},
		catalogActivityEvent{EventID: 4, PaymentID: 4, PaidAt: transfer, Scenario: "FIRST", Amount: 3000},
		catalogActivityEvent{EventID: 5, PaymentID: 4, PaidAt: transfer, Scenario: "FIRST", Amount: 3000, Refund: true})
	report, err = groupCatalogPeriodEvents(events, versions, directory, nil, performance, open.AddDate(0, 1, 0))
	if err != nil || len(report.Groups) != 3 || len(report.Issues) != 0 {
		t.Fatalf("fixed organization groups: %+v / %v", report, err)
	}
	totals := map[string]int64{}
	for _, g := range report.Groups {
		target := g.posting.Targets[0]
		if target.Person != 20 {
			t.Fatalf("fixed award belongs to the period-end holder: %+v", target)
		}
		totals[target.AppointmentUnit] = g.quote().Total
	}
	if totals[unit] != 100000 || totals[other] != 100000 || totals[fullyRefunded] != 0 {
		t.Fatalf("each eligible organization receives one fixed award per period: %v", totals)
	}
}

func TestCatalogPeriodItemsFilterSKUAndKeepOriginalRefundScope(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, shanghai)
	base := json.RawMessage(twoTierPeriodRule)
	fixed := int64(2500)
	edits := []catalogPeriodComponentEdit{
		{Label: "会员提成", TargetKind: "person", PersonID: 1, ScenarioCodes: []string{"FIRST"}, Tiers: []catalogPeriodTier{{Rate: 1000}}},
		{Label: "大使补贴", TargetKind: "person", PersonID: 1, ScenarioCodes: []string{"SECOND"}, FixedMinor: &fixed},
		{Label: "全期提成", TargetKind: "person", PersonID: 1, Tiers: []catalogPeriodTier{{Rate: 500}}},
	}
	body, err := editCatalogPeriodComponents(base, base, edits, catalogStructureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := parseCatalogPeriodRule(body)
	if err != nil {
		t.Fatal(err)
	}
	itemKeys := make(map[string]string)
	for _, item := range definition.Items {
		itemKeys[item.ItemKey] = item.Label
	}
	versions := []catalogAggregateRule{{ID: "active", Version: 1, From: month, Definition: definition}}
	directory := catalogAggregateDirectory{People: map[int64]string{1: "收款人"}}
	first := catalogActivityEvent{EventID: 1, PaymentID: 11, PaidAt: month, Scenario: "FIRST", Amount: 30000}
	second := catalogActivityEvent{EventID: 2, PaymentID: 12, PaidAt: month, Scenario: "SECOND", Amount: 20000}
	report, err := groupCatalogPeriodEvents([]catalogActivityEvent{first, second}, versions, directory, nil, catalogPerformanceDirectory{}, month.AddDate(0, 1, 0))
	if err != nil || len(report.Groups) != 3 || len(report.Issues) != 0 {
		t.Fatalf("unexpected group result: %+v / %v", report, err)
	}
	want := map[string][2]int64{"会员提成": {30000, 3000}, "大使补贴": {20000, 2500}, "全期提成": {50000, 2500}}
	frozen := make(map[catalogFrozenGroupReference]catalogPeriodGroup)
	var refs []catalogFrozenGroupReference
	for _, group := range report.Groups {
		label := itemKeys[group.Targets[0].ItemKey]
		if got := [2]int64{group.Receipts, group.quote().Total}; got != want[label] {
			t.Fatalf("%s: got %v; want %v", label, got, want[label])
		}
		if label != "大使补贴" {
			ref := catalogFrozenGroupReference{PeriodID: 7, Key: group.ID}
			group.Receipts, group.Payments = 0, 0
			frozen[ref] = group
			refs = append(refs, ref)
		}
	}
	refund := first
	refund.EventID, refund.Amount, refund.Refund = 3, 6000, true
	refund.frozenPeriod, refund.frozenGroups = 7, refs
	renewal := "RENEWAL"
	refund.MembershipType = &renewal
	versions[0].Definition.Items[0].ScenarioCodes = []string{"SECOND"}
	versions[0].Definition.Items[0].MembershipNewOnly = true
	report, err = groupCatalogPeriodEvents([]catalogActivityEvent{refund}, versions, directory, frozen, catalogPerformanceDirectory{}, month.AddDate(0, 2, 0))
	if err != nil || len(report.Groups) != 2 || len(report.Issues) != 0 {
		t.Fatalf("frozen refund should only use its original two receipt arrangements: %+v / %v", report, err)
	}
	for _, group := range report.Groups {
		if itemKeys[group.Targets[0].ItemKey] == "大使补贴" || group.Refunds != 6000 {
			t.Fatalf("refund left its original item scope: %+v", group)
		}
	}
}

func TestCatalogPeriodMembershipTypeOnlyFiltersConfiguredItem(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, shanghai)
	newType, renewal, missing := "NEW", "RENEWAL", ""
	rule := catalogAggregateRule{ID: "member-rule", Version: 1, From: start, Definition: catalogPeriodDefinition{
		Cycle: "MONTHLY", Scenarios: []string{"VIP", "HEALTH"}, Items: []catalogPeriodItem{
			{ItemKey: "strict", Label: "会员新报及健康", TargetKind: "person", PersonID: 1, MembershipNewOnly: true, Tiers: []catalogPeriodTier{{Rate: 1000}}},
			{ItemKey: "general", Label: "全期", TargetKind: "person", PersonID: 1, Tiers: []catalogPeriodTier{{Rate: 1000}}},
		},
	}}
	events := []catalogActivityEvent{
		{EventID: 1, PaymentID: 1, Scenario: "VIP", PaidAt: start, Amount: 10000, MembershipType: &newType},
		{EventID: 2, PaymentID: 2, Scenario: "VIP", PaidAt: start, Amount: 20000, MembershipType: &renewal},
		{EventID: 3, PaymentID: 3, Scenario: "VIP", PaidAt: start, Amount: 35000, MembershipType: &missing},
		{EventID: 4, PaymentID: 4, Scenario: "HEALTH", PaidAt: start, Amount: 40000},
		{EventID: 5, PaymentID: 2, Scenario: "VIP", PaidAt: start, Amount: 1500, Refund: true, MembershipType: &renewal},
	}
	directory := catalogAggregateDirectory{People: map[int64]string{1: "收款人"}}
	group := func() (catalogPeriodGroupReport, map[string][2]int64) {
		t.Helper()
		report, err := groupCatalogPeriodEvents(events, []catalogAggregateRule{rule}, directory, nil, catalogPerformanceDirectory{}, start.AddDate(0, 1, 0))
		if err != nil || len(report.Groups) != 2 || len(report.events) != len(events) {
			t.Fatalf("unexpected grouping: %+v / %v", report, err)
		}
		totals := make(map[string][2]int64)
		for _, receipt := range report.Groups {
			totals[receipt.Targets[0].ItemKey] = [2]int64{receipt.Receipts, receipt.Refunds}
		}
		return report, totals
	}
	report, totals := group()
	if totals["strict"] != [2]int64{50000, 0} || totals["general"] != [2]int64{105000, 1500} || len(report.TargetIssues) != 1 ||
		report.TargetIssues[0].ItemKey != "strict" || report.TargetIssues[0].Code != "MEMBERSHIP_TYPE_PENDING" || report.TargetIssues[0].Count != 1 ||
		len(report.postingIssues) != 1 || report.postingIssues[0].FactID != 3 {
		t.Fatalf("wrong item totals or pending membership warnings: %v / %+v", totals, report.TargetIssues)
	}
	events[2].MembershipType = &newType
	report, totals = group()
	if totals["strict"] != [2]int64{85000, 0} || len(report.postingIssues) != 0 {
		t.Fatalf("source correction must let the new purchase into the same item: %v / %+v", totals, report.postingIssues)
	}
}

func TestCatalogPeriodCarryUsesEffectiveItemSKUs(t *testing.T) {
	def := catalogPeriodDefinition{Cycle: "MONTHLY", Scenarios: []string{"FIRST"}, Items: []catalogPeriodItem{{ItemKey: "lead"}}}
	targets := []catalogGroupTargetKey{{Item: "lead", Person: 42}}
	key := func() string {
		t.Helper()
		result, err := catalogAggregateCarryKey(def, targets)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	original := key()
	def.Scenarios = []string{"FIRST", "SECOND"}
	def.Items[0].ScenarioCodes = []string{"FIRST"}
	if key() != original {
		t.Fatal("adding an unrelated package SKU must preserve the same effective scope and its carry")
	}
	def.Items[0].ScenarioCodes = []string{"SECOND"}
	if key() == original {
		t.Fatal("a different applicable SKU must not receive the original item's carry")
	}
	def.Items[0].ScenarioCodes = []string{"FIRST"}
	def.Items[0].MembershipNewOnly = true
	if key() == original {
		t.Fatal("a membership-only restriction changes the carry scope even if all SKU names match")
	}
}

func TestCatalogPeriodGroupsKeepOriginalRuleAndSplitOnPoolMembership(t *testing.T) {
	rule, err := parseCatalogPeriodRule(json.RawMessage(`{
	 "amount_basis":"ACTUAL_RECEIVED","recognition_trigger":"PAID","refund_policy":"PROPORTIONAL","stacking_policy":"SUM",
	 "rounding_policy":{"mode":"HALF_UP","scale":2},
	 "policy_scope":{"sales_systems":["DCAI"],"business_sku_codes":["FIRST","SECOND"],"financial_event_types":["order.paid"]},
	 "settlement_policy":{"cycle":"MONTHLY","negative_balance_policy":"CARRY_FORWARD"},
	 "benefit_actions":[{"item_key":"ops","label":"运营","role":"MEMBER","type":"TIER_RATE","metric_code":"MONTHLY_NET_RECEIPTS","metric_unit":"AMOUNT_MINOR",
	 "beneficiary_mode":"TEAM_POOL","pool_name":"运营池","pool_code":"ops","pool_party_id":"org","pool_organization_unit_id":"unit",
	 "allocation_owner_role":"OWNER","tier_table":[{"rate_bps":100}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, shanghai)
	change := month.AddDate(0, 0, 10)
	owners := catalogAggregateDirectory{
		Organizations: []vipBenefitOrganization{{Party: "org", Unit: "unit", Name: "运营部"}},
		Duties: map[catalogAggregateDutyKey][]catalogAggregateDuty{
			{"unit", "OWNER"}:  {{ID: 1, Name: "原负责人", vipInterval: vipInterval{From: month.AddDate(0, -1, 0)}}},
			{"unit", "MEMBER"}: {{ID: 2, vipInterval: vipInterval{From: month.AddDate(0, -1, 0)}}, {ID: 3, vipInterval: vipInterval{From: change}}},
		},
	}
	versions := []catalogAggregateRule{
		{ID: "previous", Version: 1, From: month.AddDate(0, -1, 0), Through: &month, Definition: rule},
		{ID: "current", Version: 2, From: month, Definition: rule},
	}
	events := []catalogActivityEvent{
		{PaymentID: 11, PaidAt: month.Add(-time.Second), Scenario: "FIRST", Amount: 6000, Refund: true},
		{PaymentID: 12, PaidAt: month, Scenario: "FIRST", Amount: 30000},
		{PaymentID: 13, PaidAt: change.Add(-time.Second), Scenario: "SECOND", Amount: 20000},
		{PaymentID: 14, PaidAt: change, Scenario: "SECOND", Amount: 40000},
		{PaymentID: 15, PaidAt: change, Scenario: "FIRST", Amount: 10000},
		{PaymentID: 14, PaidAt: change, Scenario: "SECOND", Amount: 2000, Refund: true},
		{PaymentID: 16, PaidAt: month.AddDate(0, -2, 0), Scenario: "FIRST", Amount: 1000, Refund: true},
	}
	report, err := groupCatalogPeriodEvents(events, versions, owners, nil, catalogPerformanceDirectory{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	totals := map[[2]int][4]int64{}
	for _, group := range report.Groups {
		if len(group.Targets) != 1 || group.Targets[0].OwnerName != "原负责人" {
			t.Fatalf("unexpected pool target: %+v", group.Targets)
		}
		totals[[2]int{group.Version, group.Targets[0].MemberCount}] = [4]int64{group.Receipts, group.Refunds, group.Net, int64(group.Payments)}
	}
	if len(report.Groups) != 3 || totals[[2]int{1, 1}] != [4]int64{0, 6000, -6000, 0} ||
		totals[[2]int{2, 1}] != [4]int64{50000, 0, 50000, 2} ||
		totals[[2]int{2, 2}] != [4]int64{50000, 2000, 48000, 2} || report.Issues["ORIGINAL_RULE_MISSING"] != 1 {
		t.Fatalf("unexpected grouping totals=%v issues=%v", totals, report.Issues)
	}
	versions[1].Definition.Scenarios = []string{"FIRST"}
	onlyFirst := []catalogActivityEvent{
		{PaymentID: 30, PaidAt: month, Scenario: "SECOND", Amount: 30000},
		{PaymentID: 31, PaidAt: month.Add(-time.Second), Scenario: "SECOND", Amount: 6000, Refund: true},
	}
	report, err = groupCatalogPeriodEvents(onlyFirst, versions, owners, nil, catalogPerformanceDirectory{}, time.Time{})
	if err != nil || len(report.Groups) != 1 || len(report.Issues) != 0 || report.Groups[0].Version != 1 || report.Groups[0].Refunds != 6000 {
		t.Fatalf("a removed scenario still contributes its refunds on old eligible orders: %+v %v", report, err)
	}
	owners.Organizations = nil
	owners.InactiveUnits = map[string]bool{"unit": true}
	onlyFirst[1].EventID = 3101
	report, err = groupCatalogPeriodEvents(onlyFirst, versions, owners, nil, catalogPerformanceDirectory{}, time.Time{})
	if err != nil || len(report.Groups) != 0 || report.Issues["BENEFIT_ORG_INACTIVE"] != 1 {
		t.Fatalf("a missing mapping for a known inactive unit needs the specific warning: %+v %v", report, err)
	}
	var response struct {
		TargetIssues []struct {
			RuleID, ItemKey, Label, Code string
			Version, Count               int
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil || json.Unmarshal(encoded, &response) != nil || len(response.TargetIssues) != 1 {
		t.Fatalf("activity should identify the exact blocked receipt arrangement: %s, %v", encoded, err)
	}
	issue := response.TargetIssues[0]
	if issue.RuleID != "previous" || issue.Version != 1 || issue.ItemKey != "ops" || issue.Label != "运营" ||
		issue.Code != "BENEFIT_ORG_INACTIVE" || issue.Count != 1 || len(report.postingIssues) != 1 || report.postingIssues[0].Code != issue.Code {
		t.Fatalf("warning should trace to original version, blocked item and draft warning: %+v %+v", issue, report.postingIssues)
	}
}

func TestCatalogPeriodTargetIssueIdentifiesProposedArrangement(t *testing.T) {
	window := time.Date(2026, 10, 1, 0, 0, 0, 0, shanghai)
	definition := catalogPeriodDefinition{Scenarios: []string{"FIRST"}, Items: []catalogPeriodItem{
		{ItemKey: "existing-person", Label: "运营计提", TargetKind: "person", PersonID: 1, Tiers: []catalogPeriodTier{{Rate: 1000}}},
		{ItemKey: "generated-pool-key", Label: "运营计提", TargetKind: "pool", OrganizationKey: "team", OwnerRole: "OWNER"},
	}}
	directory := catalogAggregateDirectory{
		People:        map[int64]string{1: "运营人员"},
		Organizations: []vipBenefitOrganization{{Party: "team", Unit: "unit", Name: "运营组"}},
	}
	event := catalogActivityEvent{EventID: 1, PaymentID: 1, PaidAt: window, Scenario: "FIRST", Amount: 1000}
	for _, tc := range []struct {
		ruleID     string
		isProposal bool
	}{{"", true}, {"published-version", false}} {
		rule := catalogAggregateRule{ID: tc.ruleID, From: window, Definition: definition}
		report, err := groupCatalogPeriodEvents([]catalogActivityEvent{event}, []catalogAggregateRule{rule}, directory, nil, catalogPerformanceDirectory{}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			TargetIssues []struct {
				RuleID, ItemKey, Code string
				ProposedItemIndex     *int
			}
		}
		encoded, err := json.Marshal(report)
		if err != nil || json.Unmarshal(encoded, &response) != nil || len(response.TargetIssues) != 1 {
			t.Fatalf("expected one issue: %s, %v", encoded, err)
		}
		issue := response.TargetIssues[0]
		if issue.RuleID != tc.ruleID || issue.ItemKey != "generated-pool-key" || issue.Code != "BENEFIT_POOL_OWNER_MISSING" {
			t.Fatalf("incorrect issue: %+v", issue)
		}
		if tc.isProposal {
			if issue.ProposedItemIndex == nil || *issue.ProposedItemIndex != 1 {
				t.Fatalf("expected the second arrangement's index: %s", encoded)
			}
		} else if issue.ProposedItemIndex != nil {
			t.Fatalf("published arrangement should use its stored key: %s", encoded)
		}
	}
}

func TestCatalogPeriodFixedAwardUsesWholeNetAndClosingPoolOwner(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, shanghai)
	handoff := start.AddDate(0, 0, 15)
	end := start.AddDate(0, 1, 0)
	amount := int64(25000)
	items := []catalogPeriodItem{
		{ItemKey: "pool-rate", Label: "比例分润", TargetKind: "pool", OrganizationKey: "p", OwnerRole: "OWNER", MemberRole: "MEMBER",
			Tiers: []catalogPeriodTier{{Rate: 1000}}},
		{ItemKey: "pool-fixed", Label: "整期补贴", TargetKind: "pool", OrganizationKey: "p", OwnerRole: "OWNER", MemberRole: "MEMBER",
			FixedMinor: &amount},
	}
	rules := []catalogAggregateRule{{ID: "rule", Version: 1, From: start, Definition: catalogPeriodDefinition{
		Items: items, Scenarios: []string{"IN"},
	}}}
	directory := catalogAggregateDirectory{
		Organizations: []vipBenefitOrganization{{Party: "p", Unit: "pool", Name: "运营池"}},
		Duties: map[catalogAggregateDutyKey][]catalogAggregateDuty{
			{"pool", "OWNER"}: {
				{ID: 1, Name: "原负责人", vipInterval: vipInterval{From: start, To: &handoff}},
				{ID: 2, Name: "新负责人", vipInterval: vipInterval{From: handoff, To: &end}},
				{ID: 3, Name: "次期负责人", vipInterval: vipInterval{From: end}},
			},
			{"pool", "MEMBER"}: {{ID: 8, vipInterval: vipInterval{From: start}}},
		},
	}
	first := catalogActivityEvent{EventID: 1, PaymentID: 1, PaidAt: start, Scenario: "IN", Amount: 20000}
	last := catalogActivityEvent{EventID: 2, PaymentID: 2, PaidAt: handoff, Scenario: "IN", Amount: 10000}
	outside := catalogActivityEvent{EventID: 3, PaymentID: 3, PaidAt: start, Scenario: "OUT", Amount: 90000}
	report, err := groupCatalogPeriodEvents([]catalogActivityEvent{first, last, outside}, rules, directory, nil, catalogPerformanceDirectory{}, end.Add(-time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Groups) != 3 {
		t.Fatalf("the proportional part still uses the two payment-date groups alongside the fixed award: %+v", report)
	}
	for _, group := range report.Groups {
		if group.FixedOnly {
			if group.Receipts != 30000 || group.quote().Total != amount || group.posting.Targets[0].Owner != 2 || group.Targets[0].OwnerName != "新负责人" {
				t.Fatalf("one complete fixed payout should go to the person in office at the close: %+v", group)
			}
		} else if group.quote().Total != group.Receipts/10 || len(group.Targets) != 1 {
			t.Fatalf("the fixed amount must not be duplicated in proportional groups: %+v", group)
		}
	}
}

func TestCatalogPeriodScopesSeparatePersonalAndTeamAwards(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, shanghai)
	const company = "40000000-0000-4000-8000-000000000123"
	const otherCompany = "40000000-0000-4000-8000-000000000124"
	lead := catalogPerformanceDuty{PersonID: 19, UnitID: company, Role: "LEAD"}
	appointmentStart := month.Add(time.Hour)
	rule := catalogAggregateRule{ID: "current", Version: 1, From: month, Definition: catalogPeriodDefinition{
		Scenarios: []string{"SKU"}, Items: []catalogPeriodItem{
			{ItemKey: "sales", Label: "本人销售", TargetKind: "person", PersonID: 19, OwnSales: true, Tiers: []catalogPeriodTier{{Rate: 1000}}},
			{ItemKey: "lead", Label: "团队负责人", TargetKind: "person", PersonID: 19, CommissionUnits: []string{company},
				Appointment: &lead, Tiers: []catalogPeriodTier{{Rate: 200}}},
		},
	}}
	payees := catalogAggregateDirectory{People: map[int64]string{19: "负责人兼销售"}}
	performance := catalogPerformanceDirectory{
		claims: map[int64]vipClaim{1: {User: "own", Company: "team"}, 2: {User: "peer", Company: "team"}, 3: {User: "peer", Company: "other"},
			4: {User: "peer", Company: "team"}},
		users:     [2]map[string][]vipLink{{"own": {{Key: "own", Value: "19"}}, "peer": {{Key: "peer", Value: "20"}}}},
		companies: [2]map[string][]vipLink{{"team": {{Key: "team", Value: company}}, "other": {{Key: "other", Value: otherCompany}}}},
		duties:    map[catalogPerformanceDuty][]vipInterval{lead: {{From: appointmentStart}}},
	}
	orders := []catalogActivityEvent{
		{EventID: 1, PaymentID: 1, PaidAt: appointmentStart, Scenario: "SKU", Amount: 10000},
		{EventID: 2, PaymentID: 2, PaidAt: appointmentStart, Scenario: "SKU", Amount: 20000},
		{EventID: 3, PaymentID: 3, PaidAt: month, Scenario: "SKU", Amount: 30000},
		{EventID: 4, PaymentID: 4, PaidAt: month, Scenario: "SKU", Amount: 40000},
	}
	report, err := groupCatalogPeriodEvents(orders, []catalogAggregateRule{rule}, payees, nil, performance, month.AddDate(0, 1, 0))
	if err != nil || len(report.events) != 4 || len(report.Groups) != 2 || len(report.Issues) != 0 {
		t.Fatalf("four source events and two arrangement groups: %+v %v", report, err)
	}
	frozen := map[catalogFrozenGroupReference]catalogPeriodGroup{}
	refs := []catalogFrozenGroupReference{}
	for _, group := range report.Groups {
		item := group.Targets[0].ItemKey
		if item == "sales" && (group.Receipts != 10000 || group.quote().Total != 1000) ||
			item == "lead" && (group.Receipts != 30000 || group.quote().Total != 600) {
			t.Fatalf("incorrect group: %+v", group)
		}
		ref := catalogFrozenGroupReference{PeriodID: 1, Key: group.ID}
		group.Receipts, group.Refunds, group.Payments = 0, 0, 0
		group.posting.Events = nil
		frozen[ref] = group
		refs = append(refs, ref)
	}
	refund := catalogActivityEvent{EventID: 5, PaymentID: 1, PaidAt: appointmentStart, Scenario: "SKU", Amount: 5000, Refund: true, frozenPeriod: 1, frozenGroups: refs}
	report, err = groupCatalogPeriodEvents([]catalogActivityEvent{refund}, nil, payees, frozen, catalogPerformanceDirectory{}, month.AddDate(0, 2, 0))
	if err != nil || len(report.events) != 1 || len(report.Groups) != 2 {
		t.Fatalf("the original order's refund should use only its two booked groups: %+v %v", report, err)
	}
	for _, group := range report.Groups {
		if group.Refunds != 5000 || group.quote().CarryForward != -5000 {
			t.Fatalf("original arrangement refund should carry forward separately: %+v", group)
		}
	}
}
