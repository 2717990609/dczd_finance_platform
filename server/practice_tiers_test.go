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

func TestPracticeCumulativeAmountUsesSelectedEnrollmentTypes(t *testing.T) {
	for _, tc := range []struct {
		metric string
		limit  int64
	}{
		{"cumulative_new", 1000},
		{"cumulative_retrain", 1200},
		{"cumulative_combined", 1800},
	} {
		t.Run(tc.metric, func(t *testing.T) {
			var tiers practiceSalesTiers
			raw := fmt.Sprintf(`{"cumulativeMetric":%q,"countFromOrdinal":1,
			  "performance":[{"from":0,"through":%d,"mode":"rate","value":100},
			   {"from":%d,"mode":"rate","value":200}]}`, tc.metric, tc.limit-1, tc.limit)
			if err := json.Unmarshal([]byte(raw), &tiers); err != nil {
				t.Fatal(err)
			}
			part := component{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER", Tiers: &tiers}
			if err := validateComponents([]component{part}); err != nil {
				t.Fatal(err)
			}
			rows := []practiceTierPurchase{
				{EnrollmentID: 1, PersonID: 9, Base: 600},
				{EnrollmentID: 2, PersonID: 9, Base: 800, Retrain: true, MeasureOnly: true},
			}
			totals, err := practiceCumulativeAmountTotals(tiers, rows, map[int64]int64{9: 400})
			if err != nil {
				t.Fatal(err)
			}
			awards := choosePracticeCumulativeAmountTiers(tiers, rows, totals)
			if awards[1].Value != 200 || len(awards) != 1 {
				t.Fatalf("selected cumulative amount should qualify only the new-registration payout: %+v %v", awards, err)
			}
			totals, err = practiceCumulativeAmountTotals(tiers, rows, map[int64]int64{9: 399})
			if err != nil {
				t.Fatal(err)
			}
			awards = choosePracticeCumulativeAmountTiers(tiers, rows, totals)
			if awards[1].Value != 100 {
				t.Fatalf("one minor unit less from a booked refund should lower the tier: %+v", awards)
			}
		})
	}
}

func TestPracticeFixedTierRefundKeepsOriginalPaymentShares(t *testing.T) {
	checkPracticeFixedRefundPaymentShares(t, component{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER",
		Tiers: &practiceSalesTiers{CountFromOrdinal: 1, Count: []practiceTier{{Mode: "fixed", Value: 2000}}}}, 0, 400, 1200)
}

func TestPracticeFixedSalesRefundKeepsOriginalPaymentShares(t *testing.T) {
	part := component{Type: "sales", Mode: "fixed", ClassMember: "new", Value: 2001,
		SourceRole: "DIRECT_REFERRER", RefundPolicy: "proportional"}
	t.Run("new settlement", func(t *testing.T) { checkPracticeFixedRefundPaymentShares(t, part, 0, 400, 1200) })
	t.Run("legacy settlement", func(t *testing.T) {
		checkPracticeFixedRefundPaymentShares(t, part, practiceReplayFixedTierPaymentShares, 400, 1201)
	})
}

func TestPracticeFixedRetrainSalesRefundDefaultsToPaymentShares(t *testing.T) {
	part := component{Type: "sales", Mode: "fixed", ClassMember: "retrain", Value: 2001,
		SourceRole: "DIRECT_REFERRER"}
	t.Run("new settlement", func(t *testing.T) { checkPracticeFixedRefundPaymentShares(t, part, 0, 400, 1200) })
	t.Run("legacy settlement", func(t *testing.T) {
		checkPracticeFixedRefundPaymentShares(t, part, practiceReplayFixedPersonalPaymentShares, 500, 1501)
	})
	t.Run("signup basis", func(t *testing.T) {
		part.PracticeSignupBasis = true
		checkPracticeFixedRefundPaymentShares(t, part, 0, 801, 1200)
	})
}

func checkPracticeFixedRefundPaymentShares(t *testing.T, part component, replayVersion int, depositShare, tailShare int64) {
	t.Helper()
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	const sourceKey = "fixed-tier-refunds"
	const rosterKey = "PRACTICE:1:1:1"
	at := time.Date(2026, time.August, 8, 9, 0, 0, 0, shanghai)
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_FIXED_TIER", Name: "固定阶梯退款", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: sourceKey}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "测试销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-08-01T00:00:00+08:00"}))
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "一期", Ordinal: 1, StartsOn: at.Format(time.DateOnly),
		EndsOn: at.Format(time.DateOnly), SourcePeriodKey: rosterKey}))
	refundPeriod := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月退款", SettlementMonth: "2026-09"}))
	membership := 3
	if part.ClassMember == "retrain" {
		membership = 2
	}
	roster := practiceRoster{Key: rosterKey, Hash: "roster-1", SourceVersion: 1, StartsAt: at, ConfirmedAt: at.Add(time.Hour),
		Enrollments: []practiceEnrollment{{SourceEnrollmentID: "10", ParticipantID: "10", MembershipType: membership,
			CommissionEligible: true, ContentHash: "10", PaidOrderIDs: []string{"1", "2"}}}}
	paid := func(order string, amount int64, paidAt time.Time) sourceFact {
		return sourceFact{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: sourceKey,
			OrderID: order, PaymentID: order, RecordKey: "paid:" + order, Version: "1", ReferrerID: "seller",
			AmountMinor: &amount, OccurredAt: paidAt, UpdatedAt: paidAt}
	}
	refund := func(order, number string, amount int64, bookedAt time.Time) sourceFact {
		return sourceFact{Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE", SourceKey: sourceKey,
			OrderID: order, RefundID: number, RecordKey: "refund:" + number, Version: "1", AmountMinor: &amount,
			OccurredAt: bookedAt, UpdatedAt: bookedAt,
			RefundsPayment: []sourcePaymentReference{{OrderID: order, RecordID: order}}}
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, []sourceFact{
			paid("1", 4000, at.Add(-3*time.Hour)),
			paid("2", 6000, at.Add(-2*time.Hour)),
			refund("1", "11", 2000, at.Add(-time.Hour)),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &period, Label: "固定提成", Components: []component{part}}); err != nil {
		t.Fatal(err)
	}
	class, err := s.calculateCurrentDraft(ctx, period, 0)
	total := int64(1600)
	if part.PracticeSignupBasis {
		total = depositShare + tailShare
	}
	if err != nil || class.IssueCount != 0 || class.Total != total || class.LineCount != 2 {
		t.Fatalf("expected class total %d after the partial deposit refund: %+v %v", total, class, err)
	}
	if replayVersion != 0 {
		// Recreate the posted allocations and replay version from the old calculation.
		if _, err := s.db.Exec(ctx, `UPDATE calculation_drafts SET practice_replay_inputs=jsonb_set(practice_replay_inputs,'{version}',to_jsonb($2::int)),
		 total_minor=$3 WHERE period_id=$1`, period, replayVersion, depositShare+tailShare); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, `UPDATE calculation_draft_lines l SET amount_minor=CASE v.order_id WHEN '1' THEN $2::bigint ELSE $3::bigint END
		 FROM source_order_versions v WHERE l.period_id=$1 AND v.id=l.source_version_id`, period, depositShare, tailShare); err != nil {
			t.Fatal(err)
		}
	}
	verifyShares := func() {
		t.Helper()
		var amounts []int64
		err := s.db.QueryRow(ctx, `SELECT array_agg(l.amount_minor ORDER BY v.order_id) FROM calculation_draft_lines l
		 JOIN source_order_versions v ON v.id=l.source_version_id WHERE l.period_id=$1`, period).Scan(&amounts)
		if err != nil || len(amounts) != 2 || amounts[0] != depositShare || amounts[1] != tailShare {
			t.Fatalf("the split allocation must remain tied to the original payment legs: %v %v", amounts, err)
		}
	}
	verifyShares()
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, class.Version); err != nil {
		t.Fatal(err)
	}
	post := func(events ...sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, events)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	september := at.AddDate(0, 1, 0)
	post(refund("1", "12", 2000, september))
	booked, err := s.calculateCurrentDraft(ctx, refundPeriod, 0)
	if err != nil || booked.IssueCount != 0 || booked.RefundTotal != -depositShare || booked.RefundCount != 1 {
		t.Fatalf("the remaining deposit refund must reverse only its original share: %+v %v", booked, err)
	}
	post(refund("2", "13", 3000, september.Add(time.Hour)))
	booked, err = s.calculateCurrentDraft(ctx, refundPeriod, 0)
	wantRefund, wantCount := -depositShare-(tailShare+1)/2, 2
	if part.PracticeSignupBasis {
		wantRefund, wantCount = -depositShare, 1
	}
	if err != nil || booked.IssueCount != 0 || booked.RefundTotal != wantRefund || booked.RefundCount != wantCount {
		t.Fatalf("expected monthly total %d and %d deductions after the balance partial refund: %+v %v", wantRefund, wantCount, booked, err)
	}
	if part.PracticeSignupBasis {
		post(refund("2", "14", 3000, september.Add(2*time.Hour)))
		booked, err = s.calculateCurrentDraft(ctx, refundPeriod, 0)
		if err != nil || booked.IssueCount != 0 || booked.RefundTotal != -depositShare-tailShare || booked.RefundCount != 2 {
			t.Fatalf("a fully refunded balance must reverse its entire original share: %+v %v", booked, err)
		}
	}
	verifyShares()
}

