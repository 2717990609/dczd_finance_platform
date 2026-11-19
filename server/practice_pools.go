package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type practicePoolTarget struct {
	PoolID  int64 `json:"poolId"`
	OwnerID int64 `json:"ownerId"`
}

func (p practicePoolTarget) valid() bool { return p.PoolID > 0 && p.OwnerID > 0 }

type createPracticePoolInput struct {
	ProductID int64  `json:"productId"`
	UnitID    string `json:"unitId"`
	Name      string `json:"name"`
}

func (s workspace) createPracticePool(ctx context.Context, input createPracticePoolInput) (any, error) {
	unit, ok := canonicalSourceUUID(input.UnitID)
	if input.ProductID <= 0 || !ok || !textOK(input.Name, 80) {
		return nil, invalid
	}
	if actor := financeUser(ctx); actor != nil && !actor.permitsRule(input.ProductID, true) {
		return nil, errFinanceForbidden
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO practice_pools(product_id,unit_id,name)
	 SELECT p.id,u.id,btrim($3) FROM products p CROSS JOIN organization_units u
	 WHERE p.id=$1 AND p.period_mode='class' AND u.id=$2::uuid AND u.status='ACTIVE'
	 ON CONFLICT(product_id,unit_id,name) DO UPDATE SET name=excluded.name RETURNING id`,
		input.ProductID, unit, input.Name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = invalid
	}
	return map[string]int64{"id": id}, err
}

func practicePoolTargets(items []component) []practicePoolTarget {
	var targets []practicePoolTarget
	for _, part := range items {
		if b := part.PeriodBenefit; b != nil && b.Pool != nil {
			targets = append(targets, *b.Pool)
		}
		for _, split := range part.retrainSplits() {
			targets = append(targets, split.Pool)
		}
	}
	return targets
}

func readPracticePoolUnits(ctx context.Context, tx pgx.Tx, periodID int64, items []component) (map[int64]pgtype.UUID, error) {
	targets := practicePoolTargets(items)
	if len(targets) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(targets))
	for _, p := range targets {
		ids = append(ids, p.PoolID)
	}
	rows, err := tx.Query(ctx, `SELECT p.id,p.unit_id FROM practice_pools p
	 JOIN periods period ON period.product_id=p.product_id WHERE period.id=$1 AND p.id=ANY($2::bigint[])`, periodID, ids)
	if err != nil {
		return nil, err
	}
	units := make(map[int64]pgtype.UUID)
	var id int64
	var unit pgtype.UUID
	_, err = pgx.ForEachRow(rows, []any{&id, &unit}, func() error {
		units[id] = unit
		return nil
	})
	return units, err
}

type practiceRetrainSplit struct {
	CompanyID       string             `json:"companyId"`
	Pool            practicePoolTarget `json:"pool"`
	PersonalMinor   int64              `json:"personalMinor"`
	ServicePersonID int64              `json:"servicePersonId,omitempty"`
	ServiceMinor    int64              `json:"serviceMinor,omitempty"`
}

func (part component) retrainSplits() []practiceRetrainSplit {
	if part.RetrainSplits == nil {
		return nil
	}
	return *part.RetrainSplits
}

func validPracticeRetrainSplits(part component) bool {
	if len(part.retrainSplits()) == 0 {
		return true
	}
	if part.Type != "sales" || part.Mode != "fixed" || part.ClassMember != "retrain" || !validSourceRole(part.SourceRole) {
		return false
	}
	seen := make(map[string]bool)
	for _, r := range part.retrainSplits() {
		if !textOK(r.CompanyID, 128) || seen[r.CompanyID] || !r.Pool.valid() || r.PersonalMinor < 0 || r.PersonalMinor > part.Value ||
			r.ServiceMinor < 0 || r.ServiceMinor > part.Value-r.PersonalMinor || r.ServicePersonID < 0 ||
			(r.ServiceMinor != 0) != (r.ServicePersonID != 0) {
			return false
		}
		seen[r.CompanyID] = true
	}
	return true
}

func (p practicePurchase) retrainSplit(splits []practiceRetrainSplit) (*practiceRetrainSplit, string) {
	if len(splits) == 0 {
		return nil, ""
	}
	company, issue := practicePurchaseCompany(p.payments)
	if issue != "" {
		return nil, issue
	}
	for i := range splits {
		if splits[i].CompanyID == company {
			return &splits[i], ""
		}
	}
	return nil, ""
}

func appendPracticeRetrainSplit(draft *paymentDraft, p practicePurchase, rule paymentRule, index int, amount int64,
	sellers []identityAssignment, split practiceRetrainSplit, unit pgtype.UUID) error {
	var err error
	draft.Total, err = addMinor(draft.Total, amount)
	if err != nil {
		return err
	}
	total := practiceGrossShares(p, p.payments, amount)
	personal := practiceGrossShares(p, p.payments, split.PersonalMinor)
	cumulative := practiceGrossShares(p, p.payments, split.PersonalMinor+split.ServiceMinor)
	for i, payment := range p.payments {
		line := draftLine{FactID: payment.ID, RuleID: rule.ID, EnrollmentID: p.member.ID, Base: p.netAmount(payment)}
		if personal[i] > 0 {
			sale := line
			sale.Component, sale.PersonID, sale.IdentityID, sale.Amount = "sales", sellers[i].PersonID, sellers[i].ID, personal[i]
			draft.Lines = append(draft.Lines, sale)
		}
		if share := cumulative[i] - personal[i]; share > 0 {
			service := line
			service.Component = practicePeriodComponentKey(component{Type: "retrain_service"}, index)
			service.PersonID, service.Amount = split.ServicePersonID, share
			draft.Lines = append(draft.Lines, service)
		}
		if share := total[i] - cumulative[i]; share > 0 {
			line.Component = practicePeriodComponentKey(component{Type: "retrain_pool"}, index)
			line.PracticePool, line.BeneficiaryUnit, line.Amount = &split.Pool, &unit, share
			draft.Lines = append(draft.Lines, line)
		}
	}
	return nil
}
