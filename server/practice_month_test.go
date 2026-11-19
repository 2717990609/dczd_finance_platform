package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeRefundMonthSettlementUsesBoundOrderCoverage(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), sources: map[string]sourceConfig{
		"DCZD": {BaseURL: "http://127.0.0.1:9126", Token: "isolated-order-fixture"},
	}}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_CLOSE", Name: "实战班", PeriodMode: "class"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "结算人员"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, SettlementMonth: "2026-08", Name: "八月退款"}))
	if _, err := s.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	value, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	draft := value.(draftSummary)
	review := func() settlementReview {
		t.Helper()
		w := httptest.NewRecorder()
		s.getSettlementReview(w, httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", period, draft.Version), nil))
		var got settlementReview
		if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil || len(got.Sources) != 1 {
			t.Fatalf("expected a DCZD source review: %d %s", w.Code, w.Body.String())
		}
		return got
	}
	if r := review(); r.Sources[0].Status != "NOT_STARTED" || r.Token != "" {
		t.Fatalf("a draft without synchronized orders must be blocked: %+v", r)
	}
	key := s.orderSourceKey("DCZD")
	if _, err := s.db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,completed_through,last_success_at,next_updated_at,next_id)
	 VALUES($1,$2,'2026-08-01',now(),now(),now(),'0')`, dczdOrderStream, key); err != nil {
		t.Fatal(err)
	}
	if r := review(); r.Sources[0].Status != "SOURCE_CHANGED" || r.Token != "" {
		t.Fatalf("checkpoint alone cannot bind the provider: %+v", r)
	}
	if _, err := s.db.Exec(ctx, "UPDATE source_write_state SET provider_key=$1 WHERE source='DCZD'", key); err != nil {
		t.Fatal(err)
	}
	if r := review(); r.Sources[0].Status != "OUTSIDE_RANGE" || r.Token != "" {
		t.Fatalf("synchronizing from August 1 misses the start of the August account window: %+v", r)
	}
	if _, err := s.db.Exec(ctx, "UPDATE dcai_order_sync SET initial_from='2026-07-01',last_success_at=now()-interval '2 hours',completed_through=now()-interval '2 hours'"); err != nil {
		t.Fatal(err)
	}
	if r := review(); r.Sources[0].Status != "STALE" || r.Token != "" {
		t.Fatalf("stale synchronized orders must block close: %+v", r)
	}
	if _, err := s.db.Exec(ctx, "UPDATE dcai_order_sync SET last_success_at=now(),completed_through=now()"); err != nil {
		t.Fatal(err)
	}
	if r := review(); r.Sources[0].Status != "READY" || r.Token == "" || r.Sources[0].Stream != dczdOrderStream {
		t.Fatalf("expected complete DCZD coverage: %+v", r)
	}
	id(s.createPeriod(ctx, createPeriodInput{ProductID: product, SettlementMonth: "2026-06", Name: "六月退款"}))
	if r := review(); r.Sources[0].Status != "OUTSIDE_RANGE" || r.Token != "" {
		t.Fatalf("a later checkpoint start must not hide refunds in an earlier open month: %+v", r)
	}
	if _, err := s.db.Exec(ctx, "UPDATE dcai_order_sync SET initial_from='2026-05-01'"); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	member := practiceEnrollment{SourceEnrollmentID: "10", ParticipantID: "10", MembershipType: 3,
		CommissionEligible: true, PaidOrderIDs: []string{"10"}, SignedAt: start, ContentHash: "aa"}
	roster := practiceRoster{Key: "PRACTICE:1:1:6", Hash: "first", SourceVersion: 1,
		StartsAt: start, ConfirmedAt: start.Add(time.Hour), Enrollments: []practiceEnrollment{member}}
	amount, happened := int64(1000), start.AddDate(0, 0, 5)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
			return err
		}
		_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{{
			Source: "DCZD", Kind: "REFUND_SUCCEEDED", OrderID: "10", RecordKey: "refund:1", RefundID: "1", Version: "1",
			Domain: "PRACTICE", SourceKey: "practice", OccurredAt: happened, UpdatedAt: happened, AmountMinor: &amount,
			RefundsPayment: []sourcePaymentReference{{OrderID: "10", RecordID: "10"}},
		}}, key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := review(); len(r.PracticeDependencies) != 1 || r.PracticeDependencies[0].Name != roster.Key || r.Token != "" {
		t.Fatalf("the synchronized refund must expose even a roster without its finance period: %+v", r)
	}
	class := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "待结算实战班", Ordinal: 1,
		StartsOn: "2026-07-24", EndsOn: "2026-07-24", SourcePeriodKey: "PRACTICE:1:1:6"}))
	if r := review(); len(r.PracticeDependencies) != 1 || r.PracticeDependencies[0].PeriodID != class || r.Token != "" {
		t.Fatalf("cannot silently exclude the late refund from the still-open class: %+v", r)
	}
	roster.SourceVersion, roster.Hash, roster.Enrollments = 2, "new", nil
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, roster)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	value, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	draft = value.(draftSummary)
	r := review()
	if r.Token == "" || len(r.PracticeDependencies) != 0 {
		t.Fatalf("a refund no longer associated with a qualifying roster must not block close: %+v", r)
	}
	verifySettlementHTTP(t, ctx, s, person, product, draft, r.Token)
}

func TestPracticeRefundUsesFinancialMonthAndKeepsFrozenClass(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_MONTH", Name: "实战班", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	clock := time.Now().In(shanghai)
	if clock.Day() >= 25 {
		clock = time.Date(clock.Year(), clock.Month()+1, 1, 0, 0, 0, 0, shanghai)
	}
	processingMonth := clock.Format("2006-01")
	oldMonth := clock.AddDate(0, -3, 0).Format("2006-01")
	august := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "发生月退款", SettlementMonth: oldMonth}))
	november := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "当前月退款", SettlementMonth: processingMonth}))
	expectedWindow, err := calendarWindow(oldMonth, 25)
	if err != nil {
		t.Fatal(err)
	}
	var first time.Time
	if err := s.db.QueryRow(ctx, "SELECT starts_on FROM periods WHERE id=$1", august).Scan(&first); err != nil || first.Format(time.DateOnly) != expectedWindow.Start.Format(time.DateOnly) {
		t.Fatalf("expected 25–24 calendar, got %v (%v)", first, err)
	}
	at := expectedWindow.Start.AddDate(0, -1, 5).Add(time.Hour)
	classDate := at.AddDate(0, 0, 4).Format(time.DateOnly)
	class := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "已确认实战班", Ordinal: clock.Year()*100 + int(clock.Month()),
		StartsOn: classDate, EndsOn: classDate, SourcePeriodKey: "PRACTICE:1:1:5"}))
	seller := id(s.createPerson(ctx, createPersonInput{Name: "销售甲"}))
	id(s.assignIdentity(ctx, assignIdentityInput{Source: "DCZD", ExternalID: "seller", PersonID: seller, ValidFrom: at.Add(-time.Hour).Format(time.RFC3339)}))
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &class, Label: "原班次规则", Components: []component{
		{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "DIRECT_REFERRER", ClassMember: "new"},
	}}); err != nil {
		t.Fatal(err)
	}
	start, _ := time.ParseInLocation(time.DateOnly, classDate, shanghai)
	paid := int64(10000)
	payment := sourceFact{Source: "DCZD", RecordKey: "payment:10", OrderID: "10", PaymentID: "10", Kind: "ORDER_PAID", Version: "1",
		Domain: "PRACTICE", SourceKey: "practice", AmountMinor: &paid, OccurredAt: at, UpdatedAt: at, ReferrerID: "seller"}
	refund := func(ref string, amount int64, happened time.Time) sourceFact {
		return sourceFact{Source: "DCZD", RecordKey: "refund:" + ref, OrderID: "10", RefundID: ref, Kind: "REFUND_SUCCEEDED", Version: "1",
			Domain: "PRACTICE", SourceKey: "practice", AmountMinor: &amount, OccurredAt: happened, UpdatedAt: happened, RefundsPayment: []sourcePaymentReference{{OrderID: "10", RecordID: "10"}}}
	}
	change := func(facts ...sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { _, err := storeSourceFacts(ctx, tx, facts); return err }); err != nil {
			t.Fatal(err)
		}
	}
	change(payment, refund("1", 1000, start.Add(-time.Hour)))
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: "PRACTICE:1:1:5", Hash: "monthly-sample", SourceVersion: 1, StartsAt: start, ConfirmedAt: start.Add(time.Hour),
			Enrollments: []practiceEnrollment{{SourceEnrollmentID: "1", ParticipantID: "2", MembershipType: 3, CommissionEligible: true,
				SignedAt: start, PaidOrderIDs: []string{"10"}, ContentHash: "ab"}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	calculate := func(period int64) draftSummary {
		t.Helper()
		raw, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		return raw.(draftSummary)
	}
	freeze := func(d draftSummary) {
		t.Helper()
		// Seed an immutable posted result for exercising the downstream month workflow.
		if _, err := s.db.Exec(ctx, "INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')", d.PeriodID, d.Version); err != nil {
			t.Fatal(err)
		}
	}
	original := calculate(class)
	if original.Total != 900 || original.IssueCount != 0 {
		t.Fatalf("pre-confirmation refund must already be included: %+v", original)
	}
	freeze(original)
	calculate(august)
	calculate(november)
	after := expectedWindow.Start.AddDate(0, 0, 6).Add(time.Hour)
	change(refund("2", 2000, after))
	var queued bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)", august).Scan(&queued); err != nil || !queued {
		t.Fatalf("incoming refund did not queue the actual refund month: %t %v", queued, err)
	}
	open := calculate(august)
	if open.Total != -200 || open.RefundCount != 1 || open.IssueCount != 0 {
		t.Fatalf("open refund month: %+v", open)
	}
	if current := calculate(november); current.Total != 0 || current.RefundCount != 0 {
		t.Fatalf("refund must stay in its open month: %+v", current)
	}
	freeze(open)
	change(refund("3", 1500, after.Add(time.Hour)))
	late := calculate(november)
	if late.Total != -150 || late.RefundCount != 1 || late.IssueCount != 0 {
		t.Fatalf("late August refund should go in current month: %+v", late)
	}
	freeze(late)
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", august); err == nil {
		t.Fatal("a prior monthly debit cannot be removed while a later debit for the same payment is settled")
	}
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", november); err != nil {
		t.Fatalf("technical correction must allow deleting the latest monthly debit: %v", err)
	}
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", august); err != nil {
		t.Fatalf("the earlier debit should reopen after its dependent settlement is removed: %v", err)
	}
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)", november).Scan(&queued); err != nil || !queued {
		t.Fatalf("reopening August must refresh November's still-readable draft: %t %v", queued, err)
	}
	freeze(open)
	var detail json.RawMessage
	if err := s.db.QueryRow(ctx, draftDetailSQL, november, 0, 0, late.Version, 0, 0).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var view struct {
		Refunds []struct {
			OriginalPeriod, RefundPeriod, PostingPeriod, PersonName string
			RuleVersion                                             int64
		}
	}
	if err := json.Unmarshal(detail, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Refunds) != 1 || fmt.Sprint(view.Refunds[0]) != "{已确认实战班 发生月退款 当前月退款 销售甲 1}" {
		t.Fatalf("expected original, refund and posting month and original recipient, got %+v", view)
	}
	if repeat := calculate(november); repeat.Version != late.Version {
		t.Fatalf("same input must reuse same draft: %+v", repeat)
	}
	// The payment system later supplies a refund that was already successful before
	// roster confirmation. The frozen class had never included this distinct refund.
	change(refund("4", 1000, start.Add(time.Minute)))
	if waiting := calculate(november); waiting.IssueCount != 1 || waiting.Total != -150 {
		t.Fatalf("late refund with no registered month must stay visible: %+v", waiting)
	}
	earlier := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "补建退款月",
		SettlementMonth: expectedWindow.Start.Format("2006-01")}))
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)", november).Scan(&queued); err != nil || !queued {
		t.Fatalf("registering the missing month must refresh the current period: %t %v", queued, err)
	}
	if booked := calculate(earlier); booked.Total != -100 || booked.RefundCount != 1 || booked.IssueCount != 0 {
		t.Fatalf("unrecorded pre-roster refund must go to its original open month: %+v", booked)
	}
	if resolved := calculate(november); resolved.Total != -150 || resolved.RefundCount != 1 || resolved.IssueCount != 0 {
		t.Fatalf("the current month must clear the missing-month issue: %+v", resolved)
	}
	updated := refund("2", 2200, after)
	updated.Version = "2"
	updated.UpdatedAt = time.Now()
	change(updated)
	if amended := calculate(november); amended.Total != -150 || amended.RefundCount != 1 || amended.IssueCount != 1 {
		t.Fatalf("a source revision to an already posted refund must be pending: %+v", amended)
	}
	var originalMinor int64
	if err := s.db.QueryRow(ctx, "SELECT total_minor FROM calculation_drafts WHERE period_id=$1", class).Scan(&originalMinor); err != nil || originalMinor != 900 {
		t.Fatalf("posted class changed: %d %v", originalMinor, err)
	}
}

func TestPracticeRefundUsesFrozenSinglePaymentTerms(t *testing.T) {
	rate := int64(400000)
	company := component{Type: "company_sales", Mode: "company_sales", ClassMember: "new",
		CompanySales: &practiceCompanySales{Categories: []practiceCompanyCategory{
			{Label: "类别", Companies: []string{"company"}, practiceCompanyValue: practiceCompanyValue{Mode: "rate", Value: rate}},
		}}}
	line := refundObligation{Base: 10000, Accrued: 4000, ComponentType: "period:company_sales:0"}
	policy := practiceMonthlyRefundPolicy([]component{company}, line, 1, 3, "company")
	got, err := refundDelta(policy, line.Base, line.Accrued, 2500, 0)
	if err != nil || got != -1000 {
		t.Fatalf("expected exact company deduction, got %d %v", got, err)
	}
	policy = practiceMonthlyRefundPolicy([]component{company}, line, 2, 3, "company")
	if _, err := refundDelta(policy, line.Base, line.Accrued, 2500, 0); !errors.Is(err, errPracticeRefundReplay) {
		t.Fatalf("split enrollment must wait for the original class replay: %v", err)
	}
	aggregate := component{Type: "delivery", PeriodBenefit: &practicePeriodBenefit{TierOnWholeClass: true}}
	policy = practiceMonthlyRefundPolicy([]component{company, aggregate}, line, 1, 3, "company")
	if _, err := refundDelta(policy, line.Base, line.Accrued, 2500, 0); !errors.Is(err, errPracticeRefundReplay) {
		t.Fatalf("dependent period terms must wait for the original class replay: %v", err)
	}
}

func TestPracticeRefundReplaysSettledClassTierAndFullyRefundedDeposit(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), sources: map[string]sourceConfig{
		"DCZD": {BaseURL: "http://127.0.0.1:9126", Token: "isolated-order-fixture"},
	}}
	sourceKey := s.orderSourceKey("DCZD")
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_REPLAY", Name: "实战班", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	class := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "六月实战班", Ordinal: 1,
		StartsOn: "2026-06-12", EndsOn: "2026-06-12", SourcePeriodKey: "PRACTICE:1:1:6"}))
	august := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月退款", SettlementMonth: "2026-08"}))
	at := time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC)
	seller := id(s.createPerson(ctx, createPersonInput{Name: "结算销售"}))
	later := id(s.createPerson(ctx, createPersonInput{Name: "接任销售"}))
	const unit = "41000000-0000-4000-8000-000000000010"
	const oldDept = "41000000-0000-4000-8000-000000000011"
	const newDept = "41000000-0000-4000-8000-000000000012"
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,kind,name,status)
	 VALUES($1,NULL,'COMPANY','交付公司','ACTIVE'),($2,$1,'DEPARTMENT','结算部门','ACTIVE'),($3,$1,'DEPARTMENT','调动后部门','ACTIVE')`,
		unit, oldDept, newDept); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
	 VALUES('41000000-0000-4000-8000-000000000013',$1,$2,'PRIMARY',$3)`, seller, oldDept, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	pool := id(s.createPracticePool(ctx, createPracticePoolInput{ProductID: product, UnitID: unit, Name: "交付池"}))
	id(s.assignIdentity(ctx, assignIdentityInput{Source: "DCZD", ExternalID: "seller", PersonID: seller, ValidFrom: at.Add(-time.Hour).Format(time.RFC3339)}))
	publish := func(parts []component) {
		t.Helper()
		if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &class, Label: "实战班销售阶梯", Components: parts}); err != nil {
			t.Fatal(err)
		}
	}
	through := int64(14999)
	publish([]component{
		{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER",
			Tiers: &practiceSalesTiers{Performance: []practiceTier{{Mode: "rate", Value: 1000}, {From: 15000, Mode: "rate", Value: 2000}}}},
		{Type: "delivery", Mode: "period_rate",
			PeriodBenefit: &practicePeriodBenefit{Label: "全班交付", Pool: &practicePoolTarget{PoolID: pool, OwnerID: seller},
				TierOnWholeClass: true, Tiers: []practicePeriodRate{{Through: &through, Value: 100000}, {From: 15000, Value: 200000}}}},
	})
	paid := func(order string, amount int64) sourceFact {
		return sourceFact{Source: "DCZD", RecordKey: "payment:" + order, OrderID: order, PaymentID: order, Kind: "ORDER_PAID",
			Version: "1", Domain: "PRACTICE", SourceKey: "practice", AmountMinor: &amount, OccurredAt: at, UpdatedAt: at, ReferrerID: "seller"}
	}
	refund := func(order, key string, amount int64, happened time.Time) sourceFact {
		return sourceFact{Source: "DCZD", RecordKey: "refund:" + key, OrderID: order, RefundID: key, Kind: "REFUND_SUCCEEDED",
			Version: "1", Domain: "PRACTICE", SourceKey: "practice", AmountMinor: &amount, OccurredAt: happened, UpdatedAt: happened,
			RefundsPayment: []sourcePaymentReference{{OrderID: order, RecordID: order}}}
	}
	store := func(facts ...sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { _, err := storeSourceFactsFrom(ctx, tx, facts, sourceKey); return err }); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, 6, 12, 1, 0, 0, 0, time.UTC)
	store(paid("10", 4000), paid("11", 6000), paid("12", 10000), refund("10", "1", 1000, at.Add(time.Hour)))
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: "PRACTICE:1:1:6", Hash: "replay", SourceVersion: 1, StartsAt: start,
			ConfirmedAt: start.Add(time.Hour), Enrollments: []practiceEnrollment{
				{SourceEnrollmentID: "1", ParticipantID: "1", MembershipType: 3, CommissionEligible: true,
					SignedAt: start, PaidOrderIDs: []string{"10", "11"}, ContentHash: "aa"},
				{SourceEnrollmentID: "2", ParticipantID: "2", MembershipType: 3, CommissionEligible: true,
					SignedAt: start, PaidOrderIDs: []string{"12"}, ContentHash: "bb"},
			}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	calculate := func(period int64) draftSummary {
		t.Helper()
		raw, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		return raw.(draftSummary)
	}
	freeze := func(d draftSummary) {
		t.Helper()
		if _, err := s.db.Exec(ctx, "INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')", d.PeriodID, d.Version); err != nil {
			t.Fatal(err)
		}
	}
	original := calculate(class)
	if original.Total != 7600 || original.IssueCount != 0 {
		t.Fatalf("settled tier before refund: %+v", original)
	}
	freeze(original)
	future := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月实战班", Ordinal: 2,
		StartsOn: "2026-09-10", EndsOn: "2026-09-10", SourcePeriodKey: "PRACTICE:1:1:9"}))
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &future, Label: "交付分润", Components: []component{
		{Type: "delivery", Mode: "period_rate", Value: 100000,
			PeriodBenefit: &practicePeriodBenefit{Label: "交付", Pool: &practicePoolTarget{PoolID: pool, OwnerID: seller}}},
	}}); err != nil {
		t.Fatal(err)
	}
	futureAt := time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)
	futurePayment := paid("13", 2000)
	futurePayment.OccurredAt, futurePayment.UpdatedAt = futureAt.Add(-time.Hour), futureAt.Add(-time.Hour)
	store(futurePayment)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{
			Key: "PRACTICE:1:1:9", Hash: "future", SourceVersion: 1, StartsAt: futureAt, ConfirmedAt: futureAt.Add(time.Hour),
			Enrollments: []practiceEnrollment{{SourceEnrollmentID: "3", ParticipantID: "3", MembershipType: 3,
				CommissionEligible: true, SignedAt: futureAt, PaidOrderIDs: []string{"13"}, ContentHash: "cc"}},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	futureDraft := calculate(future)
	if futureDraft.Total != 200 || futureDraft.IssueCount != 0 {
		t.Fatalf("unexpected later pool credit: %+v", futureDraft)
	}
	var poolBasis string
	if err := s.db.QueryRow(ctx, poolAllocationBasisSQL+"$1", future).Scan(&poolBasis); err != nil {
		t.Fatal(err)
	}
	scope := poolScope{Unit: unit, Scenario: "class", Item: fmt.Sprint(pool)}
	if _, err := s.savePoolAllocation(ctx, savePoolAllocationInput{PeriodID: future, Version: futureDraft.Version, Basis: poolBasis,
		poolScope: scope, Retained: 4000}); err != nil {
		t.Fatal(err)
	}
	freeze(futureDraft)
	if _, err := s.db.Exec(ctx, "UPDATE organization_assignments SET unit_id=$2 WHERE person_id=$1", seller, newDept); err != nil {
		t.Fatal(err)
	}
	id(s.assignIdentity(ctx, assignIdentityInput{Source: "DCZD", ExternalID: "seller", PersonID: later, ValidFrom: at.Format(time.RFC3339)}))
	publish([]component{{Type: "sales", Mode: "rate", Value: 5000, ClassMember: "new", SourceRole: "DIRECT_REFERRER"}})
	revised := paid("12", 9900)
	revised.Version, revised.UpdatedAt = "2", at.Add(time.Hour)
	julyDate := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	store(revised, refund("10", "2", 3000, julyDate), refund("12", "3", 2000, julyDate.Add(time.Hour)),
		refund("12", "4", 1000, time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)))
	calculate(august)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		deps, err := readPracticeMonthSettlementDependencies(ctx, tx, august)
		if err == nil && (len(deps) != 1 || deps[0].Kind != "missing_month" || deps[0].Name != "2026-07 退款账期（待建立）") {
			t.Fatalf("an unregistered earlier month must still block August: %+v", deps)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	july := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "七月退款", SettlementMonth: "2026-07"}))
	draft := calculate(july)
	if draft.Total != -4800 || draft.RefundCount != 6 || draft.IssueCount != 0 {
		t.Fatalf("whole class must cross the tier using frozen inputs: %+v", draft)
	}
	readPool := func(period, version int64) poolDraftItem {
		t.Helper()
		w := httptest.NewRecorder()
		s.draftPools(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/draft-pools?periodId=%d&version=%d", period, version), nil))
		var report poolDraftReport
		if err := json.Unmarshal(w.Body.Bytes(), &report); w.Code != http.StatusOK || err != nil || len(report.Items) != 1 {
			t.Fatalf("expected one team pool: %d %s", w.Code, w.Body.String())
		}
		return report.Items[0]
	}
	if julyPool := readPool(july, draft.Version); julyPool.Opening != 3800 || julyPool.Refund != -2400 || julyPool.Balance != 1400 {
		t.Fatalf("July pool must exclude the September class despite its small ordinal: %+v", julyPool)
	}
	var laterActivity bool
	if err := s.db.QueryRow(ctx, laterSettlementPoolSQL, july, []poolScope{scope}).Scan(&laterActivity); err != nil || !laterActivity {
		t.Fatalf("September's confirmed pool must be visible as later activity: %t %v", laterActivity, err)
	}
	var lines, related, principal int64
	if err := s.db.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE p.order_id='11')
	 FROM calculation_draft_refunds d JOIN calculation_draft_lines l ON l.id=d.original_line_id
	 JOIN source_order_versions p ON p.id=l.source_version_id WHERE d.period_id=$1 AND l.person_id=$2`,
		july, seller).Scan(&lines, &related); err != nil || lines != 3 || related != 1 {
		t.Fatalf("deductions must use the original seller and include the unrefunded balance: %d %d %v", lines, related, err)
	}
	var poolDebit int64
	if err := s.db.QueryRow(ctx, `SELECT coalesce(sum(d.amount_minor),0)::bigint
	 FROM calculation_draft_refunds d JOIN calculation_draft_lines l ON l.id=d.original_line_id
	 WHERE d.period_id=$1 AND l.practice_pool_id=$2 AND l.beneficiary_unit_id=$3`,
		july, pool, unit).Scan(&poolDebit); err != nil || poolDebit != -2400 {
		t.Fatalf("the original pool must absorb the whole-class delivery deduction: %d %v", poolDebit, err)
	}
	var detail struct {
		Refunds                 []struct{ OriginalOrderID, ReplayTriggerOrderID string }
		PoolAllocationAvailable bool
	}
	if err := s.db.QueryRow(ctx, draftDetailSQL, july, 0, 0, draft.Version, 0, 0).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var linked int
	for _, row := range detail.Refunds {
		if row.OriginalOrderID == "11" && row.ReplayTriggerOrderID == "12" {
			linked++
		}
	}
	if linked != 2 || !detail.PoolAllocationAvailable {
		t.Fatalf("affected balance must identify the triggering refund's order: %+v", detail)
	}
	if err := s.db.QueryRow(ctx, `SELECT sum((v.fact->>'amountMinor')::bigint) FROM calculation_draft_performance f
	 JOIN source_order_versions v ON v.id=f.source_version_id WHERE f.period_id=$1 AND f.person_id=$2`,
		july, seller).Scan(&principal); err != nil || principal != 5000 {
		t.Fatalf("principal must only include the two actual refunds: %d %v", principal, err)
	}
	for dimension, originalID := range map[string]string{"person": fmt.Sprint(seller), "company": unit, "department": oldDept} {
		var report struct {
			PerformanceMinor int64
			Items            []struct {
				ID          string
				RefundMinor int64
			}
		}
		if err := s.db.QueryRow(ctx, draftReportSQL, july, draft.Version, dimension, 0).Scan(&report); err != nil {
			t.Fatal(err)
		}
		var total, attributed int64
		for _, row := range report.Items {
			total += row.RefundMinor
			if row.ID == originalID {
				attributed = row.RefundMinor
			}
		}
		want := int64(-2400)
		if dimension == "company" {
			want = -4800
		}
		if total != -4800 || attributed != want || report.PerformanceMinor != -5000 {
			t.Fatalf("%s report must reconcile to the original attribution: %+v", dimension, report)
		}
	}
	if repeat := calculate(july); repeat.Version != draft.Version {
		t.Fatalf("repeat should reuse the same draft: %+v", repeat)
	}
	if open := calculate(august); open.Total != -400 || open.IssueCount != 0 {
		t.Fatalf("August draft before July closes: %+v", open)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		deps, err := readPracticeMonthSettlementDependencies(ctx, tx, august)
		if err == nil && (len(deps) != 1 || deps[0].PeriodID != july || deps[0].Kind != "refund_month") {
			t.Fatalf("settling August before July would skip a relevant earlier tier change: %+v", deps)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	freeze(draft)
	if septemberPool := readPool(future, futureDraft.Version); septemberPool.Opening != 3800 || septemberPool.Confirmed != 4000 || septemberPool.Balance != 0 {
		t.Fatalf("later backfill must not change an already confirmed class's pool balance: %+v", septemberPool)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		return enqueueDraftPeriods(ctx, tx, draftRefreshAfterClose, july)
	}); err != nil {
		t.Fatal(err)
	}
	var queued bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)", august).Scan(&queued); err != nil || !queued {
		t.Fatalf("closing July must refresh August's overlapping class: %t %v", queued, err)
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var next draftSummary
	if err := s.db.QueryRow(ctx, "SELECT version,refund_minor,refund_count,issue_count FROM calculation_drafts WHERE period_id=$1",
		august).Scan(&next.Version, &next.Total, &next.RefundCount, &next.IssueCount); err != nil || next.Total != -200 || next.RefundCount != 2 || next.IssueCount != 0 {
		t.Fatalf("August must debit only the incremental amount: %+v", next)
	}
	earlier := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "五月退款", SettlementMonth: "2026-05"}))
	store(refund("12", "5", 500, at.AddDate(0, 0, 5)))
	early := calculate(earlier)
	if early.Total != -100 || early.IssueCount != 0 {
		t.Fatalf("late-synced refund still belongs to May: %+v", early)
	}
	if mayPool := readPool(earlier, early.Version); mayPool.Opening != 0 || mayPool.Refund != -50 || mayPool.Balance != -50 {
		t.Fatalf("the class credit has not entered the May pool: %+v", mayPool)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,completed_through,last_success_at,next_updated_at,next_id)
	 VALUES($1,$2,'2026-04-01',now(),now(),now(),'0')`, dczdOrderStream, sourceKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.getSettlementReview(w, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", earlier, early.Version), nil))
	var mayReview settlementReview
	if err := json.Unmarshal(w.Body.Bytes(), &mayReview); w.Code != http.StatusOK || err != nil || mayReview.Token == "" || mayReview.LaterPoolActivity {
		t.Fatalf("the negative May refund month must be eligible to settle: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: earlier, ExpectedVersion: early.Version, ReviewToken: mayReview.Token}, "fixture"); err != nil {
		t.Fatalf("close May through the real settlement transaction: %v", err)
	}
	if augustPool := readPool(august, calculate(august).Version); augustPool.Opening != 1350 || augustPool.Refund != -100 || augustPool.Balance != 1250 {
		t.Fatalf("the June class must offset the deficit before the following months: %+v", augustPool)
	}
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", earlier); err != nil {
		t.Fatalf("an already frozen pool report that never used May must not prevent removing May: %v", err)
	}
	empty := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "十月退款", SettlementMonth: "2026-10"}))
	emptyDraft := calculate(empty)
	freeze(emptyDraft)
	if item := readPool(empty, emptyDraft.Version); item.Refund != 0 || item.Income != 0 || item.Opening != -2400 || item.Balance != -2400 {
		t.Fatalf("an empty refund month still carries the confirmed pool balance: %+v", item)
	}
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", future); err == nil {
		t.Fatal("removing September would change the confirmed October carry-in")
	}
	if _, err := s.db.Exec(ctx, "UPDATE calculation_drafts SET practice_replay_inputs=NULL WHERE period_id=$1", class); err == nil {
		t.Fatal("settled inputs must be immutable")
	}
}

func TestClassProductConfigurationCanReimportAfterOpeningRefundMonth(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	input := legacyCatalogFile{Environment: "fixture", CapturedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Products: []legacyCatalogProduct{
		{SourceID: "40000000-0000-4000-8000-000000000011", Name: "实战班", Code: "PRACTICE_MONTH_IMPORTED",
			Project: "PRACTICE", Source: "DCZD", SourceKey: "practice", PeriodMode: "class"},
	}}
	if err := normalizeLegacyCatalog(&input); err != nil {
		t.Fatal(err)
	}
	load := func() {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return storeLegacyCatalog(ctx, tx, input) }); err != nil {
			t.Fatal(err)
		}
	}
	load()
	var product int64
	if err := s.db.QueryRow(ctx, "SELECT id FROM products WHERE code='PRACTICE_MONTH_IMPORTED'").Scan(&product); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月退款账期", SettlementMonth: "2026-08"}); err != nil {
		t.Fatal(err)
	}
	load()
	var day int
	if err := s.db.QueryRow(ctx, "SELECT cycle_start_day FROM products WHERE id=$1", product).Scan(&day); err != nil || day != 25 {
		t.Fatalf("import replay should preserve the configured refund calendar: %d %v", day, err)
	}
}
