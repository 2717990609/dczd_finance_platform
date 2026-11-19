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
	result, err := calculatePracticeDraft(members, rule, orders, identities, nil)
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
			w.Enrollments = append(w.Enrollments, practiceEnrollmentWire{SourceEnrollmentID: "1", ParticipantID: "21",
				SourceOrderID: &orders[1], PaidOrderIDs: &orders, MembershipType: 3, CommissionEligible: true, SignedAt: signedAt})
		}
		w.Enrollments = append(w.Enrollments, practiceEnrollmentWire{SourceEnrollmentID: "2", ParticipantID: "22", MembershipType: 2, SignedAt: signedAt})
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

	originalStart := targetStart.AddDate(0, 0, -7)
	old := roster(10, 1, originalStart, true)
	importRoster(old, true)
	check(original, 1, 1, 300, 0)
	importRoster(roster(11, 1, targetStart, true), true)
	check(original, 2, 1, 0, 1)
	check(target, 1, 1, 0, 1)
	importRoster(roster(10, 2, originalStart, false), true)
	importRoster(old, false)
	check(original, 3, 2, 0, 0)
	check(target, 2, 1, 501, 0)
	importRoster(roster(11, 2, targetStart, true), true)
	check(target, 2, 1, 501, 0)

	var first, second int64
	err := service.db.QueryRow(ctx, `SELECT min(amount_minor),max(amount_minor) FROM calculation_draft_lines WHERE period_id=$1`, target).Scan(&first, &second)
	if err != nil || first != 200 || second != 301 {
		t.Fatalf("deposit=%d balance=%d error=%v", first, second, err)
	}
	var uniqueGifts int
	err = service.db.QueryRow(ctx, `SELECT count(*) FROM practice_enrollments WHERE period_key='PRACTICE:1:1:10' AND source_enrollment_id='2'`).Scan(&uniqueGifts)
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
