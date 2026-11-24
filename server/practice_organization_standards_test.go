package main

import (
	"encoding/json"
	"testing"
)

func TestPracticeStandardsMatchTheMostSpecificPaidOrganization(t *testing.T) {
	const (
		company    = "10000000-0000-4000-8000-000000000001"
		department = "10000000-0000-4000-8000-000000000002"
		team       = "10000000-0000-4000-8000-000000000003"
		category   = "10000000-0000-4000-8000-000000000004"
	)
	var rows []practicePeriodStandardRate
	if err := json.Unmarshal([]byte(`[
	  {"salesUnits":["`+company+`"],"value":10000},
	  {"salesUnits":["`+department+`"],"value":20000},
	  {"salesUnits":["`+team+`"],"value":0},
	  {"salesCategoryIds":["`+category+`"],"value":40000},
	  {"value":50000}
	]`), &rows); err != nil {
		t.Fatal(err)
	}
	index, ok := indexPracticeStandardRates(rows)
	if !ok {
		t.Fatal("organization, category and common rates should coexist")
	}
	input := paymentInput{ID: 5, Fact: sourceFact{Source: "DCZD", PaymentID: "paid", SalesCompanyID: "company", ReferrerID: "seller"}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 7}}}
	entry := practiceSalesOrganization{PersonID: 7, SourceCompany: "company", Company: company, Department: department, Group: team}
	org := practiceSalesOrganizations{5: {"DIRECT_REFERRER": entry}}
	runtime := practiceDraftRuntime{salesOrganizations: org, companyClassifications: map[string]string{"paid": category}}
	rate, issue := index.match(input, false, runtime, "DIRECT_REFERRER", identities)
	if issue != "" || rate != 0 {
		t.Fatalf("explicit team rate must precede department, company and category: %d %s", rate, issue)
	}
	entry.Group = ""
	org[5]["DIRECT_REFERRER"] = entry
	rate, issue = index.match(input, false, runtime, "DIRECT_REFERRER", identities)
	if issue != "" || rate != 20000 {
		t.Fatalf("department rate must precede the company rate: %d %s", rate, issue)
	}
	entry.Department = ""
	entry.ScopeAlternatives = []organizationScopeCandidate{{Department: department}, {Department: team}}
	org[5]["DIRECT_REFERRER"] = entry
	if _, issue = index.match(input, false, runtime, "DIRECT_REFERRER", identities); issue != "SALES_ORGANIZATION_SCOPE_CONFLICT" {
		t.Fatalf("different standards for ambiguous payment organization must be reviewed: %s", issue)
	}
}

func TestPracticeCompanyOrderScopeMatchesOrganizationsAndGeneralStandard(t *testing.T) {
	const department = "10000000-0000-4000-8000-000000000002"
	var part component
	if err := json.Unmarshal([]byte(`{"type":"company_sales","mode":"company_sales","classMember":"retrain",
	  "sourceRole":"DIRECT_REFERRER","companySales":{"payee":{"kind":"person","personId":7},"ownSales":true,
	  "general":{"mode":"fixed","value":8000},"categories":[{"salesUnits":["`+department+`"],"mode":"fixed","value":3000}]}}`), &part); err != nil {
		t.Fatal(err)
	}
	if err := validateComponents([]component{part}); err != nil {
		t.Fatal(err)
	}
	offers := practiceCompanyStandards(paymentRule{Components: []component{part}})
	for _, tc := range []struct {
		name, dept   string
		seller, want int64
	}{
		{"organization exception", department, 7, 3000},
		{"general standard", "", 7, 8000},
		{"other seller outside own scope", department, 8, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := paymentInput{ID: 1, Fact: sourceFact{Source: "DCZD", SalesCompanyID: "sales-company", ReferrerID: "seller"}}
			p := practicePurchase{member: practiceEnrollment{MembershipType: 2}, payments: []paymentInput{input}}
			identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: tc.seller}}}
			runtime := practiceDraftRuntime{salesOrganizations: practiceSalesOrganizations{
				1: {"DIRECT_REFERRER": {PersonID: tc.seller, SourceCompany: "sales-company", Department: tc.dept}},
			}}
			p.classifyCompanyWithOrganizations(offers, nil, identities, runtime)
			var amount int64
			if p.companyAward != nil {
				amount = p.companyAward.Value
			}
			if p.companyRoutingIssue != "" || amount != tc.want {
				t.Fatalf("wrong award: %d %s", amount, p.companyRoutingIssue)
			}
		})
	}
}
