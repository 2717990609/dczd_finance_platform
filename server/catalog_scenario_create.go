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
	SalesRates         []catalogConditionInput `json:"salesRates"`
	SalesLabel         string                  `json:"salesLabel,omitempty"`
	SalesScope         []string                `json:"salesScopeUnitIds,omitempty"`
	SalesCategoryScope []string                `json:"salesScopeCategoryIds,omitempty"`
	MembershipNewOnly  bool                    `json:"membershipNewOnly,omitempty"`
}

type catalogScenarioSetup struct {
	SKUs    []catalogScenarioSKU    `json:"skus"`
	Options catalogStructureOptions `json:"options"`
}

type catalogScenarioSKU struct {
	Code               string `json:"code"`
	Label              string `json:"label"`
	RequiresCompletion bool   `json:"requiresCompletion"`
	MembershipSKU      bool   `json:"membershipSku,omitempty"`
}

const catalogScenarioCreationProducts = `FROM products pro JOIN product_catalog_settings c ON c.product_id=pro.id
 WHERE pro.id=$1 AND c.source_system='DCAI' AND pro.period_mode='calendar' AND pro.cycle_start_day IN (1,25)`

const catalogScenarioSupportedRoute = `r.source='DCAI' AND (r.domain='VIP' OR (
 r.domain='HEALTH_IDENTITY' AND r.record_table='ai_health_ambassador_order'
 AND r.source_product_code IN ('HEALTH_CREATOR','HEALTH_AMBASSADOR')))`

const catalogScenarioCompletion = `(r.domain='HEALTH_IDENTITY' AND r.source_product_code='HEALTH_CREATOR')`

const catalogMembershipSourceRoute = `(r.domain='VIP' AND r.record_table='ai_vip_order')`

func catalogMembershipScopeAvailable(ctx context.Context, db catalogPeriodReader, product int64, sku string, at time.Time) (bool, error) {
	var supported bool
	err := db.QueryRow(ctx, `SELECT coalesce(bool_and(`+catalogMembershipSourceRoute+`),false)`+catalogScenarioCreationRoutes+`
	 AND r.sku_code=$2 AND r.effective_from<=$3 AND (r.effective_to IS NULL OR r.effective_to>$3)`, product, sku, at).Scan(&supported)
	return supported, err
}

func catalogNewMembershipOnly(raw json.RawMessage) bool {
	var rule struct {
		Scope struct {
			NewOnly bool `json:"vip_new_membership_only"`
		} `json:"policy_scope"`
	}
	return json.Unmarshal(raw, &rule) == nil && rule.Scope.NewOnly
}

func editCatalogNewMembershipScope(raw json.RawMessage, enabled bool) (json.RawMessage, error) {
	var rule map[string]json.RawMessage
	var scope map[string]json.RawMessage
	if json.Unmarshal(raw, &rule) != nil || json.Unmarshal(rule["policy_scope"], &scope) != nil || scope == nil {
		return nil, errCatalogRateUnavailable
	}
	if enabled {
		scope["vip_new_membership_only"] = json.RawMessage("true")
	} else {
		delete(scope, "vip_new_membership_only")
	}
	rule["policy_scope"], _ = json.Marshal(scope)
	return json.Marshal(rule)
}

