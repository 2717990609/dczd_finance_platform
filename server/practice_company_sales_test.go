package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
	payee := id(s.createPerson(ctx, createPersonInput{Name: "指定收款人"}))
	const delivery = "40000000-0000-4000-8000-000000000002"
	pool := id(s.createPracticePool(ctx, createPracticePoolInput{ProductID: product, UnitID: delivery, Name: "指定团队池"}))
	for _, test := range []struct {
		recipient        practiceCompanyPayee
		personID, poolID int64
		unitID           string
	}{
		{recipient: practiceCompanyPayee{Kind: "order_person", Role: "DIRECT_REFERRER"}, personID: person},
		{recipient: practiceCompanyPayee{Kind: "person", PersonID: payee}, personID: payee},
		{recipient: practiceCompanyPayee{Kind: "company", CompanyID: delivery}, unitID: delivery},
		{recipient: practiceCompanyPayee{Kind: "pool", Pool: &practicePoolTarget{PoolID: pool, OwnerID: payee}}, poolID: pool, unitID: delivery},
	} {
		rule.Components[1].CompanySales.Payee = &test.recipient
		if _, err := s.publishRule(ctx, rule); err != nil {
			t.Fatalf("publish %s recipient: %v", test.recipient.Kind, err)
		}
		raw, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil || raw.(draftSummary).Total != 15901 || raw.(draftSummary).IssueCount != 0 {
			t.Fatalf("%s must keep company rate and seller performance: %+v %v", test.recipient.Kind, raw, err)
		}
		var amount int64
		if err := s.db.QueryRow(ctx, `SELECT coalesce(sum(amount_minor),0) FROM calculation_draft_lines
		 WHERE period_id=$1 AND component_type='period:company_sales:1' AND coalesce(person_id,0)=$2
		  AND coalesce(practice_pool_id,0)=$3 AND coalesce(beneficiary_unit_id::text,'')=$4`,
			period, test.personID, test.poolID, test.unitID).Scan(&amount); err != nil || amount != 5001 {
			t.Fatalf("%s must receive both payment shares: got %d, %v", test.recipient.Kind, amount, err)
		}
	}
	rule.Components[1].CompanySales.Payee = nil
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
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
			{rosterID: 2, ruleKnown: true, companyOffers: &practiceCompanyOffers{companies: map[string]map[string]practiceCompanyAward{"new": {"partner": {}}}}},
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

