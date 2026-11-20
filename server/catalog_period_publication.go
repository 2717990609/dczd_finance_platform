package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

var errCatalogPeriodTiers = errors.New("档位须从零开始、连续衔接，最后一档无上限")
var errCatalogPeriodStart = errors.New("请选择原规则有效范围内、晚于原版本的完整账期起点")

const catalogPeriodRuleScope = `scope.source_system='DCAI' AND p.project_code=scope.project_code
 AND p.policy_kind='COMPONENT_RULE' AND p.scope_type='CROSS_SCENARIO_AGGREGATE'`

type catalogPeriodVersion struct {
	ProductID   int64                 `json:"productId"`
	ProductName string                `json:"productName"`
	Label       string                `json:"label"`
	Version     int                   `json:"version"`
	From        time.Time             `json:"effectiveFrom"`
	Through     *time.Time            `json:"effectiveTo"`
	MayPublish  bool                  `json:"mayPublish"`
	Paused      bool                  `json:"paused"`
	Cycle       string                `json:"cycle"`
	Scenarios   []catalogPeriodSKU    `json:"scenarios"`
	Items       []catalogPeriodItem   `json:"items"`
	Options     *catalogPeriodOptions `json:"options,omitempty"`
	packageID   string
	retiredFrom *time.Time
	raw         json.RawMessage
}

type catalogPeriodSKU struct {
	Code   string `json:"code"`
	Label  string `json:"label"`
	Mapped bool   `json:"mapped"`
	Locked bool   `json:"locked,omitempty"`
}

type catalogPeriodPublishInput struct {
	BaseID        string                       `json:"baseId"`
	EffectiveFrom time.Time                    `json:"effectiveFrom"`
	Edits         []catalogPeriodTierEdit      `json:"edits"`
	Components    []catalogPeriodComponentEdit `json:"components"`
	ScenarioCodes []string                     `json:"scenarioCodes,omitempty"`
}

type catalogPeriodQuoteInput struct {
	BaseID     string                       `json:"baseId"`
	Edits      []catalogPeriodTierEdit      `json:"edits"`
	Components []catalogPeriodComponentEdit `json:"components"`
	Receipts   int64                        `json:"receiptsMinor"`
	Refunds    int64                        `json:"refundsMinor"`
	CarryIn    int64                        `json:"carryInMinor"`
}

type catalogPeriodReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type catalogPeriodOptions struct {
	Organizations []catalogStructureOption `json:"organizations"`
	Roles         []catalogStructureOption `json:"roles"`
	Scenarios     []catalogPeriodSKU       `json:"scenarios"`
}

func readCatalogPeriodVersion(ctx context.Context, db catalogPeriodReader, id string) (catalogPeriodVersion, error) {
	var rule catalogPeriodVersion
	if _, ok := canonicalSourceUUID(id); !ok {
		return rule, invalid
	}
	err := db.QueryRow(ctx, `SELECT p.product_id,pro.name,p.name,v.version_no,v.effective_from,`+catalogDisplayedThrough+`,
	 v.package_id::text,v.definition,(v.source_status='PUBLISHED' AND `+catalogVersionVisible+`),p.paused,`+catalogRuleRetiredAtSQL+`
	 FROM product_rule_versions v JOIN product_rule_packages p ON p.source_id=v.package_id
	 JOIN product_catalog_settings scope ON scope.product_id=p.product_id
	 JOIN products pro ON pro.id=p.product_id
	 WHERE v.source_id=$1::uuid AND `+catalogPeriodRuleScope, id).
		Scan(&rule.ProductID, &rule.ProductName, &rule.Label, &rule.Version, &rule.From, &rule.Through, &rule.packageID,
			&rule.raw, &rule.MayPublish, &rule.Paused, &rule.retiredFrom)
	return rule, err
}

func (s workspace) getCatalogPeriodRule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, err := s.financeRateActor(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	ctx := r.Context()
	rule, err := readCatalogPeriodVersion(ctx, s.db, r.URL.Query().Get("ruleId"))
	if err != nil || !actor.permits(rule.ProductID, false) {
		if err == nil {
			err = errRulePermission
		}
		respond(w, nil, err)
		return
	}
	period, err := parseCatalogPeriodRule(rule.raw)
	if err != nil {
		respond(w, nil, err)
		return
	}
	rule.MayPublish = rule.MayPublish && actor.permits(rule.ProductID, true)
	rule.Cycle, rule.Items = period.Cycle, period.Items
	skus, err := readCatalogPeriodScenarioOptions(ctx, s.db, rule.ProductID, nil, nil)
	if err != nil {
		respond(w, nil, err)
		return
	}
	skuLabels := make(map[string]string, len(skus))
	for _, sku := range skus {
		skuLabels[sku.Code] = sku.Label
	}
	err = nameCatalogPeriodTargets(ctx, s.db, rule.Items)
	for _, code := range period.Scenarios {
		label := skuLabels[code]
		if label == "" {
			label = code
		}
		rule.Scenarios = append(rule.Scenarios, catalogPeriodSKU{Code: code, Label: label, Mapped: skuLabels[code] != ""})
	}
	if err == nil && rule.MayPublish {
		var options catalogStructureOptions
		options, err = readCatalogPeriodStructureOptions(ctx, s.db)
		organizations := slices.DeleteFunc(options.Organizations, func(org catalogStructureOption) bool {
			_, direct := catalogPeriodUnit(org.Value)
			return !direct
		})
		rule.Options = &catalogPeriodOptions{Organizations: organizations, Roles: options.Roles}
		if err == nil {
			first := firstCatalogPeriodChange(rule.From, rule.Cycle)
			var eligible []catalogPeriodScenarioOption
			eligible, err = readCatalogPeriodScenarioOptions(ctx, s.db, rule.ProductID, &first, rule.Through)
			for _, sku := range eligible {
				if sku.canChange(actor) {
					rule.Options.Scenarios = append(rule.Options.Scenarios, sku.catalogPeriodSKU)
					continue
				}
				for i := range rule.Scenarios {
					if rule.Scenarios[i].Code == sku.Code {
						rule.Scenarios[i].Locked = true
					}
				}
			}
		}
	}
	respond(w, rule, err)
}

