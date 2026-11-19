package main

import (
	"testing"
	"time"
)

func TestRuleAppliesFromPaymentInstantWithinPeriod(t *testing.T) {
	at := time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC)
	amount := int64(100)
	window := periodWindow{Start: at.Add(-time.Hour), Through: at.Add(time.Hour)}
	role := "LOCK_SALES_USER"
	first := paymentRule{ID: 11, Components: []component{{Type: "sales", Mode: "rate", Value: 1000, SourceRole: role}}}
	later := paymentRule{ID: 12, From: &at, Components: []component{{Type: "sales", Mode: "rate", Value: 3000, SourceRole: role}}}
	makeFact := func(instant time.Time, order string) sourceFact {
		return sourceFact{Source: "DCAI", OrderID: order, PaymentID: order, Kind: "ORDER_PAID", AmountMinor: &amount, OccurredAt: instant,
			Participants: []sourceParticipant{{Role: role, UserID: "a"}}}
	}
	facts := []paymentInput{
		{ID: 1, Fact: makeFact(at.Add(-time.Nanosecond), "first")},
		{ID: 2, Fact: makeFact(at, "second")},
	}
	people := map[identityKey][]identityAssignment{{Source: "DCAI", ExternalID: "a"}: {{PersonID: 7, ValidFrom: window.Start}}}
	got, err := calculatePaymentDraft(window, []paymentRule{later, first}, facts, people)
	if err != nil || len(got.Issues) != 0 || len(got.Lines) != 2 || got.Lines[0].RuleID != 11 || got.Lines[1].RuleID != 12 || got.Total != 40 {
		t.Fatalf("draft=%+v, error=%v", got, err)
	}
	got, err = calculatePaymentDraft(window, []paymentRule{later}, facts, people)
	if err != nil || len(got.Lines) != 1 || len(got.Issues) != 1 || got.Issues[0].Code != "MISSING_EFFECTIVE_RULE" {
		t.Fatalf("draft=%+v, error=%v", got, err)
	}
}

func TestEmptyDraftStillReportsUnsupportedComponent(t *testing.T) {
	start := time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC)
	window := periodWindow{Start: start, Through: start.Add(time.Hour)}
	rules := []paymentRule{{ID: 12, Components: []component{{Type: "manager", Mode: "fixed", Value: 200}}}}
	got, err := calculatePaymentDraft(window, rules, nil, nil)
	if err != nil || len(got.Issues) != 1 || got.Issues[0].RuleID != 12 || got.Issues[0].Code != "UNCONFIGURED_COMPONENT" {
		t.Fatalf("draft=%+v, error=%v", got, err)
	}
}
