package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeCorrectionReplaysBothLegsAndTheWholeSalesTier(t *testing.T) {
	t.Run("shared cumulative tier", func(t *testing.T) {
		testPracticeCorrectionAcrossClasses(t, false, "", "new", "")
	})
	t.Run("personal cumulative tier", func(t *testing.T) {
		testPracticeCorrectionAcrossClasses(t, true, "", "new", "")
	})
	t.Run("cumulative new amount", func(t *testing.T) {
		testPracticeCorrectionAcrossClasses(t, false, "cumulative_new", "new", "")
	})
	t.Run("cumulative retrain amount", func(t *testing.T) {
		testPracticeCorrectionAcrossClasses(t, true, "cumulative_retrain", "retrain", "")
	})
	t.Run("cumulative combined amount", func(t *testing.T) {
		testPracticeCorrectionAcrossClasses(t, false, "cumulative_combined", "retrain", "")
	})
	t.Run("retrain is outside the new amount metric", func(t *testing.T) {
		testPracticeCorrectionAcrossClasses(t, false, "cumulative_new", "retrain", "")
	})
	t.Run("later opening has a lower class number", func(t *testing.T) {
		testPracticeCorrectionAcrossClasses(t, false, "cumulative_new", "new", "time")
	})
	t.Run("personal start excludes the original class", func(t *testing.T) {
		testPracticeCorrectionAcrossClasses(t, false, "cumulative_new", "new", "reset")
	})
}

