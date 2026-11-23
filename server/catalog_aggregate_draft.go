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
	EventID, PaymentID  int64
	Scenario            string
	CompletionVersionID int64 `json:",omitempty"`
}

type catalogFrozenGroupReference struct {
	PeriodID int64
	Key      string
}

type catalogAggregateGroupInput struct {
	Key, RuleID       string
	Targets           []catalogGroupTargetKey
	Events            []int64
	Receipts, Refunds int64
	CarryIn, CarryOut int64
	CarryFromPeriod   int64
	FixedOnly         bool `json:",omitempty"`
}

func catalogAggregateWindow(month string, cycle string) (periodWindow, error) {
	day := 1
	if cycle == "MONTHLY_25_TO_24" {
		day = 25
	}
	return calendarWindow(month, day)
}

func readFrozenCatalogAggregateGroups(ctx context.Context, tx pgx.Tx, events []catalogActivityEvent, display bool) (map[catalogFrozenGroupReference]catalogPeriodGroup, error) {
	type paymentReference struct{ period, payment int64 }
	eventIndexes := make(map[paymentReference][]int)
	var periods, payments []int64
	for i, event := range events {
		if event.frozenPeriod == 0 {
			continue
		}
		key := paymentReference{event.frozenPeriod, event.PaymentID}
		if len(eventIndexes[key]) == 0 {
			periods, payments = append(periods, key.period), append(payments, key.payment)
		}
		eventIndexes[key] = append(eventIndexes[key], i)
	}
	if len(periods) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT membership.period_id,membership.source_version_id,membership.group_key
	 FROM unnest($1::bigint[],$2::bigint[]) AS payment(period_id,source_version_id)
	 JOIN catalog_aggregate_event_groups membership USING(period_id,source_version_id)
	 ORDER BY membership.period_id,membership.source_version_id,membership.group_key`, periods, payments)
	if err != nil {
		return nil, err
	}
	var refs []catalogFrozenGroupReference
	seen := make(map[catalogFrozenGroupReference]bool)
	var payment int64
	var ref catalogFrozenGroupReference
	_, err = pgx.ForEachRow(rows, []any{&ref.PeriodID, &payment, &ref.Key}, func() error {
		for _, i := range eventIndexes[paymentReference{ref.PeriodID, payment}] {
			events[i].frozenGroups = append(events[i].frozenGroups, ref)
		}
		if !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return readCatalogAggregateGroupsByReference(ctx, tx, refs, display)
}

func readCatalogAggregateGroupsByReference(ctx context.Context, tx pgx.Tx, refs []catalogFrozenGroupReference, display bool) (map[catalogFrozenGroupReference]catalogPeriodGroup, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ids, keys := make([]int64, len(refs)), make([]string, len(refs))
	for i, ref := range refs {
		ids[i], keys[i] = ref.PeriodID, ref.Key
	}
	rows, err := tx.Query(ctx, `SELECT g.period_id,g.group_key,g.rule_id::text,g.targets,v.version_no,v.definition,
	 coalesce((SELECT l.id FROM calculation_draft_lines l WHERE l.period_id=g.period_id AND l.aggregate_group_key=g.group_key LIMIT 1),0),
	 CASE WHEN $3 THEN (
	  SELECT jsonb_agg(jsonb_build_object('itemKey',a->>'Item','name',coalesce(person.name,u.name,''),
	   'ownerName',coalesce(owner.name,''),'memberCount',coalesce(jsonb_array_length(nullif(a->'Members','null')),0),
	   'organizationName',coalesce(appointment.name,'')) ORDER BY n)
	  FROM jsonb_array_elements(g.targets) WITH ORDINALITY AS chosen(a,n)
	  LEFT JOIN people person ON person.id=(a->>'person')::bigint
	  LEFT JOIN people owner ON owner.id=(a->>'Owner')::bigint
	  LEFT JOIN organization_units u ON u.id=NULLIF(a->>'Unit','')::uuid
	  LEFT JOIN organization_units appointment ON appointment.id=NULLIF(a->>'appointmentUnit','')::uuid
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
	_, err = pgx.ForEachRow(rows, []any{&ref.PeriodID, &ref.Key, &group.RuleID, &group.posting.Targets, &group.Version, &raw, &group.originalLineID, &group.Targets}, func() error {
		definition, known := definitions[group.RuleID]
		if !known {
			var err error
			definition, err = parseCatalogPeriodRule(raw)
			if err != nil {
				return err
			}
			definitions[group.RuleID] = definition
		}
		group.ID = ref.Key
		group.items = nil
		for _, target := range group.posting.Targets {
			index := slices.IndexFunc(definition.Items, func(item catalogPeriodItem) bool { return item.ItemKey == target.Item })
			if index < 0 {
				return errProductConfigConflict
			}
			group.items = append(group.items, definition.Items[index])
		}
		group.FixedOnly = len(group.items) == 1 && group.items[0].FixedMinor != nil
		group.FixedPerOrder = len(group.items) == 1 && group.items[0].FixedPerOrder
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
	rule.packageID = *summary.AggregatePackage
	err := tx.QueryRow(ctx, `SELECT p.product_id,p.paused,p.resume_from,v.definition,
	 `+catalogRuleRetiredAtSQL+`
	 FROM product_rule_packages p JOIN product_catalog_settings scope ON scope.product_id=p.product_id
	 JOIN product_rule_versions v ON v.package_id=p.source_id
	 WHERE p.source_id=$1::uuid AND `+catalogPeriodRuleScope+` AND v.source_status='PUBLISHED' AND `+catalogVersionVisible+`
	  AND v.effective_from<$2
	 ORDER BY v.effective_from DESC,v.version_no DESC LIMIT 1`, rule.packageID, window.Through).
		Scan(&rule.ProductID, &rule.Paused, &rule.ResumeFrom, &raw, &rule.retiredFrom)
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
	result.aggregatePredecessor = activity.Grouping.carryFromPeriod
	result.aggregateEvents = activity.Grouping.events
	slices.SortFunc(result.aggregateEvents, func(a, b catalogAggregateEventReference) int { return cmp.Compare(a.EventID, b.EventID) })
	if activity.Grouping.PriorUnsettled {
		result.Issues = append(result.Issues, draftIssue{Code: "AGGREGATE_PRIOR_UNSETTLED"})
	}
	slices.SortFunc(result.Issues, func(a, b draftIssue) int {
		return cmp.Or(cmp.Compare(a.FactID, b.FactID), cmp.Compare(a.Code, b.Code), cmp.Compare(a.Component, b.Component))
	})
	organizations, err := loadReportAttribution(ctx, tx, nil, nil)
	if err != nil {
		return result, nil, err
	}
	for _, group := range activity.Grouping.Groups {
		posting := group.posting
		quote := group.quote()
		posting.Key, posting.RuleID = group.ID, group.RuleID
		posting.FixedOnly = group.FixedOnly
		posting.Receipts, posting.Refunds = group.Receipts, group.Refunds
		posting.CarryIn, posting.CarryOut = group.CarryIn, quote.CarryForward
		slices.Sort(posting.Events)
		result.aggregateGroups = append(result.aggregateGroups, posting)
		var applied pgtype.UUID
		if err := applied.Scan(group.RuleID); err != nil {
			return result, nil, err
		}
		var refundTotal int64
		for _, refund := range group.orderRefunds {
			if refund.OriginalLineID == 0 {
				return result, nil, errRefundAmount
			}
			refund.RefundPeriodID = summary.PeriodID
			result.Refunds = append(result.Refunds, refund)
			if err := addCatalogActivityAmount(refund.Amount, &refundTotal, &result.RefundTotal); err != nil {
				return result, nil, err
			}
		}
		for i, award := range quote.Awards {
			// The group quote shows signed net commission. Persist deductions
			// through the same refund flow used for individual-order awards.
			award.Amount, err = addMinor(award.Amount, -refundTotal)
			if err != nil {
				return result, nil, err
			}
			if award.Amount == 0 {
				continue
			}
			target := posting.Targets[i]
			benefit := &catalogDraftBenefit{Rule: applied, AllocationOwnerID: target.Owner, Rate: award.Rate, PoolID: target.PoolID}
			if target.Unit != "" {
				if err := benefit.Unit.Scan(target.Unit); err != nil {
					return result, nil, err
				}
			}
			reportUnit := benefit.Unit
			if group.FixedPerOrder && target.AppointmentUnit != "" {
				if err := reportUnit.Scan(target.AppointmentUnit); err != nil {
					return result, nil, err
				}
			}
			result.Total, err = addMinor(result.Total, award.Amount)
			if err != nil {
				return result, nil, err
			}
			result.Lines = append(result.Lines, draftLine{Component: award.ItemKey, PersonID: target.Person,
				AggregateGroup: group.ID, Catalog: benefit, Amount: award.Amount, Base: quote.Basis,
				organization: organizations.ancestors(reportUnit)})
		}
	}
	slices.SortFunc(result.Refunds, func(a, b refundLine) int {
		return cmp.Or(cmp.Compare(a.RefundSourceVersionID, b.RefundSourceVersionID), cmp.Compare(a.OriginalLineID, b.OriginalLineID))
	})
	digest, err := digestDraft(0, result, nil)
	return result, digest, err
}

func readCatalogAggregateCarry(ctx context.Context, tx pgx.Tx, report *catalogPeriodGroupReport, packageID string,
	start time.Time, rules []catalogAggregateRule, display, resumed bool) error {
	var confirmedPeriod int64
	err := tx.QueryRow(ctx, `SELECT p.id,coalesce((
	  SELECT booked.id FROM periods booked JOIN settlements locked ON locked.period_id=booked.id
	  WHERE booked.aggregate_package_id=p.aggregate_package_id
	   AND booked.starts_on=($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date
	 ),0) FROM periods p JOIN settlements final ON final.period_id=p.id
	 WHERE p.aggregate_package_id=$1::uuid AND p.ends_on<($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date
	 AND ($3 OR p.ends_on=($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date-1)
	 ORDER BY p.ends_on DESC,p.id DESC LIMIT 1`, packageID, start, resumed).
		Scan(&report.carryFromPeriod, &confirmedPeriod)
	if errors.Is(err, pgx.ErrNoRows) {
		report.PriorUnsettled = !resumed
		return nil
	}
	if err != nil {
		return err
	}
	report.Groups, err = applyCatalogAggregateCarry(ctx, tx, report.carryFromPeriod, confirmedPeriod, report.Groups, rules, display)
	return err
}

func applyCatalogAggregateCarry(ctx context.Context, tx pgx.Tx, prior, confirmed int64, groups []catalogPeriodGroup,
	rules []catalogAggregateRule, display bool) ([]catalogPeriodGroup, error) {
	byRule := make(map[string]catalogAggregateRule, len(rules))
	for _, rule := range rules {
		byRule[rule.ID] = rule
	}
	index := make(map[string]int, len(groups))
	type carryDestination struct {
		index int
		from  time.Time
	}
	byArrangement := make(map[string]carryDestination, len(groups))
	for i, group := range groups {
		index[group.ID] = i
		if confirmed != 0 || group.Receipts == 0 || group.FixedPerOrder {
			continue
		}
		rule := byRule[group.RuleID]
		key, err := catalogAggregateCarryKey(rule.Definition, group.posting.Targets)
		if err != nil {
			return nil, err
		}
		if current, ok := byArrangement[key]; !ok || rule.From.After(current.from) {
			byArrangement[key] = carryDestination{index: i, from: rule.From}
		}
	}
	// For closed periods, read their stored carry instead of assigning the prior balance again.
	balancePeriod := prior
	if confirmed != 0 {
		balancePeriod = confirmed
	}
	rows, err := tx.Query(ctx, `SELECT g.group_key,g.rule_id::text,g.targets,
	 CASE WHEN $2 THEN g.carry_in_minor ELSE g.carry_out_minor END,v.definition
	 FROM catalog_aggregate_draft_groups g JOIN product_rule_versions v ON v.source_id=g.rule_id
	 WHERE g.period_id=$1 AND (CASE WHEN $2 THEN g.carry_in_minor ELSE g.carry_out_minor END)<0 ORDER BY g.group_key`,
		balancePeriod, confirmed != 0)
	if err != nil {
		return nil, err
	}
	var previous catalogAggregateGroupInput
	var raw json.RawMessage
	definitions := make(map[string]catalogPeriodDefinition)
	firstCarryOnly := len(groups)
	var carryOnly []catalogFrozenGroupReference
	_, err = pgx.ForEachRow(rows, []any{&previous.Key, &previous.RuleID, &previous.Targets, &previous.CarryOut, &raw}, func() error {
		targets := previous.Targets
		previous.Targets = nil
		i, found := index[previous.Key]
		if len(byArrangement) > 0 && (!found || groups[i].Receipts == 0) {
			definition, parsed := definitions[previous.RuleID]
			if !parsed {
				var err error
				definition, err = parseCatalogPeriodRule(raw)
				if err != nil {
					return err
				}
				definitions[previous.RuleID] = definition
			}
			key, err := catalogAggregateCarryKey(definition, targets)
			if err != nil {
				return err
			}
			if target, ok := byArrangement[key]; ok {
				i, found = target.index, true
			}
		}
		if found {
			group := &groups[i]
			group.posting.CarryFromPeriod = prior
			return addCatalogActivityAmount(previous.CarryOut, &group.CarryIn)
		}
		// A period with no new payments still passes its original group's debt forward.
		posting := catalogAggregateGroupInput{Targets: targets, CarryFromPeriod: prior}
		groups = append(groups, catalogPeriodGroup{ID: previous.Key, RuleID: previous.RuleID, posting: posting,
			CarryIn: previous.CarryOut, AfterCarry: &catalogPeriodQuote{CarryForward: previous.CarryOut}})
		if display {
			carryOnly = append(carryOnly, catalogFrozenGroupReference{PeriodID: balancePeriod, Key: previous.Key})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range groups[:firstCarryOnly] {
		group := &groups[i]
		if group.CarryIn != 0 {
			quote, err := group.calculateQuote(group.CarryIn)
			if err != nil {
				return nil, err
			}
			group.AfterCarry = &quote
		}
	}
	if len(carryOnly) == 0 {
		return groups, nil
	}
	original, err := readCatalogAggregateGroupsByReference(ctx, tx, carryOnly, true)
	if err != nil {
		return nil, err
	}
	for i, ref := range carryOnly {
		group, ok := original[ref]
		if !ok {
			return nil, errProductConfigConflict
		}
		groups[firstCarryOnly+i].Version, groups[firstCarryOnly+i].Targets = group.Version, group.Targets
	}
	return groups, nil
}

func storeCatalogAggregateDraft(ctx context.Context, tx pgx.Tx, periodID int64, groups []catalogAggregateGroupInput, events []catalogAggregateEventReference) error {
	if _, err := tx.Exec(ctx, `DELETE FROM catalog_aggregate_draft_events WHERE period_id=$1`, periodID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM catalog_aggregate_draft_groups WHERE period_id=$1`, periodID); err != nil {
		return err
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"catalog_aggregate_draft_events"},
		[]string{"period_id", "source_version_id", "payment_version_id", "sku_code", "completion_version_id"},
		pgx.CopyFromSlice(len(events), func(i int) ([]any, error) {
			event := events[i]
			return []any{periodID, event.EventID, event.PaymentID, event.Scenario, optionalDraftID(event.CompletionVersionID)}, nil
		}))
	if err != nil || len(groups) == 0 {
		return err
	}
	type groupEvent struct {
		group string
		event int64
	}
	var memberships []groupEvent
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"catalog_aggregate_draft_groups"},
		[]string{"period_id", "group_key", "rule_id", "targets", "receipts_minor", "refunds_minor",
			"carry_in_minor", "carry_out_minor", "carry_from_period_id", "fixed_only"},
		pgx.CopyFromSlice(len(groups), func(i int) ([]any, error) {
			group := groups[i]
			for _, eventID := range group.Events {
				memberships = append(memberships, groupEvent{group.Key, eventID})
			}
			return []any{periodID, group.Key, group.RuleID, group.Targets, group.Receipts, group.Refunds,
				group.CarryIn, group.CarryOut, optionalDraftID(group.CarryFromPeriod), group.FixedOnly}, nil
		}))
	if err != nil || len(memberships) == 0 {
		return err
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"catalog_aggregate_event_groups"},
		[]string{"period_id", "group_key", "source_version_id"}, pgx.CopyFromSlice(len(memberships), func(i int) ([]any, error) {
			return []any{periodID, memberships[i].group, memberships[i].event}, nil
		}))
	return err
}

const catalogAggregateDraftEventsSQL = `WITH scope AS (
 SELECT d.period_id,g.group_key FROM calculation_drafts d
 JOIN catalog_aggregate_draft_groups g USING(period_id)
 WHERE d.period_id=$1 AND d.version=$2 AND g.group_key=$3
), related AS NOT MATERIALIZED (
 SELECT a.* FROM scope JOIN catalog_aggregate_event_groups membership USING(period_id,group_key)
 JOIN catalog_aggregate_draft_events a USING(period_id,source_version_id)
)` + catalogAggregateEventsPageSQL + ` FROM scope`

const catalogAggregateEventsPageSQL = `, page AS (
 SELECT o.id AS "sourceVersionId",paid.order_id AS "orderId",paid.fact->>'orderNo' AS "orderNo",o.version AS "sourceVersion",
  o.kind,o.occurred_at AS "occurredAt",coalesce((o.fact->>'amountMinor')::bigint,0) AS "amountMinor",
  completed.occurred_at AS "completedAt"
 FROM related a
 JOIN source_order_versions o ON o.id=a.source_version_id
 JOIN source_order_versions paid ON paid.id=a.payment_version_id
 LEFT JOIN source_order_versions completed ON completed.id=a.completion_version_id
 ORDER BY o.occurred_at,o.id LIMIT 25 OFFSET $4
)
SELECT jsonb_build_object('items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY "occurredAt","sourceVersionId") FROM page),'[]'),
 'total',(SELECT count(*) FROM related))`

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
