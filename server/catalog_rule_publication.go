package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	errCatalogRateStale          = errors.New("所选生效时间的适用规则已变化，请重新选择版本")
	errCatalogRateUnavailable    = errors.New("此规则未开放费率维护")
	errCatalogRateDates          = errors.New("生效时间须晚于所选版本的起点")
	errCatalogConfigurationOwned = errors.New("此产品已在本平台发布规则，请通过规则配置维护")
	errCatalogTeamRate           = errors.New("此调整会改变内部团队的拆分比例或收款方式，请核对团队规则")
	errCatalogTeamSplitTotal     = errors.New("普通销售成交和团队负责人亲自成交的岗位比例合计，都须等于销售总比例")
	errCatalogTeamSplitService   = errors.New("服务人员比例大于零时，请为每个内部团队选择服务人员")
	errCatalogTeamSplitStandard  = errors.New("请先确认内部团队有独立的公司销售标准，再调整总比例")
	errCatalogRequiredEntry      = errors.New("请至少保留一项收款安排。成交团队拆分及在用的费率表须保留适用费率")
)

type catalogRateChange struct {
	ItemKey string  `json:"itemKey"`
	Row     *int    `json:"row,omitempty"` // One-based within the selected version's rate table.
	Rate    *int64  `json:"rateBps"`
	Fixed   *int64  `json:"fixedMinor,omitempty"`
	Label   *string `json:"label,omitempty"`
}

type publishCatalogRatesInput struct {
	BaseID             string                            `json:"baseId"`
	EffectiveFrom      time.Time                         `json:"effectiveFrom"`
	Changes            []catalogRateChange               `json:"changes"`
	RecipientChanges   []catalogRuleRecipientChange      `json:"recipientChanges"`
	TeamChanges        []catalogTeamRecipientChange      `json:"teamChanges"`
	TeamSplitRates     *catalogSplitRates                `json:"teamSplitRates,omitempty"`
	LeaderChanges      []catalogLeaderChange             `json:"leaderChanges,omitempty"`
	ScopeChanges       []catalogCommissionScopeChange    `json:"scopeChanges,omitempty"`
	NewItems           []catalogNewBenefitItem           `json:"newItems"`
	NewSalesRates      []catalogConditionInput           `json:"newSalesRates,omitempty"`
	NewSalesLabel      string                            `json:"newSalesLabel,omitempty"`
	NewSalesScope      []string                          `json:"newSalesScope,omitempty"`
	NewSalesCategories []string                          `json:"newSalesCategoryScope,omitempty"`
	NewRateRows        []catalogNewRateRow               `json:"newRateRows"`
	TargetChanges      []catalogBenefitTargetChange      `json:"targetChanges"`
	CalculationChanges []catalogBenefitCalculationChange `json:"calculationChanges,omitempty"`
	RemovedItems       []string                          `json:"removedItems"`
	RemovedRateRows    []catalogRateRowRemoval           `json:"removedRateRows"`
	MembershipNewOnly  *bool                             `json:"membershipNewOnly,omitempty"`
	Copy               *catalogScenarioCopy              `json:"copy,omitempty"`
	Create             *catalogScenarioCreate            `json:"create,omitempty"`
}

type catalogPublication struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

const catalogHealthRateRule = `coalesce((` + catalogHealthRecognitionPlan + `) AND EXISTS (
 SELECT 1 FROM catalog_business_routes m WHERE m.product_id=p.product_id AND m.observed_at=scope.captured_at
  AND m.sku_code=p.scope_id AND m.source='DCAI' AND m.domain='HEALTH_IDENTITY'
),false)`

const catalogRateConfigurationSQL = `coalesce(scope.source_system='DCAI' AND p.scope_type='BUSINESS_SCENARIO'
 AND (` + catalogSplitDraftRule + ` OR ` + catalogDirectDraftRule + ` OR ` + catalogHealthRateRule + `),false)`

