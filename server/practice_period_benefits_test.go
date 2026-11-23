package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPracticeIndependentBenefitCanUseCompanyAndCategoryRates(t *testing.T) {
	at := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	const category = "40000000-0000-4000-8000-000000000001"
	const other = "40000000-0000-4000-8000-000000000002"
	var part component
	if err := json.Unmarshal([]byte(`{"type":"service","mode":"period_rate","classMember":"new","value":0,"periodBenefit":{
	  "label":"按类别的服务提成","personId":7,"salesCompanies":["31","33","34","35"],"standardRates":[
	   {"salesCategoryIds":["40000000-0000-4000-8000-000000000001"],"value":50000},
	   {"salesCompanies":["31","32"],"value":100000},{"salesCompanies":["33"],"value":0},{"value":30000}]}}`), &part); err != nil {
		t.Fatal(err)
	}
	if err := validateComponents([]component{part}); err != nil {
		t.Fatal(err)
	}
	payment := func(id int64, company string, amount int64) paymentInput {
		key := strconv.FormatInt(id, 10)
		return paymentInput{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: key, PaymentID: key, SalesCompanyID: company,
			AmountMinor: &amount, OccurredAt: at}}
	}
	member := func(id int64, payments ...string) practiceEnrollment {
		return practiceEnrollment{ID: id, SourceEnrollmentID: strconv.FormatInt(id, 10), MembershipType: 3,
			CommissionEligible: true, PaidOrderIDs: payments, ConfirmedAt: at.Add(time.Hour)}
	}
	orders := map[string][]paymentInput{"1": {payment(1, "31", 4000)}, "2": {payment(2, "34", 6000)},
		"3": {payment(3, "35", 5000)}, "4": {payment(4, "33", 15000)}}
	members := []practiceEnrollment{member(1, "1", "2"), member(2, "3"), member(3, "4")}
	runtime := practiceDraftRuntime{companyClassifications: map[string]string{"2": category, "3": other}}
	rule := paymentRule{ID: 1, Components: []component{part}}
	draft, err := calculatePracticeDraft(members, rule, orders, nil, nil, practiceTierHistory{}, nil, runtime)
	if err != nil || draft.Total != 850 || len(draft.Issues) != 0 || len(draft.Lines) != 3 {
		t.Fatalf("expected company override, category rate, and fallback, with a zero company override: %+v %v", draft, err)
	}
	for _, line := range draft.Lines {
		want := map[int64]int64{1: 400, 2: 300, 3: 150}[line.FactID]
		if line.PersonID != 7 || line.Amount != want || !line.practiceProportionalRefund {
			t.Fatalf("each original payment gets its own rate and proportional refund: %+v", line)
		}
	}
	var payments []paymentInput
	for _, batch := range orders {
		payments = append(payments, batch...)
	}
	snapshot := capturePracticeReplayInputs(rule, draft, payments, nil, runtime)
	if snapshot.CompanyClassifications["2"] != category {
		t.Fatalf("the posted rule must retain the payment-time category for refunds: %+v", snapshot.CompanyClassifications)
	}
	testRefund := int64(3000)
	replayed, err := calculatePracticeDraft(members, rule, orders, nil, nil, practiceTierHistory{replaying: true},
		map[int64][]practiceRefundMatch{2: {{event: paymentInput{Fact: sourceFact{AmountMinor: &testRefund, OccurredAt: at.AddDate(0, 1, 0)}}}}},
		practiceDraftRuntime{companyClassifications: snapshot.CompanyClassifications})
	if err != nil || len(replayed.Issues) != 0 || replayed.Total != 700 {
		t.Fatalf("refund must affect only the matching payment's original entitlement: %d %+v %v", replayed.Total, replayed.Issues, err)
	}
	delete(runtime.companyClassifications, "2")
	draft, err = calculatePracticeDraft(members, rule, orders, nil, nil, practiceTierHistory{}, nil, runtime)
	if err != nil || draft.Total != 550 || len(draft.Lines) != 2 || len(draft.Issues) != 1 || draft.Issues[0].Code != "PRACTICE_TIER_SALES_CATEGORY_MISSING" || draft.Issues[0].FactID != 2 {
		t.Fatalf("unknown category must not silently use the general rate: %+v %v", draft, err)
	}
	orders["3"] = []paymentInput{payment(3, "35", 1)}
	standard := []practicePeriodStandardRate{{Value: 500000}}
	rule.Components = []component{
		{Type: "service", Mode: "period_rate", PeriodBenefit: &practicePeriodBenefit{Label: "服务", PersonID: 7, StandardRates: standard}},
		{Type: "delivery", Mode: "period_rate", PeriodBenefit: &practicePeriodBenefit{Label: "交付", PersonID: 8, StandardRates: standard}},
	}
	draft, err = calculatePracticeDraft(members[1:2], rule, orders, nil, nil, practiceTierHistory{}, nil, runtime)
	if err != nil || draft.Total != 2 || len(draft.Lines) != 2 || draft.Lines[0].Amount != 1 || draft.Lines[1].Amount != 1 ||
		draft.Lines[0].Component == draft.Lines[1].Component {
		t.Fatalf("two arrangements at the same rate must round independently: %+v %v", draft, err)
	}
}

func TestPracticeRefundCreditsOriginallyZeroPoolTier(t *testing.T) {
	t.Run("保留原归属", func(t *testing.T) { verifyPracticeZeroPoolTier(t, true) })
	t.Run("历史原结算缺少归属明细", func(t *testing.T) { verifyPracticeZeroPoolTier(t, false) })
}

