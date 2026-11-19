package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeEnrollmentAmountsTreatDepositAndBalanceAsOneEntitlement(t *testing.T) {
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	amount1, amount2 := int64(400), int64(600)
	makeOrder := func(id string, amount *int64) sourceFact {
		return sourceFact{Source: "DCZD", Kind: "ORDER_PAID", OrderID: id, PaymentID: id, OccurredAt: at, AmountMinor: amount, ReferrerID: "sales"}
	}
	rule := paymentRule{ID: 9, Components: []component{
		{Type: "sales", Mode: "fixed", SourceRole: "DIRECT_REFERRER", Value: 501, ClassMember: "new"},
		{Type: "sales", Mode: "fixed", SourceRole: "DIRECT_REFERRER", Value: 99, ClassMember: "retrain"},
	}}
	members := []practiceEnrollment{
		{ID: 11, SourceEnrollmentID: "1", MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"31", "32"}},
		{ID: 12, SourceEnrollmentID: "2", MembershipType: 2, CommissionEligible: true, PaidOrderIDs: []string{"33"}},
		{ID: 13, SourceEnrollmentID: "3", MembershipType: 2},
	}
	orders := map[string][]paymentInput{"31": {{ID: 1, Fact: makeOrder("31", &amount1)}}, "32": {{ID: 2, Fact: makeOrder("32", &amount2)}}, "33": {{ID: 3, Fact: makeOrder("33", &amount2)}}}
	identities := map[identityKey][]identityAssignment{{Source: "DCZD", ExternalID: "sales"}: {{ID: 17, PersonID: 24, ValidFrom: at.Add(-time.Hour)}}}
	result, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
	if err != nil || result.Total != 600 || len(result.Issues) != 0 || len(result.Lines) != 3 ||
		result.Lines[0].Amount != 200 || result.Lines[1].Amount != 301 || result.Lines[2].Amount != 99 || result.Lines[1].EnrollmentID != 11 {
		t.Fatalf("draft=%+v error=%v", result, err)
	}
}

func TestPracticeRosterKeepsLegacyNullAndTwoPaymentContract(t *testing.T) {
	payload := []byte(`{"contract_version":"dczd.practice-roster/1.0","source_system":"DCZD","period_key":"PRACTICE:1:1:17","period_start_at":"2026-08-01T09:00:00+08:00","strategy_id":1,"activity_type":1,"activity_id":17,"roster_version":2,"confirmed_at":"2026-08-02T18:00:00+08:00","confirmed_by":"1001","enrollments":[{"source_enrollment_id":"1","source_participant_id":"21","source_order_id":"31","membership_type":3,"commission_eligible":true,"signed_at":"2026-08-01T12:00:00+08:00"},{"source_enrollment_id":"2","source_participant_id":"22","source_order_id":null,"paid_order_ids":[],"membership_type":2,"commission_eligible":false,"signed_at":"2026-08-01T12:00:00+08:00"}]}`)
	var wire practiceRosterWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	wire.RosterHash = practiceRosterHash(wire.Enrollments)
	parsed, err := normalizePracticeRoster(wire)
	if err != nil || len(parsed.Enrollments) != 2 || len(parsed.Enrollments[0].PaidOrderIDs) != 1 ||
		parsed.Enrollments[0].PaidOrderIDs[0] != "31" || len(parsed.Enrollments[1].PaidOrderIDs) != 0 {
		t.Fatalf("parsed=%+v error=%v", parsed, err)
	}
	wire.Enrollments[0].SignedAt = "2026-08-01T12:01:00+08:00"
	if _, err := normalizePracticeRoster(wire); err == nil {
		t.Fatal("a changed roster was accepted under the old source hash")
	}
}