const catalogScenarioCreationRoutes = `FROM catalog_business_routes r
 JOIN product_catalog_settings c ON c.product_id=r.product_id AND c.captured_at=r.observed_at
 WHERE r.product_id=$1 AND r.source=c.source_system AND ` + catalogScenarioSupportedRoute

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
	err = s.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('code',sku_code,'label',label,
	   'requiresCompletion',requires_completion,'membershipSku',membership) ORDER BY label,sku_code),'[]')
	 FROM (SELECT r.sku_code,min(r.sku_label) AS label,bool_or(`+catalogScenarioCompletion+`) AS requires_completion,
	  bool_and(`+catalogMembershipSourceRoute+`) AS membership
	   `+catalogScenarioCreationRoutes+` GROUP BY r.sku_code HAVING count(DISTINCT `+catalogScenarioCompletion+`)=1) choices`,
		product).Scan(&result.SKUs)
	if err != nil {
		return result, err
	}
	result.Options, err = readCatalogStructureOptions(ctx, s.db, product)
	return result, err
}

func createCatalogScenarioRule(ctx context.Context, tx pgx.Tx, input catalogScenarioCreate, at time.Time,
	items []catalogNewBenefitItem, actor string, refreshDrafts bool) (catalogPublication, error) {
	var result catalogPublication
	input.Label = strings.TrimSpace(input.Label)
	at = at.UTC().Truncate(time.Microsecond)
	if input.ProductID <= 0 || !textOK(input.Label, 80) || !textOK(input.SKU, 128) || at.IsZero() || !textOK(actor, 128) {
		return result, invalid
	}
	if len(input.SalesRates)+len(items) == 0 || (input.SalesLabel != "" || len(input.SalesScope)+len(input.SalesCategoryScope) > 0) && len(input.SalesRates) == 0 {
		return result, errCatalogRequiredEntry
	}
	if operator := financeUser(ctx); operator != nil {
		if err := lockCatalogProductPublisher(ctx, tx, operator, input.ProductID); err != nil {
			return result, err
		}
	}
	var cycleStart int
	if err := tx.QueryRow(ctx, `SELECT pro.cycle_start_day `+catalogScenarioCreationProducts+` FOR UPDATE OF pro`, input.ProductID).Scan(&cycleStart); err != nil {
		return result, err
	}
	var mapped, compound, occupied, membership bool
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT `+catalogScenarioCompletion+`)=1,
	 coalesce(bool_or(`+catalogScenarioCompletion+`),false),`+catalogScenarioOccupiedSQL+`,
	 coalesce(bool_and(`+catalogMembershipSourceRoute+`),false) `+catalogScenarioCreationRoutes+`
	 AND r.sku_code=$2 AND r.effective_from<=$3 AND (r.effective_to IS NULL OR r.effective_to>$3)`,
		input.ProductID, input.SKU, at).Scan(&mapped, &compound, &occupied, &membership); err != nil {
		return result, err
	}
	if !mapped || input.MembershipNewOnly && !membership {
		return result, errCatalogScenarioTarget
	}
	if occupied {
		return result, errCatalogScenarioOccupied
	}
	cycle := "MONTHLY"
	if cycleStart == 25 {
		cycle = "MONTHLY_25_TO_24"
	}
	events := []string{"order.paid"}
	scope := map[string]any{"sales_systems": []string{"DCAI"}, "business_sku_codes": []string{input.SKU}}
	if input.MembershipNewOnly {
		scope["vip_new_membership_only"] = true
	}
	definition := map[string]any{
		"schema_version": "1.1", "recognition_trigger": "PAID", "sales_attribution": "ORDER_SELLER",
		"amount_basis": "ACTUAL_RECEIVED", "stacking_policy": "SUM", "refund_policy": "PROPORTIONAL",
		"rounding_policy":   map[string]any{"mode": "HALF_UP", "scale": 2},
		"settlement_policy": map[string]string{"cycle": cycle},
		"policy_scope":      scope,
	}
	if compound {
		definition["schema_version"] = "1.2"
		definition["recognition_plan"] = map[string]any{"mode": "ALL_OF", "contract_version": "finance.compound-recognition/1.0",
			"required_event_types": []string{"ORDER_PAID", "ORDER_COMPLETED"}}
		events = append(events, "order.completed")
	}
	scope["financial_event_types"] = events
	base, err := json.Marshal(definition)
	if err != nil {
		return result, err
	}
	body, err := editCatalogStructure(ctx, tx, input.ProductID, base, publishCatalogRatesInput{
		Create: &input, NewItems: items, NewSalesRates: input.SalesRates, NewSalesLabel: input.SalesLabel, NewSalesScope: input.SalesScope,
		NewSalesCategories: input.SalesCategoryScope,
	})
	if err != nil {
		return result, err
	}
	if err := validateCatalogRecipientAppointments(ctx, tx, input.ProductID, at, nil, body); err != nil {
		return result, err
	}
	digest := sha256.Sum256(body)
	result, err = insertCatalogScenarioRule(ctx, tx, input.ProductID, input.catalogScenarioCopy, at, actor, hex.EncodeToString(digest[:]), body)
	if err != nil || !refreshDrafts {
		return result, err
	}
	return result, enqueueCatalogScenarioDrafts(ctx, tx, input.ProductID, input.SKU, at, nil, result.ID, compound)
}

func lockCatalogProductPublisher(ctx context.Context, tx pgx.Tx, operator *financePrincipal, product int64) error {
	var allowed int
	err := tx.QueryRow(ctx, `SELECT 1 FROM finance_accounts a WHERE a.id=$1::uuid AND a.enabled AND EXISTS(
	 SELECT 1 FROM finance_account_rule_grants g WHERE g.account_id=a.id AND g.product_id=$2 AND g.can_publish) FOR SHARE OF a`,
		operator.ID, product).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return errRulePermission
	}
	if err != nil {
		return err
	}
	return lockFinanceOperatorSession(ctx, tx, operator)
}