const catalogRateVersionEligibilitySQL = catalogRuleEligible + `
 AND v.definition->'settlement_policy'->>'cycle'=CASE pro.cycle_start_day WHEN 25 THEN 'MONTHLY_25_TO_24' WHEN 1 THEN 'MONTHLY' END
 AND v.definition->'policy_scope'->'business_sku_codes' ? p.scope_id
 AND v.definition->'policy_scope'->'sales_systems' ? scope.source_system`

const catalogScenarioRateCandidates = `SELECT v.source_id,v.effective_from,` + catalogRuleExecutionThrough + ` AS effective_to,
 v.published_at,p.resume_from,` + catalogRulePriority + ` AS priority
 FROM product_catalog_settings scope JOIN products pro ON pro.id=scope.product_id
 JOIN product_rule_packages p ON p.product_id=scope.product_id AND p.project_code=scope.project_code
 JOIN product_rule_versions v ON v.package_id=p.source_id AND ` + catalogVersionVisible + `
 WHERE scope.product_id=$1 AND p.scope_id=$2 AND ` + catalogRateVersionEligibilitySQL

const catalogActiveRateVersionSQL = `WITH rules AS (` + catalogScenarioRateCandidates + `), winner AS (
 SELECT source_id FROM rules WHERE effective_from<=$3 AND (effective_to IS NULL OR effective_to>$3)
 AND (resume_from IS NULL OR resume_from<=$3)
  ORDER BY ` + catalogRuleOrder + ` FETCH FIRST 1 ROW WITH TIES
)
SELECT count(*),min(source_id::text) FROM winner`

const catalogRateDraftCandidates = `WITH rates AS MATERIALIZED (` + catalogScenarioRateCandidates + `), routes AS MATERIALIZED (
 SELECT r.* FROM catalog_business_routes r JOIN product_catalog_settings c ON c.product_id=r.product_id AND c.captured_at=r.observed_at
 WHERE r.product_id=$1 AND r.source=c.source_system
)`

const catalogRatePaymentWins = `AND NOT EXISTS(
 SELECT 1 FROM source_order_versions newer
 WHERE (newer.source,newer.order_id,newer.line_id,newer.record_key,newer.kind)=(v.source,v.order_id,v.line_id,v.record_key,v.kind)
 AND (newer.updated_at,newer.id)>(v.updated_at,v.id)
)
AND (SELECT count(*)=1 FROM routes m WHERE ` + catalogRouteFactSQL + `
 AND (c.entity_type,c.business_key,c.classifier)=(m.entity_type,m.business_key,m.classifier)
 AND m.effective_from<=v.occurred_at AND (m.effective_to IS NULL OR m.effective_to>v.occurred_at))
AND (SELECT count(*)=1 AND bool_and(best.source_id=$5::uuid) FROM (
 SELECT r.source_id FROM rates r WHERE r.effective_from<=v.occurred_at AND (r.effective_to IS NULL OR r.effective_to>v.occurred_at)
 AND (r.resume_from IS NULL OR r.resume_from<=v.occurred_at)
 ORDER BY ` + catalogRuleOrder + ` FETCH FIRST 1 ROW WITH TIES
) best)`

const catalogRateDraftPeriodsSQL = catalogRateDraftCandidates + `, windows AS (
 SELECT p.id,p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai' AS starts_at,
 (p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai' AS ends_at FROM periods p
 WHERE p.product_id=$1 AND p.aggregate_package_id IS NULL AND $3<((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')
 AND ($4::timestamptz IS NULL OR p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'<$4)
 AND NOT EXISTS(SELECT 1 FROM settlements st WHERE st.period_id=p.id)
)
SELECT w.id FROM windows w
WHERE EXISTS(
 SELECT 1 FROM routes m JOIN source_order_versions v ON v.kind='ORDER_PAID' AND ` + catalogRouteFactSQL + `
 JOIN source_order_classifications c ON c.source_version_id=v.id
  AND (c.entity_type,c.business_key,c.classifier)=(m.entity_type,m.business_key,m.classifier)
 WHERE m.sku_code=$2 AND v.occurred_at>=greatest(w.starts_at,$3,m.effective_from)
  AND v.occurred_at<least(w.ends_at,$4::timestamptz,m.effective_to)
 ` + catalogRatePaymentWins + `
)`

