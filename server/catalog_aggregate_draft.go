package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var errCatalogAggregatePaused = errors.New("整期规则已暂停，可在规则页只读试算")
var errCatalogAggregatePeriod = errors.New("所选账期没有生效的整期规则")

type catalogAggregatePeriodInput struct {
	RuleID string `json:"ruleId"`
	Month  string `json:"month"`
}

type catalogAggregateEventReference struct {
	EventID, PaymentID int64
	Scenario           string
}

type catalogFrozenGroupReference struct {
	PeriodID int64
	Key      string
}

type catalogAggregateGroupInput struct {
	Key, RuleID       string
	AnchorID          int64
	Targets           []catalogGroupTargetKey
	Events            []catalogAggregateEventReference
	Receipts, Refunds int64
	CarryIn, CarryOut int64
	CarryFromPeriod   int64
}

func catalogAggregateWindow(month string, cycle string) (periodWindow, error) {
	day := 1
	if cycle == "MONTHLY_25_TO_24" {
		day = 25
	}
	return calendarWindow(month, day)
}

func readFrozenCatalogAggregateGroups(ctx context.Context, tx pgx.Tx, events []catalogActivityEvent, display bool) (map[catalogFrozenGroupReference]catalogPeriodGroup, error) {
	var ids []int64
	var keys []string
	seen := make(map[catalogFrozenGroupReference]bool)
	for _, event := range events {
		ref := event.frozen
		if ref.PeriodID != 0 && !seen[ref] {
			ids, keys = append(ids, ref.PeriodID), append(keys, ref.Key)
			seen[ref] = true
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT g.period_id,g.group_key,g.rule_id::text,g.targets,v.version_no,v.definition,
	 CASE WHEN $3 THEN (
	  SELECT jsonb_agg(jsonb_build_object('itemKey',a->>'Item','name',coalesce(person.name,u.name,''),
	   'ownerName',coalesce(owner.name,''),'memberCount',coalesce(jsonb_array_length(nullif(a->'Members','null')),0)) ORDER BY n)
	  FROM jsonb_array_elements(g.targets) WITH ORDINALITY AS chosen(a,n)
	  LEFT JOIN people person ON person.id=(a->>'person')::bigint
	  LEFT JOIN people owner ON owner.id=(a->>'Owner')::bigint
	  LEFT JOIN organization_units u ON u.id=NULLIF(a->>'Unit','')::uuid
	 ) END
	 FROM unnest($1::bigint[],$2::text[]) q(period_id,group_key)
	 JOIN catalog_aggregate_draft_groups g USING(period_id,group_key)
	 JOIN settlements s USING(period_id) JOIN product_rule_versions v ON v.source_id=g.rule_id`, ids, keys, display)
	if err != nil {
		return nil, err
	}
	result := make(map[catalogFrozenGroupReference]catalogPeriodGroup, len(keys))
	var ref catalogFrozenGroupReference
	var group catalogPeriodGroup
	var raw json.RawMessage
	definitions := make(map[string]catalogPeriodDefinition)
	_, err = pgx.ForEachRow(rows, []any{&ref.PeriodID, &ref.Key, &group.RuleID, &group.posting.Targets, &group.Version, &raw, &group.Targets}, func() error {
		definition, known := definitions[group.RuleID]
		if !known {
			var err error
			definition, err = parseCatalogPeriodRule(raw)
			if err != nil {
				return err
			}
			definitions[group.RuleID] = definition
		}
		group.ID, group.items = ref.Key, definition.Items
		for i, target := range group.Targets {
			for _, item := range group.items {
				if target.ItemKey == item.ItemKey && item.PoolName != "" {
					group.Targets[i].Name = item.PoolName
					break
				}
			}
		}
		result[ref] = group
		group.Targets, group.posting.Targets = nil, nil
		return nil
	})
	return result, err
}

func (s workspace) openCatalogAggregateDraft(ctx context.Context, input catalogAggregatePeriodInput) (any, error) {
	actor := financeRuleActor{permits: func(int64, bool) bool { return s.development }}
	if principal := financeUser(ctx); principal != nil {
		actor = financeRuleActor{account: principal, permits: principal.permitsRule}
	}
	var id int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		id, err = createCatalogAggregatePeriod(ctx, tx, input, actor)
		return err
	})
	if err != nil {
		return draftSummary{}, err
	}
	if closed, err := readSettlement(ctx, s.db, id); err == nil {
		return draftSummary{PeriodID: id, Version: closed.DraftVersion}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return draftSummary{}, err
	}
	return s.calculateCurrentDraft(ctx, id, 0)
}

func createCatalogAggregatePeriod(ctx context.Context, tx pgx.Tx, input catalogAggregatePeriodInput, actor financeRuleActor) (int64, error) {
	rule, err := readCatalogPeriodVersion(ctx, tx, input.RuleID)
	if err != nil {
		return 0, err
	}
	if !actor.permits(rule.ProductID, false) || actor.account != nil && !actor.account.Workspace {
		return 0, errRulePermission
	}
	if actor.account != nil {
		if err := lockFinanceOperatorSession(ctx, tx, actor.account); err != nil {
			return 0, err
		}
	}
	month, err := parseReportMonth(input.Month)
	if err != nil {
		return 0, err
	}
	definition, err := parseCatalogPeriodRule(rule.raw)
	if err != nil {
		return 0, err
	}
	window, err := catalogAggregateWindow(input.Month, definition.Cycle)
	if err != nil {
		return 0, err
	}
	// Scope changes and initialization use the same package lock as publication.
	if err := tx.QueryRow(ctx, `SELECT paused,name,`+catalogRuleRetiredAtSQL+` FROM product_rule_packages p
	 WHERE source_id=$1::uuid FOR UPDATE`, rule.packageID).Scan(&rule.Paused, &rule.Label, &rule.retiredFrom); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM periods WHERE aggregate_package_id=$1 AND report_month=$2`, rule.packageID, month).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	if rule.Paused {
		return 0, errCatalogAggregatePaused
	}
	versions, err := readCatalogAggregateVersions(ctx, tx, rule.packageID, window.Through, definition.Cycle)
	if err != nil {
		return 0, err
	}
	active := slices.ContainsFunc(versions, func(v catalogAggregateRule) bool {
		return v.Through == nil || window.Start.Before(*v.Through)
	})
	var hasEarlierSettlement bool
	if !active && rule.retiredFrom != nil && !window.Start.Before(*rule.retiredFrom) {
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM periods p JOIN settlements final ON final.period_id=p.id
		 WHERE p.aggregate_package_id=$1::uuid AND p.report_month<$2)`, rule.packageID, month).Scan(&hasEarlierSettlement)
		if err != nil {
			return 0, err
		}
	}
	if !active && !hasEarlierSettlement {
		return 0, errCatalogAggregatePeriod
	}
	if err := tx.QueryRow(ctx, `INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,report_month,aggregate_package_id)
	 VALUES($1,$2,$3,$4::text::date,$5::text::date,$6,$7::uuid) RETURNING id`, rule.ProductID, rule.Label+" · "+input.Month,
		month.Year()*100+int(month.Month()), window.Start.Format(time.DateOnly), window.Through.AddDate(0, 0, -1).Format(time.DateOnly),
		month, rule.packageID).Scan(&id); err != nil {
		return 0, err
	}
	return id, enqueueDraftPeriods(ctx, tx, `SELECT $1::bigint`, id)
}

func computeCatalogAggregateDraft(ctx context.Context, tx pgx.Tx, summary draftSummary, window periodWindow) (paymentDraft, []byte, error) {
	var result paymentDraft
	var rule catalogPeriodVersion
	var raw json.RawMessage
	var first time.Time
	rule.packageID = *summary.AggregatePackage
	err := tx.QueryRow(ctx, `SELECT p.product_id,p.paused,v.definition,min(greatest(v.effective_from,p.resume_from)) OVER (),
	 `+catalogRuleRetiredAtSQL+`
	 FROM product_rule_packages p JOIN product_catalog_settings scope ON scope.product_id=p.product_id
	 JOIN product_rule_versions v ON v.package_id=p.source_id
	 WHERE p.source_id=$1::uuid AND `+catalogPeriodRuleScope+` AND v.source_status='PUBLISHED' AND `+catalogVersionVisible+`
	  AND v.effective_from<$2
	 ORDER BY v.effective_from DESC,v.version_no DESC LIMIT 1`, rule.packageID, window.Through).
		Scan(&rule.ProductID, &rule.Paused, &raw, &first, &rule.retiredFrom)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil, errCatalogAggregatePeriod
	}
	if err != nil {
		return result, nil, err
	}
	if rule.Paused {
		return result, nil, errCatalogAggregatePaused
	}
	definition, err := parseCatalogPeriodRule(raw)
	if err != nil {
		return result, nil, err
	}
	rule.Cycle = definition.Cycle
	activity, err := readCatalogPeriodActivity(ctx, tx, catalogPeriodActivityRequest{
		ProductID: rule.ProductID, Scenarios: definition.Scenarios, Window: window, GroupRule: &rule, DraftPeriodID: summary.PeriodID,
	})
	if err != nil {
		return result, nil, err
	}
	result.Issues = append(activity.postingIssues, activity.Grouping.postingIssues...)
	if rule.retiredFrom == nil || window.Start.Before(*rule.retiredFrom) {
		for _, scenario := range activity.Scenarios {
			if !scenario.Mapped && slices.Contains(definition.Scenarios, scenario.Code) {
				result.Issues = append(result.Issues, draftIssue{Component: scenario.Code, Code: "AGGREGATE_SCENARIO_UNMAPPED"})
			}
		}
	}
	groups := activity.Grouping.Groups
	if first.Before(window.Start) {
		err = tx.QueryRow(ctx, `SELECT p.id FROM periods p JOIN settlements final ON final.period_id=p.id
		 WHERE p.aggregate_package_id=$1::uuid AND (p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'=$2`,
			rule.packageID, window.Start).Scan(&result.aggregatePredecessor)
		if errors.Is(err, pgx.ErrNoRows) {
			result.Issues = append(result.Issues, draftIssue{Code: "AGGREGATE_PRIOR_UNSETTLED"})
		} else if err != nil {
			return result, nil, err
		} else {
			groups, err = applyCatalogAggregateCarry(ctx, tx, result.aggregatePredecessor, groups)
			if err != nil {
				return result, nil, err
			}
		}
	}
	slices.SortFunc(result.Issues, func(a, b draftIssue) int {
		return cmp.Or(cmp.Compare(a.FactID, b.FactID), cmp.Compare(a.Code, b.Code), cmp.Compare(a.Component, b.Component))
	})
	organizations, err := loadReportAttribution(ctx, tx, nil, nil)
	if err != nil {
		return result, nil, err
	}
	for _, group := range groups {
		posting := group.posting
		posting.Key, posting.RuleID = group.ID, group.RuleID
		posting.Receipts, posting.Refunds = group.Receipts, group.Refunds
		posting.CarryOut = group.BeforeCarry.CarryForward
		slices.SortFunc(posting.Events, func(a, b catalogAggregateEventReference) int { return cmp.Compare(a.EventID, b.EventID) })
		result.aggregateGroups = append(result.aggregateGroups, posting)
		var applied pgtype.UUID
		if err := applied.Scan(group.RuleID); err != nil {
			return result, nil, err
		}
		for i, award := range group.BeforeCarry.Awards {
			if award.Amount == 0 {
				continue
			}
			target := posting.Targets[i]
			benefit := &catalogDraftBenefit{Rule: applied, AllocationOwnerID: target.Owner, Rate: award.Rate}
			if target.Unit != "" {
				if err := benefit.Unit.Scan(target.Unit); err != nil {
					return result, nil, err
				}
			}
			result.Total, err = addMinor(result.Total, award.Amount)
			if err != nil {
				return result, nil, err
			}
			result.Lines = append(result.Lines, draftLine{FactID: posting.AnchorID, Component: award.ItemKey, PersonID: target.Person,
				AggregateGroup: group.ID, Catalog: benefit, Amount: award.Amount, Base: group.BeforeCarry.Basis,
				organization: organizations.ancestors(benefit.Unit)})
		}
	}
	digest, err := digestDraft(0, result, nil)
	return result, digest, err
}