func TestPracticeTierMeasuresOtherEnrollmentWithoutAPayoutSchedule(t *testing.T) {
	at := time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
	newAmount, retrainAmount := int64(10000), int64(30000)
	orders := map[string][]paymentInput{
		"1": {{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "1", PaymentID: "1", ReferrerID: "seller", SalesCompanyID: "other", AmountMinor: &newAmount, OccurredAt: at.Add(-time.Hour)}}},
		"2": {{ID: 2, Fact: sourceFact{Source: "DCZD", OrderID: "2", PaymentID: "2", ReferrerID: "seller", SalesCompanyID: "configured", AmountMinor: &retrainAmount, OccurredAt: at}}},
	}
	members := []practiceEnrollment{
		{ID: 1, RosterID: 9, SourceEnrollmentID: "1", MembershipType: 3, CommissionEligible: true, ConfirmedAt: at, PaidOrderIDs: []string{"1"}},
		{ID: 2, RosterID: 9, SourceEnrollmentID: "2", MembershipType: 2, CommissionEligible: true, ConfirmedAt: at, PaidOrderIDs: []string{"2"}},
	}
	history := practiceTierHistory{current: practiceHistoryPeriod{rosterID: 9, ordinal: 1, startsAt: &at}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 7}}}
	for _, c := range []struct {
		name   string
		metric practiceAmountMetric
		count  []practiceTier
		amount []practiceTier
	}{
		{name: "累计新报人数", count: []practiceTier{{Mode: "rate", Value: 100}, {From: 1, Mode: "rate", Value: 300}}},
		{name: "累计新报及复训金额", metric: "cumulative_combined", amount: []practiceTier{{Mode: "rate", Value: 100}, {From: 40000, Mode: "rate", Value: 300}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			part := component{Type: "sales", Mode: "tiered", ClassMember: "retrain", SourceRole: "DIRECT_REFERRER",
				Tiers: &practiceSalesTiers{CountFromOrdinal: 1, CumulativeMetric: c.metric,
					CompanySchedules: []practiceCompanySchedule{{CompanyIDs: []string{"configured"}, Count: c.count, Performance: c.amount}}}}
			rule := paymentRule{Components: []component{part}}
			if err := validateComponents(rule.Components); err != nil {
				t.Fatal(err)
			}
			result, err := calculatePracticeDraft(members, rule, orders, identities, nil, history, nil, practiceDraftRuntime{})
			if err != nil || len(result.Issues) != 0 || len(result.Lines) != 1 || result.Lines[0].FactID != 2 || result.Total != 900 {
				t.Fatalf("new enrollment must increase the tier without a payout: %+v, %v", result, err)
			}
			if c.metric == "" {
				legacy := history
				legacy.replaying, legacy.legacyMeasures, legacy.frozenPrior = true, true, result.practicePriorCounts
				old, err := calculatePracticeDraft(members, rule, orders, identities, nil, legacy, nil, practiceDraftRuntime{})
				if err != nil || len(old.Issues) != 0 || old.Total != 300 {
					t.Fatalf("earlier settlements keep the original count when replayed: %+v, %v", old, err)
				}
			}
			rule.Components[0].SalesCompanies = &[]string{"configured"}
			scoped, err := calculatePracticeDraft(members, rule, orders, identities, nil, history, nil, practiceDraftRuntime{})
			if err != nil || len(scoped.Issues) != 0 || scoped.Total != 300 {
				t.Fatalf("the explicit company range still limits orders that only contribute to the metric: %+v, %v", scoped, err)
			}
		})
	}
}

func TestPracticePriorTierSkipsConfirmedUnattributedRetrain(t *testing.T) {
	amount := int64(10000)
	old := time.Date(2026, 10, 5, 4, 0, 0, 0, time.UTC)
	orders := map[string][]paymentInput{
		"1": {{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "1", PaymentID: "1", ReferrerID: "known", AmountMinor: &amount, OccurredAt: old}}},
		"2": {{ID: 2, Fact: sourceFact{Source: "DCZD", OrderID: "2", PaymentID: "2", AmountMinor: &amount, OccurredAt: old,
			PracticeNoReferrer: &sourcePracticeSignup{EnrollmentID: 12, ParticipantID: 81, StrategyID: 4}}}},
	}
	state := practiceTierHistory{cumulative: "cumulative_retrain", periods: []practiceHistoryPeriod{{rosterID: 9, settled: true, retrainRole: "DIRECT_REFERRER"}},
		members: []practiceEnrollment{
			{ID: 11, RosterID: 9, SourceEnrollmentID: "11", MembershipType: 2, CommissionEligible: true, PaidOrderIDs: []string{"1"}},
			{ID: 12, RosterID: 9, SourceEnrollmentID: "12", ParticipantID: "81", PeriodKey: "PRACTICE:1:4:9", MembershipType: 2, CommissionEligible: true, PaidOrderIDs: []string{"2"}},
		}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "known"}: {{PersonID: 7}}}
	index, err := indexPracticePrior(state, component{SourceRole: "DIRECT_REFERRER"}, orders, identities, nil, nil, practiceDraftRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	if got, ready, err := index.personTotal(7, map[int64]bool{9: true}); err != nil || !ready || got != amount {
		t.Fatalf("known unattributed enrollment must not block or inflate the seller's prior total: %d %t %v", got, ready, err)
	}
	state.members[1].ParticipantID = "82"
	index, err = indexPracticePrior(state, component{SourceRole: "DIRECT_REFERRER"}, orders, identities, nil, nil, practiceDraftRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ready, err := index.personTotal(7, map[int64]bool{9: true}); err != nil || ready {
		t.Fatalf("an unverified source exception must still block the history: %t %v", ready, err)
	}
}

func TestPracticeCumulativeAmountUsesRefundBookingMonth(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_CUMULATIVE", Name: "累计金额", PeriodMode: "class"}))
	early := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Ordinal: 2, Name: "退款入账前", StartsOn: "2026-08-08", EndsOn: "2026-08-09"}))
	late := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Ordinal: 3, Name: "实际退款账期", StartsOn: "2026-11-08", EndsOn: "2026-11-09"}))
	accounting := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "十一月退款", SettlementMonth: "2026-11"}))
	var paid, latest, refund int64
	writeSource := func(dest *int64, kind, key, version, when string, amount int64) {
		t.Helper()
		if err := s.db.QueryRow(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
		 VALUES('DCZD','31','',$1,$2,$3,$4::timestamptz,jsonb_build_object('amountMinor',$5::bigint),$4::timestamptz) RETURNING id`,
			key, version, kind, when, amount).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	writeSource(&paid, "ORDER_PAID", "paid:31", "1", "2026-07-01", 50000)
	writeSource(&latest, "ORDER_PAID", "paid:31", "2", "2026-07-01", 50000)
	writeSource(&refund, "REFUND_SUCCEEDED", "refund:31", "1", "2026-08-05", 10000)
	exec(`INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	 VALUES($1,1,decode('','hex'),0,0,0,now())`, accounting)
	exec(`INSERT INTO calculation_draft_performance(period_id,source_version_id,payment_version_id)
	 VALUES($1,$2,$3)`, accounting, refund, paid)
	assertBooked := func(period, amount int64) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			totals, err := readPracticeCumulativeRefunds(ctx, tx, period, []int64{latest})
			if err == nil && totals[latest] != amount {
				return fmt.Errorf("want %d of original payment's posted refund, got %v", amount, totals)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertBooked(late, 0)
	for _, period := range []int64{early, late} {
		if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &period, Label: "金额阶梯", Components: []component{{
			Type: "sales", Mode: "tiered", SourceRole: "DIRECT_REFERRER", ClassMember: "new",
			Tiers: &practiceSalesTiers{CumulativeMetric: "cumulative_new", CountFromOrdinal: 1, Performance: []practiceTier{{Mode: "rate", Value: 100}}},
		}}}); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO calculation_drafts(period_id,rule_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
		 SELECT period_id,id,1,decode('','hex'),0,0,0,now() FROM rule_versions WHERE period_id=$1`, period)
	}
	exec(`DELETE FROM draft_refresh_queue WHERE period_id=ANY($1::bigint[])`, []int64{early, late, accounting})
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := insertSettlement(ctx, tx, accounting, 1, "fixture", "", nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertBooked(early, 0)
	assertBooked(late, 10000)
	var queued []int64
	if err := s.db.QueryRow(ctx, `SELECT array_agg(period_id ORDER BY period_id) FROM draft_refresh_queue
	 WHERE period_id=ANY($1::bigint[])`, []int64{early, late}).Scan(&queued); err != nil || len(queued) != 1 || queued[0] != late {
		t.Fatalf("refund settlement must refresh the applicable month only: %v %v", queued, err)
	}
}