const catalogHealthRateDraftPeriodsSQL = catalogRateDraftCandidates + `, affected AS (
 SELECT v.source,v.order_id,v.line_id,v.record_key,v.occurred_at,v.fact->>'sourceKey' AS source_key,
  v.fact->>'domain' AS domain,v.fact->>'paymentId' AS payment_id
 FROM routes m JOIN source_order_versions v ON v.kind='ORDER_PAID' AND v.line_id='' AND ` + catalogRouteFactSQL + `
 JOIN source_order_classifications c ON c.source_version_id=v.id
  AND (c.entity_type,c.business_key,c.classifier)=(m.entity_type,m.business_key,m.classifier)
 WHERE m.sku_code=$2 AND m.domain='HEALTH_IDENTITY' AND v.occurred_at>=greatest($3,m.effective_from)
  AND coalesce(v.occurred_at<least($4::timestamptz,m.effective_to),true)
 ` + catalogRatePaymentWins + `
)
SELECT target.id FROM affected f
CROSS JOIN LATERAL (
 SELECT f.occurred_at,false AS completion
 UNION ALL
 SELECT completed.occurred_at,true FROM dcai_order_blockers state
 JOIN source_order_versions completed ON completed.id=state.completion_version_id
 WHERE state.stream='HEALTH_IDENTITY_ORDERS' AND state.order_id=f.order_id
  AND state.source_row_id=split_part(f.record_key,':',2)
  AND completed.source='DCAI' AND completed.kind='ORDER_COMPLETED' AND completed.order_id=f.order_id
  AND completed.fact->>'sourceKey'=f.source_key AND completed.occurred_at>=f.occurred_at
  AND NOT EXISTS (` + catalogBookedPaymentSQL + `)
) dates
JOIN periods boundary ON boundary.product_id=$1
 AND (dates.occurred_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN boundary.starts_on AND boundary.ends_on
` + healthCompletionTargetSQL

const catalogDisplayedThrough = `least(` + catalogRuleExecutionThrough + `,(SELECT min(later.effective_from) FROM product_rule_versions later
 WHERE later.package_id=v.package_id AND later.published_at IS NOT NULL AND later.effective_from>v.effective_from))`

func (s workspace) publishCatalogRates(ctx context.Context, input publishCatalogRatesInput, actor string) (catalogPublication, error) {
	var published catalogPublication
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		published, err = writeCatalogRates(ctx, tx, input, actor, true)
		return err
	})
	return published, err
}

