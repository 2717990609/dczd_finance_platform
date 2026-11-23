package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestVIPMembershipOrderScopeIsIndependentByArrangement(t *testing.T) {
	definition := json.RawMessage(`{"amount_basis":"ACTUAL_RECEIVED","stacking_policy":"SUM","sales_attribution":"ORDER_SELLER",
	 "rounding_policy":{"mode":"HALF_UP","scale":2},"policy_scope":{"vip_new_membership_only":true},"benefit_actions":[
	 {"item_key":"strict","label":"仅新报","type":"RATE","beneficiary_mode":"PARTY","designated_party_id":"a","rate_bps":1000},
	 {"item_key":"all","label":"全部会员","type":"RATE","beneficiary_mode":"PARTY","designated_party_id":"b","rate_bps":1000,"vip_membership_new_only":false},
	 {"item_key":"own","label":"本人销售","type":"RATE","beneficiary_mode":"PARTY","designated_party_id":"a","own_sales":true,"rate_bps":1000}]}`)
	policies := readVIPQuotePolicies(map[string]json.RawMessage{"rule": definition})
	data := vipRuntimeData{BenefitReady: true, vipSalesData: vipSalesData{People: map[string]string{"a": "A", "b": "B"}}}
	for _, tc := range []struct{ typeCode, strictStatus string }{
		{"NEW", "CALCULATED"}, {"RENEWAL", vipCommissionOutsideScope}, {"", "MEMBERSHIP_TYPE_PENDING"},
	} {
		row := catalogPaymentMatch{RuleID: "rule", MembershipOrder: true, SourceTransactionType: tc.typeCode}
		policies.apply(&row, data, vipSalesRequest{Paid: 10000, Seller: "other"}, "")
		got := row.BenefitParts
		if len(got) != 3 || got[0].Status != tc.strictStatus || got[1].Status != "CALCULATED" || got[1].Amount != 1000 ||
			got[2].Status != vipCommissionOutsideScope {
			t.Fatalf("membership type %q: want strict %s and only independent all-member arrangement always payable, got %+v", tc.typeCode, tc.strictStatus, got)
		}
	}
}

func TestVIPCommissionScopeUsesPaymentSalesOrganization(t *testing.T) {
	unit := func(value string) pgtype.UUID {
		t.Helper()
		var id pgtype.UUID
		if err := id.Scan(value); err != nil {
			t.Fatal(err)
		}
		return id
	}
	company := unit("10000000-0000-4000-8000-000000000101")
	department := unit("10000000-0000-4000-8000-000000000102")
	first := unit("10000000-0000-4000-8000-000000000103")
	second := unit("10000000-0000-4000-8000-000000000104")
	otherCompany := unit("10000000-0000-4000-8000-000000000105")
	parentTeam := unit("10000000-0000-4000-8000-000000000106")
	data := vipRuntimeData{CommissionDirectory: &reportAttribution{Units: map[pgtype.UUID]reportUnit{
		company: {Kind: "COMPANY"}, otherCompany: {Kind: "COMPANY"},
		department: {Kind: "DEPARTMENT", Parent: company}, parentTeam: {Kind: "TEAM", Parent: department},
		first: {Kind: "TEAM", Parent: parentTeam}, second: {Kind: "TEAM", Parent: department},
	}}}
	req := vipSalesRequest{CompanyUnit: company.String(), commissionSalesOrg: &reportOrganization{Company: company}}
	if eligible, issue := vipCommissionScope([]string{company.String()}, req, "", data); !eligible || issue != "" {
		t.Fatalf("source company should suffice for a company-wide rule: %v %s", eligible, issue)
	}
	if eligible, issue := vipCommissionScope([]string{otherCompany.String()}, req, "", data); eligible || issue != "" {
		t.Fatalf("orders from a company outside the scope should be skipped: %v %s", eligible, issue)
	}
	req.commissionSalesOrg = &reportOrganization{Company: company, Department: department, Group: &first}
	if eligible, issue := vipCommissionScope([]string{parentTeam.String()}, req, "", data); !eligible || issue != "" {
		t.Fatalf("the team scope should include a descendant group: %v %s", eligible, issue)
	}
	if eligible, issue := vipCommissionScope([]string{first.String(), second.String()}, req, "", data); !eligible || issue != "" {
		t.Fatalf("the selected groups are a union: %v %s", eligible, issue)
	}
	req.commissionSalesOrg.Group = nil
	req.commissionSalesOrg.scopeAlternatives = []organizationScopeCandidate{
		{Department: department.String(), Group: first.String()}, {Department: department.String(), Group: second.String()},
	}
	if eligible, issue := vipCommissionScope([]string{first.String()}, req, "", data); eligible || issue != "SALES_ORGANIZATION_SCOPE_CONFLICT" {
		t.Fatalf("only one possible group is in scope: %v %s", eligible, issue)
	}
}