func applyCatalogAggregateCarry(ctx context.Context, tx pgx.Tx, prior int64, groups []catalogPeriodGroup) ([]catalogPeriodGroup, error) {
	index := make(map[string]int, len(groups))
	for i, group := range groups {
		index[group.ID] = i
	}
	rows, err := tx.Query(ctx, `SELECT g.group_key,g.rule_id::text,g.anchor_payment_id,g.targets,g.carry_out_minor
	 FROM catalog_aggregate_draft_groups g WHERE g.period_id=$1 AND g.carry_out_minor<0 ORDER BY g.group_key`, prior)
	if err != nil {
		return nil, err
	}
	var previous catalogAggregateGroupInput
	_, err = pgx.ForEachRow(rows, []any{&previous.Key, &previous.RuleID, &previous.AnchorID, &previous.Targets, &previous.CarryOut}, func() error {
		targets := previous.Targets
		previous.Targets = nil
		if i, found := index[previous.Key]; found {
			group := &groups[i]
			group.posting.CarryIn, group.posting.CarryFromPeriod = previous.CarryOut, prior
			quote, err := quoteCatalogPeriod(group.items, group.Receipts, group.Refunds, previous.CarryOut)
			group.BeforeCarry = quote
			return err
		}
		// A period with no new payments still passes its original group's debt forward.
		posting := catalogAggregateGroupInput{AnchorID: previous.AnchorID, Targets: targets,
			CarryIn: previous.CarryOut, CarryFromPeriod: prior}
		groups = append(groups, catalogPeriodGroup{ID: previous.Key, RuleID: previous.RuleID, posting: posting,
			BeforeCarry: catalogPeriodQuote{CarryForward: previous.CarryOut}})
		return nil
	})
	return groups, err
}

