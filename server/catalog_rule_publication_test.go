package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogRateRenamePreservesPoolAndOriginalDefinition(t *testing.T) {
	definition := creatorBenefitDefinition(creatorActionsFixture, "")
	var changes []catalogRateChange
	if err := json.Unmarshal([]byte(`[{"itemKey":"delivery","label":"  新交付安排  "}]`), &changes); err != nil {
		t.Fatal(err)
	}
	body, changed, err := editCatalogRates(definition, changes)
	if err != nil || !changed {
		t.Fatalf("rename changed=%t err=%v", changed, err)
	}
	before, beforeOK := readVIPBenefits(definition)
	after, afterOK := readVIPBenefits(body)
	if !beforeOK || !afterOK || len(before.actions) == 0 || len(before.actions) != len(after.actions) {
		t.Fatalf("invalid original or revised policy: before=%t after=%t", beforeOK, afterOK)
	}
	delivery := len(before.actions) - 1
	if before.actions[delivery].Item != "delivery" || before.actions[delivery].Label != "交付" || after.actions[delivery].Label != "新交付安排" {
		t.Fatalf("old and new labels were not preserved: before=%+v after=%+v", before.actions, after.actions)
	}
	before.actions[delivery].Label = "新交付安排"
	if !reflect.DeepEqual(before, after) {
		t.Fatal("renaming changed the benefit's calculation or payee")
	}
	if _, again, err := editCatalogRates(body, changes); again || err != nil {
		t.Fatalf("unchanged rename changed=%t err=%v", again, err)
	}
	for _, input := range []string{
		`[{"itemKey":"delivery","label":" "}]`,
		`[{"itemKey":"missing","label":"新名称"}]`,
		`[{"itemKey":"delivery","row":1,"label":"新名称"}]`,
		`[{"itemKey":"delivery","rateBps":300,"label":"新名称"}]`,
		`[{"itemKey":"delivery","label":"名称甲"},{"itemKey":"delivery","label":"名称乙"}]`,
	} {
		if err := json.Unmarshal([]byte(input), &changes); err != nil {
			t.Fatal(err)
		}
		if _, _, err := editCatalogRates(definition, changes); !errors.Is(err, invalid) {
			t.Fatalf("rename %s returned %v", input, err)
		}
		changes = nil
	}
}

func TestChangingSalesRatesPreservesInternalTeamSplit(t *testing.T) {
	definition := json.RawMessage(strings.NewReplacer(
		`"sales_item_key":"sales"`, `"sales_item_key":"sales","total_rate_bps":1000,"teams":[{"sales_company_party_id":"internal"}]`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`,
		`"item_key":"sales","label":"销售","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[
			{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":1000},
			{"sales_organization_party_id":"outside","sales_relationship":"DIRECT","sales_category_id":"80000000-0000-4000-8000-000000000001","rate_bps":700}]`,
	).Replace(testVIPBenefits))
	one, two, rate := 1, 2, int64(850)
	if _, _, err := editCatalogRates(definition, []catalogRateChange{{ItemKey: "sales", Row: &one, Rate: &rate}}); !errors.Is(err, errCatalogTeamRate) {
		t.Fatalf("changing the fallback broke the internal split: %v", err)
	}
	if _, changed, err := editCatalogRates(definition, []catalogRateChange{{ItemKey: "sales", Row: &two, Rate: &rate}}); err != nil || !changed {
		t.Fatalf("independent sales row should remain editable: %t %v", changed, err)
	}
}

func TestEditCatalogTeamSplitRatesKeepsLinkedRateAndFrozenDefinition(t *testing.T) {
	definition := json.RawMessage(strings.NewReplacer(
		`"sales_item_key":"sales"`,
		`"sales_item_key":"sales","total_rate_bps":1000,"assistant_seller_rate_bps":500,
		 "head_seller_rate_bps":600,"group_lead_rate_bps":100,"service_rate_bps":150,
		 "team_head_rate_bps":250,"teams":[{"sales_company_party_id":"internal"}]`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY",
		  "rate_table":[{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":1000}]`,
	).Replace(testVIPBenefits))
	rates := catalogSplitRates{Total: 1000, Sales: 450, HeadSales: 600, GroupLead: 150, Service: 175, TeamLead: 225}
	body, changed, err := editCatalogTeamSplitRates(definition, rates)
	if err != nil || !changed {
		t.Fatalf("balanced team rates should be editable: changed=%t %v", changed, err)
	}
	before, beforeOK := readVIPSalesPolicy(definition)
	after, afterOK := readVIPSalesPolicy(body)
	if !beforeOK || !afterOK || before.Split.Assistant != 500 ||
		after.Split.Assistant != 450 || after.Split.Group != 150 ||
		after.Split.Service != 175 || after.Split.TeamHead != 225 ||
		!reflect.DeepEqual(before.Sales, after.Sales) || !reflect.DeepEqual(before.Split.Teams, after.Split.Teams) {
		t.Fatal("editing shares must preserve the old definition, team directory and matching rates")
	}
	rates.HeadSales++
	if _, _, err := editCatalogTeamSplitRates(definition, rates); !errors.Is(err, errCatalogTeamSplitTotal) {
		t.Fatalf("unbalanced direct head sale was accepted: %v", err)
	}
	rates.Total, rates.Sales, rates.HeadSales = 1200, 650, 800
	if _, _, err := editCatalogTeamSplitRates(definition, rates); !errors.Is(err, errCatalogTeamSplitStandard) {
		t.Fatalf("changing a shared fallback would also change unrelated companies: %v", err)
	}
}

