package main

import (
	"testing"
	"time"
)

func TestPaymentDraftResolvesConfiguredRoleOnPaymentDateAndRoundsMoney(t *testing.T) {
	at := time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC)
	amount := int64(105)
	window := periodWindow{Start: at.Add(-time.Hour), Through: at.Add(time.Hour)}
	payment := paymentInput{
		ID: 17, Fact: sourceFact{Source: "DCAI", OrderID: "test", PaymentID: "pay", Kind: "ORDER_PAID", AmountMinor: &amount, OccurredAt: at,
			Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "a"}, {Role: "PID_USER", UserID: "b"}}},
	}
	got, err := calculatePaymentDraft(window, []paymentRule{{ID: 1, Components: []component{{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "LOCK_SALES_USER"}}}},
		[]paymentInput{payment}, map[identityKey][]identityAssignment{
			{Source: "DCAI", ExternalID: "a"}: {
				{PersonID: 28, ValidFrom: at.Add(-time.Hour), ValidTo: &at},
				{PersonID: 30, ValidFrom: at},
			},
			{Source: "DCAI", ExternalID: "b"}: {{PersonID: 43, ValidFrom: at.Add(-time.Hour)}},
		})
	if err != nil || len(got.Lines) != 1 || len(got.Issues) != 0 || got.Total != 11 {
		t.Fatalf("draft=%+v, error=%v", got, err)
	}
	line := got.Lines[0]
	if line.PersonID != 30 || line.FactID != 17 || line.Base != 105 || line.Amount != 11 {
		t.Fatalf("wrong inputs or result: %+v", line)
	}
}

func TestPaymentDraftBlocksAmbiguousAttributionAndDuplicatePayment(t *testing.T) {
	at := time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC)
	amount := int64(100)
	window := periodWindow{Start: at.Add(-time.Hour), Through: at.Add(time.Hour)}
	fact := sourceFact{Source: "DCAI", OrderID: "test", PaymentID: "pay", Kind: "ORDER_PAID", AmountMinor: &amount, OccurredAt: at,
		Participants: []sourceParticipant{{Role: "PID_USER", UserID: "a"}, {Role: "PID_USER", UserID: "b"}}}
	got, err := calculatePaymentDraft(window, []paymentRule{{ID: 1, Components: []component{{Type: "sales", Mode: "fixed", Value: 20, SourceRole: "PID_USER"}}}},
		[]paymentInput{{ID: 1, Fact: fact}, {ID: 2, Fact: fact}}, nil)
	if err != nil || len(got.Lines) != 0 || len(got.Issues) != 2 {
		t.Fatalf("draft=%+v error=%v", got, err)
	}
	for _, issue := range got.Issues {
		if issue.Code != "DUPLICATE_PAYMENT" {
			t.Fatalf("unexpected issue: %+v", issue)
		}
	}

	got, err = calculatePaymentDraft(window, []paymentRule{{ID: 1, Components: []component{{Type: "sales", Mode: "fixed", Value: 20, SourceRole: "PID_USER"}}}}, []paymentInput{{ID: 1, Fact: fact}}, nil)
	if err != nil || len(got.Lines) != 0 || len(got.Issues) != 1 || got.Issues[0].Code != "AMBIGUOUS_PARTICIPANT" {
		t.Fatalf("draft=%+v error=%v", got, err)
	}
}
