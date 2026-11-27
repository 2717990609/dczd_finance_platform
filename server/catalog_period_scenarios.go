package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

var errCatalogPeriodScenarios = errors.New("请选择已有订单映射的参与场景。至少保留一个，同一场景只选一次")
var errCatalogPeriodMembershipScope = errors.New("启用会员仅计新报的收款安排须包含会员 SKU")

const catalogPeriodScenarioOptionsSQL = `WITH routes AS (` + catalogAggregateRoutes + `)
 SELECT coalesce(jsonb_agg(jsonb_build_object('code',sku_code,'label',label,'products',products,
  'requiresCompletion',requires_completion,'membershipSku',membership) ORDER BY label,sku_code),'[]')
 FROM (SELECT sku_code,min(sku_label) AS label,array_agg(DISTINCT product_id) AS products,
   bool_and(` + catalogScenarioCompletion + `) AS requires_completion,
   bool_and(` + catalogMembershipSourceRoute + `) AS membership FROM routes r
  WHERE (` + catalogScenarioSupportedRoute + `) AND ($2::timestamptz IS NULL OR effective_to IS NULL OR $2<effective_to)
   AND ($3::timestamptz IS NULL OR effective_from<$3)
  GROUP BY sku_code) scopes`

type catalogPeriodScenarioOption struct {
	catalogPeriodSKU
	Products []int64 `json:"products"`
}

func (sku catalogPeriodScenarioOption) forMixedProduct(productID int64) bool {
	return slices.Contains(sku.Products, productID)
}

func (sku catalogPeriodScenarioOption) canChange(actor financeRuleActor) bool {
	for _, product := range sku.Products {
		if !actor.permits(product, true) {
			return false
		}
	}
	return true
}

func readCatalogPeriodScenarioOptions(ctx context.Context, db catalogPeriodReader, product int64, from, through *time.Time) ([]catalogPeriodScenarioOption, error) {
	var skus []catalogPeriodScenarioOption
	err := db.QueryRow(ctx, catalogPeriodScenarioOptionsSQL, product, from, through).Scan(&skus)
	return skus, err
}

func validateCatalogPeriodMembershipScopes(ctx context.Context, db catalogPeriodReader, product int64, raw json.RawMessage, from, through *time.Time) error {
	period, err := parseCatalogPeriodRule(raw)
	if err != nil || !slices.ContainsFunc(period.Items, func(item catalogPeriodItem) bool { return item.MembershipNewOnly }) {
		return err
	}
	options, err := readCatalogPeriodScenarioOptions(ctx, db, product, from, through)
	if err != nil {
		return err
	}
	membership := make(map[string]bool, len(options))
	for _, sku := range options {
		membership[sku.Code] = sku.MembershipSKU
	}
	for _, item := range period.Items {
		if !item.MembershipNewOnly {
			continue
		}
		codes := item.ScenarioCodes
		if len(codes) == 0 {
			codes = period.Scenarios
		}
		if !slices.ContainsFunc(codes, func(code string) bool { return membership[code] }) {
			return errCatalogPeriodMembershipScope
		}
	}
	return nil
}

func editCatalogPeriodScenarioScope(ctx context.Context, db catalogPeriodReader, raw json.RawMessage,
	base catalogPeriodVersion, original, requested []string, from time.Time, actor financeRuleActor) (json.RawMessage, error) {
	if len(requested) == 0 {
		return nil, errCatalogPeriodScenarios
	}
	for i, code := range requested {
		if !textOK(code, 256) || slices.Contains(requested[:i], code) {
			return nil, errCatalogPeriodScenarios
		}
	}
	if slices.Equal(original, requested) {
		return raw, nil
	}
	options, err := readCatalogPeriodScenarioOptions(ctx, db, base.ProductID, &from, base.Through)
	if err != nil {
		return nil, err
	}
	available := make(map[string]catalogPeriodScenarioOption, len(options))
	for _, sku := range options {
		available[sku.Code] = sku
	}
	// Versions may reference a scenario whose route has not yet been loaded
	// locally. Existing references remain usable when editing other scenarios.
	for _, code := range requested {
		if slices.Contains(original, code) {
			continue
		}
		sku, found := available[code]
		if !found {
			return nil, errCatalogPeriodScenarios
		}
		if !sku.canChange(actor) {
			return nil, errRulePermission
		}
	}
	for _, code := range original {
		if sku, found := available[code]; found && !slices.Contains(requested, code) && !sku.canChange(actor) {
			return nil, errRulePermission
		}
	}
	var root, scope map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || json.Unmarshal(root["policy_scope"], &scope) != nil || scope == nil {
		return nil, errCatalogRateUnavailable
	}
	scope["business_sku_codes"], _ = json.Marshal(requested)
	root["policy_scope"], _ = json.Marshal(scope)
	return json.Marshal(root)
}

func firstCatalogPeriodChange(from time.Time, cycle string) time.Time {
	from = from.In(shanghai)
	day := 1
	if cycle == "MONTHLY_25_TO_24" {
		day = 25
	}
	at := time.Date(from.Year(), from.Month(), day, 0, 0, 0, 0, shanghai)
	if !at.After(from) {
		return at.AddDate(0, 1, 0)
	}
	return at
}