func TestPracticeBookedRefundChangesOnlyOpenCumulativeTier(t *testing.T) {
	at := time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
	earlier := at.AddDate(0, -1, 0)
	amount, limit := int64(10000), int64(19999)
	pay := func(id int64, order string, when time.Time) paymentInput {
		return paymentInput{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: order, PaymentID: order, ReferrerID: "seller",
			AmountMinor: &amount, OccurredAt: when}}
	}
	member := func(id, roster int64, order string, when time.Time) practiceEnrollment {
		return practiceEnrollment{ID: id, RosterID: roster, SourceEnrollmentID: order, MembershipType: 3,
			CommissionEligible: true, ConfirmedAt: when.Add(time.Hour), PaidOrderIDs: []string{order}}
	}
	orders := map[string][]paymentInput{"1": {pay(1, "1", earlier)}, "2": {pay(2, "2", at)}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 7}}}
	history := practiceTierHistory{current: practiceHistoryPeriod{rosterID: 22, ordinal: 2, startsAt: &at},
		periods: []practiceHistoryPeriod{{rosterID: 11, ordinal: 1, startsAt: &earlier}},
		members: []practiceEnrollment{member(1, 11, "1", earlier)}}
	roster := []practiceEnrollment{member(2, 22, "2", at)}
	rule := paymentRule{ID: 1, Components: []component{{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER",
		Tiers: &practiceSalesTiers{CumulativeMetric: "cumulative_new", CountFromOrdinal: 1,
			Performance: []practiceTier{{From: 0, Through: &limit, Mode: "rate", Value: 100}, {From: 20000, Mode: "rate", Value: 200}}}}}}
	calculate := func(state practiceTierHistory, want int64) paymentDraft {
		t.Helper()
		draft, err := calculatePracticeDraft(roster, rule, orders, identities, nil, state, nil, practiceDraftRuntime{})
		if err != nil || len(draft.Issues) != 0 || draft.Total != want {
			t.Fatalf("want %d in tier award, got %d with issues %+v, error %v", want, draft.Total, draft.Issues, err)
		}
		return draft
	}
	before := calculate(history, 200)
	if before.practicePriorCounts[7] != 10000 || before.practiceTierAmounts[7] != 20000 {
		t.Fatalf("the settlement must retain the prior and selected bases: %v, %v", before.practicePriorCounts, before.practiceTierAmounts)
	}
	history.bookedRefunds = map[int64]int64{1: 2500}
	calculate(history, 100)
	history.replaying, history.frozenAmounts = true, before.practiceTierAmounts
	calculate(history, 200)
	history.repriceAmounts = true
	history.frozenPrior = map[int64]int64{7: 9000}
	repriced := calculate(history, 100)
	if repriced.practiceTierAmounts[7] != 19000 {
		t.Fatalf("the correction must use the frozen prior, not live history or the previously selected tier: %v", repriced.practiceTierAmounts)
	}
	history.frozenPrior = nil
	if _, err := calculatePracticeDraft(roster, rule, orders, identities, nil, history, nil, practiceDraftRuntime{}); !errors.Is(err, errPracticeRefundReplay) {
		t.Fatalf("an unavailable prior basis must block the correction: %v", err)
	}
}

func TestPracticeCumulativeTierUsesCurrentCompanyScopeForHistory(t *testing.T) {
	start := time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
	previous := start.AddDate(0, -1, 0)
	const role = "DIRECT_REFERRER"
	var orders = make(map[string][]paymentInput)
	add := func(id, roster int64, company string, amount int64, at time.Time) practiceEnrollment {
		key := fmt.Sprint(id)
		orders[key] = []paymentInput{{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: key, PaymentID: key, ReferrerID: "seller",
			SalesCompanyID: company, AmountMinor: &amount, OccurredAt: at.Add(-time.Hour)}}}
		return practiceEnrollment{ID: id, RosterID: roster, SourceEnrollmentID: key, MembershipType: 3,
			CommissionEligible: true, ConfirmedAt: at.Add(time.Hour), PaidOrderIDs: []string{key}}
	}
	history := practiceTierHistory{current: practiceHistoryPeriod{rosterID: 22, ordinal: 2, startsAt: &start},
		periods: []practiceHistoryPeriod{{rosterID: 11, ordinal: 1, startsAt: &previous, personalRole: role,
			scopedTierCompanies: []string{"甲公司", "乙公司"}}},
		members: []practiceEnrollment{add(1, 11, "甲公司", 10000, previous), add(2, 11, "乙公司", 50000, previous)}}
	current := []practiceEnrollment{add(3, 22, "甲公司", 10000, start)}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 7}}}
	for _, tc := range []struct {
		name          string
		companies     []string
		prior, amount int64
	}{
		{"甲公司", []string{"甲公司"}, 10000, 100},
		{"甲公司和乙公司", []string{"甲公司", "乙公司"}, 60000, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := component{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: role, SalesCompanies: &tc.companies,
				Tiers: &practiceSalesTiers{CumulativeMetric: "cumulative_new", CountFromOrdinal: 1,
					Performance: []practiceTier{{Mode: "rate", Value: 100}, {From: 30000, Mode: "rate", Value: 200}}}}
			if err := validateComponents([]component{part}); err != nil {
				t.Fatal(err)
			}
			draft, err := calculatePracticeDraft(current, paymentRule{ID: 1, Components: []component{part}}, orders, identities,
				nil, history, nil, practiceDraftRuntime{})
			if err != nil || draft.practicePriorCounts[7] != tc.prior || draft.Total != tc.amount || len(draft.Issues) != 0 {
				t.Fatalf("the current company scope should also filter the accumulated prior payments: prior=%v total=%d issues=%+v err=%v",
					draft.practicePriorCounts, draft.Total, draft.Issues, err)
			}
		})
	}
}

