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
	ProductID   int64                     `json:"productId"`
	ProductName string                    `json:"productName"`
	Label       string                    `json:"label"`
	Version     int                       `json:"version"`
	From        time.Time                 `json:"effectiveFrom"`
	Through     *time.Time                `json:"effectiveTo"`
	MayPublish  bool                      `json:"mayPublish"`
	Paused      bool                      `json:"paused"`
	ResumeFrom  *time.Time                `json:"resumeFrom,omitempty"`
	Cycle       string                    `json:"cycle"`
	Scenarios   []catalogPeriodSKU        `json:"scenarios"`
	Items       []catalogPeriodItem       `json:"items"`
	Options     *catalogPeriodOptions     `json:"options,omitempty"`
	Performance *catalogPerformanceScope  `json:"performanceScope,omitempty"`
	ScopeLabels *catalogPerformanceLabels `json:"scopeLabels,omitempty"`
	packageID   string
	retiredFrom *time.Time
	raw         json.RawMessage
}

type catalogPeriodSKU struct {
	Code               string `json:"code"`
	Label              string `json:"label"`
	Mapped             bool   `json:"mapped"`
	Locked             bool   `json:"locked,omitempty"`
	RequiresCompletion bool   `json:"requiresCompletion,omitempty"`
}

type catalogPeriodPublishInput struct {
	Create        *catalogPeriodCreate         `json:"create,omitempty"`
	BaseID        string                       `json:"baseId"`
	EffectiveFrom time.Time                    `json:"effectiveFrom"`
	Edits         []catalogPeriodTierEdit      `json:"edits"`
	Components    []catalogPeriodComponentEdit `json:"components"`
	ScenarioCodes []string                     `json:"scenarioCodes,omitempty"`
	Performance   *catalogPerformanceScope     `json:"performanceScope,omitempty"`
}

type catalogPeriodQuoteInput struct {
	Create        *catalogPeriodCreate         `json:"create,omitempty"`
	EffectiveFrom time.Time                    `json:"effectiveFrom"`
	ScenarioCodes []string                     `json:"scenarioCodes,omitempty"`
	Performance   *catalogPerformanceScope     `json:"performanceScope,omitempty"`
	BaseID        string                       `json:"baseId"`
	Edits         []catalogPeriodTierEdit      `json:"edits"`
	Components    []catalogPeriodComponentEdit `json:"components"`
	Receipts      int64                        `json:"receiptsMinor"`
	Refunds       int64                        `json:"refundsMinor"`
	CarryIn       int64                        `json:"carryInMinor"`
	ItemIndex     *int                         `json:"itemIndex,omitempty"`
}

type catalogPeriodReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type catalogPeriodOptions struct {
	Organizations []catalogStructureOption `json:"organizations"`
	Roles         []catalogStructureOption `json:"roles"`
	SalesUnits    []catalogStructureOption `json:"salesUnits"`
	Categories    []catalogStructureOption `json:"categories"`
	Duties        []catalogStructureOption `json:"duties"`
	TeamPools     []teamPool               `json:"teamPools"`
	Scenarios     []catalogPeriodSKU       `json:"scenarios"`
}

