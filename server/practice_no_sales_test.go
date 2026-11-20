package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const practiceNoReferrerPayment = `{
 "fact_schema_version":"source.order-fact/1.0","fact_type":"ORDER_PAID","occurred_at":"2026-06-07T02:00:00Z",
 "source_record":{"source_table":"dc_practice_total_order_t","record_id":"1100","record_version":"practice-registration/v2/%d","updated_at":"2026-06-07T02:00:00Z"},
 "order":{"source_order_id":"1100"},"amounts":{"paid_minor":"40000"},"payment":{"source_payment_id":"1100"},
 "business":{"source_domain_code":"PRACTICE","source_product_code":"PRACTICE:2:4","classification_fields":{
   "user_id":81,"strategy_id":4,"order_type":3,"activity_type":2,"registration_attribution_status":"PRACTICE_REGISTRATION_ATTRIBUTION_MISSING",
   "registration_match_count":1,"registration_id":1701,"registration_user_id":81,"registration_strategy_id":4,"retrain_no_sales_reason":"NO_REFERRER"
 }},"attribution_snapshot":{"participants":[{"source_role_code":"CUSTOMER","source_user_id":"81"}]}
}`

func TestPracticeNoReferrerRequiresValidSourceAndRosterMatch(t *testing.T) {
	raw := fmt.Sprintf(practiceNoReferrerPayment, int64(179000000000004))
	for _, tc := range []struct {
		old, replacement string
	}{
		{`practice-registration/v2/`, `practice-registration/v1/`},
		{`practice-registration/v2/`, ``},
		{`179000000000004`, `123`},
		{`"registration_match_count":1`, `"registration_match_count":2`},
		{`"registration_strategy_id":4`, `"registration_strategy_id":1`},
		{`"source_role_code":"CUSTOMER"`, `"source_role_code":"DIRECT_REFERRER"`},
		{`"source_user_id":"81"`, `"source_user_id":"82"`},
		{`"NO_REFERRER"`, `"COMPANY_DISABLED_BEFORE_PAYMENT"`},
	} {
		fact, err := normalizeFact("DCZD", "1100", json.RawMessage(strings.Replace(raw, tc.old, tc.replacement, 1)))
		if err != nil || fact.PracticeNoReferrer != nil {
			t.Fatalf("only a qualified source payment may declare no referrer for %s: %+v %v", tc.replacement, fact.PracticeNoReferrer, err)
		}
	}
	fact, err := normalizeFact("DCZD", "1100", json.RawMessage(raw))
	if err != nil || fact.PracticeNoReferrer == nil {
		t.Fatalf("expected a retained source no-referrer state, got %+v %v", fact.PracticeNoReferrer, err)
	}
	member := practiceEnrollment{PeriodKey: "PRACTICE:2:4:95", SourceEnrollmentID: "1701", ParticipantID: "81",
		MembershipType: 2, CommissionEligible: true, PaidOrderIDs: []string{"1100"}}
	if !fact.PracticeNoReferrer.matches(member) {
		t.Fatal("same signup should match")
	}
	member.PeriodKey = "PRACTICE:2:1:95"
	if fact.PracticeNoReferrer.matches(member) {
		t.Fatal("signup from another strategy must not match")
	}
}