func TestPracticeScopedCumulativeTierMeasuresOnlyEligiblePayments(t *testing.T) {
	at := time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
	previous := at.AddDate(0, -1, 0)
	const inside = "40000000-0000-4000-8000-000000000001"
	const outside = "40000000-0000-4000-8000-000000000002"
	const category = "40000000-0000-4000-8000-000000000003"
	const otherCategory = "40000000-0000-4000-8000-000000000004"
	const role = "DIRECT_REFERRER"
	history := practiceTierHistory{current: practiceHistoryPeriod{rosterID: 22, ordinal: 2, startsAt: &at},
		periods: []practiceHistoryPeriod{{rosterID: 11, ordinal: 1, startsAt: &previous, personalRole: role}}}
	var current []practiceEnrollment
	orders := make(map[string][]paymentInput)
	runtime := practiceDraftRuntime{salesOrganizations: make(practiceSalesOrganizations), companyClassifications: make(map[string]string)}
	add := func(id int64, payments []int64, units, categories []string, when time.Time, roster int64, seller string) {
		member := practiceEnrollment{ID: id, RosterID: roster, SourceEnrollmentID: fmt.Sprint(id), MembershipType: 3,
			CommissionEligible: true, ConfirmedAt: when.Add(time.Hour)}
		for i, amount := range payments {
			paymentID := id*10 + int64(i)
			key := fmt.Sprint(paymentID)
			payment := paymentInput{ID: paymentID, Fact: sourceFact{Source: "DCZD", OrderID: key, PaymentID: key,
				ReferrerID: seller, SalesCompanyID: "company", AmountMinor: &amount, OccurredAt: when.Add(time.Duration(i) * time.Minute)}}
			orders[key] = []paymentInput{payment}
			runtime.salesOrganizations[paymentID] = map[string]practiceSalesOrganization{role: {PersonID: 7, SourceCompany: "company", Group: units[i]}}
			runtime.companyClassifications[key] = categories[i]
			member.PaidOrderIDs = append(member.PaidOrderIDs, key)
		}
		if roster == 11 {
			history.members = append(history.members, member)
		} else {
			current = append(current, member)
		}
	}
	add(1, []int64{4000, 6000}, []string{inside, inside}, []string{category, category}, previous, 11, "seller")
	add(2, []int64{40000}, []string{outside}, []string{category}, previous, 11, "seller")
	add(3, []int64{30000, 6000}, []string{outside, inside}, []string{category, category}, at, 22, "seller")
	add(4, []int64{50000}, []string{inside}, []string{otherCategory}, at, 22, "seller")
	add(5, []int64{1000}, []string{inside}, []string{""}, previous, 11, "another")
	runtime.salesOrganizations[50][role] = practiceSalesOrganization{PersonID: 8, SourceCompany: "company", Group: inside}
	identities := map[identityKey][]identityAssignment{
		{"DCZD", "seller"}:  {{PersonID: 7}},
		{"DCZD", "another"}: {{PersonID: 8}},
	}
	limit := int64(15999)
	part := component{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: role,
		SaleScope: &practiceSaleScope{SalesUnits: []string{inside}, SalesCategoryIDs: []string{category}},
		Tiers: &practiceSalesTiers{CumulativeMetric: "cumulative_new", CountFromOrdinal: 1,
			Performance: []practiceTier{{From: 0, Through: &limit, Mode: "rate", Value: 100}, {From: 16000, Mode: "rate", Value: 200}}}}
	rule := paymentRule{ID: 1, Components: []component{part}}
	if err := validateComponents(rule.Components); err != nil {
		t.Errorf("eligible tier scopes must be publishable: %v", err)
	}
	prior, err := indexPracticePrior(history, part, orders, identities, nil, nil, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, valid, err := prior.personTotal(8, map[int64]bool{11: true}); err != nil || valid {
		t.Fatalf("unresolved historical category must block the affected seller: valid=%v err=%v", valid, err)
	}
	calculate := func(state practiceTierHistory, want int64) paymentDraft {
		t.Helper()
		draft, err := calculatePracticeDraft(current, rule, orders, identities, nil, state, nil, runtime)
		if err != nil || len(draft.Issues) != 0 || len(draft.Lines) != 1 || draft.Total != want ||
			draft.Lines[0].FactID != 31 || draft.Lines[0].Base != 6000 {
			t.Fatalf("only the selected company category and team can contribute or receive an award: total=%d lines=%+v issues=%+v err=%v",
				draft.Total, draft.Lines, draft.Issues, err)
		}
		return draft
	}
	first := calculate(history, 120)
	if first.practiceTierAmounts[7] != 16000 || len(first.Performance) != 3 {
		t.Fatalf("scope must affect the tier alone, never the original performance: %v %+v", first.practiceTierAmounts, first.Performance)
	}
	history.bookedRefunds = map[int64]int64{10: 1000, 20: 3000}
	calculate(history, 60)
	replay := practiceTierHistory{replaying: true, frozenAmounts: first.practiceTierAmounts}
	calculate(replay, 120)

	zero, one := int64(0), int64(1)
	rule.Components[0].Tiers = &practiceSalesTiers{CountFromOrdinal: 1, Count: []practiceTier{
		{From: 0, Through: &zero, Mode: "rate", Value: 100},
		{From: 1, Through: &one, Mode: "rate", Value: 200},
		{From: 2, Mode: "rate", Value: 300},
	}}
	byEnrollment := calculate(history, 120)
	if byEnrollment.practicePriorCounts[7] != 1 {
		t.Fatalf("two eligible historical payments must count as one enrollment: %v", byEnrollment.practicePriorCounts)
	}
}

func TestPracticeSeparateAndSharedEnrollmentTiers(t *testing.T) {
	at := time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
	newAmount, retrainAmount := int64(10000), int64(30000)
	orders := map[string][]paymentInput{
		"1": {{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "1", PaymentID: "1", ReferrerID: "seller", AmountMinor: &newAmount, OccurredAt: at}}},
		"2": {{ID: 2, Fact: sourceFact{Source: "DCZD", OrderID: "2", PaymentID: "2", ReferrerID: "seller", AmountMinor: &retrainAmount, OccurredAt: at}}},
	}
	members := []practiceEnrollment{
		{ID: 1, RosterID: 9, SourceEnrollmentID: "1", MembershipType: 3, CommissionEligible: true, ConfirmedAt: at.Add(time.Hour), PaidOrderIDs: []string{"1"}},
		{ID: 2, RosterID: 9, SourceEnrollmentID: "2", MembershipType: 2, CommissionEligible: true, ConfirmedAt: at.Add(time.Hour), PaidOrderIDs: []string{"2"}},
	}
	history := practiceTierHistory{current: practiceHistoryPeriod{rosterID: 9, ordinal: 1, startsAt: &at}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 7}}}
	newTier := component{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER",
		Tiers: &practiceSalesTiers{CountFromOrdinal: 1, Count: []practiceTier{{From: 0, Mode: "rate", Value: 200}}}}
	retrainTier := component{Type: "sales", Mode: "tiered", ClassMember: "retrain", SourceRole: "DIRECT_REFERRER",
		Tiers: &practiceSalesTiers{CountFromOrdinal: 1, CumulativeMetric: "cumulative_retrain",
			Performance: []practiceTier{{From: 0, Mode: "rate", Value: 100}, {From: 30000, Mode: "rate", Value: 300}}}}
	shared := newTier
	shared.ClassMember = ""
	retrainByCount := newTier
	retrainByCount.ClassMember = "retrain"
	retrainByCount.Tiers = &practiceSalesTiers{CountFromOrdinal: 1,
		Count: []practiceTier{{From: 1, Mode: "rate", Value: 250}}}
	for _, tc := range []struct {
		name  string
		parts []component
		want  [2]int64
	}{
		{"separate", []component{newTier, retrainTier}, [2]int64{200, 900}},
		{"shared", []component{shared}, [2]int64{200, 600}},
		{"new enrollment only supplies the metric", []component{retrainByCount}, [2]int64{0, 750}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateComponents(tc.parts); err != nil {
				t.Fatal(err)
			}
			draft, err := calculatePracticeDraft(members, paymentRule{ID: 1, Components: tc.parts}, orders, identities, nil, history, nil, practiceDraftRuntime{})
			var actual [2]int64
			for _, line := range draft.Lines {
				actual[line.EnrollmentID-1] += line.Amount
			}
			if err != nil || len(draft.Issues) != 0 || actual != tc.want || len(draft.Performance) != 2 {
				t.Fatalf("expected separate awards %v: %+v %v", tc.want, draft, err)
			}
		})
	}
}

