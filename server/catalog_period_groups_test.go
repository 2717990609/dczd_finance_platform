package main

import (
	"encoding/json"
	"testing"
	"time"
)

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
	report, err := groupCatalogPeriodEvents(events, versions, owners, nil)
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
	report, err = groupCatalogPeriodEvents(onlyFirst, versions, owners, nil)
	if err != nil || len(report.Groups) != 1 || len(report.Issues) != 0 || report.Groups[0].Version != 1 || report.Groups[0].Refunds != 6000 {
		t.Fatalf("a removed scenario still contributes its refunds on old eligible orders: %+v %v", report, err)
	}
	owners.Organizations = nil
	owners.InactiveUnits = map[string]bool{"unit": true}
	onlyFirst[1].EventID = 3101
	report, err = groupCatalogPeriodEvents(onlyFirst, versions, owners, nil)
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
		{ItemKey: "existing-person", Label: "运营计提", TargetKind: "person", PersonID: 1},
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
		report, err := groupCatalogPeriodEvents([]catalogActivityEvent{event}, []catalogAggregateRule{rule}, directory, nil)
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
	report, err := groupCatalogPeriodEvents([]catalogActivityEvent{first, last, outside}, rules, directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Groups) != 2 {
		t.Fatalf("the proportional part still uses the two payment-date groups: %+v", report)
	}
	for _, group := range report.Groups {
		if group.quote().Total != group.Receipts/10 || len(group.Targets) != 1 {
			t.Fatalf("the fixed amount must not be duplicated in proportional groups: %+v", group)
		}
	}
	if err := appendCatalogPeriodFixedGroup(&report, rules, directory, end.Add(-time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	fixed := report.Groups[len(report.Groups)-1]
	if len(report.Groups) != 3 || !fixed.FixedOnly || fixed.Receipts != 30000 ||
		fixed.quote().Total != amount || fixed.posting.Targets[0].Owner != 2 || fixed.Targets[0].OwnerName != "新负责人" {
		t.Fatalf("one complete fixed payout should go to the person in office at the close: %+v", fixed)
	}
	for _, tc := range []struct {
		label  string
		groups []catalogPeriodGroup
	}{
		{"empty period", nil},
		{"full refund", []catalogPeriodGroup{{Receipts: 15000, Refunds: 15000}}},
		{"prior refund offsets this month", []catalogPeriodGroup{{Receipts: 15000, CarryIn: -16000}}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			report := catalogPeriodGroupReport{Groups: tc.groups, Issues: map[string]int{}}
			if err := appendCatalogPeriodFixedGroup(&report, rules, catalogAggregateDirectory{}, end.Add(-time.Microsecond)); err != nil ||
				len(report.Groups) != len(tc.groups) || len(report.Issues) != 0 {
				t.Fatalf("no payable fixed amount or payee lookup is needed: %+v %v", report, err)
			}
		})
	}
}