func TestEditCatalogTeamTotalRetainsEachExternalRate(t *testing.T) {
	base := json.RawMessage(strings.NewReplacer(
		`"sales_item_key":"sales"`,
		`"sales_item_key":"sales","total_rate_bps":1000,"assistant_seller_rate_bps":500,
		 "head_seller_rate_bps":600,"group_lead_rate_bps":100,"service_rate_bps":150,
		 "team_head_rate_bps":250,"teams":[{"sales_company_party_id":"internal"}]`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY",
		  "rate_table":[{"id":"company-row","sales_organization_party_ids":["internal","external"],
		   "sales_relationship":"ANY","rate_bps":1000},
		  {"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":700}]`,
	).Replace(testVIPBenefits))
	rates := catalogSplitRates{Total: 1200, Sales: 700, HeadSales: 800, GroupLead: 100, Service: 150, TeamLead: 250}
	body, changed, err := editCatalogTeamSplitRates(base, rates)
	if err != nil || !changed {
		t.Fatalf("revised total and shares should be published together: %t %v", changed, err)
	}
	original, _ := readVIPSalesPolicy(base)
	revised, ok := readVIPSalesPolicy(body)
	if !ok || original.Split.Total != 1000 || revised.Split.Total != 1200 {
		t.Fatalf("wrong total in either version: %d/%d", original.Split.Total, revised.Split.Total)
	}
	for company, amount := range map[string]int64{"internal": 1200, "external": 1000, "other": 700} {
		rate, issue := selectVIPRate(revised.Sales, company, "DIRECT", nil)
		if rate.Rate != amount || issue != "" {
			t.Fatalf("%s should receive %d, got %+v %q", company, amount, rate, issue)
		}
	}
}

func TestRemovingLinkedSalesArrangementAlsoRemovesItsTeamSplit(t *testing.T) {
	base := json.RawMessage(strings.Replace(testVIPBenefits,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY",
		  "rate_table":[{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":1000}]`, 1))
	next, err := removeCatalogEntries(base, []string{"sales"}, nil)
	if err != nil {
		t.Fatalf("must be able to remove the sales arrangement and its split together: %v", err)
	}
	oldPolicy, _ := readVIPSalesPolicy(base)
	newPolicy, ok := readVIPSalesPolicy(next)
	oldBenefits, _ := readVIPBenefits(base)
	newBenefits, benefitOK := readVIPBenefits(next)
	if !ok || !benefitOK || oldPolicy.Split.Item != "sales" || newPolicy.Split.Item != "" || newPolicy.Item != "" ||
		!reflect.DeepEqual(oldBenefits.actions, newBenefits.actions) {
		t.Fatal("removal must preserve all independent benefits and the original rule")
	}
}