func writeCatalogRates(ctx context.Context, tx pgx.Tx, input publishCatalogRatesInput, actor string, refreshDrafts bool) (catalogPublication, error) {
	if input.Create != nil {
		if input.BaseID != "" || input.Copy != nil || input.TeamSplitRates != nil || input.NewSalesLabel != "" || input.MembershipNewOnly != nil ||
			len(input.Changes)+len(input.RecipientChanges)+len(input.TeamChanges)+len(input.LeaderChanges)+len(input.ScopeChanges)+len(input.NewSalesScope)+len(input.NewSalesCategories)+
				len(input.NewSalesRates)+len(input.NewRateRows)+len(input.TargetChanges)+len(input.CalculationChanges)+len(input.RemovedItems)+len(input.RemovedRateRows) != 0 {
			return catalogPublication{}, invalid
		}
		return createCatalogScenarioRule(ctx, tx, *input.Create, input.EffectiveFrom, input.NewItems, actor, refreshDrafts)
	}
	published := catalogPublication{ID: input.BaseID}
	id, valid := canonicalSourceUUID(input.BaseID)
	from := input.EffectiveFrom.UTC().Truncate(time.Microsecond)
	if (input.NewSalesLabel != "" || len(input.NewSalesScope)+len(input.NewSalesCategories) > 0) && len(input.NewSalesRates) == 0 {
		return published, invalid
	}
	structureChanged := len(input.NewItems)+len(input.NewSalesRates)+len(input.NewRateRows)+len(input.TargetChanges)+len(input.CalculationChanges) != 0
	leadersChanged := len(input.LeaderChanges) != 0
	scopesChanged := len(input.ScopeChanges) != 0
	removals := len(input.RemovedItems)+len(input.RemovedRateRows) != 0
	if !valid || from.IsZero() || input.Copy == nil && input.TeamSplitRates == nil && len(input.Changes)+len(input.RecipientChanges)+len(input.TeamChanges) == 0 &&
		!structureChanged && !leadersChanged && !scopesChanged && !removals && input.MembershipNewOnly == nil || !textOK(actor, 128) {
		return published, invalid
	}
	if len(input.CalculationChanges) != 0 && catalogCalculationConflicts(input) {
		return published, invalid
	}
	err := func() error {
		if operator := financeUser(ctx); operator != nil {
			if err := lockCatalogPublisher(ctx, tx, operator, id); err != nil {
				return err
			}
		}
		var product int64
		var scope, pack string
		var fromBase time.Time
		var throughBase *time.Time
		var definition json.RawMessage
		var editable, compound, copyable bool
		err := tx.QueryRow(ctx, `SELECT pro.id,p.scope_id,v.package_id::text,v.version_no,
		 v.effective_from,v.effective_to,v.definition,`+catalogRateConfigurationSQL+`,`+catalogHealthRateRule+`,
		 coalesce((`+catalogRateVersionEligibilitySQL+`) AND `+catalogVersionVisible+`,false)
		 FROM product_rule_versions v JOIN product_rule_packages p ON p.source_id=v.package_id
		 JOIN product_catalog_settings scope ON scope.product_id=p.product_id JOIN products pro ON pro.id=scope.product_id
		 WHERE v.source_id=$1::uuid AND scope.source_system='DCAI' AND pro.period_mode='calendar'
		  AND p.scope_type='BUSINESS_SCENARIO' AND p.project_code=scope.project_code FOR UPDATE OF pro`, id).
			Scan(&product, &scope, &pack, &published.Version, &fromBase, &throughBase, &definition, &editable, &compound, &copyable)
		if err != nil {
			return err
		}
		if !editable {
			return errCatalogRateUnavailable
		}
		if input.Copy == nil {
			var count int
			var active *string
			if err := tx.QueryRow(ctx, catalogActiveRateVersionSQL, product, scope, from).Scan(&count, &active); err != nil {
				return err
			}
			if count != 1 || active == nil || *active != id {
				return errCatalogRateStale
			}
			if !from.After(fromBase) {
				return errCatalogRateDates
			}
		} else {
			if !copyable {
				return errCatalogRateUnavailable
			}
			target := *input.Copy
			target.Label = strings.TrimSpace(target.Label)
			if err := checkCatalogScenarioCopy(ctx, tx, product, scope, target, from); err != nil {
				return err
			}
		}
		body := definition
		var ratesChanged, splitChanged, recipientsChanged bool
		if len(input.Changes) != 0 {
			body, ratesChanged, err = editCatalogRates(body, input.Changes)
			if err != nil {
				return err
			}
		}
		if len(input.RecipientChanges)+len(input.TeamChanges) != 0 {
			keys, err := catalogNewRecipientKeys(ctx, tx, input.RecipientChanges, input.TeamChanges)
			if err != nil {
				return err
			}
			body, recipientsChanged, err = editCatalogRecipients(body, input.RecipientChanges, input.TeamChanges, keys)
			if err != nil {
				return err
			}
		}
		if structureChanged {
			body, err = editCatalogStructure(ctx, tx, product, body, input)
			if err != nil {
				return err
			}
		}
		if removals {
			// Apply the changes indexed by the base table before removing any
			// rows. Appended rows do not change the base row numbers.
			body, err = removeCatalogEntries(body, input.RemovedItems, input.RemovedRateRows)
			if err != nil {
				return err
			}
		}
		if input.TeamSplitRates != nil {
			// Rate edits use the original row indexes. A team change can split
			// grouped companies into separate rows after those edits are applied.
			body, splitChanged, err = editCatalogTeamSplitRates(body, *input.TeamSplitRates)
			if err != nil {
				return err
			}
			if splitChanged && input.TeamSplitRates.Service > 0 {
				policy, ok := readVIPSalesPolicy(body)
				if !ok || slices.ContainsFunc(policy.Split.Teams, func(team vipInternalTeam) bool { return team.ServiceParty == "" }) {
					return errCatalogTeamSplitService
				}
			}
		}
		if leadersChanged {
			body, err = editCatalogLeaderArrangements(ctx, tx, body, input.LeaderChanges)
			if err != nil {
				return err
			}
		}
		if scopesChanged {
			body, err = editCatalogCommissionScopes(ctx, tx, body, input.ScopeChanges)
			if err != nil {
				return err
			}
		}
		membershipChanged := input.MembershipNewOnly != nil && *input.MembershipNewOnly != catalogNewMembershipOnly(definition)
		if membershipChanged {
			body, err = editCatalogNewMembershipScope(body, *input.MembershipNewOnly)
			if err != nil {
				return err
			}
		}
		if (membershipChanged || input.Copy != nil) && catalogNewMembershipOnly(body) {
			target := scope
			if input.Copy != nil {
				target = input.Copy.SKU
			}
			supported, err := catalogMembershipScopeAvailable(ctx, tx, product, target, from)
			if err != nil {
				return err
			}
			if !supported {
				return errCatalogScenarioTarget
			}
		}
		if input.Copy == nil && !ratesChanged && !splitChanged && !recipientsChanged && !structureChanged && !leadersChanged && !scopesChanged && !removals && !membershipChanged {
			return nil
		}
		if _, _, err := catalogRateActions(body); err != nil {
			return err
		}
		if recipientsChanged || len(input.NewItems)+len(input.TargetChanges) > 0 {
			if err := validateCatalogRecipientAppointments(ctx, tx, product, from, definition, body); err != nil {
				return err
			}
		}
		if input.Copy != nil {
			body, err = copyCatalogScenarioDefinition(body, scope, input.Copy.SKU)
			if err != nil {
				return err
			}
			scope, throughBase = input.Copy.SKU, nil
			contentHash := sha256.Sum256(body)
			published, err = insertCatalogScenarioRule(ctx, tx, product, *input.Copy, from, actor, hex.EncodeToString(contentHash[:]), body)
			if err != nil {
				return err
			}
		} else {
			contentHash := sha256.Sum256(body)
			if err := tx.QueryRow(ctx, `INSERT INTO product_rule_versions
		 (source_id,package_id,version_no,source_status,effective_from,effective_to,content_hash,definition,observed_at,published_at,published_by,predecessor_id)
		 SELECT gen_random_uuid(),$1::uuid,(SELECT max(version_no)+1 FROM product_rule_versions WHERE package_id=$1::uuid),
		 'PUBLISHED',$2,$3,$4,$5,now(),now(),$6,$7::uuid RETURNING source_id::text,version_no`,
				pack, from, throughBase, hex.EncodeToString(contentHash[:]), body, actor, id).Scan(&published.ID, &published.Version); err != nil {
				return err
			}
		}
		if !refreshDrafts {
			return nil
		}
		return enqueueCatalogScenarioDrafts(ctx, tx, product, scope, from, throughBase, published.ID, compound)
	}()
	return published, err
}

