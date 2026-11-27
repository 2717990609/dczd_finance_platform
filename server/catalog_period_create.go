package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var errCatalogPeriodName = errors.New("该产品已有同名整期规则，请编辑已有规则或填写其他名称")
var errCatalogMixedRuleOverlap = errors.New("该产品已有同期规则，请在统一规则生效前结束旧规则，并继续编辑已有统一规则")
var errCatalogPeriodFirstStart = errors.New("请选择所选结算周期的账期起点")

type catalogPeriodCreate struct {
	ProductID int64  `json:"productId"`
	Label     string `json:"label"`
	Cycle     string `json:"cycle"`
	Mixed     bool   `json:"mixedArrangements,omitempty"`
}

func (s workspace) catalogPeriodSetup(ctx context.Context, raw string, actor financeRuleActor, mixed bool) (catalogPeriodVersion, error) {
	rule := catalogPeriodVersion{MayPublish: true, Cycle: "MONTHLY", Items: []catalogPeriodItem{}, Scenarios: []catalogPeriodSKU{}}
	product, err := s.catalogScenarioProduct(ctx, raw, actor)
	if err != nil {
		return rule, err
	}
	var day int
	err = s.db.QueryRow(ctx, `SELECT id,name,cycle_start_day FROM products WHERE id=$1`, product).
		Scan(&rule.ProductID, &rule.ProductName, &day)
	if err != nil {
		return rule, err
	}
	rule.Mixed = mixed
	if day == 25 {
		rule.Cycle = "MONTHLY_25_TO_24"
	}
	rule.Options, err = readCatalogPeriodEditorOptions(ctx, s.db, product)
	if err != nil {
		return rule, err
	}
	skus, err := readCatalogPeriodScenarioOptions(ctx, s.db, product, nil, nil)
	for _, sku := range skus {
		if (!mixed || sku.forMixedProduct(product)) && sku.canChange(actor) {
			rule.Options.Scenarios = append(rule.Options.Scenarios, sku.catalogPeriodSKU)
		}
	}
	return rule, err
}

func validateCatalogRuleOverlap(ctx context.Context, db catalogPeriodReader, productID int64, at time.Time, mixed bool) error {
	var overlaps bool
	err := db.QueryRow(ctx, `SELECT EXISTS(
	 SELECT 1 FROM product_rule_packages p JOIN product_catalog_settings scope ON scope.product_id=p.product_id
	 JOIN product_rule_versions v ON v.package_id=p.source_id AND `+catalogVersionVisible+`
	 WHERE p.product_id=$1 AND p.project_code=scope.project_code AND p.policy_kind='COMPONENT_RULE'
	  AND p.scope_type IN ('BUSINESS_SCENARIO','CROSS_SCENARIO_AGGREGATE') AND v.source_status='PUBLISHED'
	  AND ($3 AND v.definition @> '{"mixed_arrangements":true}' OR
	   `+catalogRuleExecutionThrough+` IS NULL OR `+catalogRuleExecutionThrough+`>$2)
	  AND ($3 OR v.definition @> '{"mixed_arrangements":true}'))`, productID, at, mixed).Scan(&overlaps)
	if err == nil && overlaps {
		return errCatalogMixedRuleOverlap
	}
	return err
}

func validateMixedCatalogSKUs(ctx context.Context, db catalogPeriodReader, productID int64, scenarios []string, at time.Time) error {
	options, err := readCatalogPeriodScenarioOptions(ctx, db, productID, &at, nil)
	if err != nil {
		return err
	}
	for _, sku := range scenarios {
		if !slices.ContainsFunc(options, func(option catalogPeriodScenarioOption) bool {
			return option.Code == sku && option.forMixedProduct(productID)
		}) {
			return errCatalogScenarioTarget
		}
	}
	return nil
}