func TestCatalogRateTableRequiresARow(t *testing.T) {
	actions := strings.Replace(creatorActionsFixture,
		`"role":"TECHNOLOGY","type":"RATE","beneficiary_mode":"PARTY","company_beneficiary_party_id":"tech","rate_bps":200`,
		`"role":"TECHNOLOGY","type":"RATE_TABLE","beneficiary_mode":"PARTY","company_beneficiary_party_id":"tech","rate_bps":null,
		 "rate_table":[{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":200}]`, 1)
	definition := creatorBenefitDefinition(actions, "")
	row, rate := 1, int64(300)
	change := catalogRateChange{ItemKey: "technology", Rate: &rate}
	if _, _, err := editCatalogRates(definition, []catalogRateChange{change}); !errors.Is(err, invalid) {
		t.Fatalf("table component accepted a fixed rate: %v", err)
	}
	change.Row = &row
	if _, changed, err := editCatalogRates(definition, []catalogRateChange{change}); err != nil || !changed {
		t.Fatalf("could not edit the table row: %t %v", changed, err)
	}
}

func TestChangingCatalogRecipientsPreservesInternalTeamRates(t *testing.T) {
	definition := json.RawMessage(strings.NewReplacer(
		`"sales_item_key":"sales"`, `"sales_item_key":"sales","total_rate_bps":1000,"head_seller_rate_bps":600,
		 "assistant_seller_rate_bps":600,"team_head_rate_bps":300,"service_rate_bps":100,
		 "teams":[{"sales_company_party_id":"internal","team_unit_id":"dept","head_party_id":"old-head","service_party_id":"old-service",
		  "seller_group_unit_ids":["group-one"]}]`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`,
		`"item_key":"sales","label":"销售","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[
		 {"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":1000}]`,
	).Replace(testVIPBenefits))
	headID, serviceID := int64(11), int64(12)
	people := map[int64]string{headID: "person:11", serviceID: "person:12"}
	changes := []catalogTeamRecipientChange{{TeamKey: "internal", HeadPersonID: &headID, ServicePersonID: &serviceID}}
	body, changed, err := editCatalogRecipients(definition, nil, changes, people)
	if err != nil || !changed {
		t.Fatalf("team change=%t err=%v", changed, err)
	}
	policy, ok := readVIPSalesPolicy(body)
	if !ok || policy.Split.Service != 100 || len(policy.Split.Teams) != 1 || policy.Split.Teams[0].GroupUnits[0] != "group-one" {
		t.Fatalf("non-recipient fields changed: %+v", policy)
	}
	request := vipSalesRequest{Order: "fixture", Company: "internal", Seller: "salesperson", Project: "demo", Paid: 10000}
	data := vipSalesData{
		People: map[string]string{"salesperson": "销售样本", "person:11": "团队负责人样本", "person:12": "服务人员样本"},
		Roles: []vipRole{
			{Person: "salesperson", Role: "SALES", Scope: "PROJECT", Key: "demo"},
			{Person: "person:11", Role: "TEAM_LEAD", Scope: "ORGANIZATION_UNIT", Key: "dept"},
		},
	}
	parts, issue := calculateVIPSales(policy, request, data)
	if issue != "" || len(parts) != 3 || parts[0].Amount != 600 || parts[1].Person != "person:12" || parts[1].Amount != 100 ||
		parts[2].Person != "person:11" || parts[2].Amount != 300 {
		t.Fatalf("internal split=%+v issue=%s", parts, issue)
	}
	if _, changed, err := editCatalogRecipients(body, nil, changes, people); err != nil || changed {
		t.Fatalf("unchanged recipients published again: changed=%t err=%v", changed, err)
	}
	for _, invalidTeam := range []catalogTeamRecipientChange{
		{TeamKey: "outsider", HeadPersonID: &headID}, {TeamKey: "internal"}, {TeamKey: "internal", HeadPersonID: new(int64)},
	} {
		if _, _, err := editCatalogRecipients(definition, nil, []catalogTeamRecipientChange{invalidTeam}, people); !errors.Is(err, invalid) {
			t.Fatalf("invalid team change %+v returned %v", invalidTeam, err)
		}
	}
}
