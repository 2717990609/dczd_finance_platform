package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type catalogScenarioCreate struct {
	ProductID int64 `json:"productId"`
	catalogScenarioCopy
	SalesRates []catalogConditionInput `json:"salesRates"`
}

type catalogScenarioSetup struct {
	SKUs    []catalogPeriodSKU      `json:"skus"`
	Options catalogStructureOptions `json:"options"`
}

const catalogScenarioCreationProducts = `FROM products pro JOIN product_catalog_settings c ON c.product_id=pro.id
 WHERE pro.id=$1 AND c.source_system='DCAI' AND pro.period_mode='calendar' AND pro.cycle_start_day IN (1,25)`

const catalogScenarioCreationRoutes = `FROM catalog_business_routes r
 JOIN product_catalog_settings c ON c.product_id=r.product_id AND c.captured_at=r.observed_at
 WHERE r.product_id=$1 AND r.source=c.source_system AND r.domain='VIP'`

func (s workspace) catalogScenarioProduct(ctx context.Context, raw string, actor financeRuleActor) (int64, error) {
	product, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || product <= 0 {
		return 0, invalid
	}
	if !actor.permits(product, true) {
		return 0, errRulePermission
	}
	err = s.db.QueryRow(ctx, `SELECT pro.id `+catalogScenarioCreationProducts, product).Scan(&product)
	return product, err
}

func (s workspace) catalogScenarioOptions(ctx context.Context, raw string, actor financeRuleActor) (catalogScenarioSetup, error) {
	var result catalogScenarioSetup
	product, err := s.catalogScenarioProduct(ctx, raw, actor)
	if err != nil {
		return result, err
	}
	err = s.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('code',sku_code,'label',label) ORDER BY label,sku_code),'[]')
	 FROM (SELECT r.sku_code,min(r.sku_label) AS label `+catalogScenarioCreationRoutes+` GROUP BY r.sku_code) choices`, product).Scan(&result.SKUs)
	if err != nil {
		return result, err
	}
	result.Options, err = readCatalogStructureOptions(ctx, s.db)
	return result, err
}

func createCatalogScenarioRule(ctx context.Context, tx pgx.Tx, input catalogScenarioCreate, at time.Time,
	items []catalogNewBenefitItem, actor string, refreshDrafts bool) (catalogPublication, error) {
	var result catalogPublication
	input.Label = strings.TrimSpace(input.Label)
	at = at.UTC().Truncate(time.Microsecond)
	if input.ProductID <= 0 || !textOK(input.Label, 80) || !textOK(input.SKU, 128) || at.IsZero() || len(input.SalesRates) == 0 || !textOK(actor, 128) {
		return result, invalid
	}
	if operator := financeUser(ctx); operator != nil {
		var allowed int
		err := tx.QueryRow(ctx, `SELECT 1 FROM finance_accounts a WHERE a.id=$1::uuid AND a.enabled AND EXISTS(
		 SELECT 1 FROM finance_account_rule_grants g WHERE g.account_id=a.id AND g.product_id=$2 AND g.can_publish) FOR SHARE OF a`,
			operator.ID, input.ProductID).Scan(&allowed)
		if errors.Is(err, pgx.ErrNoRows) {
			return result, errRulePermission
		}
		if err != nil {
			return result, err
		}
		if err := lockFinanceOperatorSession(ctx, tx, operator); err != nil {
			return result, err
		}
	}
	var cycleStart int
	if err := tx.QueryRow(ctx, `SELECT pro.cycle_start_day `+catalogScenarioCreationProducts+` FOR UPDATE OF pro`, input.ProductID).Scan(&cycleStart); err != nil {
		return result, err
	}
	var mapped, occupied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 `+catalogScenarioCreationRoutes+`
	 AND r.sku_code=$2 AND r.effective_from<=$3 AND (r.effective_to IS NULL OR r.effective_to>$3)),`+catalogScenarioOccupiedSQL,
		input.ProductID, input.SKU, at).Scan(&mapped, &occupied); err != nil {
		return result, err
	}
	if !mapped {
		return result, errCatalogScenarioTarget
	}
	if occupied {
		return result, errCatalogScenarioOccupied
	}
	options, err := readCatalogStructureOptions(ctx, tx)
	if err != nil {
		return result, err
	}
	rates := make([]json.RawMessage, 0, len(input.SalesRates))
	for _, row := range input.SalesRates {
		value, err := options.condition(row)
		if err != nil {
			return result, err
		}
		rates = append(rates, value)
	}
	if err := validateCatalogNewRateConditions(nil, rates); err != nil {
		return result, err
	}
	cycle := "MONTHLY"
	if cycleStart == 25 {
		cycle = "MONTHLY_25_TO_24"
	}
	base, err := json.Marshal(map[string]any{
		"schema_version": "1.1", "recognition_trigger": "PAID", "sales_attribution": "ORDER_SELLER",
		"amount_basis": "ACTUAL_RECEIVED", "stacking_policy": "SUM", "refund_policy": "PROPORTIONAL",
		"rounding_policy":   map[string]any{"mode": "HALF_UP", "scale": 2},
		"settlement_policy": map[string]string{"cycle": cycle},
		"policy_scope":      map[string][]string{"sales_systems": {"DCAI"}, "business_sku_codes": {input.SKU}, "financial_event_types": {"order.paid"}},
		"benefit_actions": []map[string]any{{"item_key": "sales", "label": "成交销售", "role": "SALES", "type": "RATE_TABLE",
			"beneficiary_mode": "PARTY", "rate_table": rates}},
	})
	if err != nil {
		return result, err
	}
	body := base
	if len(items) != 0 {
		body, err = editCatalogStructure(ctx, tx, base, items, nil, nil)
		if err != nil {
			return result, err
		}
		if err := validateCatalogRecipientAppointments(ctx, tx, input.ProductID, at, base, body); err != nil {
			return result, err
		}
	}
	digest := sha256.Sum256(body)
	result, err = insertCatalogScenarioRule(ctx, tx, input.ProductID, input.catalogScenarioCopy, at, actor, hex.EncodeToString(digest[:]), body)
	if err != nil || !refreshDrafts {
		return result, err
	}
	return result, enqueueDraftConfiguration(ctx, tx, catalogRateDraftPeriodsSQL, input.ProductID, input.SKU, at, nil, result.ID)
}