func TestPracticeDraftFollowsLatestRosterForDeferredEnrollment(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t)}
	mustID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	product := mustID(service.createProduct(ctx, createProductInput{Code: "CLASS", Name: "Practice", PeriodMode: "class"}))
	original := mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "Original", Ordinal: 1, StartsOn: "2020-08-01", EndsOn: "2020-08-02", SourcePeriodKey: "PRACTICE:1:1:10"}))
	target := mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "Target", Ordinal: 2, StartsOn: "2020-08-08", EndsOn: "2020-08-09", SourcePeriodKey: "PRACTICE:1:1:11"}))
	person := mustID(service.createPerson(ctx, createPersonInput{Name: "Sales"}))
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "sales", ValidFrom: "2020-01-01T00:00:00+08:00"}))

	targetStart := time.Date(2020, 8, 8, 1, 0, 0, 0, time.UTC)
	tooLate := targetStart.Add(time.Minute)
	ordinal := 2
	publish := func(label string, periodID *int64, fromOrdinal *int, from *time.Time, value int64) {
		t.Helper()
		_, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: label, PeriodID: periodID, FromOrdinal: fromOrdinal, EffectiveFrom: from,
			Components: []component{{Type: "sales", Mode: "fixed", Value: value, ClassMember: "new", SourceRole: "DIRECT_REFERRER"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	publish("Default", nil, nil, nil, 300)
	publish("Second onward", nil, &ordinal, nil, 400)
	publish("Target only", &target, nil, &targetStart, 501)
	publish("Starts after class", &target, nil, &tooLate, 900)
	for i, amount := range []int64{400, 600} {
		order := strconv.Itoa(31 + i)
		at := time.Date(2020, 7, 10+i, 0, 0, 0, 0, time.UTC)
		fact := sourceFact{Source: "DCZD", Kind: "ORDER_PAID", OrderID: order, PaymentID: order, Domain: "PRACTICE", SourceKey: "practice",
			RecordKey: order, Version: "1", AmountMinor: &amount, OccurredAt: at, UpdatedAt: at, ReferrerID: "sales"}
		if _, err := service.db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
			VALUES($1,$2,'',$3,'1',$4,$5,$6,$5)`, fact.Source, fact.OrderID, fact.RecordKey, fact.Kind, at, fact); err != nil {
			t.Fatal(err)
		}
	}

	roster := func(activity, version int64, start time.Time, paid bool) practiceRoster {
		t.Helper()
		signedAt := start.Add(time.Hour).Format(time.RFC3339)
		w := practiceRosterWire{ContractVersion: "dczd.practice-roster/1.0", SourceSystem: "DCZD", StrategyID: 1, ActivityType: 1, ActivityID: activity,
			PeriodKey: fmt.Sprintf("PRACTICE:1:1:%d", activity), RosterVersion: version, PeriodStartAt: start.Format(time.RFC3339),
			ConfirmedAt: start.Add(2 * time.Hour).Format(time.RFC3339), ConfirmedBy: "100"}
		if paid {
			orders := []string{"31", "32"}
			enrollmentID := strconv.FormatInt(activity-9, 10)
			w.Enrollments = append(w.Enrollments, practiceEnrollmentWire{SourceEnrollmentID: enrollmentID, ParticipantID: "21",
				SourceOrderID: &orders[1], PaidOrderIDs: &orders, MembershipType: 3, CommissionEligible: true, SignedAt: signedAt})
		}
		w.Enrollments = append(w.Enrollments, practiceEnrollmentWire{SourceEnrollmentID: "3", ParticipantID: "22", MembershipType: 2, SignedAt: signedAt})
		w.RosterHash = practiceRosterHash(w.Enrollments)
		result, err := normalizePracticeRoster(w)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	importRoster := func(input practiceRoster, want bool) {
		t.Helper()
		err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			inserted, err := storePracticeRoster(ctx, tx, input)
			if err == nil && inserted != want {
				return fmt.Errorf("imported=%t, want %t", inserted, want)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(period, version, sourceVersion, amount int64, issueCount int) {
		t.Helper()
		data, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		got := data.(draftSummary)
		if got.Version != version || got.PracticeRosterVersion != sourceVersion || got.Total != amount || got.IssueCount != issueCount {
			t.Fatalf("unexpected class draft: %+v", got)
		}
	}
	clearQueue := func() {
		t.Helper()
		if _, err := service.db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
			t.Fatal(err)
		}
	}
	checkBothQueued := func() {
		t.Helper()
		var pending int
		err := service.db.QueryRow(ctx, `SELECT count(*) FROM draft_refresh_queue WHERE period_id IN ($1,$2)`, original, target).Scan(&pending)
		if err != nil || pending != 2 {
			t.Fatalf("expected both affected classes marked for refresh, got %d: %v", pending, err)
		}
	}

	originalStart := targetStart.AddDate(0, 0, -7)
	old := roster(10, 1, originalStart, true)
	importRoster(old, true)
	check(original, 1, 1, 300, 0)
	clearQueue()
	importRoster(roster(11, 1, targetStart, true), true)
	checkBothQueued()
	check(original, 2, 1, 0, 1)
	check(target, 1, 1, 0, 1)
	clearQueue()
	importRoster(roster(10, 2, originalStart, false), true)
	checkBothQueued()
	importRoster(old, false)
	check(original, 3, 2, 0, 0)
	check(target, 2, 1, 501, 0)
	importRoster(roster(11, 2, targetStart, true), true)
	check(target, 2, 1, 501, 0)

	for period, expected := range map[int64]int64{original: 0, target: 1000} {
		var total int64
		err := service.db.QueryRow(ctx, `SELECT coalesce(sum((v.fact->>'amountMinor')::bigint),0)
		 FROM calculation_draft_performance a JOIN source_order_versions v ON v.id=a.source_version_id WHERE a.period_id=$1`, period).Scan(&total)
		if err != nil || total != expected {
			t.Fatalf("class performance=%d, want %d, error=%v", total, expected, err)
		}
	}

	var first, second int64
	err := service.db.QueryRow(ctx, `SELECT min(amount_minor),max(amount_minor) FROM calculation_draft_lines WHERE period_id=$1`, target).Scan(&first, &second)
	if err != nil || first != 200 || second != 301 {
		t.Fatalf("deposit=%d balance=%d error=%v", first, second, err)
	}
	var uniqueGifts int
	err = service.db.QueryRow(ctx, `SELECT count(*) FROM practice_enrollments WHERE period_key='PRACTICE:1:1:10' AND source_enrollment_id='3'`).Scan(&uniqueGifts)
	if err != nil || uniqueGifts != 1 {
		t.Fatalf("gift versions=%d error=%v", uniqueGifts, err)
	}
	if _, err := service.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	_, err = service.confirmSettlement(ctx, confirmSettlementInput{PeriodID: target, ExpectedVersion: 2}, "test")
	if !errors.Is(err, errPracticeSettlementUnavailable) {
		t.Fatalf("class settlement returned %v", err)
	}
}

func TestPracticeDraftUsesLatestPartialRefundForConfirmedEnrollment(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	mustID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	product := mustID(s.createProduct(ctx, createProductInput{Code: "CLASS_NET", Name: "课班", PeriodMode: "class"}))
	period := mustID(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 1,
		StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: "PRACTICE:1:1:1"}))
	mustID(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	person := mustID(s.createPerson(ctx, createPersonInput{Name: "销售"}))
	mustID(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-07-01T00:00:00+08:00"}))
	boundary := int64(900)
	_, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "签到分润", PeriodID: &period, Components: []component{
		{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER",
			Tiers: &practiceSalesTiers{Performance: []practiceTier{
				{Through: &boundary, Mode: "rate", Value: 1000}, {From: boundary + 1, Mode: "rate", Value: 2000},
			}}},
		{Type: "referral", Mode: "fixed", Value: 40, ClassMember: "new", SourceRole: "DIRECT_REFERRER"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	confirmed := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	start := confirmed.Add(-3 * time.Hour)
	payments := []string{"41", "42"}
	wire := practiceRosterWire{ContractVersion: "dczd.practice-roster/1.0", SourceSystem: "DCZD",
		PeriodKey: "PRACTICE:1:1:1", StrategyID: 1, ActivityType: 1, ActivityID: 1, RosterVersion: 1,
		PeriodStartAt: start.Format(time.RFC3339), ConfirmedAt: confirmed.Format(time.RFC3339), ConfirmedBy: "100",
		Enrollments: []practiceEnrollmentWire{{SourceEnrollmentID: "1", SourceOrderID: &payments[1], PaidOrderIDs: &payments,
			ParticipantID: "1", MembershipType: 3, CommissionEligible: true, SignedAt: start.Add(time.Hour).Format(time.RFC3339)}}}
	wire.RosterHash = practiceRosterHash(wire.Enrollments)
	roster, err := normalizePracticeRoster(wire)
	if err != nil {
		t.Fatal(err)
	}
	at := confirmed.AddDate(0, 0, -2)
	deposit, balance, old, partial, unrelated := int64(400), int64(600), int64(100), int64(200), int64(900)
	pay := func(order string, amount *int64) sourceFact {
		return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice", Kind: "ORDER_PAID",
			OrderID: order, PaymentID: order, Version: "1", RecordKey: "payment:" + order,
			OccurredAt: at, UpdatedAt: at, AmountMinor: amount, ReferrerID: "seller"}
	}
	refund := func(id, domain, version string, when time.Time, amount *int64) sourceFact {
		return sourceFact{Source: "DCZD", Domain: domain, SourceKey: "practice", Kind: "REFUND_SUCCEEDED",
			OrderID: "41", RefundID: id, Version: version, RecordKey: "refund:" + id, OccurredAt: when, UpdatedAt: when,
			AmountMinor: amount, RefundsPayment: []sourcePaymentReference{{OrderID: "41", RecordID: "41"}}}
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, []sourceFact{pay("41", &deposit), pay("42", &balance),
			refund("1", "PRACTICE", "1", at.Add(time.Hour), &old),
			refund("1", "PRACTICE", "2", at.Add(2*time.Hour), &partial),
			refund("2", "SHOP", "1", at.Add(time.Hour), &unrelated)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	run := func() draftSummary {
		t.Helper()
		out, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		return out.(draftSummary)
	}
	if got := run(); got.IssueCount != 0 || got.Total != 120 || got.LineCount != 4 {
		t.Fatalf("open draft should use net receipts 800 and retain fixed referral 40: %+v", got)
	}
	var principal, link, depositSales int64
	err = s.db.QueryRow(ctx, `SELECT sum(CASE WHEN a.payment_version_id IS NULL THEN 1 ELSE -1 END * (v.fact->>'amountMinor')::bigint),
	 count(*) FILTER(WHERE a.payment_version_id IS NOT NULL AND v.version='2')
	 FROM calculation_draft_performance a JOIN source_order_versions v ON v.id=a.source_version_id WHERE a.period_id=$1`, period).Scan(&principal, &link)
	if err != nil || principal != 800 || link != 1 {
		t.Fatalf("net performance=%d with latest refunded postings=%d, err=%v", principal, link, err)
	}
	err = s.db.QueryRow(ctx, `SELECT l.amount_minor FROM calculation_draft_lines l JOIN source_order_versions v ON v.id=l.source_version_id
	 WHERE l.period_id=$1 AND l.component_type='sales' AND v.order_id='41'`, period).Scan(&depositSales)
	if err != nil || depositSales != 20 {
		t.Fatalf("original deposit must receive its net share of 20, got %d, err=%v", depositSales, err)
	}
	storeRefund := func(f sourceFact) {
		t.Helper()
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{f})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	duplicate := refund("1", "PRACTICE", "1", at.Add(3*time.Hour), &partial)
	duplicate.OrderID, duplicate.RecordKey = "99", "refund:other"
	duplicate.RefundsPayment = []sourcePaymentReference{{OrderID: "99"}}
	if _, err := s.db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
		t.Fatal(err)
	}
	storeRefund(duplicate)
	var marked bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, period).Scan(&marked); err != nil || !marked {
		t.Fatalf("same refund id against a different order must enqueue this class: %t %v", marked, err)
	}
	if got := run(); got.IssueCount != 1 || got.Total != 0 {
		t.Fatalf("refund ID shared with a different order must be held for review: %+v", got)
	}
	duplicate.Version, duplicate.RefundID = "2", "5"
	duplicate.UpdatedAt = duplicate.UpdatedAt.Add(time.Minute)
	storeRefund(duplicate)
	if got := run(); got.IssueCount != 0 || got.Total != 120 {
		t.Fatalf("corrected duplicate identity must restore the calculation: %+v", got)
	}

	badLink := refund("6", "PRACTICE", "1", at.Add(3*time.Hour), &partial)
	badLink.RefundsPayment = []sourcePaymentReference{{OrderID: "99"}}
	storeRefund(badLink)
	if got := run(); got.IssueCount != 1 || got.Total != 0 {
		t.Fatalf("contradictory payment reference must be held for review: %+v", got)
	}
	badLink.OrderID, badLink.Version = "99", "2"
	badLink.UpdatedAt = badLink.UpdatedAt.Add(time.Minute)
	storeRefund(badLink)
	if got := run(); got.IssueCount != 0 || got.Total != 120 {
		t.Fatalf("latest refund record after order correction must supersede the old relationship: %+v", got)
	}

	late := int64(50)
	storeRefund(refund("3", "PRACTICE", "1", confirmed.AddDate(0, 1, 0), &late))
	if got := run(); got.IssueCount != 1 || got.Total != 120 {
		t.Fatalf("later refund should surface for adjustment and preserve the class result: %+v", got)
	}
	roster.SourceVersion, roster.ConfirmedAt = 2, confirmed.AddDate(0, 1, 1)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, roster)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := run(); got.IssueCount != 1 || got.Total != 120 {
		t.Fatalf("a roster revision must not bring a later refund back into the original class: %+v", got)
	}
	rest := int64(200)
	storeRefund(refund("4", "PRACTICE", "1", at.Add(3*time.Hour), &rest))
	if got := run(); got.IssueCount != 1 || got.Total != 0 {
		t.Fatalf("paid order fully refunded before confirmation requires roster review: %+v", got)
	}
	var issue string
	if err := s.db.QueryRow(ctx, `SELECT code FROM calculation_draft_issues WHERE period_id=$1`, period).Scan(&issue); err != nil || issue != "PRACTICE_REFUND_ROSTER_CHANGED" {
		t.Fatalf("incorrect fully refunded roster issue: %q %v", issue, err)
	}
}

func TestPracticeFixedNewSalesRefundRequiresConfiguredHandling(t *testing.T) {
	paid, refunded := int64(1000), int64(200)
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	p := practicePurchase{member: practiceEnrollment{MembershipType: 3, ConfirmedAt: at.Add(2 * time.Hour)},
		base: paid, payments: []paymentInput{{ID: 1, Fact: sourceFact{AmountMinor: &paid, OccurredAt: at}}}}
	p.applyRefunds(map[int64][]practiceRefundMatch{1: {{event: paymentInput{ID: 2, Fact: sourceFact{AmountMinor: &refunded, OccurredAt: at.Add(time.Hour)}}}}})
	for _, sample := range []struct {
		policy string
		amount int64
		issue  string
	}{
		{"", 0, "REFUND_POLICY_MISSING"},
		{"proportional", 160, ""},
		{"non_refundable", 200, ""},
		{"full_reversal", 0, ""},
	} {
		got, issue := p.commissionAmount(component{Type: "sales", Mode: "fixed", Value: 200, RefundPolicy: sample.policy})
		if got != sample.amount || issue != sample.issue {
			t.Errorf("%q returned %d and %q, want %d and %q", sample.policy, got, issue, sample.amount, sample.issue)
		}
	}
	got, issue := p.commissionAmount(component{Type: "sales", Mode: "rate", Value: 1000, RefundPolicy: "non_refundable"})
	if got != 80 || issue != "" {
		t.Errorf("proportional commission must use net paid amount, got %d %q", got, issue)
	}
}