func testPracticeCorrectionAcrossClasses(t *testing.T, personalTier bool, metric practiceAmountMetric, member, openingWindow string) {
	t.Helper()
	ctx := context.Background()
	service := workspace{db: integrationDB(t), sources: map[string]sourceConfig{
		"DCZD": {BaseURL: "http://127.0.0.1:9126", Token: "isolated-practice-correction"},
	}}
	sourceKey := service.orderSourceKey("DCZD")
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(service.createProduct(ctx, createProductInput{Code: "CLASS_CORRECTION", Name: "实战班更正样本", PeriodMode: "class"}))
	id(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	const classKey = "PRACTICE:1:1:56"
	class := id(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "六月班", Ordinal: 56,
		StartsOn: "2026-06-12", EndsOn: "2026-06-12", SourcePeriodKey: classKey}))
	july := id(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "七月退款", SettlementMonth: "2026-07"}))
	august := id(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月退款", SettlementMonth: "2026-08"}))
	september := id(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月退款", SettlementMonth: "2026-09"}))
	start := time.Date(2026, 6, 12, 8, 0, 0, 0, shanghai)
	paidAt := start.AddDate(0, 0, -10)
	revisedAt := start.AddDate(0, 1, 0)
	people := make(map[string]int64)
	for _, name := range []string{"销售甲", "销售乙"} {
		person := id(service.createPerson(ctx, createPersonInput{Name: name}))
		people[name] = person
		id(service.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: name, ValidFrom: paidAt.Add(-time.Hour).Format(time.RFC3339)}))
	}
	const replacement = "销售乙新来源账号"
	id(service.assignIdentity(ctx, assignIdentityInput{PersonID: people["销售乙"], Source: "DCZD", ExternalID: replacement, ValidFrom: paidAt.Add(-time.Hour).Format(time.RFC3339)}))
	classTiers := &practiceSalesTiers{Performance: []practiceTier{{Mode: "rate", Value: 1000}, {From: 15000, Mode: "rate", Value: 2000}}}
	membershipType := 3
	if member == "retrain" {
		membershipType = 2
		classTiers.CumulativeMetric, classTiers.CountFromOrdinal = "cumulative_retrain", 56
	}
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &class, Label: "六月实战班销售阶梯",
		Components: []component{{Type: "sales", Mode: "tiered", ClassMember: member, SourceRole: "DIRECT_REFERRER",
			Tiers: classTiers}}}); err != nil {
		t.Fatal(err)
	}
	payment := func(order, version, owner string, amount int64, observed time.Time) sourceFact {
		return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice", Kind: "ORDER_PAID",
			OrderID: order, OrderNo: version, PaymentID: order, RecordKey: "payment:" + order,
			Version: version, AmountMinor: &amount, ReferrerID: owner, OccurredAt: paidAt, UpdatedAt: observed}
	}
	store := func(observed time.Time, facts ...sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			if _, err := storeSourceFactsFrom(ctx, tx, facts, sourceKey); err != nil {
				return err
			}
			for _, fact := range facts {
				if _, err := tx.Exec(ctx, `UPDATE source_order_versions SET received_at=$3 WHERE order_id=$1 AND version=$2`, fact.OrderID, fact.Version, observed); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	store(paidAt, payment("deposit", "1", "销售甲", 4000, paidAt), payment("balance", "1", "销售甲", 6000, paidAt),
		payment("second", "1", "销售甲", 10000, paidAt), payment("third", "1", "销售乙", 6000, paidAt))
	if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: classKey, Hash: "class-correction-fixture", SourceVersion: 1,
			StartsAt: start, ConfirmedAt: start.Add(time.Hour), Enrollments: []practiceEnrollment{
				{SourceEnrollmentID: "1", ParticipantID: "1", MembershipType: membershipType, CommissionEligible: true,
					SignedAt: start, PaidOrderIDs: []string{"deposit", "balance"}, ContentHash: "aa"},
				{SourceEnrollmentID: "2", ParticipantID: "2", MembershipType: membershipType, CommissionEligible: true,
					SignedAt: start, PaidOrderIDs: []string{"second"}, ContentHash: "bb"},
				{SourceEnrollmentID: "3", ParticipantID: "3", MembershipType: membershipType, CommissionEligible: true,
					SignedAt: start, PaidOrderIDs: []string{"third"}, ContentHash: "cc"},
			}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	draftAt := func(period int64) draftSummary {
		t.Helper()
		result, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		return result.(draftSummary)
	}
	freeze := func(draft draftSummary) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			_, err := insertSettlement(ctx, tx, draft.PeriodID, draft.Version, "fixture", "", nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	frozen := draftAt(class)
	if frozen.Total != 4600 || frozen.IssueCount != 0 {
		t.Fatalf("unexpected frozen class: %+v", frozen)
	}
	freeze(frozen)
	laterOrdinal := 57
	if openingWindow == "time" {
		laterOrdinal = 55
	}
	laterKey := fmt.Sprintf("PRACTICE:1:1:%d", laterOrdinal)
	later := id(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "七月班", Ordinal: laterOrdinal,
		StartsOn: "2026-07-10", EndsOn: "2026-07-10", SourcePeriodKey: laterKey}))
	laterStart := time.Date(2026, 7, 10, 8, 0, 0, 0, shanghai)
	laterTiers := &practiceSalesTiers{CountFromOrdinal: 56,
		Count: []practiceTier{{From: 1, Mode: "rate", Value: 1000}, {From: 2, Mode: "rate", Value: 2000}}}
	if metric != "" {
		laterTiers.CumulativeMetric = metric
		laterTiers.Performance = []practiceTier{{Mode: "rate", Value: 1000}, {From: 20000, Mode: "rate", Value: 2000}}
		laterTiers.Count = nil
	}
	if openingWindow != "" {
		laterTiers.CountFromOrdinal, laterTiers.CountFromAt = 0, &start
	}
	if openingWindow == "reset" {
		laterTiers.PersonCountStarts = []practicePersonCountStart{{PersonID: people["销售甲"], practiceCountStart: practiceCountStart{FromAt: &laterStart}}}
	}
	if personalTier {
		laterTiers.PersonSchedules = []practicePersonSchedule{{
			PersonID:             people["销售甲"],
			practiceTierSchedule: practiceTierSchedule{Count: laterTiers.Count, Performance: laterTiers.Performance},
		}}
		laterTiers.Count, laterTiers.Performance = nil, nil
	}
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &later, Label: "累计新报",
		Components: []component{{Type: "sales", Mode: "tiered", ClassMember: member, SourceRole: "DIRECT_REFERRER",
			Tiers: laterTiers}}}); err != nil {
		t.Fatal(err)
	}
	store(paidAt, payment("fourth", "1", "销售甲", 10000, paidAt))
	if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: laterKey, Hash: "later-tier", SourceVersion: 1,
			StartsAt: laterStart, ConfirmedAt: laterStart.Add(time.Hour), Enrollments: []practiceEnrollment{
				{SourceEnrollmentID: "4", ParticipantID: "4", MembershipType: membershipType, CommissionEligible: true,
					SignedAt: laterStart, PaidOrderIDs: []string{"fourth"}, ContentHash: "dd"},
			}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wantAdvisory := (metric == "" || metric.includes(member)) && openingWindow != "reset"
	laterTotal, advisoryCount := int64(2000), 1
	if !wantAdvisory {
		laterTotal, advisoryCount = 1000, 0
	}
	if next := draftAt(later); next.Total == laterTotal && next.IssueCount == 0 {
		freeze(next)
	} else {
		t.Fatalf("the later settled class must depend on the earlier sales count: %+v", next)
	}
	const openKey = "PRACTICE:1:1:58"
	openClass := id(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 58,
		StartsOn: "2026-08-12", EndsOn: "2026-08-12", SourcePeriodKey: openKey}))
	openStart := time.Date(2026, 8, 12, 8, 0, 0, 0, shanghai)
	openSchedule := practiceTierSchedule{Count: []practiceTier{{Mode: "rate", Value: 1000}, {From: 3, Mode: "rate", Value: 2000}}}
	openMetric := metric
	if !wantAdvisory {
		openMetric = classTiers.CumulativeMetric
	}
	if openMetric != "" {
		openSchedule = practiceTierSchedule{Performance: []practiceTier{{Mode: "rate", Value: 1000}, {From: 35000, Mode: "rate", Value: 2000}}}
	}
	openTiers := &practiceSalesTiers{CountFromOrdinal: 56, CumulativeMetric: openMetric, PersonSchedules: []practicePersonSchedule{
		{PersonID: people["销售甲"], practiceTierSchedule: openSchedule},
	}}
	if openingWindow != "" {
		openTiers.CountFromOrdinal, openTiers.CountFromAt = 0, &start
	}
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &openClass, Label: "开放班次个人累计",
		Components: []component{{Type: "sales", Mode: "tiered", ClassMember: member, SourceRole: "DIRECT_REFERRER",
			Tiers: openTiers}}}); err != nil {
		t.Fatal(err)
	}
	store(paidAt, payment("fifth", "1", "销售甲", 10000, paidAt))
	if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: openKey, Hash: "later-open-tier", SourceVersion: 1,
			StartsAt: openStart, ConfirmedAt: openStart.Add(time.Hour), Enrollments: []practiceEnrollment{
				{SourceEnrollmentID: "5", ParticipantID: "5", MembershipType: membershipType, CommissionEligible: true,
					SignedAt: openStart, PaidOrderIDs: []string{"fifth"}, ContentHash: "ee"},
			}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if next := draftAt(openClass); next.Total != 2000 || next.IssueCount != 0 {
		t.Fatalf("the open class must use the original three earlier enrollments: %+v", next)
	}
	store(revisedAt, payment("deposit", "2", replacement, 4000, revisedAt))
	if partial := draftAt(july); partial.IssueCount != 1 || partial.CorrectionCount != 0 {
		t.Fatalf("one changed leg must expose a mismatch without posting a credit: %+v", partial)
	}
	balanceRevision := revisedAt.AddDate(0, 1, 0)
	store(balanceRevision, payment("balance", "2", replacement, 6000, balanceRevision))
	var queued bool
	if err := service.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, july).Scan(&queued); err != nil || !queued {
		t.Fatalf("the second payment must queue the earlier waiting draft: %t %v", queued, err)
	}
	if earlier := draftAt(july); earlier.IssueCount != 0 || earlier.CorrectionCount != 0 {
		t.Fatalf("a complete enrollment must release the earlier partial correction: %+v", earlier)
	}
	changed := draftAt(august)
	if changed.CorrectionCount != 3 || changed.CorrectionTotal != 2600 || changed.IssueCount != 3 {
		t.Fatalf("expected both payment legs and the other seller's increased tier: %+v", changed)
	}
	if err := service.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, openClass).Scan(&queued); err != nil || !queued {
		t.Fatalf("the open class must be queued when the earlier seller changes: %t %v", queued, err)
	}
	if next := draftAt(openClass); next.Total != 1000 || next.IssueCount != 0 {
		t.Fatalf("the open class must recalculate to the affected seller's two earlier enrollments: %+v", next)
	}
	var advisory bool
	if err := service.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calculation_draft_issues
	 WHERE period_id=$1 AND code='PRACTICE_CORRECTION_LATER_TIERS' AND advisory)`, august).Scan(&advisory); err != nil || advisory != wantAdvisory {
		t.Fatalf("expected later class advisory %t, got %t %v", wantAdvisory, advisory, err)
	}
	if openingWindow == "time" {
		if err := service.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM (`+draftRefreshAfterClose+`) refresh(period_id) WHERE period_id=$2)`,
			later, august).Scan(&queued); err != nil || !queued {
			t.Fatalf("closing the actually later class must refresh the open correction review: %t %v", queued, err)
		}
	}
	displayRevision := revisedAt.AddDate(0, 2, 0)
	store(displayRevision, payment("deposit", "renamed", replacement, 4000, displayRevision))
	if repeat := draftAt(august); repeat.Version != changed.Version {
		t.Fatalf("an order number change migrated the August correction: %+v", repeat)
	}
	// Simulate an already prepared fallback in August whose receipt month was
	// closed. Confirmation happens after the fallback month has ended.
	freeze(draftAt(july))
	if _, err := service.db.Exec(ctx, `UPDATE source_order_versions SET received_at=$1
	 WHERE order_id IN ('deposit','balance') AND version='2'`, revisedAt); err != nil {
		t.Fatal(err)
	}
	if repeat := draftAt(august); repeat.Version != changed.Version || repeat.CorrectionCount != changed.CorrectionCount {
		t.Fatalf("a prepared fallback must survive the end of its handling month: %+v", repeat)
	}
	config := financeAccountConfig{PersonID: people["销售甲"], Login: "practice.recovery", Enabled: true, Workspace: true, Settlements: []int64{product}}
	const password = "isolated-practice-recovery"
	if err := service.saveFinanceAccount(ctx, config, password); err != nil {
		t.Fatal(err)
	}
	session, err := service.loginFinanceAccount(ctx, financeLoginInput{Login: config.Login, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	reviewed := changed
	api := http.NewServeMux()
	for _, route := range service.routes() {
		api.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	for range 3 {
		var issueID int64
		if err := service.db.QueryRow(ctx, `SELECT id FROM calculation_draft_issues WHERE period_id=$1
		 AND code='CORRECTION_RECOVERY_PENDING' ORDER BY id LIMIT 1`, august).Scan(&issueID); err != nil {
			t.Fatal(err)
		}
		payload := fmt.Sprintf(`{"periodId":%d,"version":%d,"issueId":%d,"outcome":"recover"}`, august, reviewed.Version, issueID)
		request := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/correction-recovery", strings.NewReader(payload))
		request.Header.Set("Origin", "http://localhost")
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: financeAccountCookie, Value: session})
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("recovery for a practice tier should be available to authorized finance: %d %s", response.Code, response.Body)
		}
		reviewed = draftAt(august)
	}
	if reviewed.CorrectionCount != 6 || reviewed.CorrectionTotal != -400 || reviewed.IssueCount != 0 {
		t.Fatalf("tier recalculation must post all six signed differences: %+v", reviewed)
	}
	response := httptest.NewRecorder()
	service.getDraft(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d", august), nil))
	var detail struct {
		AdvisoryCount int
		Corrections   []struct {
			OrderID, TriggerOrderID, PersonName string
			RuleVersion                         int
			AmountMinor                         int64
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || response.Code != http.StatusOK {
		t.Fatalf("correction detail: %d %v %s", response.Code, err, response.Body)
	}
	if detail.AdvisoryCount != advisoryCount {
		t.Fatalf("the finance workbench must receive the later-class advisory, got %d", detail.AdvisoryCount)
	}
	var linked bool
	for _, entry := range detail.Corrections {
		if entry.OrderID == "second" && entry.TriggerOrderID != "" && entry.RuleVersion == 1 && entry.AmountMinor == -1000 {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("the indirectly affected order did not retain its own original reference and rule version: %+v", detail)
	}
	if _, err := service.db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,completed_through,last_success_at,next_updated_at,next_id)
	 VALUES($1,$2,'2026-01-01',now(),now(),now(),'0')`, dczdOrderStream, sourceKey); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	reviewRequest := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://localhost/api/v1/settlement-review?periodId=%d&version=%d", august, reviewed.Version), nil)
	reviewRequest.AddCookie(&http.Cookie{Name: financeAccountCookie, Value: session})
	response = httptest.NewRecorder()
	api.ServeHTTP(response, reviewRequest)
	var review settlementReview
	if err := json.Unmarshal(response.Body.Bytes(), &review); err != nil || response.Code != http.StatusOK || review.Token == "" {
		t.Fatalf("the reviewed correction must be eligible for normal settlement: %d %v %s", response.Code, err, response.Body)
	}
	payload, err := json.Marshal(confirmSettlementInput{PeriodID: august, ExpectedVersion: reviewed.Version, ReviewToken: review.Token})
	if err != nil {
		t.Fatal(err)
	}
	confirm := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/settlements", strings.NewReader(string(payload)))
	confirm.Header.Set("Origin", "http://localhost")
	confirm.Header.Set("Content-Type", "application/json")
	confirm.AddCookie(&http.Cookie{Name: financeAccountCookie, Value: session})
	response = httptest.NewRecorder()
	api.ServeHTTP(response, confirm)
	if response.Code != http.StatusOK {
		t.Fatalf("failed to confirm the correction with its source coverage: %d %s", response.Code, response.Body)
	}
	if next := draftAt(september); next.CorrectionCount != 0 || next.IssueCount != 0 {
		t.Fatalf("the next draft must not duplicate booked differences: %+v", next)
	}
	id(service.assignIdentity(ctx, assignIdentityInput{PersonID: people["销售甲"], Source: "DCZD", ExternalID: replacement,
		ValidFrom: paidAt.Format(time.RFC3339)}))
	refundAmount := int64(2000)
	store(displayRevision.Add(time.Hour), sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice",
		Kind: "REFUND_SUCCEEDED", OrderID: "deposit", RecordKey: "refund:deposit", Version: "r1",
		RefundID: "deposit-partial-refund", AmountMinor: &refundAmount, OccurredAt: displayRevision, UpdatedAt: displayRevision,
		RefundsPayment: []sourcePaymentReference{{OrderID: "deposit", RecordID: "deposit"}}})
	refunded := draftAt(september)
	wantPending, refundIssues := int64(-1800), 3
	if classTiers.CumulativeMetric != "" {
		wantPending, refundIssues = -400, 1
	}
	if refunded.RefundTotal != -400 || refunded.CorrectionTotal != 400 || refunded.IssueCount != refundIssues {
		t.Fatalf("the later refund must account for the corrected tier and reverse the debit to the frozen recipient: %+v", refunded)
	}
	var totalPending int64
	if err := service.db.QueryRow(ctx, `SELECT coalesce(sum(recovery_minor),0) FROM calculation_draft_issues
	 WHERE period_id=$1 AND recovery_person_id=$2`, september, people["销售乙"]).Scan(&totalPending); err != nil || totalPending != wantPending {
		t.Fatalf("the booked recipient and their linked tier must survive a later identity change: %d %v", totalPending, err)
	}
	revertAt := displayRevision.Add(2 * time.Hour)
	store(revertAt, payment("deposit", "reverted", "销售甲", 4000, revertAt), payment("balance", "reverted", "销售甲", 6000, revertAt))
	draftAt(september)
	if err := service.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calculation_draft_issues
	 WHERE period_id=$1 AND code='PRACTICE_CORRECTION_LATER_TIERS' AND advisory)`, september).Scan(&advisory); err != nil || advisory != wantAdvisory {
		t.Fatalf("restoring the original recipient should affect the same cumulative metric: %t %v", advisory, err)
	}
	var frozenTotal int64
	if err := service.db.QueryRow(ctx, `SELECT total_minor FROM calculation_drafts WHERE period_id=$1`, class).Scan(&frozenTotal); err != nil || frozenTotal != frozen.Total {
		t.Fatalf("the original class was rewritten: %d %v", frozenTotal, err)
	}
	if err := service.db.QueryRow(ctx, `SELECT total_minor FROM calculation_drafts WHERE period_id=$1`, later).Scan(&frozenTotal); err != nil || frozenTotal != laterTotal {
		t.Fatalf("the later settled class was rewritten: %d %v", frozenTotal, err)
	}
	if _, err := service.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", class); err == nil || !strings.Contains(err.Error(), "booked adjustments") {
		t.Fatalf("cannot reopen the class while its related correction is booked: %v", err)
	}
}
