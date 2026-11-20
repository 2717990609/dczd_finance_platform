package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type componentRuleImpactInput struct {
	PeriodID int64            `json:"periodId"`
	Rule     publishRuleInput `json:"rule"`
}

type catalogRuleImpactInput struct {
	PeriodID int64                    `json:"periodId"`
	Rule     publishCatalogRatesInput `json:"rule"`
}

func (s workspace) postComponentRuleImpact(ctx context.Context, input componentRuleImpactInput) (any, error) {
	p := input.Rule
	if actor := financeUser(ctx); actor != nil && !actor.permitsRule(p.ProductID, true) {
		return nil, errFinanceForbidden
	}
	if !validRulePublication(p) || p.PeriodID != nil && *p.PeriodID != input.PeriodID {
		return nil, invalid
	}
	return s.compareRuleImpact(ctx, input.PeriodID, p.ProductID, p.FromOrdinal, func(tx pgx.Tx) error {
		_, err := writeComponentRule(ctx, tx, p, false)
		return err
	})
}

func (s workspace) postCatalogRuleImpact(w http.ResponseWriter, r *http.Request) {
	actor, err := s.financeRateActor(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	submitJSON(128<<10, func(ctx context.Context, input catalogRuleImpactInput) (ruleImpactReport, error) {
		_, product, err := s.ruleRateProduct(ctx, input.Rule.BaseID)
		if err != nil {
			return ruleImpactReport{}, err
		}
		if !actor.permits(product, true) {
			return ruleImpactReport{}, errRulePermission
		}
		if actor.account != nil {
			ctx = context.WithValue(ctx, financePrincipalKey{}, actor.account)
		}
		return s.compareRuleImpact(ctx, input.PeriodID, product, nil, func(tx pgx.Tx) error {
			_, err := writeCatalogRates(ctx, tx, input.Rule, actor.subject, false)
			return err
		})
	})(w, r)
}

func (s workspace) compareRuleImpact(ctx context.Context, periodID, productID int64, fromOrdinal *int, apply func(pgx.Tx) error) (ruleImpactReport, error) {
	var report ruleImpactReport
	if periodID < 1 {
		return report, invalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return report, err
	}
	defer tx.Rollback(ctx)
	var ordinal int
	var refundMonth, settled bool
	if err := tx.QueryRow(ctx, `SELECT period.ordinal,(`+practiceRefundMonthScope+`),st.period_id IS NOT NULL
	 FROM periods period JOIN products product ON product.id=period.product_id
	 LEFT JOIN settlements st ON st.period_id=period.id WHERE period.id=$1 AND period.product_id=$2`,
		periodID, productID).Scan(&ordinal, &refundMonth, &settled); err != nil {
		return report, err
	}
	if refundMonth || fromOrdinal != nil && ordinal < *fromOrdinal {
		return report, invalid
	}
	if settled {
		return report, errSettlementClosed
	}
	before, err := calculateRuleImpactDraft(ctx, tx, periodID)
	currentRuleAvailable := !errors.Is(err, errDraftRule)
	if err != nil && currentRuleAvailable {
		return report, err
	}
	if err := apply(tx); err != nil {
		return report, err
	}
	after, err := calculateRuleImpactDraft(ctx, tx, periodID)
	if err != nil {
		return report, err
	}
	report, err = readRuleImpactReport(ctx, tx, before, after)
	if err != nil {
		return report, err
	}
	report.CurrentRuleAvailable = currentRuleAvailable
	// The candidate uses the same rule matching as publication, within this
	// snapshot. Roll it back explicitly before returning the computed report.
	return report, tx.Rollback(ctx)
}

func calculateRuleImpactDraft(ctx context.Context, tx pgx.Tx, periodID int64) (paymentDraft, error) {
	summary := draftSummary{PeriodID: periodID}
	window, rules, err := readDraftScope(ctx, tx, &summary)
	if err != nil {
		return paymentDraft{}, err
	}
	// No shared quote cache or saved draft is used for an unpublished rule.
	result, _, err := computeDraft(ctx, tx, summary, window, rules, nil)
	return result, err
}