func readCatalogPeriodVersion(ctx context.Context, db catalogPeriodReader, id string) (catalogPeriodVersion, error) {
	var rule catalogPeriodVersion
	if _, ok := canonicalSourceUUID(id); !ok {
		return rule, invalid
	}
	err := db.QueryRow(ctx, `SELECT p.product_id,pro.name,p.name,v.version_no,v.effective_from,`+catalogDisplayedThrough+`,
	 v.package_id::text,v.definition,(v.source_status='PUBLISHED' AND `+catalogVersionVisible+`),p.paused,p.resume_from,`+catalogRuleRetiredAtSQL+`
	 FROM product_rule_versions v JOIN product_rule_packages p ON p.source_id=v.package_id
	 JOIN product_catalog_settings scope ON scope.product_id=p.product_id
	 JOIN products pro ON pro.id=p.product_id
	 WHERE v.source_id=$1::uuid AND `+catalogPeriodRuleScope, id).
		Scan(&rule.ProductID, &rule.ProductName, &rule.Label, &rule.Version, &rule.From, &rule.Through, &rule.packageID,
			&rule.raw, &rule.MayPublish, &rule.Paused, &rule.ResumeFrom, &rule.retiredFrom)
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
	if raw := r.URL.Query().Get("productId"); raw != "" {
		setup, err := s.catalogPeriodSetup(ctx, raw, actor)
		respond(w, setup, err)
		return
	}
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
	rule.Performance = period.Performance
	scope := catalogPerformanceScope{}
	if period.Performance != nil {
		scope = *period.Performance
		scope.UnitIDs = slices.Clone(scope.UnitIDs)
	}
	var categoryIDs []string
	for _, item := range period.Items {
		scope.UnitIDs = append(scope.UnitIDs, item.CommissionUnits...)
		categoryIDs = append(categoryIDs, item.ScopeCategories...)
		if item.Appointment != nil {
			scope.UnitIDs = append(scope.UnitIDs, item.Appointment.UnitID)
		}
	}
	rule.ScopeLabels, err = nameCatalogPerformanceScope(ctx, s.db, &scope)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if len(categoryIDs) != 0 {
		if rule.ScopeLabels == nil {
			rule.ScopeLabels = &catalogPerformanceLabels{}
		}
		if err := s.db.QueryRow(ctx, `SELECT coalesce(jsonb_object_agg(key,label),'{}') FROM catalog_sales_category_options
		 WHERE key=ANY($1::text[])`, categoryIDs).Scan(&rule.ScopeLabels.Categories); err != nil {
			respond(w, nil, err)
			return
		}
	}
	skus, err := readCatalogPeriodScenarioOptions(ctx, s.db, rule.ProductID, nil, nil)
	if err != nil {
		respond(w, nil, err)
		return
	}
	byCode := make(map[string]catalogPeriodSKU, len(skus))
	for _, sku := range skus {
		byCode[sku.Code] = sku.catalogPeriodSKU
	}
	err = nameCatalogPeriodTargets(ctx, s.db, rule.Items)
	for _, code := range period.Scenarios {
		sku, mapped := byCode[code]
		sku.Code, sku.Mapped = code, mapped
		sku.RequiresCompletion = sku.RequiresCompletion && period.ProductRecognition
		if !mapped {
			sku.Label = code
		}
		rule.Scenarios = append(rule.Scenarios, sku)
	}
	if err == nil && rule.MayPublish {
		rule.Options, err = readCatalogPeriodEditorOptions(ctx, s.db, rule.ProductID)
		if err == nil {
			first := firstCatalogPeriodChange(rule.From, rule.Cycle)
			var eligible []catalogPeriodScenarioOption
			eligible, err = readCatalogPeriodScenarioOptions(ctx, s.db, rule.ProductID, &first, rule.Through)
			for _, sku := range eligible {
				if sku.canChange(actor) {
					sku.RequiresCompletion = sku.RequiresCompletion && period.ProductRecognition
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
		if input.Create != nil {
			if input.BaseID != "" || len(input.Edits) != 0 {
				return catalogPeriodQuote{}, invalid
			}
			body, err := buildCatalogPeriodRule(ctx, s.db, *input.Create, input.EffectiveFrom, input.ScenarioCodes, input.Components, input.Performance, actor)
			if err != nil {
				return catalogPeriodQuote{}, err
			}
			return quoteCatalogPeriodDefinition(body, input)
		}
		rule, err := readCatalogPeriodVersion(ctx, s.db, input.BaseID)
		if err != nil {
			return catalogPeriodQuote{}, err
		}
		if !actor.permits(rule.ProductID, len(input.Edits) != 0 || input.Components != nil || input.Performance != nil) {
			return catalogPeriodQuote{}, errRulePermission
		}
		regulated, err := applyCatalogPeriodChanges(ctx, s.db, rule.ProductID, rule.raw, input.Edits, input.Components)
		if err != nil {
			return catalogPeriodQuote{}, err
		}
		if input.Performance != nil {
			base, err := parseCatalogPeriodRule(rule.raw)
			if err != nil {
				return catalogPeriodQuote{}, err
			}
			regulated, err = editCatalogPerformanceScope(ctx, s.db, regulated, base.Performance, input.Performance)
			if err != nil {
				return catalogPeriodQuote{}, err
			}
		}
		return quoteCatalogPeriodDefinition(regulated, input)
	})(w, r)
}

func quoteCatalogPeriodDefinition(raw json.RawMessage, input catalogPeriodQuoteInput) (catalogPeriodQuote, error) {
	period, err := parseCatalogPeriodRule(raw)
	if err != nil {
		return catalogPeriodQuote{}, err
	}
	if input.ItemIndex != nil {
		index := *input.ItemIndex
		if index < 0 || index >= len(period.Items) {
			return catalogPeriodQuote{}, invalid
		}
		period.Items = period.Items[index : index+1]
	}
	return quoteCatalogPeriod(period.Items, input.Receipts, input.Refunds, input.CarryIn)
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
	if input.Create != nil {
		if input.BaseID != "" || len(input.Edits) != 0 {
			return result, invalid
		}
		return createCatalogPeriodRule(ctx, tx, input, actor)
	}
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
	at := input.EffectiveFrom.In(shanghai)
	if input.Components != nil {
		if _, err := tx.Exec(ctx, `SELECT source FROM catalog_sales_context WHERE source='DCAI' FOR SHARE`); err != nil {
			return result, err
		}
	}
	body, err := prepareCatalogPeriodRevision(ctx, tx, base, input, actor)
	if err != nil {
		return result, err
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

func prepareCatalogPeriodRevision(ctx context.Context, db catalogPeriodReader, base catalogPeriodVersion,
	input catalogPeriodPublishInput, actor financeRuleActor) (json.RawMessage, error) {
	period, err := parseCatalogPeriodRule(base.raw)
	if err != nil {
		return nil, err
	}
	at := input.EffectiveFrom.In(shanghai)
	if !base.MayPublish || (len(input.Edits) == 0 && input.Components == nil && input.ScenarioCodes == nil && input.Performance == nil) ||
		!validCatalogPeriodStart(at, period.Cycle) || !at.After(base.From) || base.Through != nil && !at.Before(*base.Through) {
		return nil, errCatalogPeriodStart
	}
	active, err := catalogPeriodVersionAt(ctx, db, base.packageID, at)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && active != input.BaseID {
		return nil, errCatalogRateStale
	}
	if err != nil {
		return nil, err
	}
	var closed bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM periods p JOIN settlements final ON final.period_id=p.id
	 WHERE p.aggregate_package_id=$1::uuid AND (p.starts_on,p.ends_on)=($2::date,$3::date))`,
		base.packageID, at.Format(time.DateOnly), at.AddDate(0, 1, -1).Format(time.DateOnly)).Scan(&closed); err != nil {
		return nil, err
	}
	if closed {
		return nil, errSettlementClosed
	}
	body, err := applyCatalogPeriodChanges(ctx, db, base.ProductID, base.raw, input.Edits, input.Components)
	if err != nil {
		return nil, err
	}
	if input.ScenarioCodes != nil {
		body, err = editCatalogPeriodScenarioScope(ctx, db, body, base, period.Scenarios, input.ScenarioCodes, at, actor)
		if err != nil {
			return nil, err
		}
	}
	return editCatalogPerformanceScope(ctx, db, body, period.Performance, input.Performance)
}

func catalogPeriodVersionAt(ctx context.Context, db catalogPeriodReader, packageID string, at time.Time) (string, error) {
	var active string
	err := db.QueryRow(ctx, `SELECT v.source_id::text FROM product_rule_versions v
	 JOIN product_rule_packages p ON p.source_id=v.package_id JOIN product_catalog_settings scope ON scope.product_id=p.product_id
	 WHERE v.package_id=$1::uuid AND v.source_status='PUBLISHED' AND `+catalogVersionVisible+`
	  AND v.effective_from<=$2 AND (`+catalogRuleExecutionThrough+` IS NULL OR $2<`+catalogRuleExecutionThrough+`)
	 ORDER BY v.effective_from DESC,v.version_no DESC LIMIT 1`, packageID, at).Scan(&active)
	return active, err
}

func applyCatalogPeriodChanges(ctx context.Context, db catalogPeriodReader, productID int64, raw json.RawMessage,
	edits []catalogPeriodTierEdit, components []catalogPeriodComponentEdit) (json.RawMessage, error) {
	if components == nil {
		return editCatalogPeriodTiers(raw, edits)
	}
	if len(edits) != 0 {
		return nil, invalid
	}
	options, err := readCatalogPeriodComponentOptions(ctx, db, productID, components)
	if err != nil {
		return nil, err
	}
	return editCatalogPeriodComponents(raw, components, options)
}

func readCatalogPeriodComponentOptions(ctx context.Context, db catalogPeriodReader, productID int64, components []catalogPeriodComponentEdit) (catalogStructureOptions, error) {
	var personIDs []int64
	var appointments []catalogPerformanceDuty
	for _, item := range components {
		if item.PersonID > 0 {
			personIDs = append(personIDs, item.PersonID)
		}
		if item.OwnerPersonID > 0 {
			personIDs = append(personIDs, item.OwnerPersonID)
		}
		if item.Appointment != nil {
			appointments = append(appointments, *item.Appointment)
		}
	}
	if len(appointments) != 0 {
		var missing bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1
		 FROM jsonb_to_recordset($1::jsonb) AS wanted("personId" bigint,"unitId" text,role text)
		 WHERE NOT EXISTS(SELECT 1 FROM catalog_current_sales_appointments a
		  JOIN organization_units u ON u.id::text=a.scope_key AND u.status='ACTIVE'
		  WHERE a.source='DCAI' AND a.scope='ORGANIZATION_UNIT'
		   AND (a.person_id,a.scope_key,a.role)=(wanted."personId",wanted."unitId",wanted.role)))`, appointments).Scan(&missing); err != nil {
			return catalogStructureOptions{}, err
		}
		if missing {
			return catalogStructureOptions{}, errCatalogPerformanceScope
		}
	}
	if len(personIDs) > 0 {
		var missing bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unnest($1::bigint[]) selected(id)
		 WHERE NOT EXISTS(SELECT 1 FROM people WHERE id=selected.id))`, personIDs).Scan(&missing); err != nil {
			return catalogStructureOptions{}, err
		}
		if missing {
			return catalogStructureOptions{}, errCatalogPeriodRecipient
		}
	}
	return readCatalogPeriodStructureOptions(ctx, db, productID)
}

func readCatalogPeriodEditorOptions(ctx context.Context, db catalogPeriodReader, productID int64) (*catalogPeriodOptions, error) {
	options, err := readCatalogPeriodStructureOptions(ctx, db, productID)
	if err != nil {
		return nil, err
	}
	organizations := slices.DeleteFunc(options.Organizations, func(org catalogStructureOption) bool {
		_, direct := catalogPeriodUnit(org.Value)
		return !direct
	})
	return &catalogPeriodOptions{Organizations: organizations, Roles: options.Roles, SalesUnits: options.SalesUnits, Categories: options.Categories,
		Duties: options.Duties, TeamPools: options.TeamPools, Scenarios: []catalogPeriodSKU{}}, nil
}

func readCatalogPeriodStructureOptions(ctx context.Context, db catalogPeriodReader, productID int64) (catalogStructureOptions, error) {
	options, err := readCatalogStructureOptions(ctx, db, productID)
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
		if item.OwnerPersonID > 0 {
			people = append(people, item.OwnerPersonID)
		}
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
		if items[i].OwnerPersonID > 0 {
			items[i].OwnerName = names[strconv.FormatInt(items[i].OwnerPersonID, 10)]
		}
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
