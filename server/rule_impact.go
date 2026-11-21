package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

var (
	errPracticeRuleImpactNoMatch = errors.New("拟发布的规则未匹配所选班次，请核对生效时间、开班方式或该班次的专用规则")
	errRuleImpactAggregatePeriod = errors.New("请选择逐单账期进行规则差额试算")
)

type ruleImpactCandidate struct {
	classVersion int
	catalog      *catalogRuleImpactMatch
}

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
	if p.PeriodID != nil && *p.PeriodID != input.PeriodID {
		return nil, invalid
	}
	if err := validateRulePublication(p); err != nil {
		return nil, err
	}
	return s.compareRuleImpact(ctx, input.PeriodID, p.ProductID, p.FromOrdinal, func(tx pgx.Tx) (ruleImpactCandidate, error) {
		version, err := writeComponentRule(ctx, tx, p, false)
		return ruleImpactCandidate{classVersion: version}, err
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
		return s.compareRuleImpact(ctx, input.PeriodID, product, nil, func(tx pgx.Tx) (ruleImpactCandidate, error) {
			version, err := writeCatalogRates(ctx, tx, input.Rule, actor.subject, false)
			if err != nil || version.ID == input.Rule.BaseID {
				return ruleImpactCandidate{}, err
			}
			return ruleImpactCandidate{catalog: &catalogRuleImpactMatch{ruleID: version.ID}}, nil
		})
	})(w, r)
}

func (s workspace) compareRuleImpact(ctx context.Context, periodID, productID int64, fromOrdinal *int,
	apply func(pgx.Tx) (ruleImpactCandidate, error)) (ruleImpactReport, error) {
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
	var refundMonth, settled, aggregate bool
	if err := tx.QueryRow(ctx, `SELECT period.ordinal,(`+practiceRefundMonthScope+`),st.period_id IS NOT NULL,period.aggregate_package_id IS NOT NULL
	 FROM periods period JOIN products product ON product.id=period.product_id
	 LEFT JOIN settlements st ON st.period_id=period.id WHERE period.id=$1 AND period.product_id=$2`,
		periodID, productID).Scan(&ordinal, &refundMonth, &settled, &aggregate); err != nil {
		return report, err
	}
	if aggregate {
		return report, errRuleImpactAggregatePeriod
	}
	if refundMonth || fromOrdinal != nil && ordinal < *fromOrdinal {
		return report, invalid
	}
	if settled {
		return report, errSettlementClosed
	}
	before, err := calculateRuleImpactDraft(ctx, tx, periodID, ruleImpactCandidate{})
	currentRuleAvailable := !errors.Is(err, errDraftRule)
	if err != nil && currentRuleAvailable {
		return report, err
	}
	candidate, err := apply(tx)
	if err != nil {
		return report, err
	}
	after, err := calculateRuleImpactDraft(ctx, tx, periodID, candidate)
	if err != nil {
		return report, err
	}
	report, err = readRuleImpactReport(ctx, tx, before, after)
	if err != nil {
		return report, err
	}
	report.CurrentRuleAvailable = currentRuleAvailable
	if candidate.catalog != nil {
		report.MatchedPaymentCount = &candidate.catalog.payments
	}
	// The candidate uses the same rule matching as publication, within this
	// snapshot. Roll it back explicitly before returning the computed report.
	return report, tx.Rollback(ctx)
}

func calculateRuleImpactDraft(ctx context.Context, tx pgx.Tx, periodID int64, candidate ruleImpactCandidate) (paymentDraft, error) {
	summary := draftSummary{PeriodID: periodID}
	window, rules, err := readDraftScope(ctx, tx, &summary)
	if err != nil {
		if summary.PracticeRosterID != nil && candidate.classVersion != 0 && errors.Is(err, errDraftRule) {
			return paymentDraft{}, errPracticeRuleImpactNoMatch
		}
		return paymentDraft{}, err
	}
	if summary.PracticeRosterID != nil && candidate.classVersion != 0 && summary.RuleVersion != candidate.classVersion {
		return paymentDraft{}, errPracticeRuleImpactNoMatch
	}
	// No shared quote cache or saved draft is used for an unpublished rule.
	if summary.Catalog && candidate.catalog != nil {
		result, _, err := computeVIPDraft(ctx, tx, summary.PeriodID, window, nil, candidate.catalog)
		return result, err
	}
	result, _, err := computeDraft(ctx, tx, summary, window, rules, nil)
	return result, err
}