func catalogCalculationConflicts(input publishCatalogRatesInput) bool {
	replaced := make(map[string]bool, len(input.CalculationChanges))
	for _, change := range input.CalculationChanges {
		if replaced[change.ItemKey] {
			return true
		}
		replaced[change.ItemKey] = true
	}
	for _, change := range input.Changes {
		if replaced[change.ItemKey] && change.Label == nil {
			return true
		}
	}
	for _, row := range input.NewRateRows {
		if replaced[row.ItemKey] {
			return true
		}
	}
	for _, row := range input.RemovedRateRows {
		if replaced[row.ItemKey] {
			return true
		}
	}
	for _, item := range input.RemovedItems {
		if replaced[item] {
			return true
		}
	}
	return false
}

func lockCatalogPublisher(ctx context.Context, tx pgx.Tx, operator *financePrincipal, ruleID string) error {
	var allowed int
	err := tx.QueryRow(ctx, `SELECT 1 FROM finance_accounts a WHERE a.id=$1::uuid AND a.enabled AND EXISTS(
	 SELECT 1 FROM finance_account_rule_grants g JOIN product_rule_packages p ON p.product_id=g.product_id
	 JOIN product_rule_versions v ON v.package_id=p.source_id
	 WHERE g.account_id=a.id AND g.can_publish AND v.source_id=$2::uuid
	 ) FOR SHARE OF a`, operator.ID, ruleID).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return errRulePermission
	}
	if err != nil {
		return err
	}
	return lockFinanceOperatorSession(ctx, tx, operator)
}

