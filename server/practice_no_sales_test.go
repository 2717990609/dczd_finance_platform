package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		if err != nil || fact.PracticeNoReferrer != nil || fact.PracticeInactiveCompany != nil {
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
	old := fmt.Sprintf(strings.Replace(practiceNoReferrerPayment, `"NO_REFERRER"`, `"COMPANY_DISABLED_BEFORE_PAYMENT",
	 "inactive_sales_referrer_id":91,"inactive_sales_company_id":30,"inactive_sales_membership_id":1201`, 1), revision+1)
	marked := normalize(old)
	legacy := marked
	legacy.PracticeInactiveCompany = nil
	store(legacy)
	store(marked)
	store(marked)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{legacy})
		return err
	}); !errors.Is(err, conflict) {
		t.Fatalf("an existing diagnostic should not be erased by rereading the same version: %v", err)
	}
	var originalVersions int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM source_order_versions
	 WHERE source='DCZD' AND order_id='1100' AND kind='ORDER_PAID'`).Scan(&originalVersions); err != nil || originalVersions != 1 {
		t.Fatalf("rereading the same business version should retain one payment version: %d %v", originalVersions, err)
	}
	unconfirmed, err := s.calculateCurrentDraft(ctx, period, 0)
	if err != nil || unconfirmed.IssueCount == 0 || unconfirmed.Total != 400 {
		t.Fatalf("an unconfirmed source case must retain the sale issue and the independent benefit: %+v %v", unconfirmed, err)
	}
	var inactiveIssue bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calculation_draft_issues i
	 JOIN source_order_versions v ON v.id=i.source_version_id
	 WHERE i.period_id=$1 AND i.code='PRACTICE_SALES_COMPANY_INACTIVE' AND v.order_id='1100')`, period).
		Scan(&inactiveIssue); err != nil || !inactiveIssue {
		t.Fatalf("the inactive-company issue should open the exact payment version: %t %v", inactiveIssue, err)
	}
	store(normalize(raw))
	lateOlder := strings.NewReplacer(`"registration_id":1701`, `"registration_id":1709`,
		`"updated_at":"2026-06-07T02:00:00Z"`, `"updated_at":"2026-06-07T02:01:00Z"`).Replace(practiceNoReferrerPayment)
	store(normalize(fmt.Sprintf(lateOlder, revision)))
	checked, err := s.calculateCurrentDraft(ctx, period, 0)
	if err != nil || checked.IssueCount != 0 || checked.Total != 400 || checked.LineCount != 1 {
		t.Fatalf("only the independent benefit should accrue on the newest matching payment: %+v %v", checked, err)
	}
	var paid, attributed int64
	if err := s.db.QueryRow(ctx, `SELECT sum((v.fact->>'amountMinor')::bigint),count(p.person_id) FROM calculation_draft_performance p
	 JOIN source_order_versions v ON v.id=p.source_version_id WHERE p.period_id=$1`, period).Scan(&paid, &attributed); err != nil || paid != 40000 || attributed != 0 {
		t.Fatalf("expected a 400 yuan payment without personal sales performance, got %d %d %v", paid, attributed, err)
	}
	// The latest materialized revision can change the finances without advancing the business timestamp.
	revised := strings.Replace(practiceNoReferrerPayment, `"paid_minor":"40000"`, `"paid_minor":"45000"`, 1)
	latest := normalize(fmt.Sprintf(revised, revision+3))
	store(latest)
	t.Run("来源订单显示有效的最新版本", func(t *testing.T) {
		response := httptest.NewRecorder()
		s.orders(response, httptest.NewRequest(http.MethodGet, "/api/v1/orders?source=DCZD&orderId=1100", nil))
		var rows []sourceFact
		if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil || response.Code != http.StatusOK || len(rows) != 1 {
			t.Fatalf("expected one current payment from the source order view: %d %s %v", response.Code, response.Body, err)
		}
		if rows[0].Version != latest.Version || rows[0].AmountMinor == nil || *rows[0].AmountMinor != 45000 {
			t.Fatalf("the source order view must use the effective payment revision, got %s %v", rows[0].Version, rows[0].AmountMinor)
		}
	})
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(ctx, `SELECT total_minor,version FROM calculation_drafts WHERE period_id=$1`, period).
		Scan(&checked.Total, &checked.Version); err != nil || checked.Total != 450 {
		t.Fatalf("the new effective payment must refresh the existing draft: %+v %v", checked, err)
	}
	store(normalize(fmt.Sprintf(lateOlder, revision-1)))
	var queued bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, period).
		Scan(&queued); err != nil || queued {
		t.Fatalf("a later arrival of an obsolete revision must not queue the draft: %t %v", queued, err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, checked.Version); err != nil {
		t.Fatal(err)
	}
	half, refundDate := int64(22500), at.AddDate(0, 1, 0)
	store(sourceFact{Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE", SourceKey: "PRACTICE:2:4", OrderID: "1100",
		RecordKey: "refund:1", Version: "1", RefundID: "1", AmountMinor: &half, OccurredAt: refundDate, UpdatedAt: refundDate,
		RefundsPayment: []sourcePaymentReference{{OrderID: "1100", RecordID: "1100"}}})
	result, err := s.calculateCurrentDraft(ctx, refundPeriod, 0)
	if err != nil || result.IssueCount != 0 || result.RefundTotal != -225 {
		t.Fatalf("later refunds should recover only the independent amount: %+v %v", result, err)
	}

	now := time.Now().In(shanghai)
	month := now.Format("2006-01")
	if now.Day() >= 25 {
		month = now.AddDate(0, 0, 7).Format("2006-01")
	}
	current := id(s.createPeriod(ctx, createPeriodInput{ProductID: p, Name: "当前办理期", SettlementMonth: month}))
	if _, err := s.calculateCurrentDraft(ctx, current, 0); err != nil {
		t.Fatal(err)
	}
	// The source may withdraw its confirmed exception without yet providing an attribution.
	withdrawn := normalize(fmt.Sprintf(strings.Replace(revised, `,"retrain_no_sales_reason":"NO_REFERRER"`, "", 1), revision+4))
	store(withdrawn)
	replayPending := func(version string) bool {
		t.Helper()
		if err := s.refreshPendingDrafts(ctx); err != nil {
			t.Fatal(err)
		}
		var pending bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calculation_draft_issues i
		 JOIN source_order_versions v ON v.id=i.source_version_id
		 WHERE i.period_id=$1 AND i.code='PRACTICE_CORRECTION_REPLAY_REQUIRED' AND v.order_id='1100' AND v.version=$2 AND NOT i.advisory)`,
			current, version).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		return pending
	}
	if !replayPending(withdrawn.Version) {
		t.Fatal("a withdrawn source exception must flag the payment for review in the current draft")
	}
	store(normalize(fmt.Sprintf(revised, revision+5)))
	if replayPending(withdrawn.Version) {
		t.Fatal("a restored source exception must clear the review on the next recalculation")
	}
	var issues, corrections int
	if err := s.db.QueryRow(ctx, `SELECT d.issue_count,(SELECT count(*) FROM calculation_draft_corrections c WHERE c.period_id=d.period_id)
	 FROM calculation_drafts d WHERE d.period_id=$1`, current).Scan(&issues, &corrections); err != nil || issues != 0 || corrections != 0 {
		t.Fatalf("restoring the settled attribution must leave the current draft clear: %d issues, %d corrections, %v", issues, corrections, err)
	}
	if err := s.db.QueryRow(ctx, `SELECT d.total_minor,d.version FROM calculation_drafts d
	 JOIN settlements s USING(period_id) WHERE s.period_id=$1`, period).Scan(&result.Total, &result.Version); err != nil ||
		result.Total != 450 || result.Version != checked.Version {
		t.Fatalf("the source correction must leave the original settlement intact: %+v %v", result, err)
	}
}
