package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestPracticeSignupBasis(t *testing.T) {
	for _, tc := range []struct {
		name    string
		terms   string
		deposit int64
		balance int64
	}{
		{"personal rate", `"type":"sales","mode":"rate","value":5000`, 1600, 2400},
		{"personal fixed", `"type":"sales","mode":"fixed","value":1000`, 400, 600},
		{"company rate", `"type":"company_sales","mode":"company_sales","value":0,"companySales":{"payee":{"kind":"person","personId":2},"categories":[{"companies":["direct"],"mode":"rate","value":500000}]}`, 1600, 2400},
		{"company fixed", `"type":"company_sales","mode":"company_sales","value":0,"companySales":{"payee":{"kind":"person","personId":2},"categories":[{"companies":["direct"],"mode":"fixed","value":9900}]}`, 3960, 5940},
		{"period rate", `"type":"delivery","mode":"period_rate","value":500000,"periodBenefit":{"label":"交付","personId":2}`, 1600, 2400},
		{"fixed per signup", `"type":"service","mode":"period_rate","value":0,"periodBenefit":{"label":"运营","personId":2,"fixedMinor":1000}`, 400, 600},
		{"retraining split", `"type":"sales","mode":"fixed","value":1000,"retrainSplits":[{"companyId":"direct","pool":{"poolId":9,"ownerId":7},"personalMinor":850,"servicePersonId":4,"serviceMinor":60}]`, 400, 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rule paymentRule
			config := `[{"sourceRole":"DIRECT_REFERRER","classMember":"retrain","practiceSignupBasis":true,` + tc.terms + `}]`
			if err := json.Unmarshal([]byte(config), &rule.Components); err != nil {
				t.Fatal(err)
			}
			if err := validateComponents(rule.Components); err != nil {
				t.Fatal(err)
			}
			deposit, balance, partial := int64(4000), int64(6000), int64(2000)
			paidAt := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
			paid := func(id, order string, amount *int64) sourceFact {
				return sourceFact{Source: "DCZD", OrderID: order, PaymentID: id, SalesCompanyID: "direct",
					ReferrerID: "seller", AmountMinor: amount, OccurredAt: paidAt}
			}
			members := []practiceEnrollment{{ID: 1, MembershipType: 2, SourceEnrollmentID: "10",
				CommissionEligible: true, ConfirmedAt: paidAt.Add(time.Hour), PaidOrderIDs: []string{"deposit", "balance"}}}
			orders := map[string][]paymentInput{"deposit": {{ID: 1, Fact: paid("p1", "deposit", &deposit)}},
				"balance": {{ID: 2, Fact: paid("p2", "balance", &balance)}}}
			identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 2}}}
			refunds := map[int64][]practiceRefundMatch{1: {{event: paymentInput{ID: 3,
				Fact: sourceFact{OccurredAt: paidAt.Add(time.Minute), AmountMinor: &partial}}}}}
			runtime := practiceDraftRuntime{poolUnits: map[int64]pgtype.UUID{9: {Valid: true}},
				companyOffers: practiceCompanyStandards(rule)}
			draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, refunds, runtime)
			var amounts [3]int64
			for _, line := range draft.Lines {
				amounts[line.FactID] += line.Amount
			}
			if err != nil || len(draft.Issues) != 0 || amounts[1] != tc.deposit ||
				amounts[2] != tc.balance || draft.Total != tc.deposit+tc.balance {
				t.Fatalf("want deposit %d, balance %d, got %v, total %d, issues %v: %v",
					tc.deposit, tc.balance, amounts[1:], draft.Total, draft.Issues, err)
			}
		})
	}
}

func TestPracticeSignupBasisCountsPriorNewEnrollmentAcrossCompanies(t *testing.T) {
	paid := int64(1000000)
	at := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	shops := []string{"internal"}
	part := component{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER",
		SalesCompanies: &shops, PracticeSignupBasis: true}
	companyRule := paymentRule{Components: []component{{Type: "company_sales", Mode: "company_sales", ClassMember: "new",
		SourceRole: "DIRECT_REFERRER", CompanySales: &practiceCompanySales{Categories: []practiceCompanyCategory{
			{Companies: []string{"external"}, practiceCompanyValue: practiceCompanyValue{Mode: "rate", Value: 500000}},
		}}}}}
	history := practiceTierHistory{
		members: []practiceEnrollment{{ID: 1, RosterID: 7, MembershipType: 3, SourceEnrollmentID: "10",
			CommissionEligible: true, ConfirmedAt: at.Add(time.Hour), PaidOrderIDs: []string{"outside"}}},
		periods: []practiceHistoryPeriod{{rosterID: 7, ruleKnown: true, companyOffers: practiceCompanyStandards(companyRule)}},
	}
	orders := map[string][]paymentInput{"outside": {{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "outside",
		PaymentID: "p1", SalesCompanyID: "external", ReferrerID: "seller", AmountMinor: &paid, OccurredAt: at}}}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 2}}}
	for _, enabled := range []bool{false, true} {
		part.PracticeSignupBasis = enabled
		index, err := indexPracticePrior(history, part, orders, identities, nil, nil, practiceDraftRuntime{})
		if err != nil {
			t.Fatal(err)
		}
		count, complete, err := index.personTotal(2, map[int64]bool{7: true})
		var want int64
		if enabled {
			want = 1
		}
		if err != nil || !complete || count != want {
			t.Fatalf("signup basis %t: want count %d, got %d, complete %t: %v", enabled, want, count, complete, err)
		}
	}
}
