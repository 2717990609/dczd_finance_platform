package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestVIPDraftQuotesRecalculateOnlyChangedPayments(t *testing.T) {
	paidAt := time.Date(2026, 8, 4, 12, 0, 0, 0, shanghai)
	paid, net := int64(10000), int64(7000)
	definition := json.RawMessage(strings.Replace(testVIPBenefits, `"type":"RATE_TABLE","beneficiary_mode":"PARTY"},`,
		`"type":"RATE_TABLE","beneficiary_mode":"PARTY","rate_table":[{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":1000}]},`, 1))
	data := vipRuntimeData{
		Product: "DCAI_VIP", Project: "AI", BenefitReady: true,
		Definitions: map[string]json.RawMessage{"rule": definition},
		Claims: map[int64][]vipClaim{
			1: {{Role: "PID_USER", User: "source-seller", Company: "source-company"}},
			2: {{Role: "PID_USER", User: "source-other", Company: "source-company"}},
		},
		Users:         []vipLink{{Key: "source-seller", Value: "seller"}, {Key: "source-other", Value: "person:13"}},
		Companies:     []vipLink{{Key: "source-company", Value: "company"}},
		Organizations: []vipBenefitOrganization{{Party: "tech", Unit: "company"}, {Party: "team", Unit: "dept"}},
		vipSalesData: vipSalesData{
			People: map[string]string{"alice": "负责人", "seller": "销售", "bob": "分配人", "person:13": "另一名销售"},
			Roles: []vipRole{{Person: "alice", Role: "PROJECT_OWNER", Scope: "PROJECT", Key: "AI"},
				{Person: "seller", Role: "SALES", Scope: "PROJECT", Key: "AI"},
				{Person: "person:13", Role: "SALES", Scope: "PROJECT", Key: "AI"},
				{Person: "bob", Role: "TEAM_LEAD", Scope: "ORGANIZATION_UNIT", Key: "dept"}},
		},
	}
	rows := []catalogPaymentMatch{
		{SourceVersionID: 1, OrderID: "payment-one", PaidAt: paidAt, PaidMinor: &paid, Status: "MATCHED", RuleID: "rule"},
		{SourceVersionID: 2, OrderID: "payment-two", PaidAt: paidAt, PaidMinor: &paid, Status: "MATCHED", RuleID: "rule"},
	}
	cache := &vipDraftQuoteCache{}
	calculate := func() []catalogPaymentMatch {
		t.Helper()
		got, full := slices.Clone(rows), slices.Clone(rows)
		cache.apply(1, got, data)
		applyVIPQuotes(full, data)
		if !reflect.DeepEqual(got, full) {
			t.Fatalf("partial quote differs from full computation\npartial: %+v\nfull: %+v", got, full)
		}
		return got
	}
	first := calculate()
	if len(first[0].BenefitParts) == 0 || first[0].BenefitParts[0].Amount != 500 {
		t.Fatalf("fixture did not earn the expected benefit: %+v", first[0])
	}
	rows[0].SourceVersionID = 3
	data.Claims[3] = data.Claims[1]
	delete(data.Claims, 1)
	updated := calculate()
	if &first[0].BenefitParts[0] == &updated[0].BenefitParts[0] || &first[1].BenefitParts[0] != &updated[1].BenefitParts[0] {
		t.Fatal("a new payment version should only recalculate that payment")
	}
	rows[0].RecognizedMinor = &net
	adjusted := calculate()
	if adjusted[0].BenefitParts[0].Amount != 350 || &adjusted[1].BenefitParts[0] != &updated[1].BenefitParts[0] {
		t.Fatal("changed carried basis should only recalculate that payment")
	}
	data.Memberships = []vipMembership{{Person: "seller", Unit: "group"}}
	memberChange := calculate()
	if &memberChange[0].BenefitParts[0] == &adjusted[0].BenefitParts[0] || &memberChange[1].BenefitParts[0] != &adjusted[1].BenefitParts[0] {
		t.Fatal("seller-specific inputs must not invalidate other sellers")
	}
	// Source classification can be recorded after the source version was stored.
	data.Claims[3] = nil
	changedClaim := calculate()
	if changedClaim[0].BenefitParts[3].Status != "SALES_PERSON_MISSING" || &changedClaim[1].BenefitParts[0] != &memberChange[1].BenefitParts[0] {
		t.Fatal("classification changes must invalidate the affected quote")
	}
	data.Roles = data.Roles[1:]
	newRoles := calculate()
	if newRoles[1].BenefitParts[0].Status != "INACTIVE" || &changedClaim[1].BenefitParts[0] == &newRoles[1].BenefitParts[0] {
		t.Fatal("appointment changes must invalidate the period's quotes")
	}
	data.ConfigurationRevision++
	newConfig := calculate()
	if &newRoles[1].BenefitParts[0] == &newConfig[1].BenefitParts[0] {
		t.Fatal("configuration revision should force an entire recalculation")
	}
	data.People["person:13"] = "更新后的人员名称"
	data.DirectoryFingerprint = "updated-directory"
	directoryUpdate := calculate()
	if &newConfig[1].BenefitParts[0] == &directoryUpdate[1].BenefitParts[0] || directoryUpdate[1].SalesParts[0].Name != data.People["person:13"] {
		t.Fatal("person directory changes must invalidate cached names")
	}
	data.Definitions["rule"] = json.RawMessage(strings.Replace(string(definition), `"rate_bps":500`, `"rate_bps":600`, 1))
	newRule := calculate()
	if &directoryUpdate[1].BenefitParts[0] == &newRule[1].BenefitParts[0] {
		t.Fatal("changed rule content must invalidate quotes")
	}
	data.Definitions["another-eligible-rule"] = definition
	rows[0].RuleID = "another-eligible-rule"
	switchedRule := calculate()
	if &newRule[1].BenefitParts[0] != &switchedRule[1].BenefitParts[0] {
		t.Fatal("the first payment to match another published rule must not invalidate other payments")
	}
	rows[0].Status = "DUPLICATE_PAYMENT"
	if got := calculate(); len(got[0].BenefitParts) != 0 {
		t.Fatal("an ineligible payment should never reuse a quote")
	}
	rows[0].Status = "MATCHED"
	if got := calculate(); &switchedRule[0].BenefitParts[0] == &got[0].BenefitParts[0] {
		t.Fatal("payments no longer in the result should be removed from the cache")
	}
}

func TestVIPDraftQuotesBoundRetainedPeriodsAndRows(t *testing.T) {
	cache := &vipDraftQuoteCache{}
	period := vipDraftQuotePeriod{quotes: map[int64]vipDraftQuote{1: {}}, rows: 1}
	for id := int64(1); id <= maxDraftCachedPeriods+1; id++ {
		period.id = id
		cache.remember(period)
	}
	if cache.read(1, period.scope) != nil || cache.periods.Len() != maxDraftCachedPeriods {
		t.Fatal("least recent period must be evicted")
	}
	period.rows = maxDraftCachedRows
	cache.remember(period)
	if cache.rows != maxDraftCachedRows || cache.periods.Len() != 1 {
		t.Fatal("only the full-size period fits the row budget")
	}
	period.rows++
	cache.remember(period)
	if cache.read(period.id, period.scope) != nil || cache.rows != 0 {
		t.Fatal("oversize periods must use full calculation without retaining results")
	}
}
