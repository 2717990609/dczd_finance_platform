package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCalendarRefundWaitsForItsMissingAccountingMonth(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t)}
	mustID := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	day := 25
	now := time.Now().In(shanghai)
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, shanghai)
	if now.Day() >= day {
		currentMonth = currentMonth.AddDate(0, 1, 0)
	}
	paidMonth, refundMonth := currentMonth.AddDate(0, -4, 0), currentMonth.AddDate(0, -2, 0)
	product := mustID(service.createProduct(ctx, createProductInput{Code: "refund-missing-month", Name: "Refund month", PeriodMode: "calendar", CycleStartDay: &day}))
	createMonth := func(date time.Time) int64 {
		label := date.Format("2006-01")
		return mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: label, SettlementMonth: label}))
	}
	original, current := createMonth(paidMonth), createMonth(currentMonth)
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "VIP", SourceKey: "VIP"}))
	person := mustID(service.createPerson(ctx, createPersonInput{Name: "Original recipient"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{
		PersonID: person, Source: "DCAI", ExternalID: "refund-person", ValidFrom: paidMonth.AddDate(-1, 0, 0).Format(time.RFC3339),
	}))
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Original", Components: []component{
		{Type: "sales", Mode: "rate", SourceRole: "PID_USER", Value: 1000, RefundPolicy: "proportional"},
		{Type: "referral", Mode: "rate", SourceRole: "DIRECT_REFERRER", Value: 100, RefundPolicy: "proportional"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	save := func(facts ...sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, facts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	refreshed := func() {
		t.Helper()
		if err := service.refreshPendingDrafts(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertDraft := func(period, refund int64, wantIssues ...string) {
		t.Helper()
		var actual int64
		var codes []string
		err := service.db.QueryRow(ctx, `SELECT refund_minor,ARRAY(SELECT code FROM calculation_draft_issues
		 WHERE period_id=$1 ORDER BY id) FROM calculation_drafts WHERE period_id=$1`, period).Scan(&actual, &codes)
		if err != nil || actual != refund || fmt.Sprint(codes) != fmt.Sprint(wantIssues) {
			t.Fatalf("period %d: refund=%d issues=%v err=%v, want %d / %v", period, actual, codes, err, refund, wantIssues)
		}
	}
	paidAt, refundedAt := paidMonth.AddDate(0, 0, 4), refundMonth.AddDate(0, 0, 4)
	paid, refunded := int64(100000), int64(50000)
	save(sourceFact{Source: "DCAI", Kind: "ORDER_PAID", OrderID: "order", PaymentID: "payment", RecordKey: "orders:paid",
		Domain: "VIP", SourceKey: "VIP", Version: "1", OccurredAt: paidAt, UpdatedAt: paidAt, AmountMinor: &paid,
		ReferrerID:   "refund-person",
		Participants: []sourceParticipant{{Role: "PID_USER", UserID: "refund-person"}}})
	value, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: original})
	if err != nil {
		t.Fatal(err)
	}
	initial := value.(draftSummary)
	if initial.Total != 11000 {
		t.Fatalf("original order must pay 100 and 10 yuan: %+v", initial)
	}
	booked, err := service.confirmSettlement(ctx, confirmSettlementInput{PeriodID: original, ExpectedVersion: initial.Version}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	refreshed()
	refund := sourceFact{Source: "DCAI", Kind: "REFUND_SUCCEEDED", OrderID: "order", RecordKey: "refunds:refund",
		Domain: "VIP", SourceKey: "VIP", Version: "1", RefundID: "refund", OccurredAt: refundedAt, UpdatedAt: now, AmountMinor: &refunded,
		RefundsPayment: []sourcePaymentReference{{OrderID: "order", RecordID: "paid"}}}
	unlinked := refund
	unlinked.RecordKey, unlinked.RefundID = "refunds:unlinked", "unlinked"
	unlinked.OccurredAt = refundedAt.AddDate(0, -1, 0)
	unlinked.RefundsPayment = []sourcePaymentReference{{OrderID: "order", RecordID: "other-payment"}}
	save(refund, unlinked)
	refreshed()
	assertDraft(current, 0, "REFUND_MONTH_MISSING")
	posted := createMonth(refundMonth)
	refreshed()
	assertDraft(current, 0)
	assertDraft(posted, -5500)
	assertDraft(original, 0)
	var deductions []int64
	if err := service.db.QueryRow(ctx, `SELECT ARRAY(SELECT amount_minor FROM calculation_draft_refunds
	 WHERE period_id=$1 AND refund_period_id=$1 ORDER BY amount_minor)`, posted).Scan(&deductions); err != nil ||
		len(deductions) != 2 || deductions[0] != -5000 || deductions[1] != -500 {
		t.Fatalf("refund must recover 50 and 5 yuan in the occurrence month: %v %v", deductions, err)
	}

	if _, err := service.db.Exec(ctx, "DELETE FROM settlements WHERE id=$1", booked.ID); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := service.db.QueryRow(ctx, `SELECT count(*) FROM draft_refresh_queue WHERE period_id IN ($1,$2) AND retry_on_scan`,
		original, posted).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("technical removal should schedule the reopened and affected refund periods: %d %v", queued, err)
	}
	refreshed()
	assertDraft(original, 0)
	assertDraft(posted, 0, "REFUND_ORIGINAL_NOT_SETTLED")
	reopened, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: original})
	if err != nil {
		t.Fatal(err)
	}
	redo, err := service.confirmSettlement(ctx, confirmSettlementInput{PeriodID: original, ExpectedVersion: reopened.(draftSummary).Version}, "fixture")
	if err != nil || redo.ID == booked.ID {
		t.Fatalf("reopened financial result should confirm once under a new ID: %+v %v", redo, err)
	}
	refreshed()
	assertDraft(posted, -5500)
}

