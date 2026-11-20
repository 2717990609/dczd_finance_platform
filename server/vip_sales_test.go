package main

import (
	"encoding/json"
	"strings"
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
	partner, unclassified, other := "partner", "", "known"
	for _, test := range []struct {
		company  string
		category *string
		issue    string
		want     int64
	}{{"fixed", &partner, "", 1000}, {"default", &partner, "", 900},
		{"default", nil, "", 700}, {"other", nil, "SALES_CATEGORY_MISSING", 0},
		{"other", &unclassified, "", 0}, {"other", &other, "", 0}, {"other", &partner, "", 900}} {
		if got, issue := resolveVIPRate(rows, test.company, "DIRECT", test.category); got != test.want || issue != test.issue {
			t.Fatalf("%+v: rate=%d issue=%s", test, got, issue)
		}
	}
	rows = append(rows, vipRateRow{Company: "ANY", Relationship: "ANY", Category: "partner", Rate: 902})
	if _, issue := resolveVIPRate(rows, "other", "DIRECT", &partner); issue != "SALES_RATE_CONFLICT" {
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

func TestVIPExplicitZeroSaleNeedsNoPayeeButPreservesReview(t *testing.T) {
	category := ""
	request := vipSalesRequest{Order: "order", Paid: 49900, Company: "company", Category: &category,
		Product: "product", Project: "project", At: time.Date(2026, 8, 1, 0, 0, 0, 0, shanghai)}
	policy := vipSalesPolicy{Sales: []vipRateRow{{Company: "ANY", Category: "partner", Relationship: "ANY", Rate: 3000},
		{Company: "ANY", Relationship: "ANY", Rate: 0}}}
	data := vipSalesData{People: map[string]string{"seller": "销售"}}
	if parts, issue := calculateVIPSales(policy, request, data); issue != "" || len(parts) != 0 {
		t.Fatalf("explicit zero without a payee = %+v / %s", parts, issue)
	}
	request.Seller = "seller"
	if parts, issue := calculateVIPSales(policy, request, data); issue != "" || len(parts) != 1 || parts[0].Person != "seller" || parts[0].Amount != 0 {
		t.Fatalf("explicit zero can still show a known salesperson = %+v / %s", parts, issue)
	}
	request.Category = nil
	data.Roles = []vipRole{{Person: "seller", Role: "SALES", Scope: "PROJECT", Key: request.Project}}
	if _, issue := calculateVIPSales(policy, request, data); issue != "SALES_CATEGORY_MISSING" {
		t.Fatalf("an absent category must not select the zero default: %s", issue)
	}
	request.Category = &category
	if err := json.Unmarshal([]byte(`{"order_company_reviews":[{"source_order_id":"order","seller_party_id":"seller","sales_company_party_id":"corrected-company"}]}`), &policy.Split); err != nil {
		t.Fatal(err)
	}
	if _, issue := calculateVIPSales(policy, request, data); issue != "SALES_COMPANY_REVIEW" {
		t.Fatalf("the zero rate must not suppress a known company discrepancy: %s", issue)
	}
	policy.Split.Reviews = nil
	policy.Sales[1].Rate = 1 // Even if this amount would round to zero, a positive rate still needs an eligible salesperson.
	request.Paid = 1
	data.Roles = nil
	if _, issue := calculateVIPSales(policy, request, data); issue != "SALES_APPOINTMENT_MISSING" {
		t.Fatalf("a positive rate requires a sales appointment: %s", issue)
	}
}

func TestVIPZeroRateUsesConfirmedSourceAttribution(t *testing.T) {
	paid := int64(49900)
	definition := strings.Replace(testVIPBenefits, `"type":"RATE_TABLE","beneficiary_mode":"PARTY"},`,
		`"type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[{"sales_organization_party_id":"company","sales_relationship":"ANY","rate_bps":0}]},`, 1)
	data := vipRuntimeData{Definitions: map[string]json.RawMessage{"rule": json.RawMessage(definition)},
		Companies: []vipLink{{Key: "source-company", Value: "company"}},
		Claims: map[int64][]vipClaim{
			1: {{Role: "PID_USER", User: "unmapped-seller", Company: "source-company"}},
			2: {{Role: "SALES_COMPANY", Company: "source-company"}},
		},
	}
	rows := []catalogPaymentMatch{
		{SourceVersionID: 1, OrderID: "one", RuleID: "rule", PaidMinor: &paid, Status: "MATCHED"},
		{SourceVersionID: 2, OrderID: "two", RuleID: "rule", PaidMinor: &paid, Status: "MATCHED"},
	}
	applyVIPQuotes(rows, data)
	if rows[0].SalesStatus != "CALCULATED" || len(rows[0].SalesParts) != 0 || rows[1].SalesStatus != "SALES_PERSON_MISSING" {
		t.Fatalf("unexpected sales statuses for a resolved rate or missing source attribution: %s, %s", rows[0].SalesStatus, rows[1].SalesStatus)
	}
}

func TestVIPInactiveSalesRequiresOneMatchingCompanyMembership(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, shanghai)
	request := vipSalesRequest{Order: "order", Paid: 49900, Seller: "seller", Company: "company", CompanyUnit: "company-unit",
		Product: "product", Project: "project", At: at}
	policy := vipSalesPolicy{Sales: []vipRateRow{{Company: "company", Relationship: "ANY", Rate: 3000}}}
	member := vipMembership{Person: "seller", Unit: "company-unit", UnitKind: "COMPANY", Kind: "MEMBER", vipInterval: vipInterval{From: at}}
	otherCompany := vipMembership{Person: "seller", Unit: "different-unit", UnitKind: "COMPANY", Kind: "MANAGER", vipInterval: vipInterval{From: at}}
	ordinaryAssignment := vipMembership{Person: "seller", Unit: "company-unit", UnitKind: "COMPANY", Kind: "PRIMARY", vipInterval: vipInterval{From: at}}
	departmentMember := vipMembership{Person: "seller", Unit: "other-department", UnitKind: "DEPARTMENT", Kind: "MEMBER", vipInterval: vipInterval{From: at}}
	wrongOrganizationType := member
	wrongOrganizationType.UnitKind = "DEPARTMENT"
	data := vipSalesData{People: map[string]string{"seller": "销售"}}
	for _, tc := range []struct {
		name        string
		memberships []vipMembership
		issue       string
	}{
		{"no membership", nil, "SALES_APPOINTMENT_MISSING"},
		{"ordinary assignment", []vipMembership{ordinaryAssignment}, "SALES_APPOINTMENT_MISSING"},
		{"source company mapped to a department", []vipMembership{wrongOrganizationType}, "SALES_APPOINTMENT_MISSING"},
		{"other company", []vipMembership{otherCompany}, "SALES_APPOINTMENT_MISSING"},
		{"overlapping memberships", []vipMembership{member, otherCompany}, "SALES_APPOINTMENT_MISSING"},
		{"verified company member", []vipMembership{member, ordinaryAssignment, departmentMember}, "SALES_APPOINTMENT_INACTIVE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data.Memberships = tc.memberships
			if parts, issue := calculateVIPSales(policy, request, data); len(parts) != 0 || issue != tc.issue {
				t.Fatalf("parts=%+v issue=%q, want %q", parts, issue, tc.issue)
			}
		})
	}
	request.At = at.Add(-time.Microsecond)
	if _, issue := calculateVIPSales(policy, request, data); issue != "SALES_APPOINTMENT_MISSING" {
		t.Fatalf("membership should take effect at the configured instant: %s", issue)
	}
	request.At = at
	data.Roles = []vipRole{{Person: "seller", Role: "SALES", Scope: "PROJECT", Key: request.Project, vipInterval: vipInterval{From: at}}}
	parts, issue := calculateVIPSales(policy, request, data)
	if issue != "" || len(parts) != 1 || parts[0].Amount != 14970 {
		t.Fatalf("valid sales appointment should earn the configured amount: %+v %s", parts, issue)
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
	if _, issue := resolveVIPRate(rows, "company", "DIRECT", nil); issue != "SALES_RATE_CONFLICT" {
		t.Fatalf("equal specificity: %s", issue)
	}
}
