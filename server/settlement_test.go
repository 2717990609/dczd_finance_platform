package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestSettlementChecksFreshnessFreezesReferencesAndAllowsTechnicalRedo(t *testing.T) {
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
	product := mustID(service.createProduct(ctx, createProductInput{
		Code: "SETTLEMENT_TEST", Name: "Settlement Test", PeriodMode: "calendar", CycleStartDay: &day,
	}))
	period := mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "August", SettlementMonth: "2026-08"}))
	previous := mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "July", SettlementMonth: "2026-07"}))
	person := mustID(service.createPerson(ctx, createPersonInput{Name: "Sample Salesperson"}))
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "VIP", SourceKey: "VIP"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCAI", ExternalID: "sample", ValidFrom: "2026-01-01T00:00:00+08:00"}))
	first := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	effective := first.Add(time.Hour)
	for _, p := range []publishRuleInput{
		{ProductID: product, Label: "Before", Components: []component{{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "LOCK_SALES_USER"}}},
		{ProductID: product, Label: "After", EffectiveFrom: &effective, Components: []component{{Type: "sales", Mode: "rate", Value: 2000, SourceRole: "LOCK_SALES_USER"}}},
	} {
		if _, err := service.publishRule(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	store := func(order, version string, minor int64, occurred, updated time.Time) {
		t.Helper()
		err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{{
				Source: "DCAI", OrderID: order, PaymentID: order, RecordKey: order, Domain: "VIP", SourceKey: "VIP",
				Kind: "ORDER_PAID", AmountMinor: &minor, Version: version, OccurredAt: occurred, UpdatedAt: updated,
				Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "sample"}},
			}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	store("first", "1", 100, first, first)
	store("second", "1", 100, effective, effective)
	draft := func(want int64) draftSummary {
		t.Helper()
		value, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		got := value.(draftSummary)
		if got.Total != want || got.LineCount != 2 || got.IssueCount != 0 {
			t.Fatalf("draft=%+v", got)
		}
		return got
	}
	initial := draft(30)
	req := confirmSettlementInput{PeriodID: period, ExpectedVersion: initial.Version}
	if _, err := service.confirmSettlement(ctx, req, "integration-test"); !errors.Is(err, errSettlementDisabled) {
		t.Fatalf("default configuration allowed settlement: %v", err)
	}
	if _, err := service.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	store("first", "2", 200, first, effective.Add(time.Second))
	if _, err := service.confirmSettlement(ctx, req, "integration-test"); !errors.Is(err, errSettlementStale) {
		t.Fatalf("confirmation accepted stale input: %v", err)
	}
	current := draft(40)
	req.ExpectedVersion = current.Version
	other := mustID(service.createPerson(ctx, createPersonInput{Name: "Another Payee"}))
	var line int64
	if err := service.db.QueryRow(ctx, `SELECT min(id) FROM calculation_draft_lines WHERE period_id=$1`, period).Scan(&line); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name, column  string
		before, after int64
	}{
		{"same amount, different payee", "person_id", person, other},
		{"changed amount", "amount_minor", 20, 21},
	} {
		t.Run(change.name, func(t *testing.T) {
			if _, err := service.db.Exec(ctx, `UPDATE calculation_draft_lines SET `+change.column+`=$2 WHERE id=$1`, line, change.after); err != nil {
				t.Fatal(err)
			}
			confirmed, err := service.confirmSettlement(ctx, req, "integration-test")
			if !errors.Is(err, errSettlementStale) {
				t.Errorf("confirmation accepted changed stored postings: %+v %v", confirmed, err)
				if err == nil {
					if _, err := service.db.Exec(ctx, "DELETE FROM settlements WHERE id=$1", confirmed.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := service.db.Exec(ctx, `UPDATE calculation_draft_lines SET `+change.column+`=$2 WHERE id=$1`, line, change.before); err != nil {
				t.Fatal(err)
			}
			current = draft(40)
			req.ExpectedVersion = current.Version
		})
	}
	t.Run("open draft from before posting verification", func(t *testing.T) {
		if _, err := service.db.Exec(ctx, `UPDATE calculation_drafts SET posting_digest=NULL WHERE period_id=$1`, period); err != nil {
			t.Fatal(err)
		}
		if _, err := service.db.Exec(ctx, "DELETE FROM finance_schema_revisions"); err != nil {
			t.Fatal(err)
		}
		if err := migrate(ctx, service.db); err != nil {
			t.Fatal(err)
		}
		var queued bool
		if err := service.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, period).Scan(&queued); err != nil || !queued {
			t.Fatalf("legacy open draft did not queue for recalculation: %t %v", queued, err)
		}
		if _, err := service.confirmSettlement(ctx, req, "integration-test"); !errors.Is(err, errSettlementStale) {
			t.Fatalf("legacy open draft could be confirmed without verification: %v", err)
		}
		current = draft(40)
		req.ExpectedVersion = current.Version
	})
	type outcome struct {
		settlement settlement
		err        error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			got, err := service.confirmSettlement(ctx, req, "integration-test")
			results <- outcome{got, err}
		}()
	}
	close(start)
	firstResult, secondResult := <-results, <-results
	firstConfirmation := firstResult.settlement
	if firstResult.err != nil || secondResult.err != nil || firstConfirmation.ID < 1 || firstConfirmation.ID != secondResult.settlement.ID {
		t.Fatalf("concurrent confirmations=%+v %+v", firstResult, secondResult)
	}
	repeat, err := service.confirmSettlement(ctx, req, "integration-test")
	if err != nil || repeat.ID != firstConfirmation.ID {
		t.Fatalf("repeated confirmation=%+v, error=%v", repeat, err)
	}
	for _, sql := range []string{
		"UPDATE calculation_draft_lines SET amount_minor=0 WHERE period_id=$1",
		"UPDATE calculation_drafts SET total_minor=0 WHERE period_id=$1",
		"UPDATE settlements SET confirmed_by='another' WHERE period_id=$1",
		"UPDATE source_order_versions SET fact='{}' WHERE id IN (SELECT source_version_id FROM calculation_draft_lines WHERE period_id=$1)",
		"UPDATE rule_versions SET label='another' WHERE id IN (SELECT rule_id FROM calculation_draft_lines WHERE period_id=$1)",
	} {
		if _, err = service.db.Exec(ctx, sql, period); err == nil {
			t.Fatalf("database allowed frozen result modification: %s", sql)
		}
	}
	t.Run("confirmed cutoff", func(t *testing.T) {
		for _, id := range []int64{period, previous} {
			if _, err := service.db.Exec(ctx, "UPDATE periods SET refund_cutoff_at='2026-07-26T00:00:00+08:00' WHERE id=$1", id); err == nil {
				t.Errorf("database allowed cutoff %d to change a confirmed refund window", id)
			}
		}
	})
	store("second", "2", 200, effective, effective.Add(2*time.Second))
	if _, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: period}); !errors.Is(err, errSettlementClosed) {
		t.Fatalf("recalculation changed a frozen scope: %v", err)
	}
	if _, err := service.db.Exec(ctx, "DELETE FROM settlements WHERE id=$1", firstConfirmation.ID); err != nil {
		t.Fatal(err)
	}
	var recorded bool
	if err := service.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM settlement_removals WHERE settlement_id=$1 AND period_id=$2 AND removed_by=session_user)", firstConfirmation.ID, period).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("technical removal was not recorded: %v", err)
	}
	corrected := draft(60)
	req.ExpectedVersion = corrected.Version
	redo, err := service.confirmSettlement(ctx, req, "integration-test")
	if err != nil || redo.ID == firstConfirmation.ID || redo.DraftVersion != corrected.Version {
		t.Fatalf("recomputed confirmation=%+v, error=%v", redo, err)
	}
	var oldTotal int64
	if err := service.db.QueryRow(ctx, `SELECT coalesce(sum((body->>'amount_minor')::bigint),0)
	 FROM settlement_removal_rows WHERE settlement_id=$1 AND section='accrual'`, firstConfirmation.ID).Scan(&oldTotal); err != nil || oldTotal != 40 {
		t.Fatalf("original confirmed postings unavailable after redo: total=%d, error=%v", oldTotal, err)
	}
	if _, err := service.db.Exec(ctx, `DELETE FROM settlement_removal_rows WHERE settlement_id=$1`, firstConfirmation.ID); err == nil {
		t.Fatal("database allowed an archived settlement to be changed")
	}
	if _, err := service.db.Exec(ctx, "DELETE FROM settlements WHERE id=$1", redo.ID); err != nil {
		t.Fatal(err)
	}
	verifyLocalCalendarSettlementHTTP(t, service, draft(60))
}