const testVIPBenefits = `{
 "amount_basis":"ACTUAL_RECEIVED","stacking_policy":"SUM","rounding_policy":{"mode":"HALF_UP","scale":2},
 "internal_sales_direct_split":{"mode":"DCAI_INTERNAL_DIRECT","sales_item_key":"sales"},
 "benefit_actions":[
  {"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"},
  {"item_key":"owner","label":"负责人","type":"RATE","beneficiary_mode":"PARTY","role":"PROJECT_OWNER","designated_party_id":"alice","rate_bps":500},
  {"item_key":"company","label":"技术","type":"RATE","beneficiary_mode":"PARTY","role":"TECHNOLOGY","company_beneficiary_party_id":"tech","rate_bps":200},
  {"item_key":"delivery","label":"交付","type":"RATE","beneficiary_mode":"TEAM_POOL","role":"DELIVERY","pool_code":"delivery","pool_name":"交付池","pool_party_id":"team","pool_organization_unit_id":"dept","allocation_owner_role":"TEAM_LEAD","rate_bps":300},
  {"item_key":"corp","label":"企业","type":"RATE_TABLE","beneficiary_mode":"PARTY","role":"PROJECT_OWNER","designated_party_id":"alice","rate_table":[
    {"sales_organization_party_id":"company","sales_relationship":"ANY","rate_bps":100},
    {"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":0}
  ]}
 ]
}`

func TestVIPDesignatedOwnSalesUsesTheOrderSeller(t *testing.T) {
	rate := int64(1000)
	var own vipBenefitAction
	if err := json.Unmarshal([]byte(`{"item_key":"my-sales","label":"本人销售","role":"SALES","type":"RATE","beneficiary_mode":"PARTY","designated_party_id":"lead","own_sales":true}`), &own); err != nil {
		t.Fatal(err)
	}
	own.Rate = &rate
	leadership := own
	if err := json.Unmarshal([]byte(`{"item_key":"lead","label":"团队负责人","own_sales":false}`), &leadership); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	data := vipRuntimeData{BenefitReady: true, vipSalesData: vipSalesData{
		People: map[string]string{"lead": "销售兼负责人"},
		Roles:  []vipRole{{Person: "lead", Role: "SALES", Scope: "PRODUCT", Key: "vip"}},
	}}
	policy := vipBenefitPolicy{actions: []vipBenefitAction{own, leadership}}
	request := vipSalesRequest{Seller: "peer", Paid: 10000, At: at, Product: "vip"}
	rows := quoteVIPBenefits(request, "", policy, data)
	if rows[0].Status != vipCommissionOutsideScope || rows[1].Amount != 1000 || rows[1].Party != "lead" {
		t.Fatalf("peer's order should only count toward leadership: %+v", rows)
	}
	request.Seller = "lead"
	rows = quoteVIPBenefits(request, "", policy, data)
	if rows[0].Amount != 1000 || rows[1].Amount != 1000 || rows[0].Item == rows[1].Item {
		t.Fatalf("personal order should produce two separate arrangements: %+v", rows)
	}
}

