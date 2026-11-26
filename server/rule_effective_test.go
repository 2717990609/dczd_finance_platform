package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestPracticePeriodListUsesValidAndConfirmedRule(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	createID := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := createID(s.createProduct(ctx, createProductInput{Code: "CLASS_RULE_LIST", Name: "实战班", PeriodMode: "class"}))
	const key = "PRACTICE:2:1:157"
	period := createID(s.createPeriod(ctx, createPeriodInput{ProductID: product, Ordinal: 157, Name: "157 期",
		StartsOn: "2026-11-06", EndsOn: "2026-11-07", SourcePeriodKey: key}))
	cut := time.Date(2026, 11, 8, 14, 30, 0, 0, shanghai)
	publish := func(label string, at *time.Time) {
		t.Helper()
		if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: label, EffectiveFrom: at, Components: []component{
			{Type: "sales", Mode: "rate", Value: 1000, ClassMember: "new", SourceRole: "DIRECT_REFERRER"},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	publish("默认规则", nil)
	publish("本班确认规则", &cut)
	store := func(version int64, startsAt time.Time) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: key, SourceVersion: version, Hash: "test-roster",
				StartsAt: startsAt, ConfirmedAt: cut.Add(time.Duration(version) * time.Hour), Enrollments: []practiceEnrollment{}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	type periodRule struct {
		RuleVersion   *int
		RuleLabel     string
		ClassStartsAt time.Time
	}
	read := func() periodRule {
		t.Helper()
		var records []periodRule
		if err := s.db.QueryRow(ctx, periodsSQL, []int64{product}).Scan(&records); err != nil || len(records) != 1 {
			t.Fatalf("period list: %+v %v", records, err)
		}
		return records[0]
	}
	store(1, cut.AddDate(0, 0, -18))
	if got := read(); got.RuleVersion == nil || *got.RuleVersion != 1 {
		t.Fatalf("a linked roster should select the rule at its own class time: %+v", got)
	}
	if _, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period}); err != nil {
		t.Fatalf("a linked roster should calculate without matching the accounting dates: %v", err)
	}
	store(2, cut)
	if got := read(); got.RuleVersion == nil || *got.RuleVersion != 2 || got.RuleLabel != "本班确认规则" {
		t.Fatalf("the actual class time should select rule v2: %+v", got)
	}
	d, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`,
		period, d.(draftSummary).Version); err != nil {
		t.Fatal(err)
	}
	updated := cut.Add(30 * time.Minute)
	publish("后续生效规则", &updated)
	store(3, updated)
	if got := read(); !got.ClassStartsAt.Equal(cut) || got.RuleVersion == nil ||
		*got.RuleVersion != 2 || got.RuleLabel != "本班确认规则" {
		t.Fatalf("confirmed listing should keep the frozen roster and rule: %+v", got)
	}
}