func (s workspace) postCatalogPeriodQuote(w http.ResponseWriter, r *http.Request) {
	actor, err := s.financeRateActor(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	submitJSON(128<<10, func(ctx context.Context, input catalogPeriodQuoteInput) (catalogPeriodQuote, error) {
		var result catalogPeriodQuote
		rule, err := readCatalogPeriodVersion(ctx, s.db, input.BaseID)
		if err != nil {
			return result, err
		}
		if !actor.permits(rule.ProductID, len(input.Edits) != 0 || input.Components != nil) {
			return result, errRulePermission
		}
		regulated, err := applyCatalogPeriodChanges(ctx, s.db, rule.raw, input.Edits, input.Components)
		if err != nil {
			return result, err
		}
		period, err := parseCatalogPeriodRule(regulated)
		if err != nil {
			return result, err
		}
		return quoteCatalogPeriod(period.Items, input.Receipts, input.Refunds, input.CarryIn)
	})(w, r)
}

func (s workspace) postCatalogPeriodRule(w http.ResponseWriter, r *http.Request) {
	actor, err := s.financeRateActor(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	submitJSON(128<<10, func(ctx context.Context, input catalogPeriodPublishInput) (catalogPublication, error) {
		var result catalogPublication
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var err error
			result, err = writeCatalogPeriodRule(ctx, tx, input, actor)
			return err
		})
		return result, err
	})(w, r)
}