func TestVIPBenefitsUseHistoricalRecipientsAndKeepPoolSeparate(t *testing.T) {
	policy, ok := readVIPBenefits(json.RawMessage(testVIPBenefits))
	if !ok {
		t.Fatal("configured benefits should be recognized")
	}
	start := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	data := vipRuntimeData{
		Product: "VIP", Project: "AI", BenefitReady: true,
		Claims:        map[int64][]vipClaim{1: {{Role: "PID_USER", Company: "source-company"}}},
		Companies:     []vipLink{{Key: "source-company", Value: "company"}},
		Organizations: []vipBenefitOrganization{{Party: "tech", Unit: "company", Name: "技术公司"}, {Party: "team", Unit: "dept", Name: "交付部"}},
		vipSalesData: vipSalesData{
			People: map[string]string{"alice": "负责人甲", "bob": "分配负责人", "other": "其他负责人"},
			Roles: []vipRole{
				{Person: "alice", Role: "PROJECT_OWNER", Scope: "PROJECT", Key: "AI", vipInterval: vipInterval{From: start}},
				{Person: "other", Role: "TEAM_LEAD", Scope: "PROJECT", Key: "AI"},
				{Person: "bob", Role: "TEAM_LEAD", Scope: "ORGANIZATION_UNIT", Key: "dept", vipInterval: vipInterval{From: start}},
			},
		},
	}
	amount := int64(10010)
	quote := func(row catalogPaymentMatch) []vipBenefitPart {
		req, issue := vipPaymentInputs(row, data)
		return quoteVIPBenefits(req, issue, policy, data)
	}
	before := quote(catalogPaymentMatch{SourceVersionID: 1, PaidAt: start.Add(-time.Nanosecond), PaidMinor: &amount})
	if len(before) != 4 || before[0].Status != "INACTIVE" || before[3].Status != "INACTIVE" || before[2].Owner != "other" {
		t.Fatalf("before appointment: %+v", before)
	}
	row := catalogPaymentMatch{SourceVersionID: 1, PaidAt: start, PaidMinor: &amount}
	got := quote(row)
	if got[0].Party != "alice" || got[0].Amount != 501 || got[1].Kind != "company" || got[1].Unit != "company" ||
		got[2].Kind != "pool" || got[2].Party != "team" || got[2].Owner != "bob" || got[2].Amount != 300 ||
		got[3].Amount != 100 {
		t.Fatalf("results at appointment start: %+v", got)
	}
	data.Roles = append(data.Roles, vipRole{Person: "other", Role: "TEAM_LEAD", Scope: "ORGANIZATION_UNIT", Key: "dept"})
	got = quote(row)
	if got[2].Status != "BENEFIT_POOL_OWNER_CONFLICT" || got[2].Amount != 0 || got[0].Amount != 501 {
		t.Fatalf("a pool conflict should affect only that benefit: %+v", got)
	}
	data.Claims = nil
	got = quote(row)
	if got[3].Status != "SALES_SOURCE_MISSING" || got[0].Amount != 501 {
		t.Fatalf("only company-specific selection depends on the order's sales attribution: %+v", got)
	}
	amount = 0
	for _, line := range quote(row) {
		if line.Status != "ZERO" {
			t.Fatalf("no accrual or review is needed for zero paid: %+v", line)
		}
	}
}

func TestVIPPoolSummaryCountsEachPaymentOncePerPool(t *testing.T) {
	amount := int64(1000)
	payment := func(parts ...vipBenefitPart) catalogPaymentMatch {
		return catalogPaymentMatch{PaidMinor: &amount, Status: "MATCHED", BenefitParts: parts}
	}
	rows := []catalogPaymentMatch{
		payment(
			vipBenefitPart{Kind: "pool", Pool: "delivery", Unit: "team", Name: "交付池", Amount: 15, Status: "CALCULATED"},
			vipBenefitPart{Kind: "pool", Pool: "delivery", Unit: "team", Name: "交付池", Amount: 25, Status: "CALCULATED"},
			vipBenefitPart{Kind: "person", Amount: 500, Status: "CALCULATED"},
		),
		payment(
			vipBenefitPart{Kind: "pool", Pool: "delivery", Unit: "team", Name: "交付池", Amount: 35, Status: "CALCULATED"},
			vipBenefitPart{Kind: "pool", Status: "BENEFIT_POOL_OWNER_MISSING"},
			vipBenefitPart{Kind: "pool", Status: "SALES_CATEGORY_MISSING"},
		),
		payment(vipBenefitPart{Kind: "pool", Status: "ZERO"}),
		{PaidMinor: &amount, Status: "MATCHED", BenefitParts: []vipBenefitPart{}},
		{PaidMinor: &amount, Status: "COMPOUND_COMPLETION_PENDING"},
		{PaidMinor: &amount, Status: "COMPOUND_COMPLETED_IN_OTHER_PERIOD"},
		{PaidMinor: &amount, Status: "MATCHED", BenefitStatus: "BENEFIT_RULE_UNSUPPORTED"},
		{PaidMinor: &amount, Status: "RULE_MISSING"},
	}
	got, err := summarizeVIPPools(rows)
	if err != nil || got.PendingPaymentCount != 3 || got.PaymentCount != 8 || len(got.Pools) != 1 ||
		got.Pools[0].Amount != 75 || got.Pools[0].PaymentCount != 2 {
		t.Fatalf("pool report includes gross configured pool inputs only: %+v, %v", got, err)
	}
}