func TestHistoricalRefundRoutesToAugustThenCurrentAndReusesFrozenRule(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t)}
	mustID := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	day := 25
	product := mustID(service.createProduct(ctx, createProductInput{Code: "refundable", Name: "Refund Sample", PeriodMode: "calendar", CycleStartDay: &day}))
	now := time.Now().In(shanghai)
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, shanghai)
	if now.Day() >= day {
		currentMonth = currentMonth.AddDate(0, 1, 0)
	}
	originalMonth, refundMonth := currentMonth.AddDate(0, -5, 0), currentMonth.AddDate(0, -3, 0)
	createPeriod := func(month time.Time) int64 {
		t.Helper()
		label := month.Format("2006-01")
		return mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: label, SettlementMonth: label}))
	}
	june, august, november := createPeriod(originalMonth), createPeriod(refundMonth), createPeriod(currentMonth)
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "VIP", SourceKey: "VIP"}))
	person := mustID(service.createPerson(ctx, createPersonInput{Name: "Original Recipient"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{
		PersonID: person, Source: "DCAI", ExternalID: "refund-person", ValidFrom: originalMonth.AddDate(-1, 0, 0).Format(time.RFC3339),
	}))
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Original", Components: []component{
		{Type: "sales", Mode: "rate", SourceRole: "PID_USER", Value: 3333, RefundPolicy: "proportional"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	saveFact := func(fact sourceFact) {
		t.Helper()
		err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	paidAt, refundedAt := originalMonth.AddDate(0, 0, 4), refundMonth.AddDate(0, 0, 4)
	originalAmount := int64(105)
	saveFact(sourceFact{Source: "DCAI", Kind: "ORDER_PAID", OrderID: "order", PaymentID: "payment", RecordKey: "orders:1",
		Domain: "VIP", SourceKey: "VIP", Version: "pay-v1", OccurredAt: paidAt, UpdatedAt: paidAt, AmountMinor: &originalAmount,
		Participants: []sourceParticipant{{Role: "PID_USER", UserID: "refund-person"}}})
	sameWindowAmount, sameWindowRefund := int64(100), int64(50)
	saveFact(sourceFact{Source: "DCAI", Kind: "ORDER_PAID", OrderID: "same-window", PaymentID: "same-window", RecordKey: "orders:2",
		Domain: "VIP", SourceKey: "VIP", Version: "1", OccurredAt: paidAt, UpdatedAt: paidAt, AmountMinor: &sameWindowAmount,
		Participants: []sourceParticipant{{Role: "PID_USER", UserID: "refund-person"}}})
	saveFact(sourceFact{Source: "DCAI", Kind: "REFUND_SUCCEEDED", OrderID: "same-window", RecordKey: "refunds:same-window",
		Version: "1", Domain: "VIP", SourceKey: "VIP", RefundID: "same-window", AmountMinor: &sameWindowRefund,
		OccurredAt: paidAt.AddDate(0, 0, 1), UpdatedAt: paidAt.AddDate(0, 0, 1),
		RefundsPayment: []sourcePaymentReference{{OrderID: "same-window", RecordID: "2"}}})
	draftAt := func(periodID int64, net, refunded int64, rows, issues int) draftSummary {
		t.Helper()
		value, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: periodID})
		if err != nil {
			t.Fatal(err)
		}
		draft := value.(draftSummary)
		if draft.Total != net || draft.RefundTotal != refunded || draft.RefundCount != rows || draft.IssueCount != issues {
			t.Fatalf("unexpected draft: %+v", draft)
		}
		return draft
	}
	confirm := func(draft draftSummary) settlement {
		t.Helper()
		result, err := service.confirmSettlement(ctx, confirmSettlementInput{PeriodID: draft.PeriodID, ExpectedVersion: draft.Version}, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	makeRefund := func(id, version string, amount int64, occurred, updated time.Time) sourceFact {
		return sourceFact{Source: "DCAI", Kind: "REFUND_SUCCEEDED", OrderID: "order", RecordKey: "refunds:" + id,
			Version: version, Domain: "VIP", SourceKey: "VIP", RefundID: id, AmountMinor: &amount, OccurredAt: occurred, UpdatedAt: updated,
			RefundsPayment: []sourcePaymentReference{{OrderID: "order", RecordID: "1"}}}
	}
	saveFact(makeRefund("first", "1", 34, refundedAt, refundedAt))
	draftAt(august, 0, 0, 0, 1)
	original := confirm(draftAt(june, 51, -17, 1, 0))
	if _, err := service.db.Exec(ctx, "DELETE FROM settlements WHERE id=$1", original.ID); err != nil {
		t.Fatalf("same-period refund incorrectly blocked technical redo: %v", err)
	}
	original = confirm(draftAt(june, 51, -17, 1, 0))
	if err := service.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var deduction int64
	var issues int
	if err := service.db.QueryRow(ctx, `SELECT refund_minor,issue_count FROM calculation_drafts WHERE period_id=$1`, august).Scan(&deduction, &issues); err != nil || deduction != -11 || issues != 0 {
		t.Fatalf("closed payment did not refresh the open refund period: deduction=%d issues=%d error=%v", deduction, issues, err)
	}

	// The source sent the same refund number in a different record and month.
	currentWindow, err := calendarWindow(currentMonth.Format("2006-01"), day)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := makeRefund("first", "1", 34, currentWindow.Start, now)
	duplicate.RecordKey = "refunds:duplicate"
	saveFact(duplicate)
	if err := service.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	for _, period := range []int64{august, november} {
		if err := service.db.QueryRow(ctx, `SELECT refund_minor,issue_count FROM calculation_drafts WHERE period_id=$1`, period).Scan(&deduction, &issues); err != nil || deduction != 0 || issues != 1 {
			t.Fatalf("both refund months must refresh without a debit: deduction=%d issues=%d error=%v", deduction, issues, err)
		}
		var code string
		if err := service.db.QueryRow(ctx, `SELECT code FROM calculation_draft_issues WHERE period_id=$1`, period).Scan(&code); err != nil || code != "REFUND_DUPLICATE_ID" {
			t.Fatalf("both affected months must report the same duplicate refund: %q %v", code, err)
		}
	}
	duplicate.Version, duplicate.RefundID, duplicate.SourceKey = "2", "distinct", "other-product"
	duplicate.UpdatedAt = now.Add(time.Second)
	saveFact(duplicate)
	if err := service.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRow(ctx, `SELECT refund_minor,issue_count FROM calculation_drafts WHERE period_id=$1`, august).Scan(&deduction, &issues); err != nil || deduction != -11 || issues != 0 {
		t.Fatalf("source correction did not refresh the other refund month: deduction=%d issues=%d error=%v", deduction, issues, err)
	}
	confirm(draftAt(august, -11, -11, 1, 0))
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "New rate",
		Components: []component{{Type: "sales", Mode: "rate", SourceRole: "PID_USER", Value: 5000, RefundPolicy: "non_refundable"}},
	}); err != nil {
		t.Fatal(err)
	}
	saveFact(makeRefund("second", "1", 71, refundedAt.Add(time.Hour), now))
	current := draftAt(november, -24, -24, 1, 0)
	if repeated := draftAt(november, -24, -24, 1, 0); repeated.Version != current.Version {
		t.Fatalf("no-op changed draft from %d to %d", current.Version, repeated.Version)
	}

	response := httptest.NewRecorder()
	service.getDraft(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d", november), nil))
	var view struct {
		Refunds []struct {
			RefundID, PersonName, OriginalPeriod, RefundPeriod, PostingPeriod string
			RuleVersion                                                       int
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || response.Code != 200 || len(view.Refunds) != 1 {
		t.Fatalf("failed to read adjustment page: HTTP %d, %v", response.Code, err)
	}
	got := view.Refunds[0]
	if got.RefundID != "second" || got.PersonName != "Original Recipient" || got.RuleVersion != 1 ||
		got.OriginalPeriod != originalMonth.Format("2006-01") || got.RefundPeriod != refundMonth.Format("2006-01") || got.PostingPeriod != currentMonth.Format("2006-01") {
		t.Fatalf("incorrect refund attribution: %+v", got)
	}
	saveFact(makeRefund("first", "2", 35, refundedAt, now.Add(time.Second)))
	draftAt(november, -24, -24, 1, 1)
	if _, err := service.db.Exec(ctx, "DELETE FROM settlements WHERE id=$1", original.ID); err == nil {
		t.Fatal("source settlement was removable with a booked deduction referencing it")
	}
}
