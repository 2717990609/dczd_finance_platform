package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRuleRetirementStopsSameScopeWithoutChangingPreviousVersions(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	day := 1
	productAny, err := s.createProduct(ctx, createProductInput{Code: "RETIRE-FIXTURE", Name: "停用样本", PeriodMode: "calendar", CycleStartDay: &day})
	if err != nil {
		t.Fatal(err)
	}
	product := productAny.(map[string]int64)["id"]
	periodAny, err := s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "2026-10", SettlementMonth: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	period := periodAny.(map[string]int64)["id"]
	for _, input := range []publishRuleInput{
		{ProductID: product, Label: "产品规则", Components: []component{{Type: "sales", Mode: "rate", Value: 100, SourceRole: "SELLER"}}},
		{ProductID: product, PeriodID: &period, Label: "账期 v1", Components: []component{{Type: "sales", Mode: "rate", Value: 200, SourceRole: "SELLER"}}},
		{ProductID: product, PeriodID: &period, Label: "账期 v2", Components: []component{{Type: "sales", Mode: "rate", Value: 300, SourceRole: "SELLER"}}},
	} {
		if _, err := s.publishRule(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	var latest, defaultID int64
	if err := s.db.QueryRow(ctx, `SELECT max(id),min(id) FROM rule_versions WHERE product_id=$1`, product).Scan(&latest, &defaultID); err != nil {
		t.Fatal(err)
	}
	draft, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`,
		period, draft.(draftSummary).Version); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 10, 4, 0, 0, 0, 0, shanghai)
	denied := context.WithValue(ctx, financePrincipalKey{}, &financePrincipal{Rules: []financeRuleGrant{{ProductID: product}}})
	in := retireRuleInput{ProductID: product, RuleID: latest, EffectiveFrom: cutoff}
	if _, err := s.retireRule(denied, in); !errors.Is(err, errFinanceForbidden) {
		t.Fatalf("read-only actor retired a rule: %v", err)
	}
	if _, err := s.retireRule(ctx, in); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		summary := draftSummary{PeriodID: period}
		_, rules, err := readDraftScope(ctx, tx, &summary)
		if err != nil {
			return err
		}
		if before, after := selectPaymentRule(rules, cutoff.Add(-time.Microsecond)), selectPaymentRule(rules, cutoff); before == nil || after == nil || before.ID != latest || after.ID != defaultID {
			t.Fatalf("rule before cutoff=%+v, after cutoff=%+v", before, after)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM rule_versions WHERE product_id=$1`, product).Scan(&count); err != nil || count != 3 {
		t.Fatalf("existing versions were changed: %d %v", count, err)
	}
	var periods []struct {
		RuleVersion int `json:"ruleVersion"`
	}
	if err := s.db.QueryRow(ctx, periodsSQL, []int64{product}).Scan(&periods); err != nil || len(periods) != 1 || periods[0].RuleVersion != 3 {
		t.Fatalf("closed period lost its frozen rule: %v %v", periods, err)
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM draft_refresh_queue WHERE period_id=$1`, period).Scan(&count); err != nil || count != 0 {
		t.Fatalf("closed period was queued for refresh: %d %v", count, err)
	}
}
