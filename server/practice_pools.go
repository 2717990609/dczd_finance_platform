package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

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

type teamPool struct {
	ID       int64  `json:"id"`
	UnitID   string `json:"unitId"`
	Name     string `json:"name"`
	UnitName string `json:"unitName"`
}

const catalogPoolPrefix = "team-pool:"

func catalogPoolID(code string) int64 {
	key, ok := strings.CutPrefix(code, catalogPoolPrefix)
	id, err := strconv.ParseInt(key, 10, 64)
	if !ok || err != nil || id <= 0 || strconv.FormatInt(id, 10) != key {
		return 0
	}
	return id
}

func catalogRegisteredPool(pools []teamPool, id int64) (teamPool, bool) {
	i := slices.IndexFunc(pools, func(pool teamPool) bool { return pool.ID == id })
	if i < 0 {
		return teamPool{}, false
	}
	return pools[i], true
}

func (p teamPool) bind(action *vipBenefitAction) {
	action.PoolCode = catalogPoolPrefix + strconv.FormatInt(p.ID, 10)
	action.PoolUnit, action.PoolParty, action.PoolName = p.UnitID, catalogPeriodUnitPrefix+p.UnitID, p.Name
}

func readCatalogTeamPools(ctx context.Context, db catalogPeriodReader, productID int64) ([]teamPool, error) {
	var pools []teamPool
	err := db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('id',p.id,'name',p.name,'unitId',u.id,
	  'unitName',concat_ws(' / ',parent.name,u.name)) ORDER BY p.name,p.id),'[]')
	 FROM practice_pools p JOIN organization_units u ON u.id=p.unit_id
	 LEFT JOIN organization_units parent ON parent.id=u.parent_id WHERE p.product_id=$1 AND u.status='ACTIVE'`, productID).Scan(&pools)
	return pools, err
}

func (s workspace) listPracticePools(w http.ResponseWriter, r *http.Request) {
	product, err := strconv.ParseInt(r.URL.Query().Get("productId"), 10, 64)
	if err != nil || product <= 0 {
		respond(w, nil, invalid)
		return
	}
	actor := financeUser(r.Context())
	if actor != nil && !actor.permitsRule(product, false) {
		respond(w, nil, errFinanceForbidden)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), `SELECT jsonb_build_object(
	 'items',coalesce((SELECT jsonb_agg(jsonb_build_object('id',p.id,'name',p.name,
	  'unitName',concat_ws(' / ',parent.name,u.name),'unitStatus',u.status) ORDER BY parent.name,u.name,p.name,p.id)
	  FROM practice_pools p JOIN organization_units u ON u.id=p.unit_id
	  LEFT JOIN organization_units parent ON parent.id=u.parent_id WHERE p.product_id=$1),'[]'),
	 'units',coalesce((SELECT jsonb_agg(jsonb_build_object('id',u.id,'name',concat_ws(' / ',parent.name,u.name)) ORDER BY parent.name,u.name,u.id)
	  FROM organization_units u LEFT JOIN organization_units parent ON parent.id=u.parent_id
	  WHERE u.status='ACTIVE' AND $2),'[]'))
	 FROM products WHERE id=$1 AND period_mode='class'`, product,
		actor == nil || actor.permitsRule(product, true)).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		err = invalid
	}
	respond(w, result, err)
}

func (s workspace) createPracticePool(ctx context.Context, input createPracticePoolInput) (any, error) {
	pool, err := s.upsertTeamPool(ctx, input, "class")
	return map[string]int64{"id": pool.ID}, err
}

func (s workspace) createCatalogTeamPool(ctx context.Context, input createPracticePoolInput) (any, error) {
	return s.upsertTeamPool(ctx, input, "calendar")
}

func (s workspace) upsertTeamPool(ctx context.Context, input createPracticePoolInput, mode string) (teamPool, error) {
	var pool teamPool
	unit, ok := canonicalSourceUUID(input.UnitID)
	if input.ProductID <= 0 || !ok || !textOK(input.Name, 80) {
		return pool, invalid
	}
	if actor := financeUser(ctx); actor != nil && !actor.permitsRule(input.ProductID, true) {
		return pool, errFinanceForbidden
	}
	err := s.db.QueryRow(ctx, `WITH saved AS (
	 INSERT INTO practice_pools(product_id,unit_id,name)
	 SELECT p.id,u.id,btrim($3) FROM products p CROSS JOIN organization_units u
	 WHERE p.id=$1 AND p.period_mode=$4 AND u.id=$2::uuid AND u.status='ACTIVE'
	 ON CONFLICT(product_id,unit_id,name) DO UPDATE SET name=excluded.name RETURNING id,unit_id,name)
	 SELECT saved.id,saved.unit_id::text,saved.name,concat_ws(' / ',parent.name,u.name)
	 FROM saved JOIN organization_units u ON u.id=saved.unit_id
	 LEFT JOIN organization_units parent ON parent.id=u.parent_id`,
		input.ProductID, unit, input.Name, mode).Scan(&pool.ID, &pool.UnitID, &pool.Name, &pool.UnitName)
	if errors.Is(err, pgx.ErrNoRows) {
		err = invalid
	}
	return pool, err
}

func practicePoolTargets(items []component) []practicePoolTarget {
	var targets []practicePoolTarget
	for _, part := range items {
		if b := part.PeriodBenefit; b != nil && b.Pool != nil {
			targets = append(targets, *b.Pool)
		}
		if s := part.CompanySales; s != nil && s.Payee != nil && s.Payee.Pool != nil {
			targets = append(targets, *s.Payee.Pool)
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
	CompanyID          string               `json:"companyId"`
	Pool               practicePoolTarget   `json:"pool"`
	PersonalMinor      int64                `json:"personalMinor"`
	SalesRate          *practiceRetrainRate `json:"salesRate,omitempty"`
	ServicePersonID    int64                `json:"servicePersonId,omitempty"`
	ServiceMinor       int64                `json:"serviceMinor,omitempty"`
	ServiceRate        *practiceRetrainRate `json:"serviceRate,omitempty"`
	ServiceLabel       string               `json:"serviceLabel,omitempty"`
	ServiceSalesPeople []int64              `json:"serviceSalesPeople,omitempty"`
	ServiceSalesUnits  []string             `json:"serviceSalesUnits,omitempty"`
}

const (
	practiceRetrainCommissionBasis = "commission"
	practiceRetrainReceiptsBasis   = "net_receipts"
)

type practiceRetrainRate struct {
	Basis string `json:"basis"`
	Value int64  `json:"value"`
}

func (r *practiceRetrainRate) validWith(fixed int64) bool {
	if r == nil {
		return fixed >= 0 && fixed <= 100000000000
	}
	return fixed == 0 && r.Value >= 0 && r.Value <= periodRateScale &&
		(r.Basis == practiceRetrainCommissionBasis || r.Basis == practiceRetrainReceiptsBasis)
}

func (r *practiceRetrainRate) amount(commission, net, fixed int64) int64 {
	if r == nil {
		return fixed
	}
	if r.Basis == practiceRetrainReceiptsBasis {
		return practiceCompanyRate(net, r.Value)
	}
	return practiceCompanyRate(commission, r.Value)
}

type practiceRetrainAmounts struct {
	commission, sales, service int64
}

func (r practiceRetrainSplit) amounts(commission, net int64) (practiceRetrainAmounts, bool) {
	sales := r.SalesRate.amount(commission, net, r.PersonalMinor)
	service := r.ServiceRate.amount(commission, net, r.ServiceMinor)
	if a, b := r.SalesRate, r.ServiceRate; a != nil && b != nil && a.Basis == b.Basis {
		if a.Value > periodRateScale-b.Value {
			return practiceRetrainAmounts{}, false
		}
		// Allocate cents once across shares using the same base, then pass the remainder to the pool.
		combined := practiceRetrainRate{Basis: a.Basis, Value: a.Value + b.Value}
		service = combined.amount(commission, net, 0) - sales
	}
	return practiceRetrainAmounts{commission, sales, service}, sales <= commission-service
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
	if !isPracticeRetrainWithSplits(part) || !validSourceRole(part.SourceRole) {
		return false
	}
	seen := make(map[string]bool)
	for _, r := range part.retrainSplits() {
		if !textOK(r.CompanyID, 128) || seen[r.CompanyID] || !r.Pool.valid() ||
			!r.SalesRate.validWith(r.PersonalMinor) || !r.ServiceRate.validWith(r.ServiceMinor) || r.ServicePersonID < 0 ||
			(r.ServiceMinor > 0 || r.ServiceRate != nil && r.ServiceRate.Value > 0) && r.ServicePersonID == 0 ||
			r.ServiceRate == nil && r.ServiceMinor == 0 && r.ServicePersonID != 0 {
			return false
		}
		if r.ServiceLabel != "" && !textOK(r.ServiceLabel, 80) ||
			r.ServicePersonID == 0 && (r.ServiceLabel != "" || len(r.ServiceSalesPeople)+len(r.ServiceSalesUnits) != 0) {
			return false
		}
		for i, person := range r.ServiceSalesPeople {
			if person <= 0 || slices.Contains(r.ServiceSalesPeople[:i], person) {
				return false
			}
		}
		for i, unit := range r.ServiceSalesUnits {
			if normalized, ok := canonicalSourceUUID(unit); !ok || normalized != unit || slices.Contains(r.ServiceSalesUnits[:i], unit) {
				return false
			}
		}
		if _, valid := r.amounts(part.Value, 0); part.Mode == "fixed" && !valid {
			return false
		}
		if part.Mode == "rate" && r.SalesRate != nil && r.ServiceRate != nil &&
			r.SalesRate.Basis == r.ServiceRate.Basis && r.SalesRate.Value > periodRateScale-r.ServiceRate.Value {
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

func appendPracticeRetrainSplit(draft *paymentDraft, p practicePurchase, rule paymentRule, index int, amounts practiceRetrainAmounts,
	sellers []identityAssignment, split practiceRetrainSplit, unit pgtype.UUID, proportional, reduceFixedShares bool, serviceMatches [2]bool) error {
	var totalAccrued int64
	total := p.benefitShares(p.payments, amounts.commission, proportional)
	personal := p.benefitShares(p.payments, amounts.sales, proportional)
	cumulative := p.benefitShares(p.payments, amounts.sales+amounts.service, proportional)
	for i, payment := range p.payments {
		if !serviceMatches[i] {
			cumulative[i] = personal[i]
		}
		sales, service, pool := personal[i], cumulative[i]-personal[i], total[i]-cumulative[i]
		if reduceFixedShares {
			sales = p.remainingFixedShare(payment, sales)
			service = p.remainingFixedShare(payment, service)
			pool = p.remainingFixedShare(payment, pool)
		}
		totalAccrued += sales + service + pool
		line := draftLine{FactID: payment.ID, RuleID: rule.ID, EnrollmentID: p.member.ID, Base: p.netAmount(payment), practiceProportionalRefund: true}
		if sales > 0 {
			sale := line
			sale.Component, sale.PersonID, sale.IdentityID, sale.Amount = "sales", sellers[i].PersonID, sellers[i].ID, sales
			sale.practiceArrangementLabel = rule.Components[index].arrangementLabel()
			draft.Lines = append(draft.Lines, sale)
		}
		if service > 0 {
			serviceLine := line
			serviceLine.Component = practicePeriodComponentKey(component{Type: "retrain_service"}, index)
			serviceLine.PersonID, serviceLine.Amount = split.ServicePersonID, service
			serviceLine.practiceArrangementLabel = split.ServiceLabel
			draft.Lines = append(draft.Lines, serviceLine)
		}
		if pool > 0 {
			line.Component = practicePeriodComponentKey(component{Type: "retrain_pool"}, index)
			line.PracticePool, line.BeneficiaryUnit, line.Amount = &split.Pool, &unit, pool
			draft.Lines = append(draft.Lines, line)
		}
	}
	var err error
	draft.Total, err = addMinor(draft.Total, totalAccrued)
	return err
}
