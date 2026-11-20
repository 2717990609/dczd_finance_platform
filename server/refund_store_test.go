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
