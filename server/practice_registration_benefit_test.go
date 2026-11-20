package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeRegistrationBenefitMatchesConfirmedSignupAndKeepsRefundPayee(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	createID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	product := createID(s.createProduct(ctx, createProductInput{Code: "REGISTRATION_CLASS", Name: "家庭教育验收", PeriodMode: "class"}))
	createID(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "PRACTICE:2:4"}))
	const key = "PRACTICE:2:4:95"
	period := createID(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "第20届", Ordinal: 20,
		StartsOn: "2026-06-08", EndsOn: "2026-06-08", SourcePeriodKey: key}))
	refundPeriod := createID(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "七月退款", SettlementMonth: "2026-07"}))
	person := createID(s.createPerson(ctx, createPersonInput{Name: "个人收款方"}))
	const unit = "43000000-0000-4000-8000-000000000001"
	exec(`INSERT INTO organization_units(id,kind,name,status) VALUES($1,'COMPANY','公司收款方','ACTIVE')`, unit)
	exec(`INSERT INTO configuration_options(group_key,option_key,label,display_order) VALUES
	 ('practiceSalesCompany','30','本人公司甲',10),('practiceSalesCompany','31','下属公司甲',20),
	 ('practiceSalesCompany','32','本人公司乙',30),('practiceSalesCompany','33','下属公司乙',40)`)
	raw := fmt.Sprintf(`[
	 {"type":"manager","mode":"period_rate","value":30000,"periodBenefit":{"label":"甲总监","personId":%d,
	  "registrationScope":{"sellerId":"481","sellerCompanyId":"30","companies":["31"]}}},
	 {"type":"manager","mode":"period_rate","value":30000,"periodBenefit":{"label":"乙总监","unitId":%q,
	  "registrationScope":{"sellerId":"581","sellerCompanyId":"32","companies":["33"]}}}
	]`, person, unit)
	rule := publishRuleInput{ProductID: product, PeriodID: &period, Label: "按报名归属"}
	if err := json.Unmarshal([]byte(raw), &rule.Components); err != nil {
		t.Fatal(err)
	}
	overlapping := append(append([]component(nil), rule.Components...), rule.Components[0])
	if err := validateComponents(overlapping); !errors.Is(err, errPracticeRegistrationOverlap) {
		t.Fatalf("repeated signup scope should be rejected, got %v", err)
	}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 6, 8, 9, 38, 0, 0, shanghai)
	roster := practiceRoster{Key: key, Hash: "registration-class", SourceVersion: 1, StartsAt: at, ConfirmedAt: at.Add(time.Hour)}
	var facts []sourceFact
	for i, tc := range []struct {
		order, enrollment, seller, company, sourcePeriod string
		kind                                             int
		paid                                             int64
	}{
		{"1001", "701", "481", "30", key, 3, 580000},
		{"1002", "702", "499", "31", "", 2, 40000},
		{"1003", "703", "499", "30", key, 3, 580000},
		{"1004", "704", "581", "32", key, 3, 580000},
		{"1005", "705", "599", "33", key, 2, 40000},
	} {
		roster.Enrollments = append(roster.Enrollments, practiceEnrollment{SourceEnrollmentID: tc.enrollment,
			ParticipantID: tc.enrollment, MembershipType: tc.kind, CommissionEligible: true, PaidOrderIDs: []string{tc.order}, SignedAt: at, ContentHash: fmt.Sprintf("%064x", i+1)})
		facts = append(facts, sourceFact{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: "PRACTICE:2:4",
			OrderID: tc.order, PaymentID: tc.order, RecordKey: "paid:" + tc.order, Version: "1", AmountMinor: &tc.paid,
			OccurredAt: at.Add(-time.Hour), UpdatedAt: at, SalesCompanyID: "90", ReferrerID: "999"})
		signup := sourceFact{Source: "DCZD", Kind: "DELIVERY_COMPLETED", Domain: "PRACTICE", SourceKey: "PRACTICE:2:4",
			OrderID: tc.order, RecordKey: "dc_offline_order_log_t:" + tc.enrollment, Version: "1",
			OccurredAt: at, UpdatedAt: at, ReferrerID: tc.seller,
			PracticeRegistration: &sourcePracticeRegistration{CompanyID: tc.company, PeriodKey: tc.sourcePeriod}}
		if i == 1 {
			signup.OccurredAt = at.AddDate(0, 2, 0)
		}
		facts = append(facts, signup)
	}
	save := func(items []sourceFact, withRoster bool) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if withRoster {
				if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
					return err
				}
			}
			_, err := storeSourceFacts(ctx, tx, items)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	save(facts, true)
	preview, err := s.calculateCurrentDraft(ctx, period, 0)
	if err != nil || preview.IssueCount != 2 || preview.Total != 0 {
		t.Fatalf("different legacy sign time must prevent guessing a registration, got %+v %v", preview, err)
	}
	fix := facts[3]
	fix.Version, fix.OccurredAt, fix.UpdatedAt = "2", at, at.Add(time.Hour)
	save([]sourceFact{fix}, false)
	var queued bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, period).Scan(&queued); err != nil || !queued {
		t.Fatalf("receiving a corrected attendance event should queue the affected draft: %v %v", queued, err)
	}
	preview, err = s.calculateCurrentDraft(ctx, period, 0)
	if err != nil || preview.IssueCount != 0 || preview.Total != 37200 || preview.LineCount != 4 {
		t.Fatalf("personal and subordinate-company sales must calculate once per recipient, got %+v %v", preview, err)
	}
	var own, corporate int64
	if err := s.db.QueryRow(ctx, `SELECT sum(amount_minor) FILTER(WHERE person_id=$2),
	 sum(amount_minor) FILTER(WHERE beneficiary_unit_id=$3) FROM calculation_draft_lines WHERE period_id=$1`,
		period, person, unit).Scan(&own, &corporate); err != nil || own != 18600 || corporate != 18600 {
		t.Fatalf("expected different recipients with 18600 each, got %d %d %v", own, corporate, err)
	}
	exec(`INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, preview.Version)
	if _, err := s.db.Exec(ctx, `UPDATE source_order_versions SET practice_registration='{}'
	 WHERE source='DCZD' AND kind='DELIVERY_COMPLETED' AND order_id='1001' AND version='1'`); err == nil {
		t.Fatal("confirmed attendance input can no longer be modified")
	}
	changed := facts[1]
	changed.Version, changed.UpdatedAt, changed.ReferrerID = "2", at.Add(2*time.Hour), "581"
	changed.PracticeRegistration = &sourcePracticeRegistration{CompanyID: "32", PeriodKey: key}
	amount := int64(100000)
	refundedAt := time.Date(2026, 7, 9, 9, 0, 0, 0, shanghai)
	save([]sourceFact{changed, {Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE", SourceKey: "PRACTICE:2:4",
		OrderID: "1001", RecordKey: "refund:1", Version: "1", RefundID: "1", AmountMinor: &amount,
		RefundsPayment: []sourcePaymentReference{{OrderID: "1001", RecordID: "1001"}}, OccurredAt: refundedAt, UpdatedAt: refundedAt}}, false)
	refund, err := s.calculateCurrentDraft(ctx, refundPeriod, 0)
	if err != nil || refund.IssueCount != 0 || refund.RefundTotal != -3000 {
		t.Fatalf("later refund must use the original signup attribution, got %+v %v", refund, err)
	}
	var refundedPerson int64
	if err := s.db.QueryRow(ctx, `SELECT l.person_id FROM calculation_draft_refunds r
	 JOIN calculation_draft_lines l ON l.id=r.original_line_id WHERE r.period_id=$1`, refundPeriod).
		Scan(&refundedPerson); err != nil || refundedPerson != person {
		t.Fatalf("expected the original person to have the refund, got %d %v", refundedPerson, err)
	}
}
