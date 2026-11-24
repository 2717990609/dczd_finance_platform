package main

import (
	"encoding/json"
	"testing"
)

func TestPracticeCategoryScopeUsesOneStandardWithPersonAndCompanyExceptions(t *testing.T) {
	var part component
	if err := json.Unmarshal([]byte(`{
		"type":"sales","mode":"fixed","value":5000,"sourceRole":"DIRECT_REFERRER","classMember":"retrain",
		"saleScope":{"kind":"category","salesCategoryIds":["10000000-0000-4000-8000-000000000001"],
		  "companyExceptions":[{"companyId":"special","mode":"fixed","value":7000}],
		  "personExceptions":[{"personId":2,"mode":"rate","value":1000},{"personId":3,"mode":"fixed","value":0}]}
	}`), &part); err != nil {
		t.Fatal(err)
	}
	if err := validateComponents([]component{part}); err != nil {
		t.Fatal(err)
	}
	const category = "10000000-0000-4000-8000-000000000001"
	paid := int64(100000)
	rule := paymentRule{Components: []component{part, {
		Type: "company_sales", Mode: "company_sales", ClassMember: "retrain", SourceRole: "DIRECT_REFERRER",
		CompanySales: &practiceCompanySales{Payee: &practiceCompanyPayee{Kind: "person", PersonID: 9}, Categories: []practiceCompanyCategory{
			{Companies: []string{"different"}, practiceCompanyValue: practiceCompanyValue{Mode: "fixed", Value: 8300}},
		}},
	}}}
	offers := practiceCompanyStandards(rule)
	for _, tc := range []struct {
		name, company, classification string
		person, want                  int64
	}{
		{"category default", "ordinary", category, 1, 5000},
		{"company exception", "special", category, 1, 7000},
		{"person overrides company", "special", category, 2, 10000},
		{"explicit zero overrides company", "special", category, 3, 0},
		{"exception stays within selected categories", "special", "other", 2, 0},
		{"separate company award is not duplicated", "different", category, 2, 8300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := paymentInput{ID: 1, Fact: sourceFact{Source: "DCZD", PaymentID: "paid", SalesCompanyID: tc.company,
				AmountMinor: &paid, ReferrerID: "seller"}}
			purchase := practicePurchase{member: practiceEnrollment{ID: 10, MembershipType: 2},
				payments: []paymentInput{input}, base: paid, gross: paid}
			identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: tc.person}}}
			runtime := practiceDraftRuntime{companyClassifications: map[string]string{"paid": tc.classification}}
			purchase.classifyCompany(offers, runtime.companyClassifications)
			var draft paymentDraft
			err := appendPracticeEnrollmentAwards(&draft, rule, purchase, identities, runtime, nil)
			if err != nil || len(draft.Issues) != 0 || draft.Total != tc.want || len(draft.Lines) > 1 {
				t.Fatalf("wrong payout: got %d (%d lines, %d issues), want %d: %v",
					draft.Total, len(draft.Lines), len(draft.Issues), tc.want, err)
			}
		})
	}
	companies := []string{"special"}
	part.SalesCompanies = &companies
	if err := validateComponents([]component{part}); err == nil {
		t.Fatal("category scope must reject a second company scope")
	}
}

func TestPracticeCompanyExceptionUsesOriginalPaymentShares(t *testing.T) {
	const category = "10000000-0000-4000-8000-000000000001"
	part := component{Type: "sales", Mode: "fixed", Value: 5000, SourceRole: "DIRECT_REFERRER", ClassMember: "retrain",
		SaleScope: &practiceSaleScope{Kind: "category", SalesCategoryIDs: []string{category},
			CompanyExceptions: []practiceSaleCompanyException{{CompanyID: "special", practiceSaleTerms: practiceSaleTerms{Mode: "fixed", Value: 7000}}}}}
	deposit, balance := int64(40000), int64(60000)
	payments := []paymentInput{
		{ID: 1, Fact: sourceFact{Source: "DCZD", PaymentID: "deposit", SalesCompanyID: "special", AmountMinor: &deposit, ReferrerID: "seller"}},
		{ID: 2, Fact: sourceFact{Source: "DCZD", PaymentID: "balance", SalesCompanyID: "special", AmountMinor: &balance, ReferrerID: "seller"}},
	}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 10}}}
	runtime := practiceDraftRuntime{companyClassifications: map[string]string{"deposit": category, "balance": category}}
	award := func(purchase practicePurchase) paymentDraft {
		t.Helper()
		var draft paymentDraft
		if err := appendPracticeEnrollmentAwards(&draft, paymentRule{Components: []component{part}}, purchase, identities, runtime, nil); err != nil {
			t.Fatal(err)
		}
		return draft
	}
	purchase := practicePurchase{member: practiceEnrollment{ID: 1, MembershipType: 2}, payments: payments, base: 100000, gross: 100000}
	original := award(purchase)
	if len(original.Issues) != 0 || len(original.Lines) != 2 || original.Total != 7000 || original.Lines[0].Amount != 2800 {
		t.Fatalf("the alternate fixed amount must be shared across both original payments: %+v", original)
	}
	purchase.originalPayments, purchase.payments = payments, payments[1:]
	purchase.base, purchase.gross, purchase.netPayments = 60000, 60000, map[int64]int64{1: 0}
	after := award(purchase)
	if len(after.Issues) != 0 || len(after.Lines) != 1 || after.Total != 4200 || after.Lines[0].FactID != 2 {
		t.Fatalf("full refund of the deposit must retain the original balance share: %+v", after)
	}
	payments[0].Fact.SalesCompanyID = "other"
	conflict := award(purchase)
	if len(conflict.Lines) != 0 || len(conflict.Issues) != 1 || conflict.Issues[0].Code != "PRACTICE_SALE_STANDARD_CONFLICT" {
		t.Fatalf("different standards for the two original payments must be reviewed: %+v", conflict)
	}
}