func enqueueCatalogScenarioDrafts(ctx context.Context, tx pgx.Tx, product int64, sku string, from time.Time,
	through *time.Time, version string, requiresCompletion bool) error {
	periods := catalogRateDraftPeriodsSQL
	if requiresCompletion {
		periods = catalogHealthRateDraftPeriodsSQL
	}
	return enqueueDraftConfiguration(ctx, tx, periods, product, sku, from, through, version)
}

func catalogRateActions(raw json.RawMessage) (map[string]json.RawMessage, []map[string]json.RawMessage, error) {
	if _, ok := readVIPSalesPolicy(raw); !ok {
		return nil, nil, errCatalogRateUnavailable
	}
	if _, ok := readVIPBenefits(raw); !ok {
		return nil, nil, errCatalogRateUnavailable
	}
	var root map[string]json.RawMessage
	var actions []map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || json.Unmarshal(root["benefit_actions"], &actions) != nil {
		return nil, nil, errCatalogRateUnavailable
	}
	return root, actions, nil
}

func editCatalogRates(raw json.RawMessage, changes []catalogRateChange) (json.RawMessage, bool, error) {
	root, actions, err := catalogRateActions(raw)
	if err != nil {
		return nil, false, err
	}
	items := make(map[string]map[string]json.RawMessage, len(actions))
	for _, action := range actions {
		var key string
		if err := json.Unmarshal(action["item_key"], &key); err != nil {
			return nil, false, err
		}
		items[key] = action
	}
	tables := make(map[string][]map[string]json.RawMessage)
	type cell struct {
		Item string
		Row  int
	}
	seen := make(map[cell]bool, len(changes))
	renamed := make(map[string]bool)
	changed := false
	for _, change := range changes {
		action := items[change.ItemKey]
		if action == nil {
			return nil, false, invalid
		}
		if change.Label != nil {
			label := strings.TrimSpace(*change.Label)
			if change.Row != nil || change.Rate != nil || change.Fixed != nil || renamed[change.ItemKey] || !textOK(label, 80) {
				return nil, false, invalid
			}
			renamed[change.ItemKey] = true
			var previous string
			if len(action["label"]) > 0 && json.Unmarshal(action["label"], &previous) != nil {
				return nil, false, invalid
			}
			if label != previous {
				action["label"], _ = json.Marshal(label)
				changed = true
			}
			continue
		}
		if (change.Rate == nil) == (change.Fixed == nil) ||
			change.Rate != nil && (*change.Rate < 0 || *change.Rate > 10000) ||
			change.Fixed != nil && (*change.Fixed < 0 || change.Row != nil) {
			return nil, false, invalid
		}
		var mode string
		if json.Unmarshal(action["type"], &mode) != nil ||
			change.Row == nil && mode != "RATE" && mode != "FIXED" || change.Row != nil && mode != "RATE_TABLE" {
			return nil, false, invalid
		}
		rowNum := 0
		fields := action
		if change.Row != nil {
			rowNum = *change.Row
			rows, parsed := tables[change.ItemKey]
			if !parsed {
				if json.Unmarshal(action["rate_table"], &rows) != nil {
					return nil, false, invalid
				}
				tables[change.ItemKey] = rows
			}
			if rowNum < 1 || rowNum > len(rows) {
				return nil, false, invalid
			}
			fields = rows[rowNum-1]
		}
		target := cell{change.ItemKey, rowNum}
		field, desiredMode, desired := "rate_bps", "RATE", change.Rate
		if change.Fixed != nil {
			field, desiredMode, desired = "amount_minor", "FIXED", change.Fixed
		}
		if seen[target] {
			return nil, false, invalid
		}
		seen[target] = true
		if rowNum == 0 && mode != desiredMode {
			delete(fields, "rate_bps")
			delete(fields, "amount_minor")
			fields["type"], _ = json.Marshal(desiredMode)
			fields[field], _ = json.Marshal(*desired)
			changed = true
			continue
		}
		var existing int64
		if json.Unmarshal(fields[field], &existing) != nil {
			return nil, false, invalid
		}
		if *desired != existing {
			fields[field], _ = json.Marshal(*desired)
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	for key, rows := range tables {
		items[key]["rate_table"], err = json.Marshal(rows)
		if err != nil {
			return nil, false, err
		}
	}
	root["benefit_actions"], err = json.Marshal(actions)
	if err != nil {
		return nil, false, err
	}
	body, err := json.Marshal(root)
	if err != nil {
		return nil, false, err
	}
	before, _ := readVIPSalesPolicy(raw)
	if len(before.Split.Teams) != 0 && len(tables[before.Split.Item]) != 0 {
		after, ok := readVIPSalesPolicy(body)
		if !ok || internalTeamRatesChanged(before, after) {
			return nil, false, errCatalogTeamRate
		}
	}
	return body, true, nil
}

func internalTeamRatesChanged(before, after vipSalesPolicy) bool {
	// Valid category IDs are UUIDs. Also check no history, unclassified, and unmatched.
	categories := map[string]bool{"": true, "unmatched": true}
	for _, policy := range []vipSalesPolicy{before, after} {
		for _, row := range policy.Sales {
			for _, id := range row.categories() {
				categories[id] = true
			}
		}
	}
	categoryChecks := []*string{nil}
	for category := range categories {
		categoryChecks = append(categoryChecks, &category)
	}
	for _, team := range before.Split.Teams {
		for _, relationship := range []string{"", "DIRECT", "INDIRECT", "NONE"} {
			for _, category := range categoryChecks {
				oldRate, oldIssue := selectVIPRateForCompany(before.Sales, team.Company, relationship, category, "INTERNAL")
				newRate, newIssue := selectVIPRateForCompany(after.Sales, team.Company, relationship, category, "INTERNAL")
				expected := oldRate.Rate
				if oldIssue == "" && oldRate.Payee != vipPayeeSalesCompany && oldRate.Rate == before.Split.Total {
					expected = after.Split.Total
				}
				if expected != newRate.Rate || oldRate.Payee != newRate.Payee || oldIssue != newIssue {
					return true
				}
			}
		}
	}
	return false
}

func editCatalogTeamSplitRates(raw json.RawMessage, rates catalogSplitRates) (json.RawMessage, bool, error) {
	policy, ok := readVIPSalesPolicy(raw)
	split := policy.Split
	if !ok || len(split.Teams) == 0 {
		return nil, false, errCatalogRateUnavailable
	}
	for _, value := range []int64{rates.Total, rates.Sales, rates.HeadSales, rates.GroupLead, rates.Service, rates.TeamLead} {
		if value < 0 || value > 10000 {
			return nil, false, invalid
		}
	}
	common := rates.Service + rates.TeamLead
	if rates.Sales+rates.GroupLead+common != rates.Total || rates.HeadSales+common != rates.Total {
		return nil, false, errCatalogTeamSplitTotal
	}
	if rates.Total == split.Total && rates.Sales == split.Assistant && rates.HeadSales == split.HeadSeller &&
		rates.GroupLead == split.Group && rates.Service == split.Service && rates.TeamLead == split.TeamHead {
		return raw, false, nil
	}
	root, actions, err := catalogRateActions(raw)
	if err != nil {
		return nil, false, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(root["internal_sales_direct_split"], &fields) != nil {
		return nil, false, errCatalogRateUnavailable
	}
	for key, value := range map[string]int64{
		"total_rate_bps":            rates.Total,
		"assistant_seller_rate_bps": rates.Sales, "head_seller_rate_bps": rates.HeadSales,
		"group_lead_rate_bps": rates.GroupLead, "service_rate_bps": rates.Service, "team_head_rate_bps": rates.TeamLead,
	} {
		fields[key], _ = json.Marshal(value)
	}
	root["internal_sales_direct_split"], _ = json.Marshal(fields)
	if rates.Total != split.Total {
		if err := changeCatalogTeamStandards(actions, policy, rates.Total); err != nil {
			return nil, false, err
		}
		root["benefit_actions"], err = json.Marshal(actions)
		if err != nil {
			return nil, false, err
		}
	}
	body, err := json.Marshal(root)
	if err != nil {
		return nil, false, err
	}
	if rates.Total != split.Total {
		revised, ok := readVIPSalesPolicy(body)
		if !ok || internalTeamRatesChanged(policy, revised) {
			return nil, false, errCatalogTeamSplitStandard
		}
	}
	return body, true, nil
}

func changeCatalogTeamStandards(actions []map[string]json.RawMessage, policy vipSalesPolicy, total int64) error {
	for _, action := range actions {
		var key string
		if json.Unmarshal(action["item_key"], &key) != nil || key != policy.Item {
			continue
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(action["rate_table"], &rows) != nil || len(rows) != len(policy.Sales) {
			return errCatalogRateUnavailable
		}
		teams := make(map[string]bool)
		for _, team := range policy.Split.Teams {
			teams[team.Company] = true
		}
		revised := make([]map[string]json.RawMessage, 0, len(rows))
		for index, row := range rows {
			standard := policy.Sales[index]
			var internal, external []string
			for _, company := range standard.companies() {
				if teams[company] {
					internal = append(internal, company)
				} else {
					external = append(external, company)
				}
			}
			if len(internal) == 0 || standard.Rate != policy.Split.Total || standard.Payee == vipPayeeSalesCompany {
				revised = append(revised, row)
				continue
			}
			if len(external) != 0 {
				// Keep the existing condition for other companies at its old rate.
				old := maps.Clone(row)
				old["sales_organization_party_ids"], _ = json.Marshal(external)
				revised = append(revised, old)
				row["sales_organization_party_ids"], _ = json.Marshal(internal)
				delete(row, "id")
			}
			row["rate_bps"], _ = json.Marshal(total)
			revised = append(revised, row)
		}
		var err error
		action["rate_table"], err = json.Marshal(revised)
		return err
	}
	return errCatalogRateUnavailable
}