func buildCatalogPeriodRule(ctx context.Context, db catalogPeriodReader, create catalogPeriodCreate, at time.Time,
	scenarios []string, components []catalogPeriodComponentEdit, performance *catalogPerformanceScope, actor financeRuleActor) (json.RawMessage, error) {
	if !actor.permits(create.ProductID, true) {
		return nil, errRulePermission
	}
	if !textOK(strings.TrimSpace(create.Label), 80) {
		return nil, invalid
	}
	if (create.Cycle != "MONTHLY" && create.Cycle != "MONTHLY_25_TO_24") || at.IsZero() || !validCatalogPeriodStart(at.In(shanghai), create.Cycle) {
		return nil, errCatalogPeriodFirstStart
	}
	if create.Mixed {
		var supported bool
		if err := db.QueryRow(ctx, `SELECT period_mode='calendar' AND cycle_start_day=CASE WHEN $2='MONTHLY_25_TO_24' THEN 25 ELSE 1 END
		 FROM products WHERE id=$1`, create.ProductID, create.Cycle).Scan(&supported); err != nil {
			return nil, err
		}
		if !supported {
			return nil, errCatalogPeriodFirstStart
		}
		if err := validateMixedCatalogSKUs(ctx, db, create.ProductID, scenarios, at); err != nil {
			return nil, err
		}
	}
	definition := map[string]any{
		"schema_version": "1.1", "recognition_trigger": catalogPeriodProductRecognition, "amount_basis": "ACTUAL_RECEIVED",
		"stacking_policy": "SUM", "refund_policy": "PROPORTIONAL",
		"rounding_policy":   map[string]any{"mode": postingModeTruncate, "scale": 2},
		"settlement_policy": map[string]string{"cycle": create.Cycle, "negative_balance_policy": "CARRY_FORWARD"},
		"policy_scope":      map[string][]string{"sales_systems": {"DCAI"}, "financial_event_types": {"order.paid"}},
		"benefit_actions":   []any{},
	}
	if create.Mixed {
		definition["mixed_arrangements"] = true
	}
	initial, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	initial, err = editCatalogPeriodScenarioScope(ctx, db, initial, catalogPeriodVersion{ProductID: create.ProductID}, nil, scenarios, at, actor)
	if err != nil {
		return nil, err
	}
	options, err := readCatalogPeriodComponentOptions(ctx, db, create.ProductID, components)
	if err != nil {
		return nil, err
	}
	body, err := writeCatalogPeriodComponents(initial, nil, components, options)
	if err != nil {
		return nil, err
	}
	if err := validateCatalogPeriodMembershipScopes(ctx, db, create.ProductID, body, &at, nil); err != nil {
		return nil, err
	}
	return editCatalogPerformanceScope(ctx, db, body, nil, performance)
}

func createCatalogPeriodRule(ctx context.Context, tx pgx.Tx, input catalogPeriodPublishInput, actor financeRuleActor) (catalogPublication, error) {
	var result catalogPublication
	create := *input.Create
	if !actor.permits(create.ProductID, true) {
		return result, errRulePermission
	}
	if actor.account != nil {
		if err := lockCatalogProductPublisher(ctx, tx, actor.account, create.ProductID); err != nil {
			return result, err
		}
	}
	var product int64
	if err := tx.QueryRow(ctx, `SELECT pro.id `+catalogScenarioCreationProducts+` FOR UPDATE OF pro`, create.ProductID).Scan(&product); err != nil {
		return result, err
	}
	create.Label = strings.TrimSpace(create.Label)
	at := input.EffectiveFrom.UTC().Truncate(time.Microsecond)
	if err := validateCatalogRuleOverlap(ctx, tx, product, at, create.Mixed); err != nil {
		return result, err
	}
	var occupied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_rule_packages p
	 JOIN product_catalog_settings scope ON scope.product_id=p.product_id
	 JOIN product_rule_versions v ON v.package_id=p.source_id AND `+catalogVersionVisible+`
	 WHERE p.product_id=$1 AND `+catalogPeriodRuleScope+` AND lower(p.name)=lower($2)
	 AND v.source_status='PUBLISHED' AND (`+catalogRuleExecutionThrough+` IS NULL OR `+catalogRuleExecutionThrough+`>$3))`,
		product, create.Label, at).Scan(&occupied); err != nil {
		return result, err
	}
	if occupied {
		return result, errCatalogPeriodName
	}
	if _, err := tx.Exec(ctx, `SELECT source FROM catalog_sales_context WHERE source='DCAI' FOR SHARE`); err != nil {
		return result, err
	}
	body, err := buildCatalogPeriodRule(ctx, tx, create, at, input.ScenarioCodes, input.Components, input.Performance, actor)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(body)
	err = tx.QueryRow(ctx, `WITH package AS (
	 INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
	 SELECT id,s.product_id,'local-'||id::text,$2,s.project_code,'COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE',id::text,false
	 FROM product_catalog_settings s CROSS JOIN (SELECT gen_random_uuid() AS id) key WHERE s.product_id=$1
	 RETURNING source_id
	)
	INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at,published_at,published_by)
	SELECT gen_random_uuid(),source_id,1,'PUBLISHED',$3,$4,$5,now(),now(),$6 FROM package RETURNING source_id::text,version_no`,
		product, create.Label, at, hex.EncodeToString(digest[:]), body, actor.subject).Scan(&result.ID, &result.Version)
	return result, err
}
