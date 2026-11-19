package main

import (
	"testing"
	"time"
)

func TestVIPRateResolvesCompanyBeforeCategoryAndKeepsExplicitZero(t *testing.T) {
	rows := []vipRateRow{
		{Company: "fixed", Relationship: "ANY", Category: "partner", Rate: 1000},
		{Company: "default", Relationship: "ANY", SnapshotDefault: true, Rate: 700},
		{Company: "ANY", Relationship: "ANY", Category: "partner", Rate: 900},
		{Company: "ANY", Relationship: "ANY", Rate: 0},
	}
	for _, test := range []struct {
		company, category, issue string
		want                     int64
	}{{"fixed", "partner", "", 1000}, {"default", "partner", "", 900},
		{"default", "", "", 700}, {"other", "", "SALES_CATEGORY_MISSING", 0},
		{"other", "known", "", 0}, {"other", "partner", "", 900}} {
		if got, issue := resolveVIPRate(rows, test.company, "DIRECT", test.category); got != test.want || issue != test.issue {
			t.Fatalf("%+v: rate=%d issue=%s", test, got, issue)
		}
	}
	rows = append(rows, vipRateRow{Company: "ANY", Relationship: "ANY", Category: "partner", Rate: 902})
	if _, issue := resolveVIPRate(rows, "other", "DIRECT", "partner"); issue != "SALES_RATE_CONFLICT" {
		t.Fatalf("rate conflict=%q", issue)
	}
}

func TestVIPInternalAllocationUsesHistoricalHoldersAndConservesCents(t *testing.T) {
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, shanghai)
	prior := at.AddDate(0, -1, 0)
	rule := vipSalesPolicy{
		Sales: []vipRateRow{{Company: "company", Relationship: "ANY", Rate: 2500}},
		Split: vipInternalSplit{Total: 2500, Assistant: 900, HeadSeller: 1300, Group: 400, Service: 300, TeamHead: 900,
			Teams: []vipInternalTeam{{Company: "company", TeamUnit: "team", HeadParty: "head", ServiceParty: "service", GroupUnits: []string{"group"}}}},
	}
	data := vipSalesData{
		Roles: []vipRole{
			{Person: "seller", Role: "SALES", Scope: "PRODUCT", Key: "product", vipInterval: vipInterval{From: prior}},
			{Person: "head", Role: "SALES", Scope: "PRODUCT", Key: "product", vipInterval: vipInterval{From: prior}},
			{Person: "head", Role: "TEAM_LEAD", Scope: "ORGANIZATION_UNIT", Key: "team", vipInterval: vipInterval{From: prior}},
			{Person: "old", Role: "GROUP_LEAD", Scope: "ORGANIZATION_UNIT", Key: "group", vipInterval: vipInterval{From: prior, To: &at}},
			{Person: "new", Role: "GROUP_LEAD", Scope: "ORGANIZATION_UNIT", Key: "group", vipInterval: vipInterval{From: at}},
		},
		Memberships: []vipMembership{{Person: "seller", Unit: "group", vipInterval: vipInterval{From: prior}}},
		People:      map[string]string{"seller": "销售", "head": "负责人", "old": "上任", "new": "接任", "service": "服务"},
	}
	req := vipSalesRequest{Order: "order", Paid: 49901, At: at, Product: "product", Project: "project", Seller: "seller", Company: "company"}
	parts, issue := calculateVIPSales(rule, req, data)
	if issue != "" || len(parts) != 4 {
		t.Fatalf("split: %q %+v", issue, parts)
	}
	seen := map[string]string{}
	var total int64
	for _, p := range parts {
		seen[p.Role] = p.Person
		total += p.Amount
	}
	if seen["DCAI_GROUP_LEAD"] != "new" || seen["DCAI_TEAM_HEAD"] != "head" || seen["DCAI_SERVICE"] != "service" || seen["SALES"] != "seller" || total != 12475 {
		t.Fatalf("parts=%+v total=%d", parts, total)
	}
	req.At = at.Add(-time.Nanosecond)
	parts, issue = calculateVIPSales(rule, req, data)
	if issue != "" || parts[1].Person != "old" {
		t.Fatalf("early group lead: %q %+v", issue, parts)
	}
	req.At, req.Seller = at, "head"
	parts, issue = calculateVIPSales(rule, req, data)
	if issue != "" || len(parts) != 3 || parts[0].Person != "head" {
		t.Fatalf("head sale: %q %+v", issue, parts)
	}
}

func TestVIPSourceClaimsDistinguishARepeatedRoleFromAnotherSeller(t *testing.T) {
	pid := vipClaim{Role: "PID_USER", User: "seller", Company: "source-company", Relationship: "DIRECT"}
	claims := []vipClaim{pid, {Role: "LOCK_SALES_USER", User: "seller", Company: "source-company"}}
	if result, issue := vipSellerClaim(claims); issue != "" || result != pid {
		t.Fatalf("repeat sales roles selected %+v / %s", result, issue)
	}
	claims[1].User = "other-seller"
	if _, issue := vipSellerClaim(claims); issue != "SALES_SOURCE_CONFLICT" {
		t.Fatalf("distinct sellers: %s", issue)
	}
}

func TestVIPNonCategoryRateRejectsEqualSpecificity(t *testing.T) {
	rows := []vipRateRow{{Company: "company", Relationship: "ANY", Rate: 1000}, {Company: "ANY", Relationship: "DIRECT", Rate: 500}}
	if _, issue := resolveVIPRate(rows, "company", "DIRECT", ""); issue != "SALES_RATE_CONFLICT" {
		t.Fatalf("equal specificity: %s", issue)
	}
}