func verifyPracticeZeroPoolTier(t *testing.T, retainReferences bool) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_ZERO_TIER", Name: "交付阶梯样本", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "zero-tier"}))
	const rosterKey = "PRACTICE:1:1:60"
	class := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "零计提班次", Ordinal: 60,
		StartsOn: "2026-08-08", EndsOn: "2026-08-08", SourcePeriodKey: rosterKey}))
	september := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "补计账期", SettlementMonth: "2026-09"}))
	october := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "次月退款", SettlementMonth: "2026-10"}))
	owner := id(s.createPerson(ctx, createPersonInput{Name: "结算时交付负责人"}))
	const unit = "41000000-0000-4000-8000-000000000201"
	mustExec(`INSERT INTO organization_units(id,name,kind,status) VALUES($1,'交付公司','COMPANY','ACTIVE')`, unit)
	pool := id(s.createPracticePool(ctx, createPracticePoolInput{ProductID: product, UnitID: unit, Name: "交付池"}))
	through := int64(24999)
	config := publishRuleInput{ProductID: product, PeriodID: &class, Label: "原交付阶梯", Components: []component{{
		Type: "delivery", Mode: "period_rate", PeriodBenefit: &practicePeriodBenefit{Label: "交付",
			Pool: &practicePoolTarget{PoolID: pool, OwnerID: owner}, TierOnWholeClass: true,
			Tiers: []practicePeriodRate{{Through: &through, Value: 100000}, {From: 25000}}},
	}}}
	if _, err := s.publishRule(ctx, config); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 8, 9, 0, 0, 0, shanghai)
	paid := func(order string) sourceFact {
		at, amount := start.Add(-24*time.Hour), int64(15000)
		return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "zero-tier", Kind: "ORDER_PAID", Version: "1",
			OrderID: order, PaymentID: order, RecordKey: "payment:" + order, OccurredAt: at, UpdatedAt: at, AmountMinor: &amount}
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, practiceRoster{Key: rosterKey, Hash: "zero-tier", SourceVersion: 1,
			StartsAt: start, ConfirmedAt: start.Add(time.Hour), Enrollments: []practiceEnrollment{
				{SourceEnrollmentID: "1", ParticipantID: "1", MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"1"}, ContentHash: "aa"},
				{SourceEnrollmentID: "2", ParticipantID: "2", MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"2"}, ContentHash: "bb"},
			}}); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, []sourceFact{paid("1"), paid("2")})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	calculate := func(period, amount int64) draftSummary {
		t.Helper()
		draft, err := s.calculateCurrentDraft(ctx, period, 0)
		if err != nil || draft.IssueCount != 0 || draft.Total != amount || draft.LineCount != 0 {
			t.Fatalf("expected %d with no visible accrual or pending issue: %+v %v", amount, draft, err)
		}
		return draft
	}
	freeze := func(draft draftSummary) {
		t.Helper()
		mustExec(`INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, draft.PeriodID, draft.Version)
	}
	original := calculate(class, 0)
	var detail struct {
		Lines                   []json.RawMessage
		PoolAllocationAvailable bool
	}
	if err := s.db.QueryRow(ctx, draftDetailSQL, class, 0, 0, original.Version, 0, 0).Scan(&detail); err != nil ||
		len(detail.Lines) != 0 || detail.PoolAllocationAvailable {
		t.Fatalf("zero references should not appear as accrued benefits: %+v %v", detail, err)
	}
	if !retainReferences {
		// Reproduce a settlement created before zero tier references were stored.
		mustExec(`DELETE FROM calculation_draft_lines WHERE period_id=$1 AND practice_tier_reference`, class)
	}
	freeze(original)
	// New rules must not provide the rate or recipient for the original order.
	config.Components[0].PeriodBenefit = &practicePeriodBenefit{Label: "新版交付", PersonID: owner}
	config.Components[0].Value = 500000
	if _, err := s.publishRule(ctx, config); err != nil {
		t.Fatal(err)
	}
	refund := func(order string, month time.Month, amount int64) {
		t.Helper()
		at := time.Date(2026, month, 5, 9, 0, 0, 0, shanghai)
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{{
				Source: "DCZD", Domain: "PRACTICE", SourceKey: "zero-tier", Kind: "REFUND_SUCCEEDED", Version: "1",
				OrderID: order, RefundID: order, RecordKey: "refund:" + order, OccurredAt: at, UpdatedAt: at, AmountMinor: &amount,
				RefundsPayment: []sourcePaymentReference{{OrderID: order, RecordID: order}},
			}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	refund("1", time.September, 15000)
	if !retainReferences {
		draft, err := s.calculateCurrentDraft(ctx, september, 0)
		if err != nil || draft.Total != 0 || draft.IssueCount != 1 {
			t.Fatalf("historical data without a frozen recipient must stay pending: %+v %v", draft, err)
		}
		var issue string
		if err := s.db.QueryRow(ctx, `SELECT code FROM calculation_draft_issues WHERE period_id=$1`, september).
			Scan(&issue); err != nil || issue != "PRACTICE_REFUND_REPLAY_NEW_LINE" {
			t.Fatalf("expected an explicit issue for a missing historical posting: %s %v", issue, err)
		}
		return
	}
	credited := calculate(september, 1500)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		stages, err := readPoolStages(ctx, tx, september)
		if err != nil {
			return err
		}
		for _, stage := range stages {
			if stage.Opening != 0 || stage.Refund != 1500 || stage.Balance != 1500 {
				t.Fatalf("credit must go to the original pool: %+v", stage.poolDraftItem)
			}
		}
		if len(stages) != 1 {
			t.Fatalf("expected only the original pool: %v", stages)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	freeze(credited)
	refund("2", time.October, 5000)
	calculate(october, -500)
}

func TestPracticeScopeUsesSavedGroupAlternatives(t *testing.T) {
	const role = "DIRECT_REFERRER"
	payment := paymentInput{ID: 11, Fact: sourceFact{Source: "DCZD", ReferrerID: "seller", SalesCompanyID: "company"}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{ID: 1, PersonID: 24}}}
	inputs := practiceReplayInputs{SalesOrganizations: practiceSalesOrganizations{11: {role: {
		PersonID: 24, SourceCompany: "company", Department: "department",
		ScopeAlternatives: []organizationScopeCandidate{{Department: "department", Group: "first"}, {Department: "department", Group: "second"}},
	}}}}
	snapshot, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	var saved practiceReplayInputs
	if err := json.Unmarshal(snapshot, &saved); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		location []string
		eligible bool
		issue    string
	}{
		{[]string{"department"}, true, ""},
		{[]string{"first", "second"}, true, ""},
		{[]string{"first"}, false, "SALES_ORGANIZATION_SCOPE_CONFLICT"},
		{[]string{"third"}, false, ""},
	} {
		benefit := practicePeriodBenefit{SalesUnits: tc.location}
		eligible, issue := benefit.eligiblePayment(payment, role, nil, identities, practiceDraftRuntime{salesOrganizations: saved.SalesOrganizations})
		if eligible != tc.eligible || issue != tc.issue {
			t.Fatalf("scope %v resolved to %v/%q, want %v/%q", tc.location, eligible, issue, tc.eligible, tc.issue)
		}
	}
}

func TestPracticeOwnSalesAndLeadershipPayTheSamePersonIndependently(t *testing.T) {
	amount := int64(10000)
	at := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	members := []practiceEnrollment{
		{ID: 1, MembershipType: 3, CommissionEligible: true, ConfirmedAt: at, PaidOrderIDs: []string{"11"}},
		{ID: 2, MembershipType: 3, CommissionEligible: true, ConfirmedAt: at, PaidOrderIDs: []string{"22"}},
	}
	orders := map[string][]paymentInput{}
	identities := map[identityKey][]identityAssignment{}
	for i, member := range members {
		order := member.PaidOrderIDs[0]
		identities[identityKey{"DCZD", order}] = []identityAssignment{{PersonID: int64(i + 4)}}
		orders[order] = []paymentInput{{ID: member.ID, Fact: sourceFact{Source: "DCZD", OrderID: order, PaymentID: order,
			ReferrerID: order, AmountMinor: &amount, OccurredAt: at.Add(-time.Hour)}}}
	}
	const config = `[
		{"type":"manager","mode":"period_rate","sourceRole":"DIRECT_REFERRER","value":100000,
		 "periodBenefit":{"label":"本人销售","personId":4,"ownSales":true}},
		{"type":"manager","mode":"period_rate","value":20000,
		 "periodBenefit":{"label":"负责人","personId":4}}
	]`
	rule := paymentRule{ID: 1}
	if err := json.Unmarshal([]byte(config), &rule.Components); err != nil || validateComponents(rule.Components) != nil {
		t.Fatalf("valid arrangements: %v, %+v", err, rule)
	}
	draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
	if err != nil || len(draft.Issues) != 0 || len(draft.Lines) != 3 || draft.Total != 1400 {
		t.Fatalf("one personal and two leadership lines expected: %+v, %v", draft, err)
	}
	for _, line := range draft.Lines {
		if line.PersonID != 4 || line.FactID == 2 && line.Amount != 200 {
			t.Fatalf("unexpected order or payee: %+v", line)
		}
	}
	rule.Components[0].SourceRole = ""
	if validateComponents(rule.Components) == nil {
		t.Fatal("own sales requires a source role")
	}
}

func TestPracticePersonalAndReferralArrangementsUseConfiguredOrderScope(t *testing.T) {
	const category = "41000000-0000-4000-8000-000000000212"
	const team = "41000000-0000-4000-8000-000000000213"
	at := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	members := []practiceEnrollment{
		{ID: 1, MembershipType: 3, CommissionEligible: true, ConfirmedAt: at, PaidOrderIDs: []string{"11", "12"}},
		{ID: 2, MembershipType: 3, CommissionEligible: true, ConfirmedAt: at, PaidOrderIDs: []string{"22"}},
	}
	payment := func(id int64, order, referrer string, amount int64) []paymentInput {
		return []paymentInput{{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: order, PaymentID: order, ReferrerID: referrer,
			SalesCompanyID: "company", AmountMinor: &amount, OccurredAt: at.Add(-time.Hour)}}}
	}
	orders := map[string][]paymentInput{"11": payment(11, "11", "a", 4000), "12": payment(12, "12", "a", 6000), "22": payment(22, "22", "b", 10000)}
	identities := map[identityKey][]identityAssignment{{"DCZD", "a"}: {{PersonID: 4}}, {"DCZD", "b"}: {{PersonID: 5}}}
	runtime := practiceDraftRuntime{
		companyClassifications:  map[string]string{"11": practiceInternalCompany, "12": practiceInternalCompany, "22": "another-category"},
		internalSalesCategories: map[string]string{"11": category, "12": category},
		salesOrganizations: practiceSalesOrganizations{
			11: {"DIRECT_REFERRER": {PersonID: 4, SourceCompany: "company", Group: team}},
			12: {"DIRECT_REFERRER": {PersonID: 4, SourceCompany: "company", Group: "other"}},
			22: {"DIRECT_REFERRER": {PersonID: 5, SourceCompany: "company", Group: team}},
		}}
	var rule paymentRule
	definition := `[
	 {"type":"sales","mode":"rate","sourceRole":"DIRECT_REFERRER","value":1000,"label":"本人销售","salesPersonId":4,"saleScope":{"ownSales":true}},
	 {"type":"referral","mode":"rate","sourceRole":"DIRECT_REFERRER","value":500,"label":"该小组推荐",
	  "saleScope":{"salesCategoryIds":["` + category + `"],"salesUnits":["` + team + `"]}}
	]`
	if err := json.Unmarshal([]byte(definition), &rule.Components); err != nil || validateComponents(rule.Components) != nil {
		t.Fatalf("invalid rule: %v %+v", err, rule)
	}
	draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, nil, runtime)
	if err != nil || len(draft.Issues) != 0 || len(draft.Lines) != 3 || draft.Total != 1200 || len(draft.Performance) != 3 {
		t.Fatalf("scope must apply per arrangement while preserving all three sale performance records: %+v %v", draft, err)
	}
	for _, line := range draft.Lines {
		if line.PersonID != 4 || line.Component == "referral" && (line.FactID != 11 || line.Amount != 200) {
			t.Fatalf("scope paid the wrong order or beneficiary: %+v", line)
		}
	}
	referral := &rule.Components[1]
	referral.Mode, referral.Value, referral.RefundPolicy = "fixed", 1000, "proportional"
	refund := map[int64][]practiceRefundMatch{11: {{event: payment(31, "refund", "a", 2000)[0]}}}
	draft, err = calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, refund, runtime)
	if err != nil || len(draft.Issues) != 0 || draft.Total != 1300 {
		t.Fatalf("refund for a scoped payment must reduce both its personal and its fixed referral awards: %+v %v", draft, err)
	}
	for _, line := range draft.Lines {
		if line.Component == "referral" && (line.FactID != 11 || line.Amount != 500 || !line.practiceProportionalRefund) {
			t.Fatalf("wrong refundable fixed award: %+v", line)
		}
	}
}

func TestPracticePeriodBenefitChecksTheConfiguredPaymentRole(t *testing.T) {
	amount := int64(5000)
	const role = "LOCKED_SELLER"
	payments := []paymentInput{
		{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "1", AmountMinor: &amount, ReferrerID: "first", SalesCompanyID: "company",
			Participants: []sourceParticipant{{Role: role, UserID: "locked"}}}},
		{ID: 2, Fact: sourceFact{Source: "DCZD", OrderID: "2", AmountMinor: &amount, ReferrerID: "second", SalesCompanyID: "company",
			Participants: []sourceParticipant{{Role: role, UserID: "locked"}}}},
	}
	identities := map[identityKey][]identityAssignment{
		{"DCZD", "locked"}:    {{PersonID: 30, ID: 1}},
		{"DCZD", "different"}: {{PersonID: 31, ID: 2}},
	}
	part := component{Type: "delivery", Mode: "period_rate", SourceRole: role,
		PeriodBenefit: &practicePeriodBenefit{PersonID: 3, SalesPeople: []int64{30}}}
	snapshot := capturePracticeReplayInputs(paymentRule{Components: []component{part}}, paymentDraft{}, payments,
		identities, practiceDraftRuntime{})
	if snapshot.Version != practiceReplayConfiguredBenefitRole {
		t.Fatalf("configured payment roles must be preserved in the new replay version: %+v", snapshot)
	}
	purchases := []practicePurchase{{payments: payments, base: amount * 2}}
	shares, base, issues, err := collectPracticeBenefitShares(part, purchases, nil, identities, nil, practiceDraftRuntime{})
	if err != nil || len(issues) != 0 || len(shares) != 1 || base != amount*2 {
		t.Fatalf("the selected locked seller matches both legs regardless of their referrer: %+v %d %+v %v", shares, base, issues, err)
	}
	payments[1].Fact.ReferrerID = "first"
	payments[1].Fact.Participants[0].UserID = "different"
	_, _, issues, err = collectPracticeBenefitShares(part, purchases, nil, identities, nil, practiceDraftRuntime{})
	if err != nil || len(issues) != 1 || issues[0].Code != "PRACTICE_ATTRIBUTION_CONFLICT" {
		t.Fatalf("different people in the selected role must be flagged: %+v %v", issues, err)
	}
	shares, base, issues, err = collectPracticeBenefitShares(part, purchases, nil, identities, nil, practiceDraftRuntime{legacyBenefitAttribution: true})
	if err != nil || len(issues) != 0 || len(shares) != 1 || base != amount {
		t.Fatalf("a legacy replay must preserve the original attribution and scope for refunds: %+v %d %+v %v", shares, base, issues, err)
	}
}

func TestPracticePeriodBenefitTrialsKeepResolvedEnrollments(t *testing.T) {
	at := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	member := func(id int64, order string) practiceEnrollment {
		return practiceEnrollment{ID: id, SourceEnrollmentID: order, MembershipType: 3,
			CommissionEligible: true, PaidOrderIDs: []string{order}, ConfirmedAt: at.Add(time.Hour)}
	}
	paid := func(id int64, order, seller string) paymentInput {
		amount := int64(10000)
		return paymentInput{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: order, PaymentID: order,
			AmountMinor: &amount, OccurredAt: at, ReferrerID: seller}}
	}
	members := []practiceEnrollment{member(101, "101"), member(102, "102"), member(103, "103")}
	orders := map[string][]paymentInput{"101": {paid(1, "101", "a")}, "102": {paid(2, "102", "b")}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "a"}: {{ID: 1, PersonID: 30}}}
	fixed := int64(2000)
	for _, tc := range []struct {
		name              string
		method            practicePeriodBenefit
		rate              int64
		partial, resolved int64
	}{
		{name: "比例", rate: 100000, partial: 1000, resolved: 3000},
		{name: "报名固定金额", method: practicePeriodBenefit{FixedMinor: &fixed}, partial: 2000, resolved: 6000},
		{name: "整班固定金额", method: practicePeriodBenefit{FixedMinor: &fixed, FixedPerClass: true}, resolved: 2000},
		{name: "阶梯", method: practicePeriodBenefit{Tiers: []practicePeriodRate{{Value: 100000}}}, resolved: 3000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			benefit := tc.method
			benefit.Label, benefit.PersonID, benefit.SalesPeople = tc.name, 7, []int64{30}
			rule := paymentRule{ID: 1, Components: []component{{Type: "delivery", Mode: "period_rate", ClassMember: "new",
				SourceRole: "DIRECT_REFERRER", Value: tc.rate, PeriodBenefit: &benefit}}}
			if err := validateComponents(rule.Components); err != nil {
				t.Fatal(err)
			}
			draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
			if err != nil || draft.Total != tc.partial || draft.blockingIssues() == 0 {
				t.Fatalf("expected partial trial %d with a blocking source issue: %+v %v", tc.partial, draft, err)
			}
			for _, line := range draft.Lines {
				if line.PersonID != 7 || line.FactID != 1 {
					t.Fatalf("only the resolved payment may be priced: %+v", line)
				}
			}
			if tc.partial > 0 && !slices.ContainsFunc(draft.Issues, func(issue draftIssue) bool {
				return issue.Code == "MISSING_IDENTITY" && issue.FactID == 2 && issue.EnrollmentID == 102
			}) {
				t.Fatalf("the source gap must identify the exact payment and enrollment: %+v", draft.Issues)
			}
			orders["103"] = []paymentInput{paid(3, "103", "a")}
			identities[identityKey{"DCZD", "b"}] = []identityAssignment{{ID: 2, PersonID: 30}}
			draft, err = calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
			delete(orders, "103")
			delete(identities, identityKey{"DCZD", "b"})
			if err != nil || draft.Total != tc.resolved || len(draft.Issues) != 0 || len(draft.Lines) != 3 {
				t.Fatalf("corrected source data should restore the complete result %d: %+v %v", tc.resolved, draft, err)
			}
		})
	}
}

func TestPracticeServiceCommissionRequiresAnExplicitArrangement(t *testing.T) {
	amount := int64(50000)
	at := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	members := []practiceEnrollment{{ID: 1, MembershipType: 2, CommissionEligible: true, ConfirmedAt: at, PaidOrderIDs: []string{"11"}}}
	orders := map[string][]paymentInput{"11": {{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "11", PaymentID: "11",
		AmountMinor: &amount, OccurredAt: at.Add(-time.Hour)}}}}
	service := component{Type: "service", ClassMember: "retrain", Mode: "period_rate", Value: 12000,
		PeriodBenefit: &practicePeriodBenefit{Label: "服务提成", PersonID: 4}}
	delivery := component{Type: "delivery", ClassMember: "retrain", Mode: "period_rate", Value: 20000,
		PeriodBenefit: &practicePeriodBenefit{Label: "交付提成", PersonID: 5}}
	for _, tc := range []struct {
		name  string
		parts []component
		total int64
		lines int
	}{
		{"配置服务和交付", []component{delivery, service}, 1600, 2},
		{"移除服务仍计提交付", []component{delivery}, 1000, 1},
		{"仅配置服务无需销售", []component{service}, 600, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateComponents(tc.parts); err != nil {
				t.Fatal(err)
			}
			rule := paymentRule{ID: 1, Components: tc.parts}
			draft, err := calculatePracticeDraft(members, rule, orders, nil, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
			if err != nil || len(draft.Issues) != 0 || draft.Total != tc.total || len(draft.Lines) != tc.lines {
				t.Fatalf("expected only the configured arrangements: %+v %v", draft, err)
			}
			for _, line := range draft.Lines {
				if line.PersonID == service.PeriodBenefit.PersonID && (line.Amount != 600 || !strings.HasPrefix(line.Component, "period:service:")) {
					t.Fatalf("service must produce its own payout line: %+v", line)
				}
			}
		})
	}
}

func TestPracticeArrangementSalesCategoryScope(t *testing.T) {
	const category = "ee260b91-1c0a-4121-b715-5fdc144bad9b"
	const otherCategory = "062c6927-edc4-4c43-8a9a-dcaf759ea873"
	amount := int64(10000)
	part := component{Type: "manager", Mode: "period_rate", ClassMember: "retrain", Value: 100000,
		PeriodBenefit: &practicePeriodBenefit{Label: "类别内服务", PersonID: 4, SalesCategoryIDs: []string{category}}}
	if err := validateComponents([]component{part}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	var members []practiceEnrollment
	var facts []paymentInput
	orders := make(map[string][]paymentInput)
	for i := int64(1); i <= 3; i++ {
		key := strconv.FormatInt(i, 10)
		members = append(members, practiceEnrollment{ID: i, MembershipType: 2, CommissionEligible: true, PaidOrderIDs: []string{key}})
		fact := paymentInput{ID: i, Fact: sourceFact{Source: "DCZD", OrderID: key, PaymentID: key,
			AmountMinor: &amount, OccurredAt: at, SalesCompanyID: key}}
		facts = append(facts, fact)
		orders[key] = []paymentInput{fact}
	}
	rule := paymentRule{ID: 1, Components: []component{part}}
	runtime := practiceDraftRuntime{
		companyClassifications:  map[string]string{"1": category, "2": practiceInternalCompany, "3": otherCategory},
		internalSalesCategories: map[string]string{"2": category},
	}
	draft, err := calculatePracticeDraft(members, rule, orders, nil, nil, practiceTierHistory{}, nil, runtime)
	if err != nil || len(draft.Issues) != 0 || len(draft.Performance) != 3 || len(draft.Lines) != 2 || draft.Total != 2000 {
		t.Fatalf("expected only the two eligible sales with full class performance: %+v %v", draft, err)
	}
	saved := capturePracticeReplayInputs(rule, draft, facts, nil, runtime)
	if saved.CompanyClassifications["1"] != category || saved.InternalSalesCategories["2"] != category {
		t.Fatalf("the confirmed payment categories must be available for refund replay: %+v", saved)
	}
	part.PeriodBenefit.SalesUnits = []string{"selected-department"}
	if eligible, issue := part.PeriodBenefit.eligiblePayment(facts[2], "DIRECT_REFERRER", nil, nil, runtime); eligible || issue != "" {
		t.Fatalf("an unrelated sales category does not require an organization match: %v %s", eligible, issue)
	}
	part.PeriodBenefit.SalesUnits = nil
	part.PeriodBenefit.SalesCategoryIDs = []string{category, category}
	if validateComponents([]component{part}) == nil {
		t.Fatal("duplicate category IDs must be rejected")
	}
}

func TestPracticeArrangementResolvesRecipientsFromPayments(t *testing.T) {
	const orderRole = "DIRECT_REFERRER"
	var company pgtype.UUID
	if err := company.Scan("40000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	const amountTotal int64 = 10000
	deposit, balance := int64(4000), int64(6000)
	timestamp := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	paid := func(id int64, amount *int64, at time.Time, referrer string) paymentInput {
		key := strconv.FormatInt(id, 10)
		return paymentInput{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: key, PaymentID: key,
			AmountMinor: amount, OccurredAt: at, ReferrerID: referrer, SalesCompanyID: "partner"}}
	}
	parts := []component{
		{Type: "manager", Mode: "period_rate", Value: 100000, PeriodBenefit: &practicePeriodBenefit{Label: "订单人员提成", OrderRecipientRole: orderRole}},
		{Type: "manager", Mode: "period_rate", Value: 50000, PeriodBenefit: &practicePeriodBenefit{Label: "公司提成", SalesCompanyRecipient: true}},
	}
	if err := validateComponents(parts); err != nil {
		t.Fatal(err)
	}
	invalidRecipient := *parts[0].PeriodBenefit
	invalidRecipient.PersonID = 7
	bad := parts[0]
	bad.PeriodBenefit = &invalidRecipient
	if validateComponents([]component{bad}) == nil {
		t.Fatal("an arrangement cannot have a named recipient and an order recipient")
	}
	members := []practiceEnrollment{{ID: 1, MembershipType: 3, CommissionEligible: true,
		PaidOrderIDs: []string{"1", "2"}, ConfirmedAt: timestamp.Add(2 * time.Hour)}}
	first, second := paid(1, &deposit, timestamp, "seller"), paid(2, &balance, timestamp.Add(time.Hour), "seller")
	orders := map[string][]paymentInput{"1": {first}, "2": {second}}
	identities := map[identityKey][]identityAssignment{
		{"DCZD", "seller"}: {{PersonID: 7, ID: 10, ValidTo: &second.Fact.OccurredAt}, {PersonID: 7, ID: 11, ValidFrom: second.Fact.OccurredAt}},
		{"DCZD", "other"}:  {{PersonID: 8, ID: 12}},
	}
	rule := paymentRule{ID: 1, Components: parts}
	runtime := practiceDraftRuntime{companyRecipients: map[string][]companySourceAssignment{"partner": {{Company: company}}}}
	calculate := func(refunds map[int64][]practiceRefundMatch) paymentDraft {
		t.Helper()
		draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, refunds, runtime)
		if err != nil {
			t.Fatal(err)
		}
		return draft
	}
	baseline := calculate(nil)
	if baseline.Total != amountTotal*15/100 || len(baseline.Lines) != 4 || len(baseline.Issues) != 0 || len(baseline.Performance) != 2 {
		t.Fatalf("both configured arrangements should share performance and pay independently: %+v", baseline)
	}
	for index, line := range baseline.Lines[:2] {
		if line.PersonID != 7 || line.IdentityID != int64(index+10) || line.Amount != []int64{400, 600}[index] {
			t.Fatalf("expected the order's person and identity at payment time: %+v", line)
		}
	}
	for index, line := range baseline.Lines[2:] {
		if line.BeneficiaryUnit == nil || *line.BeneficiaryUnit != company || line.Amount != []int64{200, 300}[index] {
			t.Fatalf("expected the order's company with the payment share: %+v", line)
		}
	}
	refundAmount := int64(2000)
	partial := calculate(map[int64][]practiceRefundMatch{1: {{event: paymentInput{ID: 3,
		Fact: sourceFact{AmountMinor: &refundAmount, OccurredAt: timestamp.Add(time.Minute)}}}}})
	if partial.Total != 1200 || len(partial.Issues) != 0 || partial.Lines[0].Amount != 200 || partial.Lines[2].Amount != 100 {
		t.Fatalf("partial refund must reduce each arrangement by its own share: %+v", partial)
	}
	orders["2"][0].Fact.ReferrerID = "other"
	conflict := calculate(nil)
	if len(conflict.Issues) != 1 || conflict.Issues[0].Code != "PRACTICE_ATTRIBUTION_CONFLICT" || conflict.Total != 500 {
		t.Fatalf("conflicting payment attribution must block only the order-person arrangement: %+v", conflict)
	}
}

func TestPracticePeriodBenefitCanUseThePaymentOrganizationAppointment(t *testing.T) {
	var benefit practicePeriodBenefit
	if err := json.Unmarshal([]byte(`{"label":"按组织任职收款","orderAppointmentLevel":"TEAM","orderAppointmentKind":"OWNER","recipientDuty":"TEAM_DUTY"}`), &benefit); err != nil {
		t.Fatal(err)
	}
	part := component{Type: "manager", Mode: "period_rate", Value: 100000, SourceRole: "DIRECT_REFERRER", PeriodBenefit: &benefit}
	if err := validateComponents([]component{part}); err != nil {
		t.Fatal(err)
	}
	const firstUnit = "40000000-0000-4000-8000-000000000021"
	const secondUnit = "40000000-0000-4000-8000-000000000022"
	paidAt := time.Date(2026, 9, 10, 9, 0, 0, 0, shanghai)
	handover := paidAt.Add(time.Hour)
	deposit, balance, another := int64(4000), int64(6000), int64(10000)
	makePayment := func(id int64, amount *int64, at time.Time, user, company string) paymentInput {
		key := strconv.FormatInt(id, 10)
		return paymentInput{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: key, PaymentID: key, AmountMinor: amount,
			OccurredAt: at, ReferrerID: user, SalesCompanyID: company}}
	}
	first := makePayment(1, &deposit, paidAt, "salesA", "parent")
	second := makePayment(2, &balance, handover, "salesB", "parent")
	third := makePayment(3, &another, handover, "salesB", "partner")
	orders := map[string][]paymentInput{"1": {first}, "2": {second}, "3": {third}}
	members := []practiceEnrollment{
		{ID: 1, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"1", "2"}, ConfirmedAt: handover.Add(time.Hour)},
		{ID: 2, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"3"}, ConfirmedAt: handover.Add(time.Hour)},
	}
	identities := map[identityKey][]identityAssignment{
		{"DCZD", "salesA"}: {{ID: 1, PersonID: 7}},
		{"DCZD", "salesB"}: {{ID: 2, PersonID: 8}},
	}
	runtime := practiceDraftRuntime{
		salesOrganizations: practiceSalesOrganizations{
			1: {part.SourceRole: {PersonID: 7, SourceCompany: "parent", Group: firstUnit}},
			2: {part.SourceRole: {PersonID: 8, SourceCompany: "parent", Group: firstUnit}},
			3: {part.SourceRole: {PersonID: 8, SourceCompany: "partner", Group: secondUnit}},
		},
		orderAppointments: practiceOrderAppointments{
			firstUnit: {"OWNER": {{PersonID: 11, vipInterval: vipInterval{To: &handover}},
				{PersonID: 12, vipInterval: vipInterval{From: handover}}}},
			secondUnit: {"OWNER": {{PersonID: 13, vipInterval: vipInterval{From: handover}}}},
		},
	}
	rule := paymentRule{ID: 1, Components: []component{part}}
	calculate := func(refunds map[int64][]practiceRefundMatch) paymentDraft {
		t.Helper()
		draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, refunds, runtime)
		if err != nil || len(draft.Issues) != 0 {
			t.Fatalf("calculate appointment benefits: %+v / %v", draft.Issues, err)
		}
		return draft
	}
	check := func(draft paymentDraft, amounts map[int64]int64) {
		t.Helper()
		if len(draft.Lines) != len(amounts) {
			t.Fatalf("expected one posting per eligible source payment, got %+v", draft.Lines)
		}
		for _, line := range draft.Lines {
			if line.Amount != amounts[line.FactID] || line.PersonID != 10+line.FactID || !line.reportUnit.Valid {
				t.Fatalf("wrong payment-time recipient or amount: %+v", line)
			}
		}
	}
	original := calculate(nil)
	check(original, map[int64]int64{1: 400, 2: 600, 3: 1000})
	stored, err := json.Marshal(capturePracticeReplayInputs(rule, original, []paymentInput{first, second, third}, identities, runtime))
	if err != nil {
		t.Fatal(err)
	}
	var saved practiceReplayInputs
	if err := json.Unmarshal(stored, &saved); err != nil {
		t.Fatal(err)
	}
	runtime.orderAppointments, runtime.salesOrganizations = saved.OrderAppointments, saved.SalesOrganizations
	refunded := int64(2000)
	refund := map[int64][]practiceRefundMatch{1: {{event: paymentInput{ID: 4, Fact: sourceFact{AmountMinor: &refunded, OccurredAt: handover}}}}}
	check(calculate(refund), map[int64]int64{1: 200, 2: 600, 3: 1000})
	fixed := int64(100000)
	rule.Components[0].Value = 0
	benefit.FixedMinor, benefit.FixedPerClass = &fixed, true
	check(calculate(nil), map[int64]int64{1: 40000, 2: 60000, 3: 100000})
	check(calculate(refund), map[int64]int64{1: 20000, 2: 60000, 3: 100000})
	benefit.FixedPerClass, benefit.FixedPerOrder = false, true
	if err := validateComponents([]component{rule.Components[0]}); err != nil {
		t.Fatal(err)
	}
	check(calculate(nil), map[int64]int64{1: 100000, 2: 100000, 3: 100000})
	check(calculate(refund), map[int64]int64{1: 50000, 2: 100000, 3: 100000})
}

func TestPracticeFixedSignupBenefitKeepsOriginalPaymentShares(t *testing.T) {
	var part component
	if err := json.Unmarshal([]byte(`{"type":"delivery","mode":"period_rate","classMember":"new",
	 "periodBenefit":{"label":"每份新报交付","personId":1,"fixedMinor":1000}}`), &part); err != nil {
		t.Fatal(err)
	}
	if err := validateComponents([]component{part}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	member := practiceEnrollment{ID: 1, MembershipType: 3, CommissionEligible: true,
		PaidOrderIDs: []string{"1", "2"}, ConfirmedAt: start}
	makePayment := func(id int64, amount int64) paymentInput {
		order := strconv.FormatInt(id, 10)
		return paymentInput{ID: id, Fact: sourceFact{Source: "DCZD", PaymentID: order, OrderID: order,
			AmountMinor: &amount, OccurredAt: start.Add(-time.Hour)}}
	}
	orders := map[string][]paymentInput{"1": {makePayment(1, 4000)}, "2": {makePayment(2, 6000)}}
	for _, tc := range []struct {
		name      string
		refund    int64
		replaying bool
		want      [2]int64
	}{
		{"one enrollment", 0, false, [2]int64{400, 600}},
		{"partial refund before confirmation", 2000, false, [2]int64{200, 600}},
		{"full deposit refund in later month", 4000, true, [2]int64{0, 600}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refunds := map[int64][]practiceRefundMatch{}
			if tc.refund != 0 {
				refunds[1] = []practiceRefundMatch{{event: makePayment(3, tc.refund)}}
			}
			draft, err := calculatePracticeDraft([]practiceEnrollment{member}, paymentRule{ID: 1, Components: []component{part}},
				orders, nil, nil, practiceTierHistory{replaying: tc.replaying}, refunds, practiceDraftRuntime{})
			if err != nil || len(draft.Issues) != 0 {
				t.Fatalf("fixed signup draft: %+v %v", draft.Issues, err)
			}
			var got [2]int64
			for _, line := range draft.Lines {
				got[line.FactID-1] += line.Amount
			}
			if got != tc.want || draft.Total != tc.want[0]+tc.want[1] {
				t.Fatalf("payment shares %v, total %d, want %v", got, draft.Total, tc.want)
			}
		})
	}
}

func TestPracticePercentageBenefitSplitsNetPaymentsAndPreservesSettledShares(t *testing.T) {
	for _, tc := range []struct {
		name          string
		legacyVersion int
		fixedPerClass bool
		deposit, tail int64
	}{
		{"new percentage settlement", 0, false, 200, 600},
		{"previous gross-share settlement", 1, false, 320, 480},
		{"previous net-share settlement", 2, false, 200, 600},
		{"fixed class with different sales attribution", 0, true, 200, 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := workspace{db: integrationDB(t)}
			id := func(value any, err error) int64 {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				return value.(map[string]int64)["id"]
			}
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := s.db.Exec(ctx, sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			const sourceKey = "rate-shares"
			const rosterKey = "PRACTICE:1:1:81"
			product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_RATE_SHARES", Name: "分笔比例", PeriodMode: "class"}))
			id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: sourceKey}))
			class := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 81,
				StartsOn: "2026-08-08", EndsOn: "2026-08-08", SourcePeriodKey: rosterKey}))
			september := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月退款", SettlementMonth: "2026-09"}))
			person := id(s.createPerson(ctx, createPersonInput{Name: "交付人员"}))
			part := component{
				Type: "delivery", Mode: "period_rate", Value: 100000,
				PeriodBenefit: &practicePeriodBenefit{Label: "交付分润", PersonID: person},
			}
			if tc.fixedPerClass {
				award := int64(1000)
				part.Value = 0
				part.PeriodBenefit.FixedMinor, part.PeriodBenefit.FixedPerClass = &award, true
			}
			if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &class, Label: "原交付规则", Components: []component{part}}); err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 8, 8, 9, 0, 0, 0, shanghai)
			paid := func(order string, amount int64) sourceFact {
				at := start.Add(-48 * time.Hour)
				fact := sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: sourceKey, Kind: "ORDER_PAID",
					OrderID: order, PaymentID: order, RecordKey: "payment:" + order, AmountMinor: &amount, Version: "1", OccurredAt: at, UpdatedAt: at}
				if tc.fixedPerClass {
					fact.SalesCompanyID, fact.ReferrerID = "company-"+order, "seller-"+order
				}
				return fact
			}
			refund := func(order, key string, amount int64, at time.Time) sourceFact {
				return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: sourceKey, Kind: "REFUND_SUCCEEDED",
					OrderID: order, RefundID: key, RecordKey: "refund:" + key, AmountMinor: &amount, Version: "1", OccurredAt: at, UpdatedAt: at,
					RefundsPayment: []sourcePaymentReference{{OrderID: order, RecordID: order}}}
			}
			if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
				if _, err := storePracticeRoster(ctx, tx, practiceRoster{Key: rosterKey, Hash: "rate-shares", SourceVersion: 1,
					StartsAt: start, ConfirmedAt: start.Add(time.Hour), Enrollments: []practiceEnrollment{
						{SourceEnrollmentID: "1", ParticipantID: "1", MembershipType: 3, CommissionEligible: true,
							SignedAt: start, PaidOrderIDs: []string{"1", "2"}, ContentHash: "ab"},
					}}); err != nil {
					return err
				}
				_, err := storeSourceFacts(ctx, tx, []sourceFact{paid("1", 4000), paid("2", 6000), refund("1", "11", 2000, start.Add(-time.Hour))})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			original, err := s.calculateCurrentDraft(ctx, class, 0)
			if err != nil || original.Total != 800 || original.LineCount != 2 || original.IssueCount != 0 {
				t.Fatalf("expected 8 yuan total, got %+v %v", original, err)
			}
			if tc.legacyVersion != 0 {
				exec(`UPDATE calculation_drafts SET practice_replay_inputs=jsonb_set(practice_replay_inputs,'{version}',to_jsonb($2::int))
				 WHERE period_id=$1`, class, tc.legacyVersion)
			}
			if tc.legacyVersion == 1 {
				// Recreate the allocation and persisted replay version produced by the previous calculator.
				exec(`UPDATE calculation_draft_lines l SET amount_minor=CASE v.order_id WHEN '1' THEN $2::bigint ELSE $3::bigint END
				 FROM source_order_versions v WHERE l.period_id=$1 AND l.source_version_id=v.id`, class, tc.deposit, tc.tail)
			}
			assertShares := func() {
				t.Helper()
				var deposit, tail int64
				err := s.db.QueryRow(ctx, `SELECT sum(l.amount_minor) FILTER(WHERE v.order_id='1'),sum(l.amount_minor) FILTER(WHERE v.order_id='2')
				 FROM calculation_draft_lines l JOIN source_order_versions v ON v.id=l.source_version_id WHERE l.period_id=$1`,
					class).Scan(&deposit, &tail)
				if err != nil || deposit != tc.deposit || tail != tc.tail {
					t.Fatalf("booked deposit and tail = %d, %d; want %d, %d: %v", deposit, tail, tc.deposit, tc.tail, err)
				}
			}
			assertShares()
			exec(`INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, class, original.Version)
			if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
				_, err := storeSourceFacts(ctx, tx, []sourceFact{refund("2", "12", 6000, start.AddDate(0, 1, 0))})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			late, err := s.calculateCurrentDraft(ctx, september, 0)
			if err != nil || late.RefundTotal != -tc.tail || late.RefundCount != 1 || late.IssueCount != 0 {
				t.Fatalf("refunding the tail must reverse its booked share only: %+v %v", late, err)
			}
			assertShares()
		})
	}
}

