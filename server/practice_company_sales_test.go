package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeCompanySalesSeparatesSellerPerformanceAndCompanyProceeds(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "CLASS_COMPANY_PAYOUT", Name: "试算班", PeriodMode: "class"}))
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 1, StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: "PRACTICE:1:1:1"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "test"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "样本销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "source-seller", ValidFrom: "2026-08-01T00:00:00Z"}))
	_, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,name,kind,status) VALUES
	 ('40000000-0000-4000-8000-000000000001','先前收款公司','COMPANY','ACTIVE'),('40000000-0000-4000-8000-000000000002','当前收款公司','COMPANY','ACTIVE');
	 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from,valid_to) VALUES
	 ('DCZD','old-company','40000000-0000-4000-8000-000000000001','2026-01-01','2026-08-08T00:00Z'),
	 ('DCZD','old-company','40000000-0000-4000-8000-000000000002','2026-08-08T00:00Z',NULL);
	 INSERT INTO configuration_options(group_key,option_key,label,display_order) VALUES
	 ('practiceSalesCompany','old-company','外部样本公司',10),('practiceSalesCompany','internal-example','内部样本公司',20);`)
	if err != nil {
		t.Fatal(err)
	}
	ruleJSON := `{
	 "label":"八月规则","components":[
	  {"type":"sales","mode":"tiered","classMember":"new","sourceRole":"DIRECT_REFERRER",
	   "tiers":{"performance":[{"from":0,"through":15000,"mode":"rate","value":1000},{"from":15001,"mode":"rate","value":2000}]}},
	  {"type":"company_sales","mode":"company_sales","classMember":"new","sourceRole":"DIRECT_REFERRER",
	   "companySales":{"internalCompanies":["internal-example"],"categories":[{"label":"标准外部","companies":["old-company"],"mode":"rate","value":400000,
	    "overrides":[{"companyId":"old-company","mode":"rate","value":500000}]}]}},
	  {"type":"company_sales","mode":"company_sales","classMember":"retrain","sourceRole":"DIRECT_REFERRER",
	   "companySales":{"categories":[{"label":"外部复训","companies":["old-company"],"mode":"fixed","value":9900}]}}]}`
	var rule publishRuleInput
	if err := json.Unmarshal([]byte(ruleJSON), &rule); err != nil {
		t.Fatal(err)
	}
	rule.ProductID, rule.PeriodID = product, &period
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatalf("publish company rule: %v", err)
	}
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	pay := func(order, company string, amount int64) sourceFact {
		return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "test", Kind: "ORDER_PAID", OrderID: order, PaymentID: order,
			RecordKey: "payment:" + order, Version: "1", SalesCompanyID: company, AmountMinor: &amount, OccurredAt: at, UpdatedAt: at, ReferrerID: "source-seller"}
	}
	roster := practiceRoster{Key: "PRACTICE:1:1:1", Hash: "test-roster", SourceVersion: 1, StartsAt: at.AddDate(0, 0, 1), ConfirmedAt: at.AddDate(0, 0, 1).Add(time.Hour), Enrollments: []practiceEnrollment{
		{SourceEnrollmentID: "1", ParticipantID: "11", MembershipType: 3, CommissionEligible: true, SignedAt: at.AddDate(0, 0, 1), PaidOrderIDs: []string{"31", "32"}, ContentHash: "ab"},
		{SourceEnrollmentID: "2", ParticipantID: "12", MembershipType: 2, CommissionEligible: true, SignedAt: at.AddDate(0, 0, 1), PaidOrderIDs: []string{"33"}, ContentHash: "cd"},
		{SourceEnrollmentID: "3", ParticipantID: "13", MembershipType: 3, CommissionEligible: true, SignedAt: at.AddDate(0, 0, 1), PaidOrderIDs: []string{"34"}, ContentHash: "ef"},
	}}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, []sourceFact{pay("31", "old-company", 4001), pay("32", "old-company", 6000), pay("33", "old-company", 9900), pay("34", "internal-example", 10000)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	got := raw.(draftSummary)
	if got.Total != 15901 || got.IssueCount != 0 || got.LineCount != 4 {
		t.Fatalf("external new 5001 + external retrain 9900 + internal 1000 expected: %+v", got)
	}
	var corporate, personal, performance int64
	err = s.db.QueryRow(ctx, `SELECT
	 (SELECT sum(amount_minor) FROM calculation_draft_lines WHERE period_id=$1 AND beneficiary_unit_id='40000000-0000-4000-8000-000000000001'),
	 (SELECT sum(amount_minor) FROM calculation_draft_lines WHERE period_id=$1 AND person_id=$2),
	 (SELECT count(*) FROM calculation_draft_performance WHERE period_id=$1 AND person_id=$2)`, period, person).Scan(&corporate, &personal, &performance)
	if err != nil || corporate != 14901 || personal != 1000 || performance != 4 {
		t.Fatalf("company %d, person %d, seller facts %d, err %v", corporate, personal, performance, err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
	 VALUES('DCZD','old-company','40000000-0000-4000-8000-000000000002','2026-07-01')`); err != nil {
		t.Fatal(err)
	}
	raw, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	got = raw.(draftSummary)
	if got.Total != 1000 || got.LineCount != 1 || got.IssueCount != 2 {
		t.Fatalf("conflicting company assignments must hold the two company entitlements: %+v", got)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM organization_company_sources WHERE source_company_id='old-company' AND valid_from='2026-07-01'`); err != nil {
		t.Fatal(err)
	}
	amount := int64(100)
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{
			Source: "DCZD", Domain: "PRACTICE", SourceKey: "test", Kind: "REFUND_SUCCEEDED", OrderID: "33", RefundID: "1",
			Version: "1", RecordKey: "refund:1", OccurredAt: at.Add(time.Hour), UpdatedAt: at.Add(time.Hour), AmountMinor: &amount,
			RefundsPayment: []sourcePaymentReference{{OrderID: "33", RecordID: "33"}},
		}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	got = raw.(draftSummary)
	if got.Total != 6001 || got.IssueCount != 1 {
		t.Fatalf("external-category fixed amount exceeding net payment must be held for review: %+v", got)
	}
	later := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "下期", Ordinal: 2,
		StartsOn: "2026-09-08", EndsOn: "2026-09-09", SourcePeriodKey: "PRACTICE:1:1:2"}))
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &later, Label: "累计规则",
		Components: []component{{Type: "sales", ClassMember: "new", Mode: "tiered", SourceRole: "DIRECT_REFERRER",
			Tiers: &practiceSalesTiers{CountFromOrdinal: 1, Count: []practiceTier{{Mode: "rate", Value: 1000}}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	var queued bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)", later).Scan(&queued); err != nil || !queued {
		t.Fatalf("the later tiered draft must be queued after the earlier class rules change: %t %v", queued, err)
	}
}

func TestPracticePriorCompanyAndPersonalRolesFollowTheirOwnRuleVersion(t *testing.T) {
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	paid := int64(100)
	history := practiceTierHistory{
		periods: []practiceHistoryPeriod{
			{rosterID: 1, ruleKnown: true, personalRole: "LOCK_SALES_USER"},
			{rosterID: 2, ruleKnown: true, companyOffers: practiceCompanyOffers{"new": {"partner": {}}}},
			{rosterID: 3},
		},
		members: []practiceEnrollment{
			{ID: 1, RosterID: 1, SourceEnrollmentID: "1", MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"11"}},
			{ID: 2, RosterID: 2, SourceEnrollmentID: "2", MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"12"}},
			{ID: 3, RosterID: 3, SourceEnrollmentID: "3", MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"13"}},
		},
	}
	orders := make(map[string][]paymentInput)
	for _, m := range history.members {
		id := m.PaidOrderIDs[0]
		orders[id] = []paymentInput{{ID: m.ID, Fact: sourceFact{Source: "DCZD", OrderID: id, PaymentID: id, AmountMinor: &paid,
			OccurredAt: at, SalesCompanyID: "partner", ReferrerID: "recommended",
			Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "locked"}}}}}
	}
	identities := map[identityKey][]identityAssignment{
		{"DCZD", "locked"}:      {{ID: 1, PersonID: 1}},
		{"DCZD", "recommended"}: {{ID: 2, PersonID: 2}},
	}
	prior, err := indexPracticePrior(history, "DIRECT_REFERRER", orders, identities, nil, nil, true)
	if err != nil || prior.counts[1][1] != 1 || prior.counts[1][2] != 0 || len(prior.counts[2]) != 0 || !prior.incomplete[3] {
		t.Fatalf("historical company scope and sales roles changed: %+v %v", prior, err)
	}
}

func TestPracticeCorporateAndIndividualClaimsCannotPayTheSamePayment(t *testing.T) {
	paid := int64(100)
	rule := paymentRule{ID: 1, Components: []component{
		{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "DIRECT_REFERRER"},
		{Type: "company_sales", ClassMember: "new", Mode: "company_sales", SourceRole: "DIRECT_REFERRER",
			CompanySales: &practiceCompanySales{InternalCompanies: []string{"internal"}, Categories: []practiceCompanyCategory{
				{Label: "标准外部", Companies: []string{"partner"}, practiceCompanyValue: practiceCompanyValue{"rate", 400000}},
			}}},
	}}
	orders := map[string][]paymentInput{
		"31": {{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "31", PaymentID: "repeated", AmountMinor: &paid, ReferrerID: "seller", SalesCompanyID: "partner"}}},
		"32": {{ID: 2, Fact: sourceFact{Source: "DCZD", OrderID: "32", PaymentID: "repeated", AmountMinor: &paid, ReferrerID: "seller", SalesCompanyID: "internal"}}},
		"33": {{ID: 3, Fact: sourceFact{Source: "DCZD", OrderID: "33", PaymentID: "different", AmountMinor: &paid, ReferrerID: "seller", SalesCompanyID: "unclassified"}}},
	}
	members := []practiceEnrollment{
		{ID: 1, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"31"}},
		{ID: 2, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"32"}},
		{ID: 3, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"33"}},
	}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{ID: 1, PersonID: 1}}}
	draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, nil,
		practiceDraftRuntime{companyOffers: practiceCompanyStandards(rule)})
	if err != nil || len(draft.Lines) != 0 || len(draft.Issues) != 3 ||
		draft.Issues[0].Code != "PRACTICE_DUPLICATE_PERFORMANCE_PAYMENT" ||
		draft.Issues[1].Code != "PRACTICE_DUPLICATE_PERFORMANCE_PAYMENT" ||
		draft.Issues[2].Code != "PRACTICE_COMPANY_SCOPE_MISSING" {
		t.Fatalf("ambiguous payments must not become company or personal commissions: %+v %v", draft, err)
	}
}