func TestPracticeNoReferrerKeepsIndependentBenefitsAndOriginalRefund(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	p := id(s.createProduct(ctx, createProductInput{Code: "CLASS_WITHOUT_SALES", Name: "复训无推荐人验收", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: p, Source: "DCZD", Domain: "PRACTICE", SourceKey: "PRACTICE:2:4"}))
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: p, Name: "开班", Ordinal: 20, StartsOn: "2026-06-08", EndsOn: "2026-06-08", SourcePeriodKey: "PRACTICE:2:4:95"}))
	refundPeriod := id(s.createPeriod(ctx, createPeriodInput{ProductID: p, Name: "七月退款", SettlementMonth: "2026-07"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "交付负责人"}))
	if _, err := s.db.Exec(ctx, `INSERT INTO configuration_options(group_key,option_key,label,display_order)
	 VALUES('practiceSalesCompany','30','销售归属样本',10)`); err != nil {
		t.Fatal(err)
	}
	rule := publishRuleInput{ProductID: p, PeriodID: &period, Label: "收入与销售分别计算", Components: []component{
		{Type: "sales", Mode: "fixed", ClassMember: "retrain", SourceRole: "DIRECT_REFERRER", Value: 5000},
		{Type: "referral", Mode: "rate", ClassMember: "retrain", SourceRole: "DIRECT_REFERRER", Value: 500},
		{Type: "company_sales", Mode: "company_sales", ClassMember: "retrain", SourceRole: "DIRECT_REFERRER",
			CompanySales: &practiceCompanySales{Categories: []practiceCompanyCategory{{Companies: []string{"30"},
				practiceCompanyValue: practiceCompanyValue{Mode: "fixed", Value: 3000}}}}},
		{Type: "delivery", Mode: "period_rate", Value: 10000, PeriodBenefit: &practicePeriodBenefit{Label: "全班交付", PersonID: person}},
		{Type: "manager", Mode: "period_rate", Value: 10000, PeriodBenefit: &practicePeriodBenefit{Label: "下属报名", PersonID: person,
			RegistrationScope: &practiceRegistrationScope{Companies: []string{"30"}}}},
	}}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 6, 8, 10, 0, 0, 0, shanghai)
	roster := practiceRoster{Key: "PRACTICE:2:4:95", Hash: "no-referrer", SourceVersion: 1, StartsAt: at, ConfirmedAt: at.Add(time.Hour),
		Enrollments: []practiceEnrollment{{SourceEnrollmentID: "1701", ParticipantID: "81", MembershipType: 2, CommissionEligible: true,
			SignedAt: at, PaidOrderIDs: []string{"1100"}, ContentHash: strings.Repeat("a", 64)}}}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, roster)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	store := func(fact sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	revision := at.UnixMilli()*100 + 10
	raw := fmt.Sprintf(practiceNoReferrerPayment, revision+2)
	normalize := func(raw string) sourceFact {
		t.Helper()
		fact, err := normalizeFact("DCZD", "1100", json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		return fact
	}
	old := fmt.Sprintf(strings.Replace(practiceNoReferrerPayment, `"NO_REFERRER"`, `"COMPANY_DISABLED_BEFORE_PAYMENT"`, 1), revision+1)
	store(normalize(old))
	unconfirmed, err := s.calculateCurrentDraft(ctx, period, 0)
	if err != nil || unconfirmed.IssueCount == 0 || unconfirmed.Total != 400 {
		t.Fatalf("an unconfirmed source case must retain the sale issue and the independent benefit: %+v %v", unconfirmed, err)
	}
	store(normalize(raw))
	lateOlder := fmt.Sprintf(strings.NewReplacer(`"registration_id":1701`, `"registration_id":1709`,
		`"updated_at":"2026-06-07T02:00:00Z"`, `"updated_at":"2026-06-07T02:01:00Z"`).Replace(practiceNoReferrerPayment), revision)
	store(normalize(lateOlder))
	checked, err := s.calculateCurrentDraft(ctx, period, 0)
	if err != nil || checked.IssueCount != 0 || checked.Total != 400 || checked.LineCount != 1 {
		t.Fatalf("only the independent benefit should accrue on the newest matching payment: %+v %v", checked, err)
	}
	var paid, attributed int64
	if err := s.db.QueryRow(ctx, `SELECT sum((v.fact->>'amountMinor')::bigint),count(p.person_id) FROM calculation_draft_performance p
	 JOIN source_order_versions v ON v.id=p.source_version_id WHERE p.period_id=$1`, period).Scan(&paid, &attributed); err != nil || paid != 40000 || attributed != 0 {
		t.Fatalf("expected a 400 yuan payment without personal sales performance, got %d %d %v", paid, attributed, err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, checked.Version); err != nil {
		t.Fatal(err)
	}
	half, refundDate := int64(20000), at.AddDate(0, 1, 0)
	store(sourceFact{Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE", SourceKey: "PRACTICE:2:4", OrderID: "1100",
		RecordKey: "refund:1", Version: "1", RefundID: "1", AmountMinor: &half, OccurredAt: refundDate, UpdatedAt: refundDate,
		RefundsPayment: []sourcePaymentReference{{OrderID: "1100", RecordID: "1100"}}})
	result, err := s.calculateCurrentDraft(ctx, refundPeriod, 0)
	if err != nil || result.IssueCount != 0 || result.RefundTotal != -200 {
		t.Fatalf("later refunds should recover only the independent amount: %+v %v", result, err)
	}
}
