package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPracticeNamedRecommendationPayee(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_REFERRAL_NAME", Name: "推荐计提样本", PeriodMode: "class"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "指定收款人"}))
	seller := id(s.createPerson(ctx, createPersonInput{Name: "订单上的推荐人"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "reference"}))
	const rosterKey = "PRACTICE:1:1:61"
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "推荐验收", Ordinal: 61,
		StartsOn: "2026-08-08", EndsOn: "2026-08-08", SourcePeriodKey: rosterKey}))
	refundMonth := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "退款账期", SettlementMonth: "2026-09"}))
	part := component{Type: "referral", Mode: "rate", ClassMember: "new", SourceRole: "DIRECT_REFERRER", Value: 1000,
		ArrangementLabel: "渠道推荐服务", SalesPersonID: person, RecipientDuty: "PRACTICE_RESOURCE_RECOMMENDATION"}
	input := publishRuleInput{ProductID: product, PeriodID: &period, Label: "推荐规则", Components: []component{part,
		{Type: "manager", Mode: "period_rate", Value: 100000, PeriodBenefit: &practicePeriodBenefit{Label: "订单人员提成", OrderRecipientRole: "DIRECT_REFERRER"}},
		{Type: "manager", Mode: "period_rate", Value: 50000, PeriodBenefit: &practicePeriodBenefit{Label: "所属公司提成", SalesCompanyRecipient: true}},
	}}
	input.Components[0].RecipientDuty = "UNREGISTERED_DUTY"
	if _, err := s.publishRule(ctx, input); !errors.Is(err, invalid) {
		t.Fatalf("expected publication to reject an unregistered duty: %v", err)
	}
	input.Components[0] = part
	if _, err := s.publishRule(ctx, input); err != nil {
		t.Fatal(err)
	}
	var saved []component
	if err := s.db.QueryRow(ctx, `SELECT components FROM rule_versions WHERE product_id=$1`, input.ProductID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved[0].ArrangementLabel != part.ArrangementLabel || saved[0].RecipientDuty != part.RecipientDuty {
		t.Fatalf("rule must persist the configured label and duty: %+v", saved[0])
	}
	const company = "41000000-0000-4000-8000-000000000211"
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, shanghai)
	paidAt := at.Add(-time.Hour)
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: seller, Source: "DCZD", ExternalID: "10", ValidFrom: paidAt.Format(time.RFC3339)}))
	deposit, balance := int64(4000), int64(6000)
	paid := func(order string, amount *int64) sourceFact {
		return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "reference", Kind: "ORDER_PAID", Version: "1",
			OrderID: order, PaymentID: order, RecordKey: "payment:" + order, ReferrerID: "10", SalesCompanyID: "partner",
			OccurredAt: paidAt, UpdatedAt: paidAt, AmountMinor: amount}
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO organization_units(id,name,kind,status) VALUES($1,'样本公司','COMPANY','ACTIVE')`, company); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
			VALUES('DCZD','partner',$1,$2)`, company, paidAt); err != nil {
			return err
		}
		if _, err := storePracticeRoster(ctx, tx, practiceRoster{Key: rosterKey, Hash: "reference", SourceVersion: 1,
			StartsAt: at, ConfirmedAt: at.Add(time.Hour), Enrollments: []practiceEnrollment{{SourceEnrollmentID: "1", ParticipantID: "1",
				MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"1", "2"}, SignedAt: at, ContentHash: "aa"}}}); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, []sourceFact{paid("1", &deposit), paid("2", &balance)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	draft, err := s.calculateCurrentDraft(ctx, period, 0)
	if err != nil || draft.IssueCount != 0 || draft.Total != 2500 || draft.LineCount != 6 {
		t.Fatalf("all selected recipients should earn their separate shares: %+v %v", draft, err)
	}
	var recipients string
	if err := s.db.QueryRow(ctx, `SELECT string_agg(coalesce(person_id::text,beneficiary_unit_id::text),',' ORDER BY component_type)
	 FROM calculation_draft_lines WHERE period_id=$1 AND base_minor=4000`, period).Scan(&recipients); err != nil ||
		recipients != fmt.Sprintf("%d,%s,%d", seller, company, person) {
		t.Fatalf("wrong recipients for the original deposit: %s %v", recipients, err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, draft.Version); err != nil {
		t.Fatal(err)
	}
	// End the source company link. Refunds still use the recipient frozen with the deposit.
	refundAt := time.Date(2026, 9, 5, 9, 0, 0, 0, shanghai)
	if _, err := s.db.Exec(ctx, `UPDATE organization_company_sources SET valid_to=$1 WHERE source_company_id='partner'`, refundAt); err != nil {
		t.Fatal(err)
	}
	refundAmount := int64(2000)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Domain: "PRACTICE", SourceKey: "reference", Kind: "REFUND_SUCCEEDED", Version: "1",
			OrderID: "1", RefundID: "1", RecordKey: "refund:1", OccurredAt: refundAt, UpdatedAt: refundAt, AmountMinor: &refundAmount,
			RefundsPayment: []sourcePaymentReference{{OrderID: "1", RecordID: "1"}}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	refund, err := s.calculateCurrentDraft(ctx, refundMonth, 0)
	if err != nil || refund.IssueCount != 0 || refund.Total != -500 {
		t.Fatalf("the refund should debit 200, 200 and 100 against the original three recipients: %+v %v", refund, err)
	}
	var deductions string
	if err := s.db.QueryRow(ctx, `SELECT string_agg(coalesce(l.person_id::text,l.beneficiary_unit_id::text)||':'||r.amount_minor,',' ORDER BY l.component_type)
	 FROM calculation_draft_refunds r JOIN calculation_draft_lines l ON l.id=r.original_line_id WHERE r.period_id=$1`, refundMonth).Scan(&deductions); err != nil ||
		deductions != fmt.Sprintf("%d:-200,%s:-100,%d:-200", seller, company, person) {
		t.Fatalf("refund must refer to the original recipients and shares: %s %v", deductions, err)
	}
}

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
	check(target, 2, 2, 501, 0)
	var rosterVersion int64
	if err := service.db.QueryRow(ctx, `SELECT r.source_version FROM calculation_drafts d
	 JOIN practice_rosters r ON r.id=d.practice_roster_id WHERE d.period_id=$1`, target).Scan(&rosterVersion); err != nil || rosterVersion != 2 {
		t.Fatalf("equivalent draft must stay attached to the latest confirmed roster: %d %v", rosterVersion, err)
	}
	reviewResponse := httptest.NewRecorder()
	service.getSettlementReview(reviewResponse, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=2", target), nil))
	var review struct {
		PracticeRoster *struct {
			Version, LatestVersion, Attendance int64
		}
		Sources []settlementSource
		Token   string `json:"reviewToken"`
	}
	if err := json.Unmarshal(reviewResponse.Body.Bytes(), &review); reviewResponse.Code != http.StatusOK || err != nil ||
		review.PracticeRoster == nil || review.PracticeRoster.Version != 2 || review.PracticeRoster.LatestVersion != 2 ||
		review.Token != "" || len(review.Sources) != 1 || review.Sources[0].Status != "NOT_CONFIGURED" {
		t.Fatalf("class review must show the selected roster and the missing order data prerequisite: %d %s %v",
			reviewResponse.Code, reviewResponse.Body.String(), err)
	}

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

func TestPracticeDeferredEnrollmentKeepsBookedPaymentInOriginalClass(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "DEFERRED", Name: "Practice", PeriodMode: "class"}))
	original := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "Original", Ordinal: 1,
		StartsOn: "2020-08-01", EndsOn: "2020-08-02", SourcePeriodKey: "PRACTICE:1:1:10"}))
	target := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "Target", Ordinal: 2,
		StartsOn: "2020-08-08", EndsOn: "2020-08-09", SourcePeriodKey: "PRACTICE:1:1:11"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "Sales"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "sales", ValidFrom: "2020-01-01T00:00:00+08:00"}))
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Sales rule",
		Components: []component{{Type: "sales", Mode: "fixed", Value: 300, ClassMember: "new", SourceRole: "DIRECT_REFERRER"}}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2020, 8, 1, 1, 0, 0, 0, time.UTC)
	paidAt, amount := start.Add(-time.Hour), int64(1000)
	paid := sourceFact{Source: "DCZD", Kind: "ORDER_PAID", OrderID: "31", PaymentID: "31", Domain: "PRACTICE", SourceKey: "practice",
		RecordKey: "payment:31", Version: "1", AmountMinor: &amount, OccurredAt: paidAt, UpdatedAt: paidAt, ReferrerID: "sales"}
	storeFact := func(fact sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	storeFact(paid)
	for _, order := range []string{"32", "33"} {
		other := paid
		other.OrderID, other.PaymentID, other.RecordKey = order, order, "payment:"+order
		storeFact(other)
	}
	member := practiceEnrollment{SourceEnrollmentID: "1", ParticipantID: "21", MembershipType: 3,
		CommissionEligible: true, PaidOrderIDs: []string{"31"}, SignedAt: start, ContentHash: "a1"}
	first := practiceRoster{Key: "PRACTICE:1:1:10", SourceVersion: 1, Hash: "first", StartsAt: start,
		ConfirmedAt: start.Add(time.Hour), Enrollments: []practiceEnrollment{member}}
	importRoster := func(roster practiceRoster) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storePracticeRoster(ctx, tx, roster)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	importRoster(first)
	value, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: original})
	if err != nil || value.(draftSummary).Total != 300 {
		t.Fatalf("original draft: %+v, %v", value, err)
	}
	originalVersion := value.(draftSummary).Version
	gift := practiceEnrollment{SourceEnrollmentID: "2", ParticipantID: "22", MembershipType: 1, SignedAt: start,
		PaidOrderIDs: []string{}, ContentHash: "a2"}
	first.SourceVersion, first.Hash, first.Enrollments = 2, "deferred", []practiceEnrollment{gift}
	importRoster(first)
	member.SignedAt = start.AddDate(0, 0, 7)
	member.PaidOrderIDs, member.ContentHash = []string{"31", "32"}, "a3"
	independent := practiceEnrollment{SourceEnrollmentID: "3", ParticipantID: "23", MembershipType: 3,
		CommissionEligible: true, PaidOrderIDs: []string{"33"}, SignedAt: member.SignedAt, ContentHash: "a4"}
	importRoster(practiceRoster{Key: "PRACTICE:1:1:11", SourceVersion: 1, Hash: "target", StartsAt: member.SignedAt,
		ConfirmedAt: member.SignedAt.Add(time.Hour), Enrollments: []practiceEnrollment{member, independent}})
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &target, Label: "Tiered sales and delivery",
		Components: []component{
			{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER", Tiers: &practiceSalesTiers{
				CountFromOrdinal: 1, Count: []practiceTier{{From: 1, Mode: "fixed", Value: 300}}, Performance: []practiceTier{{Mode: "fixed", Value: 200}}}},
			{Type: "delivery", Mode: "period_rate", Value: 100000, PeriodBenefit: &practicePeriodBenefit{Label: "Class delivery", PersonID: person}},
		}}); err != nil {
		t.Fatal(err)
	}
	paid.Version, paid.UpdatedAt = "2", paidAt.Add(time.Minute)
	storeFact(paid)
	value, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: target})
	if err != nil || value.(draftSummary).Total != 800 || value.(draftSummary).IssueCount != 0 {
		t.Fatalf("open target should calculate both enrollments before seeding the existing settlement: %+v, %v", value, err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, original, originalVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `DROP FUNCTION practice_payment_dependents(bigint)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	var queued bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, target).Scan(&queued); err != nil || !queued {
		t.Fatalf("upgrade must queue an existing duplicate draft: %v, %v", queued, err)
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var total int64
	var issueCount int
	if err := s.db.QueryRow(ctx, `SELECT total_minor,issue_count FROM calculation_drafts WHERE period_id=$1`, target).Scan(&total, &issueCount); err != nil || total != 400 || issueCount != 1 {
		t.Fatalf("class must hold the whole deferred enrollment and still calculate unrelated sales and delivery: total=%d issues=%d %v", total, issueCount, err)
	}
	var issues []string
	err = s.db.QueryRow(ctx, `SELECT array(SELECT code FROM calculation_draft_issues WHERE period_id=$1)`, target).Scan(&issues)
	if err != nil || len(issues) != 1 || issues[0] != "PRACTICE_PAYMENT_ALREADY_SETTLED" {
		t.Fatalf("class must report the booked payment: %v, %v", issues, err)
	}
	var booked int
	var targetPayments []string
	if err := s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE a.period_id=$1),
	 array_agg(v.order_id ORDER BY v.order_id) FILTER (WHERE a.period_id=$2)
	 FROM calculation_draft_performance a JOIN source_order_versions v ON v.id=a.source_version_id`, original, target).
		Scan(&booked, &targetPayments); err != nil || booked != 1 || len(targetPayments) != 1 || targetPayments[0] != "33" {
		t.Fatalf("performance duplicated or changed: original=%d target=%v, %v", booked, targetPayments, err)
	}
	month := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "August refunds", SettlementMonth: "2020-08"}))
	refundAt, refundAmount := start.AddDate(0, 0, 10), int64(100)
	storeFact(sourceFact{Source: "DCZD", Kind: "REFUND_SUCCEEDED", OrderID: "31", RefundID: "1", Domain: "PRACTICE", SourceKey: "practice",
		RecordKey: "refund:1", Version: "1", OccurredAt: refundAt, UpdatedAt: refundAt, AmountMinor: &refundAmount,
		RefundsPayment: []sourcePaymentReference{{OrderID: "31", RecordID: "31"}}})
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		dependencies, err := readPracticeMonthSettlementDependencies(ctx, tx, month)
		if err == nil && len(dependencies) != 0 {
			return fmt.Errorf("the refund of a booked payment must not wait for the target class: %+v", dependencies)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	configWriter, err := s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer configWriter.Rollback(ctx)
	if err := touchSourceWrites(ctx, configWriter, []string{"CONFIG"}); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='100ms'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM settlements WHERE period_id=$1`, original)
		return err
	})
	var lockError *pgconn.PgError
	if !errors.As(err, &lockError) || lockError.Code != "55P03" {
		t.Fatalf("class reopening must wait for in-flight roster changes: %v", err)
	}
	if err := configWriter.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM settlements WHERE period_id=$1`, original); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var originalTotal, targetTotal int64
	if err := s.db.QueryRow(ctx, `SELECT original.total_minor,target.total_minor,
	 array(SELECT code FROM calculation_draft_issues WHERE period_id=target.period_id)
	 FROM calculation_drafts original JOIN calculation_drafts target ON target.period_id=$2 WHERE original.period_id=$1`, original, target).
		Scan(&originalTotal, &targetTotal, &issues); err != nil || originalTotal != 0 || targetTotal != 800 {
		t.Fatalf("deleting the incorrect settlement must refresh both open drafts: original=%d target=%d issues=%v, %v", originalTotal, targetTotal, issues, err)
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
	var originalLines []int64
	if err := s.db.QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM calculation_draft_lines WHERE period_id=$1`,
		period).Scan(&originalLines); err != nil {
		t.Fatal(err)
	}
	originalVersion := run().Version
	storeRefund(refund("3", "PRACTICE", "1", confirmed.AddDate(0, 1, 0), &late))
	if got := run(); got.IssueCount != 0 || got.Total != 120 || got.Version != originalVersion {
		t.Fatalf("valid late refund should not prevent the class from settling: %+v", got)
	}
	var unchangedLines bool
	if err := s.db.QueryRow(ctx, `SELECT array_agg(id ORDER BY id)=$2 FROM calculation_draft_lines WHERE period_id=$1`,
		period, originalLines).Scan(&unchangedLines); err != nil || !unchangedLines {
		t.Fatalf("a handoff to the refund month must keep the class funding lines: %v %v", unchangedLines, err)
	}
	roster.SourceVersion, roster.ConfirmedAt = 2, confirmed.AddDate(0, 1, 1)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, roster)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := run(); got.IssueCount != 0 || got.Total != 120 {
		t.Fatalf("a roster revision must not bring a later refund back into the original class: %+v", got)
	}
	var issue string
	if err := s.db.QueryRow(ctx, `SELECT code FROM calculation_draft_issues WHERE period_id=$1`, period).Scan(&issue); err != nil || issue != "PRACTICE_REFUND_AFTER_ROSTER" {
		t.Fatalf("the handoff to the refund period should remain visible: %q %v", issue, err)
	}
	wrongProduct := refund("8", "PRACTICE", "1", confirmed.AddDate(0, 1, 0).Add(time.Hour), &late)
	wrongProduct.SourceKey = "another-product"
	storeRefund(wrongProduct)
	if got := run(); got.IssueCount != 1 || got.Total != 0 {
		t.Fatalf("a late refund not mapped to the payment's product must not become a handoff: %+v", got)
	}
	if err := s.db.QueryRow(ctx, `SELECT code FROM calculation_draft_issues WHERE period_id=$1 AND NOT advisory`, period).Scan(&issue); err != nil || issue != "PRACTICE_REFUND_PRODUCT_MISMATCH" {
		t.Fatalf("unexpected product mismatch issue: %q %v", issue, err)
	}
	wrongProduct.OrderID, wrongProduct.Version = "99", "2"
	wrongProduct.RefundsPayment = []sourcePaymentReference{{OrderID: "99"}}
	wrongProduct.UpdatedAt = wrongProduct.UpdatedAt.Add(time.Minute)
	storeRefund(wrongProduct)
	excess := int64(151)
	invalidLate := refund("7", "PRACTICE", "1", confirmed.AddDate(0, 1, 0).Add(time.Hour), &excess)
	storeRefund(invalidLate)
	if got := run(); got.IssueCount != 1 || got.Total != 0 {
		t.Fatalf("cumulative late refunds exceeding the payment must still block: %+v", got)
	}
	invalidLate.OrderID, invalidLate.Version = "99", "2"
	invalidLate.RefundsPayment = []sourcePaymentReference{{OrderID: "99"}}
	invalidLate.UpdatedAt = invalidLate.UpdatedAt.Add(time.Minute)
	storeRefund(invalidLate)
	rest := int64(200)
	storeRefund(refund("4", "PRACTICE", "1", at.Add(3*time.Hour), &rest))
	if got := run(); got.IssueCount != 1 || got.Total != 0 {
		t.Fatalf("paid order fully refunded before confirmation requires roster review: %+v", got)
	}
	if err := s.db.QueryRow(ctx, `SELECT code FROM calculation_draft_issues WHERE period_id=$1`, period).Scan(&issue); err != nil || issue != "PRACTICE_REFUND_ROSTER_CHANGED" {
		t.Fatalf("incorrect fully refunded roster issue: %q %v", issue, err)
	}
}

func TestPracticeRefundCutoffDoesNotCountLatePrincipalWithinTimestampTie(t *testing.T) {
	paid, late, onTime := int64(1000), int64(100), int64(900)
	cutoff := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	p := practicePurchase{member: practiceEnrollment{ConfirmedAt: cutoff}, base: paid,
		payments: []paymentInput{{ID: 1, Fact: sourceFact{AmountMinor: &paid, OccurredAt: cutoff.Add(-time.Hour)}}}}
	p.applyRefunds(map[int64][]practiceRefundMatch{1: {
		{event: paymentInput{ID: 2, Fact: sourceFact{AmountMinor: &late, OccurredAt: cutoff.Add(time.Nanosecond)}}},
		{event: paymentInput{ID: 3, Fact: sourceFact{AmountMinor: &onTime, OccurredAt: cutoff}}},
	}})
	if p.issue != "" || p.base != 100 || p.netAmount(p.payments[0]) != 100 || len(p.lateRefunds) != 1 {
		t.Fatalf("refund cutoff and total-principal validation must be independent: %+v", p)
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

func TestPracticePersonalSalesCanBeLimitedToSelectedCompanies(t *testing.T) {
	var rule paymentRule
	rule.ID = 7
	if err := json.Unmarshal([]byte(`[
	 {"type":"sales","mode":"rate","value":1900,"classMember":"new","sourceRole":"DIRECT_REFERRER","salesCompanies":["8","353"]},
	 {"type":"sales","mode":"fixed","value":8500,"classMember":"retrain","sourceRole":"DIRECT_REFERRER","salesCompanies":["8","353"]},
	 {"type":"company_sales","mode":"company_sales","classMember":"new","sourceRole":"DIRECT_REFERRER",
	  "companySales":{"internalCompanies":["other-internal"],"categories":[{"companies":["external"],"mode":"rate","value":500000}]}}
	]`), &rule.Components); err != nil {
		t.Fatal(err)
	}
	if err := validateComponents(rule.Components); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 6, 8, 9, 38, 0, 0, shanghai)
	orders := make(map[string][]paymentInput)
	var members []practiceEnrollment
	for i, item := range []struct {
		kind    int
		company string
		minor   int64
	}{
		{3, "8", 580000}, {3, "353", 580000}, {3, "other-internal", 580000},
		{2, "8", 40000}, {2, "353", 40000}, {2, "other-internal", 40000},
	} {
		id := strconv.Itoa(i + 1)
		orders[id] = []paymentInput{{ID: int64(i + 1), Fact: sourceFact{
			Source: "DCZD", Domain: "PRACTICE", Kind: "ORDER_PAID", OrderID: id, PaymentID: id,
			SalesCompanyID: item.company, AmountMinor: &item.minor, OccurredAt: at, ReferrerID: "seller",
		}}}
		members = append(members, practiceEnrollment{ID: int64(i + 1), SourceEnrollmentID: id, MembershipType: item.kind,
			CommissionEligible: true, PaidOrderIDs: []string{id}})
	}
	partialNew, partialRetrain := int64(100000), int64(10000)
	refunds := map[int64][]practiceRefundMatch{
		1: {{event: paymentInput{ID: 91, Fact: sourceFact{AmountMinor: &partialNew, OccurredAt: at.Add(time.Hour)}}}},
		4: {{event: paymentInput{ID: 92, Fact: sourceFact{AmountMinor: &partialRetrain, OccurredAt: at.Add(time.Hour)}}}},
	}
	for i := range members {
		members[i].ConfirmedAt = at.Add(2 * time.Hour)
	}
	seller := identityKey{Source: "DCZD", ExternalID: "seller"}
	result, err := calculatePracticeDraft(members, rule, orders, map[identityKey][]identityAssignment{
		seller: {{ID: 24, PersonID: 42, ValidFrom: at.Add(-time.Hour)}},
	}, nil, practiceTierHistory{}, refunds, practiceDraftRuntime{companyOffers: practiceCompanyStandards(rule)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]int64{1: 91200, 2: 110200, 4: 8500, 5: 8500}
	if len(result.Issues) != 0 || len(result.Lines) != len(want) || result.Total != 218400 {
		t.Fatalf("expected scoped rate and fixed sales, got total %d, lines %+v, issues %+v", result.Total, result.Lines, result.Issues)
	}
	for _, line := range result.Lines {
		if line.PersonID != 42 || want[line.FactID] != line.Amount {
			t.Fatalf("wrong company scope or standard: %+v", line)
		}
		delete(want, line.FactID)
	}
}
