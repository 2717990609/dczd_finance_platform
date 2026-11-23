package main

import (
	"context"
	"encoding/json"
	"slices"
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

func TestVIPExternalCompanyFallbackIsAnIndependentStandard(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":500}`),
		json.RawMessage(`{"sales_organization_party_id":"ANY","sales_relationship":"ANY","external_company_default":true,"sales_payee":"sales_company","rate_bps":425}`),
		json.RawMessage(`{"sales_organization_party_id":"ANY","sales_relationship":"ANY","sales_category_id":"partner","rate_bps":900}`),
		json.RawMessage(`{"sales_organization_party_id":"special","sales_relationship":"ANY","rate_bps":0}`),
	}
	if err := validateCatalogNewRateConditions(nil, raw); err != nil {
		t.Fatalf("external default must coexist with the general rate: %v", err)
	}
	if err := validateCatalogNewRateConditions(raw, append(raw, raw[1])); err == nil {
		t.Fatal("allowed two external defaults in the same arrangement")
	}
	rows, _ := parseVIPRateTable(raw)
	partner, other := "partner", "other-category"
	for _, tc := range []struct {
		company, companyType string
		category             *string
		rate                 int64
		payee, issue         string
	}{
		{"special", "EXTERNAL", &partner, 0, "", ""},
		{"company", "EXTERNAL", &partner, 900, "", ""},
		{"company", "EXTERNAL", nil, 425, vipPayeeSalesCompany, ""},
		{"company", "EXTERNAL", &other, 425, vipPayeeSalesCompany, ""},
		{"company", "INTERNAL", &other, 500, "", ""},
		{"company", "", &other, 0, "", "SALES_COMPANY_TYPE_MISSING"},
	} {
		row, issue := selectVIPRateForCompany(rows, tc.company, "DIRECT", tc.category, tc.companyType)
		if row.Rate != tc.rate || row.Payee != tc.payee || issue != tc.issue {
			t.Errorf("%+v: got rate=%d payee=%q issue=%q", tc, row.Rate, row.Payee, issue)
		}
	}
}

