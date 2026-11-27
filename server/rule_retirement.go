package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var errRuleAlreadyRetired = errors.New("此规则已登记停用时间，请重新读取")

// The anchor is the latest version in that scope when retirement was
// recorded. Later publications can reopen the scope without editing history.
const componentRuleRetiredAtSQL = `(SELECT min(st.effective_from)
 FROM rule_scope_retirements st JOIN rule_versions anchor ON anchor.id=st.rule_id
 WHERE anchor.product_id=rv.product_id AND anchor.version>=rv.version
 AND anchor.period_id IS NOT DISTINCT FROM rv.period_id AND anchor.from_ordinal IS NOT DISTINCT FROM rv.from_ordinal
 AND anchor.activity_type=rv.activity_type)`

// For a family-education class, each later applicable publication ends its
// predecessor for that class alone. Other classes can still inherit it.
// A subsequent explicit stop of the replacement does not revive the old one.
const componentRulePeriodEndSQL = `least(` + componentRuleRetiredAtSQL + `, CASE
 WHEN p.source_period_key ~ '^PRACTICE:[12]:4:' THEN (
 SELECT min(coalesce(n.effective_from,'-infinity'::timestamptz)) FROM rule_versions n
 WHERE n.product_id=rv.product_id AND n.version>rv.version
 AND (n.period_id=p.id OR (n.period_id IS NULL AND (n.from_ordinal IS NULL OR n.from_ordinal<=p.ordinal)))
 AND (n.activity_type=0 OR n.activity_type::text=split_part(p.source_period_key,':',2))
 ) END)`

const catalogRuleRetiredAtSQL = `(SELECT st.effective_from FROM catalog_rule_retirements st WHERE st.package_id=p.source_id)`
const catalogRuleExecutionThrough = `least(v.effective_to,` + catalogRuleRetiredAtSQL + `)`

type retireRuleInput struct {
	ProductID        int64
	RuleID           int64
	CatalogVersionID string
	EffectiveFrom    time.Time
}

func (s workspace) retireRule(ctx context.Context, input retireRuleInput) (any, error) {
	if input.ProductID < 1 || (input.RuleID > 0) == (input.CatalogVersionID != "") || input.RuleID < 0 || input.EffectiveFrom.IsZero() {
		return nil, invalid
	}
	if input.CatalogVersionID != "" {
		if _, ok := canonicalSourceUUID(input.CatalogVersionID); !ok {
			return nil, invalid
		}
	}
	actor := financeUser(ctx)
	var account *string
	if actor != nil {
		if !actor.permitsRule(input.ProductID, true) {
			return nil, errFinanceForbidden
		}
		account = &actor.ID
	}
	at := input.EffectiveFrom.UTC().Truncate(time.Microsecond)
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if input.CatalogVersionID != "" && actor != nil {
			if err := lockCatalogPublisher(ctx, tx, actor, input.CatalogVersionID); err != nil {
				return err
			}
		}
		var mode string
		if err := tx.QueryRow(ctx, `SELECT period_mode FROM products WHERE id=$1 FOR UPDATE`, input.ProductID).Scan(&mode); err != nil {
			return err
		}
		if input.CatalogVersionID != "" {
			return retireCatalogPackage(ctx, tx, input.ProductID, input.CatalogVersionID, at, account)
		}
		var periodID *int64
		var ordinal *int
		var activityType int
		err := tx.QueryRow(ctx, `SELECT b.period_id,b.from_ordinal,b.activity_type FROM rule_versions b
		 WHERE b.id=$1 AND b.product_id=$2 AND NOT EXISTS (SELECT 1 FROM rule_versions n
		 WHERE n.product_id=b.product_id AND n.period_id IS NOT DISTINCT FROM b.period_id
		  AND n.from_ordinal IS NOT DISTINCT FROM b.from_ordinal AND n.activity_type=b.activity_type AND n.version>b.version)`, input.RuleID, input.ProductID).
			Scan(&periodID, &ordinal, &activityType)
		if errors.Is(err, pgx.ErrNoRows) {
			return errRuleEditStale
		}
		if err != nil {
			return err
		}
		if periodID != nil {
			var bound bool
			if err := tx.QueryRow(ctx, `SELECT rule_plugin_version_id IS NOT NULL FROM periods WHERE id=$1`, *periodID).Scan(&bound); err != nil {
				return err
			}
			if bound {
				return errPeriodRulePluginLocked
			}
		}
		tag, err := tx.Exec(ctx, `INSERT INTO rule_scope_retirements(rule_id,effective_from,recorded_by)
		 VALUES($1,$2,$3::uuid) ON CONFLICT(rule_id) DO NOTHING`, input.RuleID, at, account)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return verifyRepeatedRetirement(at, tx.QueryRow(ctx,
				`SELECT effective_from FROM rule_scope_retirements WHERE rule_id=$1`, input.RuleID))
		}
		if mode == "class" {
			return enqueueDraftConfiguration(ctx, tx, draftRefreshClassRule, input.ProductID, periodID, ordinal, at, activityType)
		}
		return enqueueDraftConfiguration(ctx, tx, draftRefreshRule, input.ProductID, periodID, ordinal, at)
	})
	return map[string]time.Time{"retiredFrom": at}, err
}

func retireCatalogPackage(ctx context.Context, tx pgx.Tx, product int64, version string, at time.Time, account *string) error {
	var packageID, scope, code string
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT p.source_id::text,p.scope_type,v.definition,p.code
	 FROM product_rule_versions v JOIN product_rule_packages p ON p.source_id=v.package_id
	 JOIN product_catalog_settings scope ON scope.product_id=p.product_id
	 WHERE v.source_id=$1::uuid AND p.product_id=$2 AND scope.source_system='DCAI' AND p.project_code=scope.project_code
	  AND p.policy_kind='COMPONENT_RULE' AND p.scope_type IN ('BUSINESS_SCENARIO','CROSS_SCENARIO_AGGREGATE')
	  AND v.source_status='PUBLISHED' AND `+catalogVersionVisible+` FOR UPDATE OF p`, version, product).Scan(&packageID, &scope, &raw, &code)
	if err != nil {
		return err
	}
	if code == periodRulePluginPackageCode {
		return errPeriodRulePluginLocked
	}
	if scope == "CROSS_SCENARIO_AGGREGATE" {
		period, err := parseCatalogPeriodRule(raw)
		if err != nil {
			return err
		}
		if !validCatalogPeriodStart(at.In(shanghai), period.Cycle) {
			return errCatalogPeriodStart
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO catalog_rule_retirements(package_id,effective_from,recorded_by)
	 VALUES($1::uuid,$2,$3::uuid) ON CONFLICT(package_id) DO NOTHING`, packageID, at, account)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return verifyRepeatedRetirement(at, tx.QueryRow(ctx,
			`SELECT effective_from FROM catalog_rule_retirements WHERE package_id=$1::uuid`, packageID))
	}
	if scope == "CROSS_SCENARIO_AGGREGATE" {
		return enqueueDraftConfiguration(ctx, tx, `SELECT id FROM periods
		 WHERE aggregate_package_id=$1::uuid AND $2<((ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')`, packageID, at)
	}
	return enqueueDraftConfiguration(ctx, tx, `SELECT id FROM periods
	 WHERE product_id=$1 AND aggregate_package_id IS NULL AND $2<((ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')`, product, at)
}

func verifyRepeatedRetirement(at time.Time, row pgx.Row) error {
	var saved time.Time
	if err := row.Scan(&saved); err != nil {
		return err
	}
	if !at.Equal(saved) {
		return errRuleAlreadyRetired
	}
	return nil
}