func TestPracticeCompanyRecommendationUsesCurrentDirectoryAndSkipsKnownNoParent(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	const salesCompany = "40000000-0000-4000-8000-000000000001"
	const referrer = "40000000-0000-4000-8000-000000000002"
	const explicitReferrer = "40000000-0000-4000-8000-000000000003"
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_REFERRALS", Name: "实战班", PeriodMode: "class"}))
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 1, StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: "PRACTICE:1:1:1"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "fixture"}))
	seller := id(s.createPerson(ctx, createPersonInput{Name: "销售甲"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: seller, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-08-01T00:00:00Z"}))
	if _, err := s.db.Exec(ctx, `INSERT INTO configuration_options(group_key,option_key,label,display_order)
	 VALUES('practiceSalesCompany','direct','销售公司',10)`); err != nil {
		t.Fatal(err)
	}
	organizations := func(day string, referrals any) []byte {
		t.Helper()
		data, err := json.Marshal(map[string]any{"capturedAt": day, "people": []any{}, "assignments": []any{},
			"units": []map[string]string{
				{"id": salesCompany, "name": "销售公司", "kind": "COMPANY", "status": "ACTIVE"},
				{"id": referrer, "name": "推荐公司", "kind": "COMPANY", "status": "ACTIVE"},
				{"id": explicitReferrer, "name": "订单指定的推荐公司", "kind": "COMPANY", "status": "ACTIVE"},
			}, "companies": []map[string]string{
				{"source": "DCZD", "sourceCompanyId": "direct", "companyId": salesCompany, "from": "2026-01-01T00:00:00Z"},
				{"source": "DCZD", "sourceCompanyId": "explicit", "companyId": explicitReferrer, "from": "2026-01-01T00:00:00Z"},
			}, "recommendations": referrals})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	persistDirectory := func(day string, referrals any) {
		t.Helper()
		if err := storeOrganizationSnapshot(ctx, s.db, organizations(day, referrals)); err != nil {
			t.Fatal(err)
		}
	}
	persistDirectory("2026-08-01T00:00:00Z", []any{})
	var rule publishRuleInput
	err := json.Unmarshal([]byte(`{"label":"公司推荐分润","components":[{
	 "type":"company_sales","mode":"company_sales","classMember":"new","sourceRole":"DIRECT_REFERRER",
	 "companySales":{"categories":[{"label":"外部公司","companies":["direct"],"mode":"rate","value":400000,
	  "recommendationRate":40000,"overrides":[{"companyId":"direct","mode":"rate","value":500000,"recommendationRate":50000}]}]}}]}`), &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ProductID = product
	rule.Components[0].CompanySales.RecommendationRecipients = []string{referrer}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	storePayment := func(tx pgx.Tx, order, version, company string, amount int64, updated time.Time) error {
		fact := map[string]any{"fact_schema_version": sourceSchema, "fact_type": "ORDER_PAID", "occurred_at": at,
			"order": map[string]string{"source_order_id": order}, "business": map[string]string{"source_domain_code": "PRACTICE", "source_product_code": "fixture"},
			"source_record": map[string]any{"source_table": "payment", "record_id": order, "record_version": version, "updated_at": updated},
			"payment":       map[string]string{"source_payment_id": order}, "amounts": map[string]string{"paid_minor": fmt.Sprint(amount)},
		}
		parties := []sourceAttributionParticipant{{Role: "DIRECT_COMPANY", CompanyID: "direct"}, {Role: "DIRECT_REFERRER", UserID: "seller"}}
		if company != "" {
			parties = append(parties, sourceAttributionParticipant{Role: "UPSTREAM_COMPANY", CompanyID: company})
		}
		if company == "conflicting" {
			parties = append(parties, sourceAttributionParticipant{Role: "UPSTREAM_COMPANY", CompanyID: "explicit"})
		}
		fact["attribution_snapshot"] = map[string]any{"participants": parties}
		raw, err := json.Marshal(fact)
		if err != nil {
			return err
		}
		input, err := normalizeFact("DCZD", order, raw)
		if err != nil {
			return err
		}
		_, err = storeSourceFacts(ctx, tx, []sourceFact{input})
		return err
	}
	start := at.AddDate(0, 0, 1)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: "PRACTICE:1:1:1", Hash: "referral-roster", SourceVersion: 1, StartsAt: start, ConfirmedAt: start.Add(time.Hour),
			Enrollments: []practiceEnrollment{{SourceEnrollmentID: "1", ParticipantID: "11", MembershipType: 3, CommissionEligible: true,
				SignedAt: start, PaidOrderIDs: []string{"31", "32"}, ContentHash: "ab"}}})
		if err != nil {
			return err
		}
		if err := storePayment(tx, "31", "1", "", 4001, at); err != nil {
			return err
		}
		return storePayment(tx, "32", "1", "", 6009, at)
	}); err != nil {
		t.Fatal(err)
	}
	assertDraft := func(minor int64, issues, lines int) draftSummary {
		t.Helper()
		raw, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		got := raw.(draftSummary)
		if got.Total != minor || got.IssueCount != issues || got.LineCount != lines {
			t.Fatalf("wanted total %d, %d issues and %d lines, got %+v", minor, issues, lines, got)
		}
		return got
	}
	assertRecipient := func(want string) {
		t.Helper()
		var recipients []string
		var amounts []int64
		err := s.db.QueryRow(ctx, `SELECT array_agg(beneficiary_unit_id::text ORDER BY amount_minor),
		array_agg(amount_minor ORDER BY amount_minor) FROM calculation_draft_lines
		WHERE period_id=$1 AND component_type LIKE 'period:company_referral:%'`, period).Scan(&recipients, &amounts)
		if err != nil || !reflect.DeepEqual(recipients, []string{want, want}) || !reflect.DeepEqual(amounts, []int64{201, 300}) {
			t.Fatalf("expected %s with exact amounts 201 and 300: %v %v %v", want, recipients, amounts, err)
		}
	}
	assertDraft(5005, 1, 2)
	persistDirectory("2026-08-02T00:00:00Z", []map[string]any{{"companyId": salesCompany, "referrerCompanyId": referrer}})
	var queued bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)", period).Scan(&queued); err != nil || !queued {
		t.Fatalf("directory updates must enqueue the open draft: %t %v", queued, err)
	}
	current := assertDraft(5506, 0, 4)
	assertRecipient(referrer)
	if next := assertDraft(5506, 0, 4); next.Version != current.Version {
		t.Fatal("unchanged inputs replaced the draft")
	}
	rule.Components[0].CompanySales.Payee = &practiceCompanyPayee{Kind: "company", CompanyID: referrer}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	assertDraft(5506, 0, 4)
	assertRecipient(referrer)
	rule.Components[0].CompanySales.Payee = nil
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	if err := storeOrganizationSnapshot(ctx, s.db, organizations("2026-08-03T00:00:00Z", []map[string]any{{"companyId": salesCompany}})); err == nil {
		t.Fatal("an omitted referrer must not silently clear a known relationship")
	}
	persistDirectory("2026-08-03T00:00:00Z", []map[string]any{{"companyId": salesCompany, "referrerCompanyId": nil}})
	assertDraft(5005, 0, 2)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		return storePayment(tx, "31", "2", "explicit", 4001, at.Add(time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	assertDraft(5005, 1, 2)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// Older imports retained the order version without the company recommendation.
		return storePayment(tx, "32", "1", "explicit", 6009, at)
	}); err != nil {
		t.Fatal(err)
	}
	var versions int64
	if err := s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM source_order_versions WHERE order_id='32' AND kind='ORDER_PAID'),
	 EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, period).Scan(&versions, &queued); err != nil || versions != 1 || !queued {
		t.Fatalf("same-version attribution backfill must enqueue the draft without duplicating the order: %d %t %v", versions, queued, err)
	}
	assertDraft(5005, 1, 2)
	var issue string
	if err := s.db.QueryRow(ctx, "SELECT code FROM calculation_draft_issues WHERE period_id=$1", period).Scan(&issue); err != nil || issue != "PRACTICE_RECOMMENDATION_ROUTE_MISSING" {
		t.Fatalf("an active company without a configured direct route must remain pending: %s %v", issue, err)
	}
	rule.Components[0].CompanySales.RecommendationRecipients = []string{referrer, explicitReferrer}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	assertDraft(5506, 0, 4)
	assertRecipient(explicitReferrer)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		return storePayment(tx, "32", "2", "conflicting", 6009, at.Add(2*time.Hour))
	}); err != nil {
		t.Fatalf("ambiguous recommendation metadata must not block the payment: %v", err)
	}
	assertDraft(5005, 1, 2)
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