func TestPracticeFixedClassBenefitDoesNotRedistributeFullRefund(t *testing.T) {
	var part component
	if err := json.Unmarshal([]byte(`{"type":"delivery","mode":"period_rate",
	 "periodBenefit":{"label":"整班交付","personId":1,"fixedMinor":10001,"fixedPerClass":true}}`), &part); err != nil {
		t.Fatal(err)
	}
	if err := validateComponents([]component{part}); err != nil {
		t.Fatal(err)
	}
	members := []practiceEnrollment{
		{ID: 1, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"1", "2"}},
		{ID: 2, MembershipType: 2, CommissionEligible: true, PaidOrderIDs: []string{"3"}},
	}
	orders := map[string][]paymentInput{}
	for i, amount := range []int64{4000, 6000, 30000} {
		id := int64(i + 1)
		order := strconv.FormatInt(id, 10)
		orders[order] = []paymentInput{{ID: id, Fact: sourceFact{Source: "DCZD", PaymentID: order, OrderID: order, AmountMinor: &amount,
			SalesCompanyID: "company-" + order, ReferrerID: "referrer-" + order}}}
	}
	for _, tc := range []struct {
		name          string
		fullyRefunded bool
		want          [3]int64
	}{
		{"whole class with split and single payment", false, [3]int64{1000, 1500, 7501}},
		{"both payments on one enrollment fully refunded", true, [3]int64{0, 0, 7501}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refunds := map[int64][]practiceRefundMatch{}
			if tc.fullyRefunded {
				refunds[1] = []practiceRefundMatch{{event: orders["1"][0]}}
				refunds[2] = []practiceRefundMatch{{event: orders["2"][0]}}
			}
			draft, err := calculatePracticeDraft(members, paymentRule{ID: 1, Components: []component{part}},
				orders, nil, nil, practiceTierHistory{replaying: tc.fullyRefunded}, refunds, practiceDraftRuntime{})
			if err != nil || len(draft.Issues) != 0 {
				t.Fatalf("fixed class draft: %+v %v", draft.Issues, err)
			}
			var got [3]int64
			for _, line := range draft.Lines {
				got[line.FactID-1] += line.Amount
			}
			if got != tc.want || draft.Total != tc.want[0]+tc.want[1]+tc.want[2] {
				t.Fatalf("payment shares %v, total %d, want %v", got, draft.Total, tc.want)
			}
		})
	}
	t.Run("sales-scoped benefit", func(t *testing.T) {
		scoped := part
		benefit := *part.PeriodBenefit
		benefit.SalesCompanies = []string{"company-1", "company-2", "company-3"}
		scoped.PeriodBenefit = &benefit
		draft, err := calculatePracticeDraft(members, paymentRule{ID: 1, Components: []component{scoped}},
			orders, nil, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
		if err != nil || draft.Total != 0 || len(draft.Issues) != 1 || draft.Issues[0].Code != "PRACTICE_PERIOD_BENEFIT_INCOMPLETE" {
			t.Fatalf("sales-scoped award should still check the two payments' attribution: %+v %v", draft, err)
		}
	})
	for _, tc := range []struct {
		name          string
		refundIssue   bool
		scope         []string
		missing       bool
		tiered        bool
		wholeClass    bool
		want          int64
		wantComponent string
	}{
		{name: "both payments outside the company scope", scope: []string{"company-3"}, want: 10001},
		{name: "refund issue outside the company scope", scope: []string{"company-3"}, refundIssue: true, want: 10001},
		{name: "one payment enters the company scope", scope: []string{"company-1", "company-3"}, wantComponent: "PRACTICE_PERIOD_BENEFIT_INCOMPLETE"},
		{name: "unknown company may enter the scope", scope: []string{"company-3"}, missing: true, wantComponent: "PRACTICE_TIER_COMPANY_MISSING"},
		{name: "refund issue inside the company scope", scope: []string{"company-1", "company-3"}, refundIssue: true, wantComponent: "PRACTICE_PERIOD_BENEFIT_INCOMPLETE"},
		{name: "scope tier ignores refunds outside its base", scope: []string{"company-3"}, tiered: true, refundIssue: true, want: 3000},
		{name: "whole-class tier waits for refunds anywhere in its base", scope: []string{"company-3"}, tiered: true, wholeClass: true, refundIssue: true, wantComponent: "PRACTICE_PERIOD_BENEFIT_INCOMPLETE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			benefit := *part.PeriodBenefit
			benefit.SalesCompanies = tc.scope
			if tc.tiered {
				benefit.FixedMinor, benefit.FixedPerClass = nil, false
				benefit.Tiers = []practicePeriodRate{{Value: 100000}}
				benefit.TierOnWholeClass = tc.wholeClass
			}
			scoped := part
			scoped.PeriodBenefit = &benefit
			payments := make(map[string][]paymentInput, len(orders))
			for key, order := range orders {
				payments[key] = slices.Clone(order)
			}
			if tc.missing {
				payments["2"][0].Fact.SalesCompanyID = ""
			}
			var refunds map[int64][]practiceRefundMatch
			if tc.refundIssue {
				refunds = map[int64][]practiceRefundMatch{1: {{issue: "REFUND_AMOUNT_EXCEEDS_PAYMENT", event: paymentInput{ID: 4}}}}
			}
			draft, err := calculatePracticeDraft(members, paymentRule{ID: 1, Components: []component{scoped}},
				payments, nil, nil, practiceTierHistory{}, refunds, practiceDraftRuntime{})
			if err != nil || draft.Total != tc.want {
				t.Fatalf("scoped total %d, want %d, issues %+v, error %v", draft.Total, tc.want, draft.Issues, err)
			}
			if tc.want != 0 && (len(draft.Lines) != 1 || draft.Lines[0].FactID != 3) {
				t.Fatalf("only the in-scope payment should receive the award: %+v", draft.Lines)
			}
			var componentIssue string
			var hasRefundIssue bool
			for _, issue := range draft.Issues {
				if issue.Component == "period:delivery:0" {
					componentIssue = issue.Code
				}
				hasRefundIssue = hasRefundIssue || issue.Code == "REFUND_AMOUNT_EXCEEDS_PAYMENT"
			}
			if componentIssue != tc.wantComponent || hasRefundIssue != tc.refundIssue {
				t.Fatalf("unexpected issues %+v, wanted component issue %q, refund issue %t", draft.Issues, tc.wantComponent, tc.refundIssue)
			}
		})
	}
}

