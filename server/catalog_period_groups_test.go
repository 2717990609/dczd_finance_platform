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
	report, err = groupCatalogPeriodEvents(onlyFirst, versions, owners, nil)
	if err != nil || len(report.Groups) != 0 || report.Issues["BENEFIT_ORG_INACTIVE"] != 1 {
		t.Fatalf("a missing mapping for a known inactive unit needs the specific warning: %+v %v", report, err)
	}
}