func TestVIPCompanyStandardRemainsCertainWhenCategoryCannotBeatIt(t *testing.T) {
	req := vipSalesRequest{Company: "company", CompanyType: "EXTERNAL", Relationship: "DIRECT", categoryIssue: "SALES_CATEGORY_CONFLICT"}
	known := vipRateRow{Company: "company", Relationship: "DIRECT", Rate: 0}
	conditional := vipRateRow{Company: "company", Relationship: "ANY", Category: "category", Rate: 900}
	if row, issue := req.rateRow([]vipRateRow{known, conditional}, req.CompanyType); issue != "" || row.Rate != 0 {
		t.Fatalf("the more specific zero rate should take priority: %+v %s", row, issue)
	}
	conditional.Relationship = "DIRECT"
	if _, issue := req.rateRow([]vipRateRow{known, conditional}, req.CompanyType); issue != req.categoryIssue {
		t.Fatalf("a conditional rule with the same priority can change the result: %s", issue)
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

	req.Seller = "seller"
	original, issue := calculateVIPSales(rule, req, data)
	if issue != "" {
		t.Fatal(issue)
	}
	rule.Split.Mode, rule.Split.Item = "DCAI_INTERNAL_DIRECT", "sales"
	base, err := json.Marshal(map[string]any{
		"amount_basis": "ACTUAL_RECEIVED", "stacking_policy": "SUM", "rounding_policy": map[string]any{"mode": "HALF_UP", "scale": 2},
		"policy_scope": map[string]any{},
		"benefit_actions": []map[string]any{{"item_key": "sales", "label": "销售", "type": "RATE_TABLE", "role": "SALES",
			"beneficiary_mode": "PARTY", "rate_table": rule.Sales}},
		"internal_sales_direct_split": rule.Split,
	})
	if err != nil {
		t.Fatal(err)
	}
	selectedService, _, err := editCatalogMembershipArrangements(base, []catalogMembershipArrangementEdit{
		{ItemKey: vipInternalServiceItem, NewOnly: true},
	})
	if err != nil || len(selectedService) == 0 {
		t.Fatalf("cannot set the existing service scope independently: %s %v", selectedService, err)
	}
	check := func(definition json.RawMessage, orderType string, excluded map[string]string) {
		t.Helper()
		policies := readVIPQuotePolicies(map[string]json.RawMessage{"rule": definition})
		row := catalogPaymentMatch{RuleID: "rule", MembershipOrder: true, SourceTransactionType: orderType}
		policies.apply(&row, vipRuntimeData{vipSalesData: data}, req, "")
		if row.SalesStatus != "CALCULATED" || len(row.SalesParts) != len(original) {
			t.Fatalf("membership type %q: %s %+v", orderType, row.SalesStatus, row.SalesParts)
		}
		for i, part := range row.SalesParts {
			want := original[i]
			if status := excluded[part.Role]; status != "" {
				want.Status, want.Amount, want.Person, want.Name = status, 0, "", ""
			}
			if want != part {
				t.Fatalf("membership type %q: got %+v, want %+v", orderType, part, want)
			}
		}
	}
	check(selectedService, "NEW", nil)
	check(selectedService, "RENEWAL", map[string]string{"DCAI_SERVICE": vipCommissionOutsideScope})
	check(selectedService, "", map[string]string{"DCAI_SERVICE": "MEMBERSHIP_TYPE_PENDING"})
	selectedSales, _, err := editCatalogMembershipArrangements(base, []catalogMembershipArrangementEdit{{ItemKey: "sales", NewOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	check(selectedSales, "RENEWAL", map[string]string{"SALES": vipCommissionOutsideScope})
	check(selectedSales, "", map[string]string{"SALES": "MEMBERSHIP_TYPE_PENDING"})

	legacy, err := editCatalogNewMembershipScope(base, true)
	if err != nil {
		t.Fatal(err)
	}
	legacyService, _, err := editCatalogMembershipArrangements(legacy, []catalogMembershipArrangementEdit{
		{ItemKey: vipInternalServiceItem, NewOnly: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	check(legacyService, "RENEWAL", map[string]string{
		"SALES": vipCommissionOutsideScope, "DCAI_GROUP_LEAD": vipCommissionOutsideScope, "DCAI_TEAM_HEAD": vipCommissionOutsideScope,
	})
	revisedTeamRate := int64(350)
	registeredBody, err := editCatalogLeaderArrangements(context.Background(), nil, base, []catalogLeaderChange{{
		ItemKey: vipTeamLeaderItem, Label: "团队负责人", Target: "inherit", Scope: vipLeaderScope{Orders: "all"},
		catalogBenefitCalculation: catalogBenefitCalculation{Rate: &revisedTeamRate},
	}})
	if err != nil {
		t.Fatalf("edit the amount while keeping the registered team payee: %v", err)
	}
	registeredPolicy, ok := readVIPBenefits(registeredBody)
	if !ok || catalogLeaderComponents(registeredPolicy)[1].TargetKind != "registered" {
		t.Fatal("the saved rule must still use the registered team payee")
	}
	if got := quoteVIPBenefits(req, "", registeredPolicy, vipRuntimeData{BenefitReady: true, vipSalesData: data}); len(got) != 1 ||
		got[0].Party != "head" || got[0].Rate != revisedTeamRate {
		t.Fatalf("the registered leader's new rate should apply: %+v", got)
	}
	registeredPolicy.split.Teams[0].HeadParty = "old"
	if got := quoteVIPBenefits(req, "", registeredPolicy, vipRuntimeData{BenefitReady: true, vipSalesData: data}); len(got) != 1 ||
		got[0].Status != "SALES_TEAM_PENDING" || got[0].Party != "" || got[0].Amount != 0 {
		t.Fatalf("the current holder must match the registered leader before paying: %+v", got)
	}
	explicitlyChangedPayee, err := editCatalogLeaderArrangements(context.Background(), nil, registeredBody, []catalogLeaderChange{{
		ItemKey: vipTeamLeaderItem, Label: "团队负责人", Target: "appointment", Scope: vipLeaderScope{Orders: "all"},
		catalogBenefitCalculation: catalogBenefitCalculation{Rate: &revisedTeamRate},
	}})
	if err != nil {
		t.Fatal(err)
	}
	appointmentPolicy, ok := readVIPBenefits(explicitlyChangedPayee)
	if !ok || catalogLeaderComponents(appointmentPolicy)[1].TargetKind != "appointment" {
		t.Fatal("choosing the appointment payee should persist that choice")
	}
	appointmentPolicy.split.Teams[0].HeadParty = "old"
	if got := quoteVIPBenefits(req, "", appointmentPolicy, vipRuntimeData{BenefitReady: true, vipSalesData: data}); len(got) != 1 ||
		got[0].Status != "CALCULATED" || got[0].Party != "head" || got[0].Rate != revisedTeamRate {
		t.Fatalf("the explicitly selected appointment should use just the incumbent: %+v", got)
	}
	removed := rule
	removed.Split.Teams = slices.Clone(rule.Split.Teams)
	removed.Split.DisabledShares = []string{vipGroupLeaderItem, vipInternalServiceItem, vipTeamLeaderItem}
	removed.Split.Teams[0].ServiceParty = ""
	kept, issue := calculateVIPSales(removed, req, data)
	if issue != "" || len(kept) != 1 || kept[0] != original[0] {
		t.Fatalf("removed shares must neither block the sale nor redistribute cents: %s %+v", issue, kept)
	}
	noHead := data
	noHead.Roles = slices.DeleteFunc(slices.Clone(data.Roles), func(role vipRole) bool { return role.Role == "TEAM_LEAD" })
	if _, issue := calculateVIPSales(removed, req, noHead); issue != "SALES_TEAM_PENDING" {
		t.Fatal("a missing appointment still matters when it changes the seller's rate")
	}
	removed.Split.Assistant, removed.Split.Group = removed.Split.HeadSeller, 0
	if kept, issue := calculateVIPSales(removed, req, noHead); issue != "" || len(kept) != 1 || kept[0].Rate != removed.Split.HeadSeller {
		t.Fatalf("uniform sale rates do not require the removed leader's appointment: %s %+v", issue, kept)
	}
	rule.Split.independentGroup, rule.Split.independentTeam = true, true
	revised, issue := calculateVIPSales(rule, req, data)
	if issue != "" || len(revised) != 2 || revised[0] != original[0] || revised[1] != original[2] {
		t.Fatalf("replacing leader shares should keep salesperson and service amounts exactly: %s %+v", issue, revised)
	}

	rate := int64(400)
	fixed := int64(300)
	group := vipBenefitAction{
		Item: vipGroupLeaderItem, Label: "group", Role: "GROUP_LEAD", Type: "RATE", Mode: vipOrganizationAppointmentMode,
		Rate: &rate, Leader: &vipLeaderScope{Orders: "members", Groups: []string{"group"}},
	}
	team := vipBenefitAction{
		Item: vipTeamLeaderItem, Label: "team", Role: "TEAM_LEAD", Type: "FIXED", Mode: "PARTY", Designated: "head",
		Fixed: &fixed, Leader: &vipLeaderScope{Orders: "all", Companies: []string{"company"}},
	}
	policy := vipBenefitPolicy{split: rule.Split, actions: []vipBenefitAction{group, team}, leaderSales: rule.Sales}
	quote := func() []vipBenefitPart {
		return quoteVIPBenefits(req, "", policy, vipRuntimeData{BenefitReady: true, vipSalesData: data})
	}
	got := quote()
	if got[0].Party != "new" || got[0].Amount != 1996 || got[1].Party != "head" || got[1].Amount != 300 {
		t.Fatalf("scoped role quotes: %+v", got)
	}
	req.At = at.Add(-time.Second)
	if got := quote(); got[0].Party != "old" {
		t.Fatalf("the group holder should match the payment date: %+v", got)
	}
	req.Seller = "head"
	if got := quote(); got[0].Status != "ZERO" || got[1].Party != "head" {
		t.Fatalf("only the team arrangement covers the head's own sale: %+v", got)
	}
	req.Seller, req.Company = "seller", "another-company"
	if got := quote(); got[0].Status != "ZERO" || got[1].Status != "ZERO" {
		t.Fatalf("unrelated company: %+v", got)
	}
	req.Company = "company"
	category, other := "partner", "other"
	if err := json.Unmarshal([]byte(`{"orders":"all","companies":["company"],"categoryIds":["partner"]}`), policy.actions[1].Leader); err != nil {
		t.Fatal(err)
	}
	req.Category = &other
	if got := quote(); got[0].Status != "CALCULATED" || got[1].Status != "ZERO" {
		t.Fatalf("only the category-filtered team leader should be excluded: %+v", got)
	}
	req.Category = &category
	if got := quote(); got[1].Amount != 300 || got[1].Party != "head" {
		t.Fatalf("team and payment category must both match: %+v", got)
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

func TestVIPRateRejectsTwoCompanyExceptionsWithSamePriority(t *testing.T) {
	rows := []vipRateRow{{Company: "company", Relationship: "ANY", Rate: 1000},
		{CompanyIDs: []string{"company", "other"}, Relationship: "ANY", Rate: 500}}
	if _, issue := resolveVIPRate(rows, "company", "DIRECT", nil); issue != "SALES_RATE_CONFLICT" {
		t.Fatalf("equal specificity: %s", issue)
	}
}