func TestPracticePeriodBenefitsRespectAppointmentsScopeAndWholeClassTiers(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "CLASS_PERIOD_BENEFITS", Name: "班次分润", PeriodMode: "class"}))
	if _, err := s.db.Exec(ctx, `INSERT INTO product_catalog_settings
	 (product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
	 VALUES($1,'40000000-0000-4000-8000-000000000005','test','PRACTICE','DCZD','test',
	 clock_timestamp(),decode(repeat('00',32),'hex'))`, product); err != nil {
		t.Fatal(err)
	}
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 1,
		StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: "PRACTICE:1:1:100"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "test"}))
	manager := id(s.createPerson(ctx, createPersonInput{Name: "负责人"}))
	const company = "40000000-0000-4000-8000-000000000001"
	const department = "40000000-0000-4000-8000-000000000002"
	const group = "40000000-0000-4000-8000-000000000003"
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,kind,name,status) VALUES
	  ($1,NULL,'COMPANY','试算公司','ACTIVE'),($2,$1,'DEPARTMENT','试算部门','ACTIVE'),($3,$2,'TEAM','试算小组','ACTIVE')`,
		company, department, group); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
	 VALUES('40000000-0000-4000-8000-000000000004',$1,$2,'MANAGER',$3)`, manager, group, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO configuration_options(group_key,option_key,label,display_order)
	 VALUES('practiceSalesCompany','1','指定销售公司',10)`); err != nil {
		t.Fatal(err)
	}
	def := map[string]any{"productId": product, "periodId": period, "label": "分润方案", "components": []any{
		map[string]any{"type": "manager", "mode": "period_rate", "value": 11000, "classMember": "new",
			"periodBenefit": map[string]any{"label": "小组负责人", "personId": manager, "appointmentUnitId": group, "appointmentKind": "MANAGER", "salesCompanies": []string{"1"}}},
		map[string]any{"type": "delivery", "mode": "period_rate", "classMember": "new",
			"periodBenefit": map[string]any{"label": "交付团队", "unitId": department, "tierOnWholeClass": true,
				"tiers": []any{map[string]int64{"from": 0, "through": 39999, "value": 500},
					map[string]int64{"from": 40000, "value": 2150}}}},
		map[string]any{"type": "delivery", "mode": "period_rate", "classMember": "retrain",
			"periodBenefit": map[string]any{"label": "复训零计提", "unitId": department}},
	}}
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var rule publishRuleInput
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatalf("could not publish the configured class components: %v", err)
	}
	var ruleID int64
	if err := s.db.QueryRow(ctx, "SELECT id FROM rule_versions WHERE product_id=$1", product).Scan(&ruleID); err != nil {
		t.Fatal(err)
	}
	id(s.createPerson(ctx, createPersonInput{Name: "未被规则引用的人"}))
	readVersionDirectory := func(productGrant int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/practice-rule-recipients?ruleId="+strconv.FormatInt(ruleID, 10), nil)
		req = req.WithContext(context.WithValue(req.Context(), financePrincipalKey{}, &financePrincipal{Rules: []financeRuleGrant{{ProductID: productGrant}}}))
		result := httptest.NewRecorder()
		s.practiceRuleRecipients(result, req)
		return result
	}
	allowed := readVersionDirectory(product)
	var directory struct {
		People []struct{ ID int64 }
		Units  []struct{ ID string }
	}
	if err := json.Unmarshal(allowed.Body.Bytes(), &directory); err != nil {
		t.Fatal(err)
	}
	if allowed.Code != http.StatusOK || len(directory.People) != 1 || directory.People[0].ID != manager || len(directory.Units) != 2 {
		t.Fatalf("rule read grant must return only referenced recipients: %d %s", allowed.Code, allowed.Body.String())
	}
	if denied := readVersionDirectory(product + 1); denied.Code != http.StatusNotFound {
		t.Fatalf("ungranted version must be hidden, got %d", denied.Code)
	}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "非班次计提", PeriodID: &period,
		Components: []component{{Type: "manager", Mode: "rate", Value: 500}}}); !errors.Is(err, invalid) {
		t.Fatalf("published a manager component the class calculation cannot use: %v", err)
	}
	for _, enabled := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/practice-rule-recipients?productId="+strconv.FormatInt(product, 10), nil)
		actor := financePrincipal{Rules: []financeRuleGrant{{ProductID: product, Publish: enabled}}}
		req = req.WithContext(context.WithValue(req.Context(), financePrincipalKey{}, &actor))
		result := httptest.NewRecorder()
		s.practiceRuleRecipients(result, req)
		want := http.StatusForbidden
		if enabled {
			want = http.StatusOK
		}
		if result.Code != want {
			t.Fatalf("read recipient choices with publish=%t: got %d, want %d", enabled, result.Code, want)
		}
	}

	start := at.AddDate(0, 0, 1)
	partIDs := []string{"31", "32"}
	both := "32"
	retrain := "33"
	wire := practiceRosterWire{ContractVersion: "dczd.practice-roster/1.0", SourceSystem: "DCZD", PeriodKey: "PRACTICE:1:1:100",
		ActivityType: 1, StrategyID: 1, ActivityID: 100, RosterVersion: 1, PeriodStartAt: start.Format(time.RFC3339),
		ConfirmedAt: start.Add(time.Hour).Format(time.RFC3339), ConfirmedBy: "100", Enrollments: []practiceEnrollmentWire{
			{SourceEnrollmentID: "21", MembershipType: 3, SignedAt: start.Format(time.RFC3339), ParticipantID: "10",
				SourceOrderID: &both, PaidOrderIDs: &partIDs, CommissionEligible: true},
			{SourceEnrollmentID: "22", MembershipType: 2, SignedAt: start.Format(time.RFC3339), ParticipantID: "11",
				SourceOrderID: &retrain, CommissionEligible: true},
		}}
	wire.RosterHash = practiceRosterHash(wire.Enrollments)
	roster, err := normalizePracticeRoster(wire)
	if err != nil {
		t.Fatal(err)
	}
	fact := func(order string, amount int64, when time.Time) sourceFact {
		return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "test", Kind: "ORDER_PAID", RecordKey: "payment:" + order,
			PaymentID: order, OrderID: order, Version: "1", AmountMinor: &amount, OccurredAt: when, UpdatedAt: when, SalesCompanyID: "1"}
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, []sourceFact{fact("31", 10001, at.Add(-time.Minute)), fact("32", 20002, at), fact("33", 10000, at)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	summary := result.(draftSummary)
	if summary.Total != 285 || summary.LineCount != 3 || summary.IssueCount != 0 {
		t.Fatalf("manager receives 220 from the on-appointment balance and delivery receives 65 from the new enrollment: %+v", summary)
	}
	for dimension, want := range map[string]int64{"person": 285, "company": 285, "department": 285} {
		var output struct{ Items []struct{ TotalMinor int64 } }
		err := s.db.QueryRow(ctx, draftReportSQL, period, summary.Version, dimension, 0).Scan(&output)
		var total int64
		for _, row := range output.Items {
			total += row.TotalMinor
		}
		if err != nil || total != want {
			t.Fatalf("%s report total=%d and error=%v", dimension, total, err)
		}
	}

	const appointmentID = "40000000-0000-4000-8000-000000000004"
	if _, err := s.db.Exec(ctx, "DELETE FROM organization_assignments WHERE id=$1", appointmentID); err != nil {
		t.Fatal(err)
	}
	result, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Issues []struct {
			ID, RuleVersion                 int64
			Code, Component, ComponentLabel string
		}
	}
	if err := s.db.QueryRow(ctx, draftDetailSQL, period, 0, 0, result.(draftSummary).Version, 0, 0).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Issues) != 1 || detail.Issues[0].Code != "PRACTICE_PERIOD_APPOINTMENT_MISSING" ||
		detail.Issues[0].ComponentLabel != "小组负责人" || detail.Issues[0].RuleVersion != 1 {
		t.Fatalf("unexpected saved practice issue: %+v", detail.Issues)
	}
	response := httptest.NewRecorder()
	params := "?section=issues&periodId=" + strconv.FormatInt(period, 10) + "&rowId=" + strconv.FormatInt(detail.Issues[0].ID, 10)
	s.getDraftRule(response, httptest.NewRequest(http.MethodGet, "/api/v1/drafts/rule"+params, nil))
	var applied struct {
		Component string
		Rule      struct {
			ID         int64
			Components []component
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &applied); err != nil || response.Code != http.StatusOK ||
		applied.Rule.ID != ruleID || applied.Component != "period:manager:0" ||
		len(applied.Rule.Components) != 3 || applied.Rule.Components[0].PeriodBenefit.Label != "小组负责人" {
		t.Fatalf("could not open the saved issue's arrangement: %d %s (%v)", response.Code, response.Body.String(), err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
	 VALUES($1,$2,$3,'MANAGER',$4)`, appointmentID, manager, group, at); err != nil {
		t.Fatal(err)
	}

	seller := id(s.createPerson(ctx, createPersonInput{Name: "成交销售"}))
	const nextDepartment = "40000000-0000-4000-8000-000000000006"
	const nextGroup = "40000000-0000-4000-8000-000000000007"
	const otherCompany = "40000000-0000-4000-8000-000000000009"
	const otherDepartment = "40000000-0000-4000-8000-000000000010"
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,kind,name,status) VALUES
	 ($1,$3,'DEPARTMENT','新部门','ACTIVE'),($2,$1,'TEAM','新小组','ACTIVE'),
	 ($4,NULL,'COMPANY','另一家公司','ACTIVE'),($5,$4,'DEPARTMENT','另一家销售部','ACTIVE')`,
		nextDepartment, nextGroup, company, otherCompany, otherDepartment); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,valid_to) VALUES
	 (gen_random_uuid(),$1,$2,'MEMBER',$4,$5),(gen_random_uuid(),$1,$3,'MEMBER',$5,NULL),
	 (gen_random_uuid(),$1,$6,'PRIMARY',$4,NULL)`, seller, group, nextGroup, at.Add(-time.Hour), at, otherDepartment); err != nil {
		t.Fatal(err)
	}
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: seller, Source: "DCZD", ExternalID: "10", ValidFrom: at.Add(-time.Hour).Format(time.RFC3339)}))
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
	 VALUES('DCZD','1',$1,$2)`, company, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	const category = "40000000-0000-4000-8000-000000000011"
	const otherCategory = "40000000-0000-4000-8000-000000000012"
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_sales_categories(id,name,active) VALUES
	 ($1,'适用类别',true),($2,'另一类别',true)`, category, otherCategory); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE organization_units SET sales_company_type='INTERNAL' WHERE id=$1", company); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from)
	 VALUES($1,$2,$3)`, company, category, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		payments := []sourceFact{fact("31", 10001, at.Add(-time.Minute)), fact("32", 20002, at)}
		for i := range payments {
			payments[i].Version = "2"
			payments[i].UpdatedAt = at.Add(time.Hour)
			payments[i].ReferrerID = "10"
		}
		_, err := storeSourceFacts(ctx, tx, payments)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := def["components"].([]any)
	line := parts[1].(map[string]any)
	line["sourceRole"] = "DIRECT_REFERRER"
	line["periodBenefit"].(map[string]any)["salesUnits"] = []string{department}
	line["periodBenefit"].(map[string]any)["salesCategoryIds"] = []string{category}
	raw, err = json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var scoped publishRuleInput
	if err := json.Unmarshal(raw, &scoped); err != nil {
		t.Fatal(err)
	}
	fixed := int64(1000)
	scoped.Components = append(scoped.Components, component{Type: "delivery", Mode: "period_rate", ClassMember: "new",
		PeriodBenefit: &practicePeriodBenefit{Label: "新报固定交付", UnitID: department, FixedMinor: &fixed}})
	bad := scoped.Components[len(scoped.Components)-1]
	bad.Value = 1000
	if !errors.Is(validateComponents([]component{bad}), invalid) {
		t.Fatal("fixed amount must exclude ratio and tier configuration")
	}
	classFixed := int64(2000)
	scoped.Components = append(scoped.Components, component{Type: "delivery", Mode: "period_rate",
		PeriodBenefit: &practicePeriodBenefit{Label: "整班固定交付", PersonID: manager, FixedMinor: &classFixed, FixedPerClass: true}})
	scoped.Components = append(scoped.Components, component{Type: "referral", Mode: "rate", ClassMember: "new",
		SourceRole: "DIRECT_REFERRER", Value: 500, ArrangementLabel: "小组推荐提成",
		SaleScope: &practiceSaleScope{SalesCategoryIDs: []string{category}, SalesUnits: []string{group}}})
	scoped.EditBaseID = &ruleID
	for _, invalidUnit := range []string{"40000000-0000-4000-8000-000000000008"} {
		benefit := scoped.Components[1].PeriodBenefit
		valid := benefit.SalesUnits
		benefit.SalesUnits = []string{invalidUnit}
		if _, err := s.publishRule(ctx, scoped); !errors.Is(err, invalid) {
			t.Fatalf("source scope accepted an unknown organization %q: %v", invalidUnit, err)
		}
		benefit.SalesUnits = valid
	}
	if _, err := s.publishRule(ctx, scoped); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, scoped); !errors.Is(err, errRuleEditStale) {
		t.Fatalf("a concurrent edit based on a superseded version was accepted: %v", err)
	}
	result, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	summary = result.(draftSummary)
	if summary.Total != 3742 || summary.LineCount != 8 || summary.IssueCount != 0 {
		t.Fatalf("class awards use the seller's payment-time team in the order company: %+v", summary)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, summary.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE organization_assignments SET unit_id=$2 WHERE person_id=$1 AND valid_to IS NOT NULL", seller, nextGroup); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE organization_sales_category_history SET category_id=$2 WHERE unit_id=$1", company, otherCategory); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		replay, _, err := readVerifiedPracticeReplay(ctx, tx, period)
		if err != nil {
			return err
		}
		beforeRefund, err := replay.calculate()
		if err != nil || beforeRefund.Total != 3742 {
			t.Fatalf("replay must use original scope after organization and category changes: %d %v", beforeRefund.Total, err)
		}
		deposit := replay.payments["31"][0].ID
		replay.refunds[deposit] = append(replay.refunds[deposit], practiceRefundMatch{event: paymentInput{Fact: fact("refund", 1000, at)}})
		afterRefund, err := replay.calculate()
		if err != nil || afterRefund.Total != 3592 {
			t.Fatalf("refund must reselect tier using the whole class and apply it to the frozen scope: %d %v", afterRefund.Total, err)
		}
		var tail int64
		for _, line := range afterRefund.Lines {
			if line.Component == "period:delivery:3" && line.FactID == replay.payments["32"][0].ID {
				tail = line.Amount
			}
		}
		if tail != 666 {
			t.Fatalf("the original fixed share of the unrefunded balance is 666 cents, got %d", tail)
		}
		balance := replay.payments["32"][0].ID
		replay.refunds[deposit] = append(replay.refunds[deposit], practiceRefundMatch{event: paymentInput{Fact: fact("rest", 9001, at)}})
		replay.refunds[balance] = append(replay.refunds[balance], practiceRefundMatch{event: paymentInput{Fact: fact("balance", 20002, at)}})
		afterFull, err := replay.calculate()
		if err != nil || afterFull.Total != 500 || len(afterFull.Lines) != 1 || afterFull.Lines[0].FactID != replay.payments["33"][0].ID {
			t.Fatalf("fully refunded new signup must leave the retrain payment's original whole-class share: %+v %v", afterFull, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