func storeCatalogAggregateDraft(ctx context.Context, tx pgx.Tx, periodID int64, groups []catalogAggregateGroupInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM catalog_aggregate_draft_groups WHERE period_id=$1`, periodID); err != nil {
		return err
	}
	if len(groups) == 0 {
		return nil
	}
	type eventRow struct {
		key string
		catalogAggregateEventReference
	}
	var events []eventRow
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"catalog_aggregate_draft_groups"},
		[]string{"period_id", "group_key", "rule_id", "anchor_payment_id", "targets", "receipts_minor", "refunds_minor",
			"carry_in_minor", "carry_out_minor", "carry_from_period_id"},
		pgx.CopyFromSlice(len(groups), func(i int) ([]any, error) {
			g := groups[i]
			for _, event := range g.Events {
				events = append(events, eventRow{g.Key, event})
			}
			return []any{periodID, g.Key, g.RuleID, g.AnchorID, g.Targets, g.Receipts, g.Refunds,
				g.CarryIn, g.CarryOut, optionalDraftID(g.CarryFromPeriod)}, nil
		}))
	if err != nil || len(events) == 0 {
		return err
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"catalog_aggregate_draft_events"},
		[]string{"period_id", "group_key", "source_version_id", "payment_version_id", "sku_code"},
		pgx.CopyFromSlice(len(events), func(i int) ([]any, error) {
			e := events[i]
			return []any{periodID, e.key, e.EventID, e.PaymentID, e.Scenario}, nil
		}))
	return err
}

const catalogAggregateDraftEventsSQL = `WITH scope AS (
 SELECT d.period_id,g.group_key FROM calculation_drafts d
 JOIN catalog_aggregate_draft_groups g USING(period_id)
 WHERE d.period_id=$1 AND d.version=$2 AND g.group_key=$3
), page AS (
 SELECT o.id AS "sourceVersionId",o.order_id AS "orderId",o.fact->>'orderNo' AS "orderNo",o.version AS "sourceVersion",
  o.kind,o.occurred_at AS "occurredAt",coalesce((o.fact->>'amountMinor')::bigint,0) AS "amountMinor"
 FROM scope JOIN catalog_aggregate_draft_events a USING(period_id,group_key)
 JOIN source_order_versions o ON o.id=a.source_version_id ORDER BY o.occurred_at,o.id LIMIT 25 OFFSET $4
)
SELECT jsonb_build_object('items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY "occurredAt","sourceVersionId") FROM page),'[]'),
 'total',(SELECT count(*) FROM catalog_aggregate_draft_events e JOIN scope USING(period_id,group_key)))
FROM scope`

func (s workspace) getCatalogAggregateDraftEvents(w http.ResponseWriter, r *http.Request) {
	period, version, offset, err := draftReportPage(r)
	group := r.URL.Query().Get("group")
	if err != nil || !textOK(group, 64) {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), catalogAggregateDraftEventsSQL, period, version, group, offset).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errDraftChanged
	}
	respond(w, result, err)
}