func TestPracticeSeparateTierStartsPersistAndReplay(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_TWO_TIERS", Name: "新报复训独立阶梯", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "two-tiers"}))
	seller := id(s.createPerson(ctx, createPersonInput{Name: "同一销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: seller, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-06-01T00:00:00+08:00"}))
	writeClass := func(ordinal int, month time.Month) int64 {
		t.Helper()
		at := time.Date(2026, month, 8, 9, 0, 0, 0, shanghai)
		key := fmt.Sprintf("PRACTICE:1:1:%d", ordinal)
		day := at.Format(time.DateOnly)
		period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: key, Ordinal: ordinal, StartsOn: day, EndsOn: day, SourcePeriodKey: key}))
		roster := practiceRoster{Key: key, Hash: key, SourceVersion: 1, StartsAt: at, ConfirmedAt: at.Add(2 * time.Hour)}
		var facts []sourceFact
		for position, amount := range []int64{10000, 30000} {
			number := fmt.Sprintf("%d%d", ordinal, position+1)
			roster.Enrollments = append(roster.Enrollments, practiceEnrollment{SourceEnrollmentID: number, ParticipantID: number,
				MembershipType: 3 - position, CommissionEligible: true, ContentHash: number, PaidOrderIDs: []string{number}})
			facts = append(facts, sourceFact{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: "two-tiers", OrderID: number,
				PaymentID: number, RecordKey: "paid:" + number, Version: "1", ReferrerID: "seller",
				AmountMinor: &amount, OccurredAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Hour)})
		}
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
				return err
			}
			_, err := storeSourceFacts(ctx, tx, facts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return period
	}
	writeClass(1, time.July)
	current := writeClass(2, time.August)
	month := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月退款", SettlementMonth: "2026-09"}))
	_, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &current, Label: "两条阶梯", Components: []component{
		{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER", ArrangementLabel: "新报销售",
			Tiers: &practiceSalesTiers{CountFromOrdinal: 1, Count: []practiceTier{{Mode: "rate", Value: 100}, {From: 1, Mode: "rate", Value: 200}}}},
		{Type: "sales", Mode: "tiered", ClassMember: "retrain", SourceRole: "DIRECT_REFERRER", ArrangementLabel: "复训销售",
			Tiers: &practiceSalesTiers{CountFromOrdinal: 1, CumulativeMetric: "cumulative_combined",
				Performance: []practiceTier{{Mode: "rate", Value: 100}, {From: 40000, Mode: "rate", Value: 300}, {From: 80000, Mode: "rate", Value: 400}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	calculate := func(period, amount int64) draftSummary {
		t.Helper()
		draft, err := s.calculateCurrentDraft(ctx, period, 0)
		if err != nil || draft.IssueCount != 0 || draft.Total != amount {
			t.Fatalf("expected %d with no issue: %+v %v", amount, draft, err)
		}
		return draft
	}
	calculate(current, 1400)
	read := func() practiceRuleSettings {
		t.Helper()
		items, err := readPracticeRuleSettings(ctx, s.db, product)
		if err != nil || len(items) != 1 || len(items[0].CountArrangements) != 2 {
			t.Fatalf("expected both settings for one rule: %+v %v", items, err)
		}
		return items[0]
	}
	retrain := "retrain"
	_, err = s.savePracticeRuleSettings(ctx, savePracticeRuleSettingsInput{ProductID: product, RuleID: read().ID, CountMember: &retrain,
		CountStart: &practicePersonCountStart{PersonID: seller, practiceCountStart: practiceCountStart{FromOrdinal: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	arrangements := read().CountArrangements
	if len(arrangements[0].Starts) != 0 || len(arrangements[1].Starts) != 1 || arrangements[1].Starts[0].FromOrdinal != 2 {
		t.Fatalf("the retrain restart must not change the new-enrollment ladder: %+v", arrangements)
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var refreshedTotal int64
	if err := s.db.QueryRow(ctx, `SELECT total_minor FROM calculation_drafts WHERE period_id=$1`, current).
		Scan(&refreshedTotal); err != nil || refreshedTotal != 1100 {
		t.Fatalf("a changed cumulative start must update the open draft automatically: %d %v", refreshedTotal, err)
	}
	original := calculate(current, refreshedTotal)
	setting := read()
	repeated, err := s.savePracticeRuleSettings(ctx, savePracticeRuleSettingsInput{
		ProductID: product, RuleID: setting.ID, CountMember: &retrain, ExistingPersonID: seller,
		CountStart: &setting.CountArrangements[1].Starts[0],
	})
	if err != nil || repeated.(map[string]int)["version"] != setting.Version || read().ID != setting.ID {
		t.Fatalf("saving an unchanged cumulative start must keep the same rule: %v %v", repeated, err)
	}
	var pending bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, current).Scan(&pending); err != nil || pending {
		t.Fatalf("an unchanged cumulative start must leave the existing draft ready: pending=%t %v", pending, err)
	}
	var algorithm int
	if err := s.db.QueryRow(ctx, `SELECT (practice_replay_inputs->>'version')::int FROM calculation_drafts WHERE period_id=$1`, current).Scan(&algorithm); err != nil || algorithm != practiceReplayIndependentTierMeasurement {
		t.Fatalf("draft should save the measurement behavior for refund replay: %d %v", algorithm, err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, current, original.Version); err != nil {
		t.Fatal(err)
	}
	refundAmount := int64(15000)
	refundAt := time.Date(2026, time.September, 8, 9, 0, 0, 0, shanghai)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE", SourceKey: "two-tiers",
			OrderID: "22", RefundID: "1", RecordKey: "refund:1", Version: "1", AmountMinor: &refundAmount, OccurredAt: refundAt, UpdatedAt: refundAt,
			RefundsPayment: []sourcePaymentReference{{OrderID: "22", RecordID: "22"}}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	draft := calculate(month, -450)
	if draft.RefundTotal != -450 {
		t.Fatalf("refund must use the original retrain tier with its own booked amount: %+v", draft)
	}
}

func TestPracticeSalesForSelectedCompaniesPaysSpecifiedPerson(t *testing.T) {
	var parts []component
	if err := json.Unmarshal([]byte(`[{"type":"sales","mode":"tiered","classMember":"new","sourceRole":"DIRECT_REFERRER",
	 "salesLabel":"公司专属提成","salesPersonId":99,"salesCompanies":["selected"],
	 "tiers":{"performance":[{"from":0,"mode":"rate","value":1000}]}}]`), &parts); err != nil {
		t.Fatal(err)
	}
	if err := validateComponents(parts); err != nil {
		t.Fatalf("the tier arrangement should accept an explicit company scope and payee: %v", err)
	}
	shared := parts[0]
	sharedTiers := *shared.Tiers
	sharedTiers.IncludeRetrain, shared.Tiers = true, &sharedTiers
	companyRetrain := component{ClassMember: "retrain", CompanySales: &practiceCompanySales{
		Categories: []practiceCompanyCategory{{Companies: []string{"selected"}}},
	}}
	if err := checkPracticeCompanyScopes([]component{shared, companyRetrain}); err == nil {
		t.Fatal("a shared tier and a retrain company standard must not claim the same company")
	}
	amount := int64(10000)
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	members := []practiceEnrollment{
		{ID: 1, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"101"}, SignedAt: at.Add(time.Hour)},
		{ID: 2, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"102"}, SignedAt: at.Add(time.Hour)},
	}
	fact := func(id int64, company string) paymentInput {
		order := fmt.Sprint(100 + id)
		return paymentInput{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: order, PaymentID: order,
			AmountMinor: &amount, ReferrerID: "seller", OccurredAt: at, SalesCompanyID: company}}
	}
	orders := map[string][]paymentInput{"101": {fact(1, "selected")}, "102": {fact(2, "outside")}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{PersonID: 7}}}
	draft, err := calculatePracticeDraft(members, paymentRule{ID: 10, Components: parts}, orders,
		identities, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
	if err != nil || draft.Total != 1000 || len(draft.Lines) != 1 || len(draft.Issues) != 0 ||
		draft.Lines[0].PersonID != 99 || draft.Lines[0].FactID != 1 {
		t.Fatalf("only the selected company should produce a payment to the configured recipient: %+v %v", draft, err)
	}
	if len(draft.Performance) != 2 || draft.Performance[0].PersonID != 7 || draft.Performance[1].PersonID != 7 {
		t.Fatalf("sales performance remains on the attributed seller: %+v", draft.Performance)
	}
	parts[0].Tiers = &practiceSalesTiers{CountFromOrdinal: 1, Count: []practiceTier{{Mode: "rate", Value: 1000}}}
	draft, err = calculatePracticeDraft(members, paymentRule{ID: 10, Components: parts}, orders,
		identities, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
	if err != nil || len(draft.Lines) != 0 || len(draft.Issues) != 1 || draft.Issues[0].Code != "PRACTICE_TIER_HISTORY_MISSING" {
		t.Fatalf("tier issues belong to the seller even when payment goes to a named recipient: %+v %v", draft, err)
	}
}

func TestPracticePersonalTierOverridesCompanyForThatSellerOnly(t *testing.T) {
	t.Run("订单角色收款", func(t *testing.T) { verifyPracticePersonalTiers(t, false) })
	t.Run("指定个人收款并限定销售公司", func(t *testing.T) { verifyPracticePersonalTiers(t, true) })
}

func verifyPracticePersonalTiers(t *testing.T, specifyRecipient bool) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PERSONAL_TIER", Name: "个人阶梯验收", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "person-tier"}))
	key := "PRACTICE:2:1:8"
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "班次", Ordinal: 1, StartsOn: "2026-06-08", EndsOn: "2026-06-08", SourcePeriodKey: key}))
	refundPeriod := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "退款", SettlementMonth: "2026-07"}))
	ordinary := id(s.createPerson(ctx, createPersonInput{Name: "普通销售"}))
	special := id(s.createPerson(ctx, createPersonInput{Name: "特殊标准销售"}))
	for account, person := range map[string]int64{"ordinary": ordinary, "special": special} {
		id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: account, ValidFrom: "2026-01-01T00:00:00Z"}))
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO configuration_options(group_key,option_key,label,display_order)
	 VALUES('practiceSalesCompany','30','样本公司',10)`); err != nil {
		t.Fatal(err)
	}
	rule := publishRuleInput{ProductID: product, PeriodID: &period, Label: "按个人单独选档"}
	reply := fmt.Sprintf(`[{"type":"sales","mode":"tiered","classMember":"new","sourceRole":"DIRECT_REFERRER","value":0,
	 "tiers":{"countFromOrdinal":1,"performance":[{"from":0,"mode":"rate","value":1000}],
	  "companySchedules":[{"companyId":"30","performance":[{"from":0,"mode":"rate","value":500}]}],
	  "personSchedules":[{"personId":%d,"count":[{"from":0,"mode":"fixed","value":2000},{"from":1,"mode":"fixed","value":4000}]}]}}]`, special)
	if err := json.Unmarshal([]byte(reply), &rule.Components); err != nil {
		t.Fatal(err)
	}
	var recipient int64
	if specifyRecipient {
		recipient = id(s.createPerson(ctx, createPersonInput{Name: "指定收款人"}))
		rule.Components[0].SalesPersonID = recipient
		rule.Components[0].SalesCompanies = &[]string{"30"}
	}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 8, 9, 0, 0, 0, shanghai)
	roster := practiceRoster{Key: key, Hash: "personal-tier", SourceVersion: 1, StartsAt: start, ConfirmedAt: start.Add(time.Hour)}
	amount := int64(10000)
	var facts []sourceFact
	for i, account := range []string{"ordinary", "special", "special"} {
		order := fmt.Sprint(100 + i)
		paid := start.Add(time.Duration(i-3) * time.Hour)
		roster.Enrollments = append(roster.Enrollments, practiceEnrollment{SourceEnrollmentID: order, ParticipantID: order,
			MembershipType: 3, CommissionEligible: true, SignedAt: start, PaidOrderIDs: []string{order}, ContentHash: fmt.Sprintf("%064x", i+1)})
		facts = append(facts, sourceFact{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: "person-tier",
			OrderID: order, PaymentID: order, RecordKey: "paid:" + order, Version: "1", AmountMinor: &amount,
			ReferrerID: account, SalesCompanyID: "30", OccurredAt: paid, UpdatedAt: paid})
	}
	if specifyRecipient {
		outside := facts[0]
		outside.OrderID, outside.PaymentID, outside.RecordKey, outside.SalesCompanyID = "103", "103", "paid:103", "31"
		facts = append(facts, outside)
		enrollment := roster.Enrollments[0]
		enrollment.SourceEnrollmentID, enrollment.ParticipantID, enrollment.ContentHash = "103", "103", fmt.Sprintf("%064x", 4)
		enrollment.PaidOrderIDs = []string{"103"}
		roster.Enrollments = append(roster.Enrollments, enrollment)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, facts)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	draft, err := s.calculateCurrentDraft(ctx, period, 0)
	if err != nil || draft.Total != 6500 || draft.LineCount != 3 || draft.IssueCount != 0 {
		t.Fatalf("the ordinary seller gets 500, the special seller gets 2000 and 4000: %+v %v", draft, err)
	}
	var actual []int64
	if err := s.db.QueryRow(ctx, `SELECT array_agg(l.amount_minor ORDER BY v.order_id) FROM calculation_draft_lines l
	 JOIN source_order_versions v ON v.id=l.source_version_id WHERE l.period_id=$1`, period).Scan(&actual); err != nil ||
		len(actual) != 3 || actual[0] != 500 || actual[1] != 2000 || actual[2] != 4000 {
		t.Fatalf("incorrect winner for the three payments: %v %v", actual, err)
	}
	if specifyRecipient {
		var recipients, sellers int
		if err := s.db.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM calculation_draft_lines WHERE period_id=$1 AND person_id=$2 AND identity_id IS NULL),
		 (SELECT count(*) FROM calculation_draft_performance WHERE period_id=$1 AND person_id IN ($3,$4))`,
			period, recipient, ordinary, special).Scan(&recipients, &sellers); err != nil || recipients != 3 || sellers != 4 {
			t.Fatalf("three payments to the named recipient and four seller postings: %d %d %v", recipients, sellers, err)
		}
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, draft.Version); err != nil {
		t.Fatal(err)
	}
	refundedAt := start.AddDate(0, 1, 0)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE",
			SourceKey: "person-tier", OrderID: "101", RefundID: "1", RecordKey: "refund:1", Version: "1",
			AmountMinor: &amount, OccurredAt: refundedAt, UpdatedAt: refundedAt,
			RefundsPayment: []sourcePaymentReference{{OrderID: "101", RecordID: "101"}}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	refund, err := s.calculateCurrentDraft(ctx, refundPeriod, 0)
	if err != nil || refund.RefundTotal != -4000 || refund.IssueCount != 0 {
		t.Fatalf("the original personal award and that seller's later tier difference should both be reversed: %+v %v", refund, err)
	}
	if specifyRecipient {
		var debits int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM calculation_draft_refunds refund
		 JOIN calculation_draft_lines original ON original.id=refund.original_line_id
		 WHERE refund.period_id=$1 AND original.person_id=$2`,
			refundPeriod, recipient).Scan(&debits); err != nil || debits != 2 {
			t.Fatalf("both refund adjustments should belong to the original recipient: %d %v", debits, err)
		}
	}
}

func TestPracticeRetrainSharesNewEnrollmentTierWithoutRaisingIt(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_RETRAIN_TIER", Name: "复训阶梯验收", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "tier-retrain"}))
	key := "PRACTICE:2:1:8"
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "班次", Ordinal: 1, StartsOn: "2026-06-08", EndsOn: "2026-06-08", SourcePeriodKey: key}))
	refundPeriod := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "退款", SettlementMonth: "2026-07"}))
	seller := id(s.createPerson(ctx, createPersonInput{Name: "阶梯验收销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: seller, Source: "DCZD", ExternalID: "sales", ValidFrom: "2026-01-01T00:00:00Z"}))
	policy := publishRuleInput{ProductID: product, PeriodID: &period, Label: "新报复训共用阶梯"}
	if err := json.Unmarshal([]byte(`[{"type":"sales","mode":"tiered","classMember":"new","sourceRole":"DIRECT_REFERRER","value":0,
	 "tiers":{"includeRetrain":true,"countFromOrdinal":1,"count":[{"from":1,"mode":"rate","value":2000,"retrain":{"mode":"fixed","value":3000}}],
	  "performance":[{"from":0,"mode":"rate","value":1000,"retrain":{"mode":"fixed","value":1000}},
	                   {"from":20000,"mode":"rate","value":1500,"retrain":{"mode":"fixed","value":2000}}]}}]`), &policy.Components); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, policy); err != nil {
		t.Fatal(err)
	}
	separateRetrain := component{Type: "sales", Mode: "fixed", ClassMember: "retrain", SourceRole: "DIRECT_REFERRER", Value: 1500}
	overlapping := policy
	overlapping.Components = append([]component{separateRetrain}, policy.Components...)
	if _, err := s.publishRule(ctx, overlapping); !errors.Is(err, invalid) {
		t.Fatalf("shared tier and independent retraining sale must not overlap: %v", err)
	}
	at := time.Date(2026, 6, 8, 9, 0, 0, 0, shanghai)
	roster := practiceRoster{Key: key, SourceVersion: 1, Hash: "tier-retrain", StartsAt: at, ConfirmedAt: at.Add(time.Hour)}
	var facts []sourceFact
	for i, member := range []int{2, 3, 2} {
		order := fmt.Sprint(100 + i)
		amount := int64(50000)
		if member == 3 {
			amount = 10000
		}
		paid := at.Add(time.Duration(i-3) * time.Hour)
		roster.Enrollments = append(roster.Enrollments, practiceEnrollment{SourceEnrollmentID: order, ParticipantID: order,
			MembershipType: member, CommissionEligible: true, SignedAt: at, PaidOrderIDs: []string{order}, ContentHash: fmt.Sprintf("%064x", i+1)})
		facts = append(facts, sourceFact{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: "tier-retrain",
			OrderID: order, PaymentID: order, RecordKey: "paid:" + order, Version: "1", AmountMinor: &amount,
			ReferrerID: "sales", OccurredAt: paid, UpdatedAt: paid})
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, facts)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	draft, err := s.calculateCurrentDraft(ctx, period, 0)
	if err != nil || draft.Total != 5000 || draft.LineCount != 3 || draft.IssueCount != 0 {
		t.Fatalf("before the new enrollment use the lower tier and after it use the higher tier: %+v %v", draft, err)
	}
	var amounts []int64
	if err := s.db.QueryRow(ctx, `SELECT array_agg(l.amount_minor ORDER BY v.order_id) FROM calculation_draft_lines l
	 JOIN source_order_versions v ON v.id=l.source_version_id WHERE l.period_id=$1`, period).Scan(&amounts); err != nil ||
		len(amounts) != 3 || amounts[0] != 1000 || amounts[1] != 1000 || amounts[2] != 3000 {
		t.Fatalf("unexpected awards %v, %v", amounts, err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, draft.Version); err != nil {
		t.Fatal(err)
	}
	refundAt := at.AddDate(0, 1, 0)
	refund := sourceFact{Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE", SourceKey: "tier-retrain",
		OrderID: "101", RefundID: "1", RecordKey: "refund:1", Version: "1", AmountMinor: facts[1].AmountMinor,
		OccurredAt: refundAt, UpdatedAt: refundAt, RefundsPayment: []sourcePaymentReference{{OrderID: "101", RecordID: "101"}}}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{refund})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after, err := s.calculateCurrentDraft(ctx, refundPeriod, 0)
	if err != nil || after.IssueCount != 0 || after.RefundTotal != -3000 {
		t.Fatalf("refund must reverse the new award and the tier difference on the later retraining enrollment: %+v %v", after, err)
	}
}

func TestPracticeTierFallbackUsesOnlyBelowThresholdEnrollmentBase(t *testing.T) {
	config := practiceSalesTiers{
		CountFromOrdinal: 156,
		Count:            []practiceTier{{From: 500, Mode: "rate", Value: 1900}},
		Performance:      []practiceTier{{Mode: "rate", Value: 1200}, {From: 20000, Mode: "rate", Value: 1500}},
	}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	purchases := []practiceTierPurchase{
		{EnrollmentID: 13, SequenceKey: "33", PaidAt: at.Add(time.Hour), PersonID: 9, Base: 30000},
		{EnrollmentID: 11, SequenceKey: "31", PaidAt: at, PersonID: 9, Base: 10000},
		{EnrollmentID: 12, SequenceKey: "32", PaidAt: at, PersonID: 8, Base: 6000},
	}
	awards, err := choosePracticeSalesTiers(config, purchases, map[int64]int64{9: 499})
	if err != nil || awards[11].Value != 1200 || awards[13].Value != 1900 || awards[12].Value != 1200 {
		t.Fatalf("awards=%+v error=%v", awards, err)
	}
}

func TestPracticeTierPerformanceUsesSamePersonPeriodTotal(t *testing.T) {
	config := practiceSalesTiers{Performance: []practiceTier{{Mode: "rate", Value: 1000}, {From: 20000, Mode: "fixed", Value: 1700}}}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	purchases := []practiceTierPurchase{
		{EnrollmentID: 1, PersonID: 9, PaidAt: at, Base: 14000},
		{EnrollmentID: 2, PersonID: 9, PaidAt: at, Base: 6000},
		{EnrollmentID: 3, PersonID: 8, PaidAt: at, Base: 6000},
	}
	awards, err := choosePracticeSalesTiers(config, purchases, nil)
	if err != nil || awards[1].Mode != "fixed" || awards[2].Value != 1700 || awards[3].Value != 1000 {
		t.Fatalf("awards=%+v error=%v", awards, err)
	}
}

func TestPracticeTierChangesWithPastRosterAndTimeBoundAccount(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t)}
	mustID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	product := mustID(service.createProduct(ctx, createProductInput{Code: "PRACTICE", Name: "Practice", PeriodMode: "class"}))
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	current := mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "Second", Ordinal: 2, StartsOn: "2026-08-08", EndsOn: "2026-08-08", SourcePeriodKey: "PRACTICE:1:1:11"}))
	former := mustID(service.createPerson(ctx, createPersonInput{Name: "Former"}))
	replacement := mustID(service.createPerson(ctx, createPersonInput{Name: "Replacement"}))
	handover := time.Date(2026, 8, 7, 1, 0, 31, 0, time.UTC)
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: former, Source: "DCZD", ExternalID: "shared-account", ValidFrom: "2026-06-01T00:00:00+08:00"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: replacement, Source: "DCZD", ExternalID: "shared-account", ValidFrom: handover.Format(time.RFC3339)}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: former, Source: "DCZD", ExternalID: "another-account", ValidFrom: "2026-06-01T00:00:00+08:00"}))
	publish := func(tiers practiceSalesTiers) {
		t.Helper()
		_, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Personal sales", PeriodID: &current,
			Components: []component{{Type: "sales", ClassMember: "new", Mode: "tiered", SourceRole: "DIRECT_REFERRER", Tiers: &tiers}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	tiers := practiceSalesTiers{
		CountFromOrdinal: 1,
		Count:            []practiceTier{{From: 1, Mode: "rate", Value: 1900}},
		Performance:      []practiceTier{{Mode: "rate", Value: 1200}},
	}
	publish(tiers)

	addPayment := func(id, account string, at time.Time, amount int64) {
		t.Helper()
		fact := sourceFact{Source: "DCZD", Kind: "ORDER_PAID", OrderID: id, PaymentID: id, RecordKey: id, Version: "1",
			OccurredAt: at, UpdatedAt: at, Domain: "PRACTICE", SourceKey: "practice", AmountMinor: &amount, ReferrerID: account}
		_, err := service.db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
		 VALUES('DCZD',$1,'',$1,'1','ORDER_PAID',$2,$3,$2)`, id, at, fact)
		if err != nil {
			t.Fatal(err)
		}
	}
	firstPayment := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	addPayment("31", "another-account", firstPayment, 4000)
	addPayment("32", "another-account", firstPayment, 6000)
	addPayment("33", "shared-account", handover.Add(-time.Second), 10000)
	addPayment("34", "shared-account", handover, 10000)

	member := func(id string, paid ...string) practiceEnrollmentWire {
		row := practiceEnrollmentWire{SourceEnrollmentID: id, ParticipantID: id, MembershipType: 3}
		if len(paid) > 0 {
			row.CommissionEligible, row.PaidOrderIDs, row.SourceOrderID = true, &paid, &paid[len(paid)-1]
		}
		return row
	}
	writeRoster := func(activity, version int64, day int, members ...practiceEnrollmentWire) {
		t.Helper()
		start := time.Date(2026, 8, day, 4, 0, 0, 0, time.UTC)
		for i := range members {
			members[i].SignedAt = start.Add(time.Hour).Format(time.RFC3339)
		}
		wire := practiceRosterWire{ContractVersion: "dczd.practice-roster/1.0", SourceSystem: "DCZD", ActivityType: 1, StrategyID: 1,
			ActivityID: activity, PeriodKey: fmt.Sprintf("PRACTICE:1:1:%d", activity), RosterVersion: version, Enrollments: members,
			PeriodStartAt: start.Format(time.RFC3339), ConfirmedAt: start.Add(2 * time.Hour).Format(time.RFC3339), ConfirmedBy: "1"}
		wire.RosterHash = practiceRosterHash(members)
		roster, err := normalizePracticeRoster(wire)
		if err != nil {
			t.Fatal(err)
		}
		if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			_, err := storePracticeRoster(ctx, tx, roster)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(version, total int64, issues int) {
		t.Helper()
		result, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: current})
		if err != nil {
			t.Fatal(err)
		}
		draft := result.(draftSummary)
		if draft.Version != version || draft.Total != total || draft.IssueCount != issues {
			t.Fatalf("draft=%+v", draft)
		}
	}
	writeRoster(11, 1, 8, member("12", "33"), member("13", "34"))
	check(1, 0, 2)
	tiers.PersonCountStarts = []practicePersonCountStart{{PersonID: former, practiceCountStart: practiceCountStart{FromOrdinal: 2}}}
	publish(tiers)
	check(2, 1200, 1) // Only the person with a current-period reset is ready.
	writeRoster(10, 1, 1, member("11", "31", "32"))
	mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "First", Ordinal: 1, StartsOn: "2026-08-01", EndsOn: "2026-08-01", SourcePeriodKey: "PRACTICE:1:1:10"}))
	if err := service.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var version, total int64
	if err := service.db.QueryRow(ctx, `SELECT version,total_minor FROM calculation_drafts WHERE period_id=$1`, current).Scan(&version, &total); err != nil || version != 3 || total != 2400 {
		t.Fatalf("backfilled period did not refresh the cumulative tier: version=%d total=%d error=%v", version, total, err)
	}
	check(3, 2400, 0)
	var paidBefore, paidAt int64
	if err := service.db.QueryRow(ctx, `SELECT
	 max(l.person_id) FILTER (WHERE p.order_id='33'),
	 max(l.person_id) FILTER (WHERE p.order_id='34')
	 FROM calculation_draft_lines l JOIN source_order_versions p ON p.id=l.source_version_id WHERE l.period_id=$1`,
		current).Scan(&paidBefore, &paidAt); err != nil || paidBefore != former || paidAt != replacement {
		t.Fatalf("incorrect handover attribution: before=%d at=%d error=%v", paidBefore, paidAt, err)
	}
	tiers.PersonCountStarts[0].FromOrdinal = 3
	publish(tiers)
	check(4, 3100, 0) // A future reset leaves an already opened period on the default.
	check(4, 3100, 0)
	var priorCounts map[int64]int64
	if err := service.db.QueryRow(ctx, `SELECT practice_replay_inputs->'priorCounts' FROM calculation_drafts WHERE period_id=$1`,
		current).Scan(&priorCounts); err != nil || len(priorCounts) != 2 || priorCounts[former] != 1 || priorCounts[replacement] != 0 {
		t.Fatalf("expected the selected cumulative counts for both salespeople, got %v (%v)", priorCounts, err)
	}
	exactFirstClass := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	tiers.PersonCountStarts[0].practiceCountStart = practiceCountStart{FromAt: &exactFirstClass}
	publish(tiers)
	check(5, 3100, 0) // The cutoff includes a class opening exactly at the configured time.
	afterFirstClass := exactFirstClass.Add(time.Second)
	tiers.PersonCountStarts[0].FromAt = &afterFirstClass
	publish(tiers)
	check(6, 2400, 0)
	duplicate := member("14", "31", "32")
	duplicate.MembershipType = 2
	writeRoster(10, 2, 1, member("11", "31", "32"), duplicate)
	check(7, 1200, 1) // The duplicate is outside only the reset person's window.

	tiers.PersonCountStarts = nil
	tiers.CountFromOrdinal, tiers.CountFromAt = 0, &afterFirstClass
	publish(tiers)
	check(8, 2400, 0) // Existing default time-based rules still select the same history.

	const formerLegacy = "f8bdd870-7f83-413b-ba52-4a97ab33d301"
	const replacementLegacy = "f8bdd870-7f83-413b-ba52-4a97ab33d302"
	if _, err := service.db.Exec(ctx, `UPDATE people SET legacy_party_id=CASE WHEN id=$1 THEN $3::uuid ELSE $4::uuid END WHERE id IN ($1,$2)`,
		former, replacement, formerLegacy, replacementLegacy); err != nil {
		t.Fatal(err)
	}
	one, zero := int64(1), int64(0)
	historical := practiceCountOpeningFile{ProductID: product, ThroughAt: exactFirstClass,
		SourceSettlementID: "f8bdd870-7f83-413b-ba52-4a97ab33d303",
		People:             []practiceCountOpeningPerson{{LegacyPartyID: formerLegacy, NewCount: &one}, {LegacyPartyID: replacementLegacy, NewCount: &zero}},
	}
	addOpening := func(opening practiceCountOpeningFile) int64 {
		t.Helper()
		var id int64
		err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			var err error
			id, _, err = storePracticeCountOpening(ctx, tx, opening)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	tiers.OpeningID, tiers.CountFromAt = addOpening(historical), nil
	publish(tiers)
	check(9, 3100, 0) // Opening carries the first signup. History right at the cutoff is already included.
	tiers.PersonCountStarts = []practicePersonCountStart{{PersonID: former, practiceCountStart: practiceCountStart{FromOrdinal: 2}}}
	publish(tiers)
	check(10, 2400, 0) // The reset excludes the imported count for this person.
	historical.SourceSettlementID = "f8bdd870-7f83-413b-ba52-4a97ab33d304"
	historical.People = historical.People[:1]
	tiers.OpeningID, tiers.PersonCountStarts = addOpening(historical), nil
	publish(tiers)
	check(11, 1900, 1) // Missing opening is different from the explicit zero tested above.

	writeRoster(10, 3, 1, member("11", "31", "32"))
	historical.SourceSettlementID = "f8bdd870-7f83-413b-ba52-4a97ab33d305"
	historical.ThroughAt = exactFirstClass.Add(-time.Second)
	historical.People = append(historical.People, practiceCountOpeningPerson{LegacyPartyID: replacementLegacy, NewCount: &zero})
	tiers.OpeningID = addOpening(historical)
	tiers.Count = append(tiers.Count, practiceTier{From: 2, Mode: "rate", Value: 2500})
	publish(tiers)
	check(12, 3700, 0) // A later class is included on top of the frozen opening.

	tiers.OpeningID, tiers.CountFromAt = 0, &afterFirstClass
	tiers.PersonCountStarts = []practicePersonCountStart{{PersonID: 999999, practiceCountStart: practiceCountStart{FromOrdinal: 2}}}
	_, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Missing person", PeriodID: &current,
		Components: []component{{Type: "sales", ClassMember: "new", Mode: "tiered", SourceRole: "DIRECT_REFERRER", Tiers: &tiers}}})
	if !errors.Is(err, errPracticeCountPerson) {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestPracticeTimeStartUsesActualClassOrderAndRefreshesAfterDelay(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_DATE", Name: "按开班时间累计", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "dated"}))
	seller := id(s.createPerson(ctx, createPersonInput{Name: "销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: seller, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-06-01T00:00:00+08:00"}))
	start := time.Date(2026, 8, 8, 8, 0, 0, 0, shanghai)
	periodKey := func(ordinal int) string { return fmt.Sprintf("PRACTICE:1:1:%d", ordinal) }
	otherSeller := id(s.createPerson(ctx, createPersonInput{Name: "按期数累计的销售"}))
	tomorrow := start.AddDate(0, 0, 1).Format(time.DateOnly)
	id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "延期后尚无名单", Ordinal: 3,
		StartsOn: tomorrow, EndsOn: tomorrow, SourcePeriodKey: periodKey(3)}))
	writeRoster := func(ordinal, version int, at time.Time) {
		t.Helper()
		number := fmt.Sprint(ordinal)
		member := practiceEnrollment{SourceEnrollmentID: number, ParticipantID: number, MembershipType: 3, CommissionEligible: true,
			PaidOrderIDs: []string{number}, SignedAt: at.Add(time.Minute), ContentHash: fmt.Sprintf("%02x%02x", ordinal, version)}
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: periodKey(ordinal), Hash: member.ContentHash,
				SourceVersion: int64(version), StartsAt: at, ConfirmedAt: at.Add(time.Hour), Enrollments: []practiceEnrollment{member}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var current int64
	for _, entry := range []struct{ ordinal, hours int }{{4, 3}, {5, 2}, {6, 1}} {
		day := start.Format(time.DateOnly)
		period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: periodKey(entry.ordinal), Ordinal: entry.ordinal,
			StartsOn: day, EndsOn: day, SourcePeriodKey: periodKey(entry.ordinal)}))
		if entry.ordinal == 5 {
			current = period
		}
		order, amount := fmt.Sprint(entry.ordinal), int64(10000)
		paid := start.Add(-time.Hour)
		fact := sourceFact{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: "dated", Version: "1",
			OrderID: order, PaymentID: order, RecordKey: "paid:" + order, ReferrerID: "seller", AmountMinor: &amount, OccurredAt: paid, UpdatedAt: paid}
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		writeRoster(entry.ordinal, 1, start.Add(time.Duration(entry.hours)*time.Hour))
	}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "按时间累计", Components: []component{{
		Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER",
		Tiers: &practiceSalesTiers{CountFromAt: &start, Count: []practiceTier{{From: 0, Mode: "rate", Value: 100}, {From: 1, Mode: "rate", Value: 200}},
			PersonCountStarts: []practicePersonCountStart{{PersonID: otherSeller, practiceCountStart: practiceCountStart{FromOrdinal: 3}}}},
	}}}); err != nil {
		t.Fatal(err)
	}
	result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: current})
	if err != nil || result.(draftSummary).Total != 200 || result.(draftSummary).IssueCount != 0 {
		t.Fatalf("only the earlier opening (class 6 at 09:00) should enter the 10:00 draft: %+v %v", result, err)
	}
	before := result.(draftSummary)
	writeRoster(6, 2, start.Add(4*time.Hour))
	var queued bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1 AND retry_on_scan)`, current).Scan(&queued); err != nil || !queued {
		t.Fatalf("a later ordinal moved out of the history must queue the open draft: %v %v", queued, err)
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var version, total, issues int64
	if err := s.db.QueryRow(ctx, `SELECT version,total_minor,issue_count FROM calculation_drafts WHERE period_id=$1`, current).
		Scan(&version, &total, &issues); err != nil || version <= before.Version || total != 100 || issues != 0 {
		t.Fatalf("after the delay the later class should be excluded from the refreshed tier: %d %d %d %v", version, total, issues, err)
	}
}

func TestPracticeTierRangesCanHaveGapsAndRejectOverlap(t *testing.T) {
	through := int64(1)
	config := practiceSalesTiers{CountFromOrdinal: 1, Count: []practiceTier{
		{From: 1, Through: &through, Mode: "fixed", Value: 100}, {From: 3, Mode: "rate", Value: 1900},
	}}
	part := component{Type: "sales", ClassMember: "new", Mode: "tiered", SourceRole: "DIRECT_REFERRER", Tiers: &config}
	if !validPracticeSalesTiers(part) {
		t.Fatal("a deliberately empty range must be configurable")
	}
	if _, ok := matchingPracticeTier(config.Count, 2); ok {
		t.Fatal("the empty range selected a tier")
	}
	if tier, ok := matchingPracticeTier(config.Count, 3); !ok || tier.Value != 1900 {
		t.Fatalf("tier=%+v matched=%t", tier, ok)
	}
	through = 3
	if validPracticeSalesTiers(part) {
		t.Fatal("an overlapping range must be rejected")
	}
}