func writeCatalogPeriodRule(ctx context.Context, tx pgx.Tx, input catalogPeriodPublishInput, actor financeRuleActor) (catalogPublication, error) {
	var result catalogPublication
	id, ok := canonicalSourceUUID(input.BaseID)
	if !ok {
		return result, invalid
	}
	input.BaseID = id
	base, err := readCatalogPeriodVersion(ctx, tx, input.BaseID)
	if err != nil {
		return result, err
	}
	if !actor.permits(base.ProductID, true) {
		return result, errRulePermission
	}
	if actor.account != nil {
		if err := lockCatalogPublisher(ctx, tx, actor.account, input.BaseID); err != nil {
			return result, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT source_id FROM product_rule_packages WHERE source_id=$1::uuid FOR UPDATE`, base.packageID); err != nil {
		return result, err
	}
	base, err = readCatalogPeriodVersion(ctx, tx, input.BaseID)
	if err != nil {
		return result, err
	}
	period, err := parseCatalogPeriodRule(base.raw)
	if err != nil {
		return result, err
	}
	at := input.EffectiveFrom.In(shanghai)
	if !base.MayPublish || (len(input.Edits) == 0 && input.Components == nil && input.ScenarioCodes == nil) || !validCatalogPeriodStart(at, period.Cycle) || !at.After(base.From) ||
		base.Through != nil && !at.Before(*base.Through) {
		return result, errCatalogPeriodStart
	}
	var active string
	err = tx.QueryRow(ctx, `SELECT v.source_id::text FROM product_rule_versions v
	 JOIN product_rule_packages p ON p.source_id=v.package_id JOIN product_catalog_settings scope ON scope.product_id=p.product_id
	 WHERE v.package_id=$1::uuid AND v.source_status='PUBLISHED' AND `+catalogVersionVisible+`
	  AND v.effective_from<=$2 AND (`+catalogRuleExecutionThrough+` IS NULL OR $2<`+catalogRuleExecutionThrough+`)
	 ORDER BY v.effective_from DESC,v.version_no DESC LIMIT 1`, base.packageID, at).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && active != input.BaseID {
		return result, errCatalogRateStale
	}
	if err != nil {
		return result, err
	}
	if input.Components != nil {
		if _, err := tx.Exec(ctx, `SELECT source FROM catalog_sales_context WHERE source='DCAI' FOR SHARE`); err != nil {
			return result, err
		}
	}
	body, err := applyCatalogPeriodChanges(ctx, tx, base.raw, input.Edits, input.Components)
	if err != nil {
		return result, err
	}
	if input.ScenarioCodes != nil {
		body, err = editCatalogPeriodScenarioScope(ctx, tx, body, base, period.Scenarios, input.ScenarioCodes, at, actor)
		if err != nil {
			return result, err
		}
	}
	if bytes.Equal(body, base.raw) {
		return catalogPublication{ID: input.BaseID, Version: base.Version}, nil
	}
	digest := sha256.Sum256(body)
	err = tx.QueryRow(ctx, `INSERT INTO product_rule_versions
	 (source_id,package_id,version_no,source_status,effective_from,effective_to,content_hash,definition,observed_at,published_at,published_by,predecessor_id)
	 SELECT gen_random_uuid(),$1::uuid,(SELECT max(version_no)+1 FROM product_rule_versions WHERE package_id=$1::uuid),
	  'PUBLISHED',$2,$3,$4,$5,now(),now(),$6,$7::uuid RETURNING source_id::text,version_no`,
		base.packageID, at, base.Through, hex.EncodeToString(digest[:]), body, actor.subject, input.BaseID).
		Scan(&result.ID, &result.Version)
	if err != nil {
		return result, err
	}
	return result, enqueueDraftConfiguration(ctx, tx, `SELECT id FROM periods WHERE aggregate_package_id=$1::uuid
	 AND $2<(ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'`, base.packageID, at)
}

func applyCatalogPeriodChanges(ctx context.Context, db catalogPeriodReader, raw json.RawMessage,
	edits []catalogPeriodTierEdit, components []catalogPeriodComponentEdit) (json.RawMessage, error) {
	if components == nil {
		return editCatalogPeriodTiers(raw, edits)
	}
	if len(edits) != 0 {
		return nil, invalid
	}
	var personIDs []int64
	for _, item := range components {
		if item.PersonID > 0 {
			personIDs = append(personIDs, item.PersonID)
		}
	}
	if len(personIDs) > 0 {
		var missing bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unnest($1::bigint[]) selected(id)
		 WHERE NOT EXISTS(SELECT 1 FROM people WHERE id=selected.id))`, personIDs).Scan(&missing); err != nil {
			return nil, err
		}
		if missing {
			return nil, errCatalogPeriodRecipient
		}
	}
	options, err := readCatalogPeriodStructureOptions(ctx, db)
	if err != nil {
		return nil, err
	}
	return editCatalogPeriodComponents(raw, components, options)
}

func readCatalogPeriodStructureOptions(ctx context.Context, db catalogPeriodReader) (catalogStructureOptions, error) {
	options, err := readCatalogStructureOptions(ctx, db)
	if err != nil {
		return options, err
	}
	var direct []catalogStructureOption
	err = db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object(
	 'value',$1 || u.id::text,'label',concat_ws(' / ',p.name,u.name),'unitId',u.id::text,'kind',u.kind)
	 ORDER BY u.name,u.id),'[]') FROM organization_units u LEFT JOIN organization_units p ON p.id=u.parent_id
	 WHERE u.status='ACTIVE'`, catalogPeriodUnitPrefix).Scan(&direct)
	options.Organizations = append(direct, options.Organizations...)
	return options, err
}

func nameCatalogPeriodTargets(ctx context.Context, db catalogPeriodReader, items []catalogPeriodItem) error {
	var organizations, units []string
	var people []int64
	for _, item := range items {
		if item.PersonID > 0 {
			people = append(people, item.PersonID)
		} else if item.unit != "" {
			units = append(units, item.unit)
		} else {
			organizations = append(organizations, item.OrganizationKey)
		}
	}
	var names map[string]string
	if err := db.QueryRow(ctx, `SELECT coalesce(jsonb_object_agg(key,name),'{}') FROM (
	 SELECT b.party_uuid::text AS key,min(u.name) AS name FROM catalog_benefit_organizations b
	 JOIN catalog_sales_context c ON c.source=b.source AND c.captured_at=b.observed_at
	 JOIN organization_units u ON u.id=b.unit_id
	 WHERE b.source='DCAI' AND b.party_uuid::text=ANY($1) GROUP BY b.party_uuid HAVING count(DISTINCT b.unit_id)=1
	 UNION ALL SELECT id::text,name FROM people WHERE id=ANY($2::bigint[])
	 UNION ALL SELECT $4 || id::text,name || CASE WHEN status='INACTIVE' THEN '（已停用）' ELSE '' END
	 FROM organization_units WHERE id::text=ANY($3)) targets`,
		organizations, people, units, catalogPeriodUnitPrefix).Scan(&names); err != nil {
		return err
	}
	for i := range items {
		key := items[i].OrganizationKey
		if items[i].PersonID > 0 {
			key = strconv.FormatInt(items[i].PersonID, 10)
		} else if items[i].unit != "" {
			key = catalogPeriodUnitPrefix + items[i].unit
		}
		items[i].TargetName = names[key]
	}
	return nil
}

func validCatalogPeriodStart(at time.Time, cycle string) bool {
	want := 1
	if cycle == "MONTHLY_25_TO_24" {
		want = 25
	}
	return at.Day() == want && at.Hour()+at.Minute()+at.Second()+at.Nanosecond() == 0
}