func TestVIPFixedBenefitUsesRecognizedBalanceAfterPriorRefund(t *testing.T) {
	definition := strings.Replace(testVIPBenefits, `"type":"RATE","beneficiary_mode":"PARTY","role":"TECHNOLOGY","company_beneficiary_party_id":"tech","rate_bps":200`,
		`"type":"FIXED","beneficiary_mode":"PARTY","role":"TECHNOLOGY","company_beneficiary_party_id":"tech","amount_minor":501`, 1)
	policy, ok := readVIPBenefits(json.RawMessage(definition))
	if !ok {
		t.Fatal("the fixed company award must be supported")
	}
	data := vipRuntimeData{BenefitReady: true, Organizations: []vipBenefitOrganization{{Party: "tech", Unit: "company", Name: "技术公司"}}}
	gross := int64(1000)
	row := catalogPaymentMatch{OrderID: "completed-order", PaidMinor: &gross}
	for _, tc := range []struct {
		recognized int64
		amount     int64
		status     string
	}{
		{1000, 501, "CALCULATED"},
		{500, 250, "CALCULATED"},
		{0, 0, "ZERO"},
	} {
		row.RecognizedMinor = &tc.recognized
		req, issue := vipPaymentInputs(row, data)
		part := quoteVIPBenefits(req, issue, policy, data)[1]
		if part.Amount != tc.amount || part.Status != tc.status {
			t.Fatalf("recognized=%d: got %+v, expected %d/%s", tc.recognized, part, tc.amount, tc.status)
		}
	}
}

func TestVIPOrderReferrerComesFromReferralRoles(t *testing.T) {
	data := vipRuntimeData{DirectoryUsers: []vipLink{{Key: "referrer", Value: "person:2"}}, PersonIDs: map[string]int64{"person:2": 2}}
	seller := vipClaim{Role: "PID_USER", User: "seller"}
	direct := vipClaim{Role: "DIRECT_PID_USER", User: "referrer"}
	lock := vipClaim{Role: "LOCK_DIRECT_SHARE_USER", User: "referrer"}
	for _, tc := range []struct {
		name   string
		claims []vipClaim
		result vipOrderReferrer
	}{
		{"seller only", []vipClaim{seller}, vipOrderReferrer{}},
		{"order referrer", []vipClaim{seller, direct}, vipOrderReferrer{Party: "person:2"}},
		{"locked referrer", []vipClaim{seller, lock}, vipOrderReferrer{Party: "person:2"}},
		{"same person in both records", []vipClaim{seller, direct, lock}, vipOrderReferrer{Party: "person:2"}},
		{"conflicting source records", []vipClaim{seller, direct, {Role: lock.Role, User: "other"}}, vipOrderReferrer{Issue: "REFERRER_SOURCE_CONFLICT"}},
		{"unlinked source account", []vipClaim{seller, {Role: direct.Role, User: "other"}}, vipOrderReferrer{Issue: "REFERRER_PERSON_MISSING"}},
	} {
		if got := data.orderReferrer(tc.claims, time.Now()); got != tc.result {
			t.Errorf("%s got %+v, wanted %+v", tc.name, got, tc.result)
		}
	}
}