func TestPracticeSalesCategoryAdmitsNewCompanyAndKeepsOriginalRefundRate(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_CATEGORY", Name: "类别计提", PeriodMode: "class"}))
	class := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 1,
		StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: "PRACTICE:1:1:1"}))
	month := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月", SettlementMonth: "2026-09"}))
	nextMonth := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "十月", SettlementMonth: "2026-10"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "category"}))
	const first = "41000000-0000-4000-8000-000000000001"
	const second = "41000000-0000-4000-8000-000000000002"
	const company = "41000000-0000-4000-8000-000000000003"
	const internal = "41000000-0000-4000-8000-000000000004"
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO organization_sales_categories(id,name,active) VALUES ($1,'地区渠道',true),($2,'其他渠道',true)`, first, second)
	rule := publishRuleInput{ProductID: product, PeriodID: &class, Label: "按销售类别",
		Components: []component{{Type: "sales", Mode: "rate", Value: 1000, ClassMember: "new", SourceRole: "DIRECT_REFERRER"},
			{Type: "company_sales", Mode: "company_sales", ClassMember: "new", SourceRole: "DIRECT_REFERRER",
				CompanySales: &practiceCompanySales{Categories: []practiceCompanyCategory{
					{SalesCategoryID: first, practiceCompanyValue: practiceCompanyValue{"rate", 400000}},
					{SalesCategoryID: second, practiceCompanyValue: practiceCompanyValue{"rate", 100000}},
				}}}}}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	// Both the company and the person arrive after the rule has been published.
	seller := id(s.createPerson(ctx, createPersonInput{Name: "新销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: seller, Source: "DCZD", ExternalID: "new-seller", ValidFrom: "2026-08-01T00:00:00Z"}))
	mustExec(`INSERT INTO organization_units(id,name,kind,status,sales_company_type) VALUES($1,'新外部公司','COMPANY','ACTIVE','EXTERNAL')`, company)
	mustExec(`INSERT INTO organization_units(id,name,kind,status,sales_company_type) VALUES($1,'新内部公司','COMPANY','ACTIVE','INTERNAL')`, internal)
	mustExec(`INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
	 VALUES('DCZD','new-company',$1,'2026-08-01'),('DCZD','new-internal',$2,'2026-08-01')`, company, internal)
	mustExec(`INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from,valid_to)
	 VALUES($1,$2,'2026-08-01','2026-08-07'),($1,$3,'2026-08-07',NULL)`, company, first, second)
	pay := func(order, company string, day int) sourceFact {
		at := time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC)
		amount := int64(10000)
		if order == "31" {
			amount = 9999
		}
		return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "category", Kind: "ORDER_PAID", OrderID: order, PaymentID: order,
			RecordKey: "payment:" + order, Version: "1", SalesCompanyID: company, AmountMinor: &amount, OccurredAt: at, UpdatedAt: at,
			ReferrerID: "new-seller"}
	}
	start := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: "PRACTICE:1:1:1", Hash: "category", SourceVersion: 1, StartsAt: start, ConfirmedAt: start.Add(time.Hour),
			Enrollments: []practiceEnrollment{
				{SourceEnrollmentID: "1", ParticipantID: "11", MembershipType: 3, CommissionEligible: true, SignedAt: start, PaidOrderIDs: []string{"31"}, ContentHash: "ab"},
				{SourceEnrollmentID: "2", ParticipantID: "12", MembershipType: 3, CommissionEligible: true, SignedAt: start, PaidOrderIDs: []string{"32"}, ContentHash: "cd"},
				{SourceEnrollmentID: "3", ParticipantID: "13", MembershipType: 3, CommissionEligible: true, SignedAt: start, PaidOrderIDs: []string{"33"}, ContentHash: "ef"},
			}})
		if err != nil {
			return err
		}
		_, err = storeSourceFacts(ctx, tx, []sourceFact{pay("31", "new-company", 6), pay("32", "new-company", 7), pay("33", "new-internal", 7)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checkDraft := func(period int64, total int64, lines int) draftSummary {
		t.Helper()
		result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		summary := result.(draftSummary)
		if summary.Total != total || summary.IssueCount != 0 || summary.LineCount != lines {
			t.Fatalf("expected %d across %d lines, got %+v", total, lines, summary)
		}
		return summary
	}
	settled := checkDraft(class, 6000, 3)
	var earnings int64
	if err := s.db.QueryRow(ctx, `SELECT amount_minor FROM calculation_draft_lines WHERE period_id=$1 AND person_id=$2`,
		class, seller).Scan(&earnings); err != nil || earnings != 1000 {
		t.Fatalf("the new internal seller should receive the existing personal rate, got %d: %v", earnings, err)
	}
	mustExec(`INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES ($1,$2,'fixture')`, class, settled.Version)
	rule.Components[1].CompanySales.Payee = &practiceCompanyPayee{Kind: "person", PersonID: seller}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	mustExec(`UPDATE organization_sales_category_history SET category_id=$2 WHERE unit_id=$1`, company, second)
	refund := func(id string, at time.Time, amount int64) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Domain: "PRACTICE", SourceKey: "category", Kind: "REFUND_SUCCEEDED",
				OrderID: "31", RefundID: id, RecordKey: "refund:" + id, Version: "1", OccurredAt: at, UpdatedAt: at, AmountMinor: &amount,
				RefundsPayment: []sourcePaymentReference{{OrderID: "31", RecordID: "31"}}}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	refund("31", time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), 501)
	partial := checkDraft(month, -200, 0)
	if partial.RefundCount != 1 {
		t.Fatalf("expected one refund in September: %+v", partial)
	}
	var originalCompany string
	if err := s.db.QueryRow(ctx, `SELECT l.beneficiary_unit_id::text FROM calculation_draft_refunds r
	 JOIN calculation_draft_lines l ON l.id=r.original_line_id WHERE r.period_id=$1`, month).Scan(&originalCompany); err != nil || originalCompany != company {
		t.Fatalf("refund should reverse the original company, got %s: %v", originalCompany, err)
	}
	mustExec(`INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES ($1,$2,'fixture')`, month, partial.Version)
	refund("31-partial", time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), 125)
	if next := checkDraft(nextMonth, -50, 0); next.RefundCount != 1 {
		t.Fatalf("expected cumulative reversal of 250 cents, got %+v", next)
	}
	refund("31-complete", time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), 9373)
	if complete := checkDraft(nextMonth, -3800, 0); complete.RefundCount != 2 {
		t.Fatalf("full refund should recover the rest of the original company award: %+v", complete)
	}
}
func TestPracticeCompanyMultiSelectAndExceptionsUseOneStandard(t *testing.T) {
	const first = "41000000-0000-4000-8000-000000000001"
	const second = "41000000-0000-4000-8000-000000000002"
	var part component
	if err := json.Unmarshal([]byte(`{"type":"company_sales","mode":"company_sales","classMember":"new","sourceRole":"DIRECT_REFERRER",
	 "companySales":{"categories":[{"salesCategoryIds":["`+first+`","`+second+`"],"companies":[],"mode":"rate","value":100000,
	 "overrides":[{"companyIds":["special-1","special-2"],"mode":"rate","value":200000}]},
	 {"companies":["named-1","named-2"],"mode":"rate","value":300000}]}}`), &part); err != nil {
		t.Fatal(err)
	}
	if err := validateComponents([]component{part}); err != nil {
		t.Fatalf("grouped categories and unnamed company exceptions should publish: %v", err)
	}
	offers := practiceCompanyStandards(paymentRule{Components: []component{part}})
	for _, tc := range []struct {
		company, category string
		want              int64
	}{
		{"ordinary-1", first, 100000}, {"ordinary-2", second, 100000}, {"special-1", first, 200000},
		{"special-2", second, 200000}, {"named-1", first, 300000}, {"named-2", second, 300000},
	} {
		purchase := practicePurchase{member: practiceEnrollment{MembershipType: 3}, payments: []paymentInput{
			{Fact: sourceFact{PaymentID: "payment", SalesCompanyID: tc.company}},
		}}
		purchase.classifyCompany(offers, map[string]string{"payment": tc.category})
		if purchase.companyRoutingIssue != "" || purchase.companyAward == nil || purchase.companyAward.Value != tc.want {
			t.Fatalf("%s should use exactly %d, got %+v %q", tc.company, tc.want, purchase.companyAward, purchase.companyRoutingIssue)
		}
	}
}
