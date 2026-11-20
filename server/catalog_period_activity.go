package main

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type catalogPeriodActivityRequest struct {
	ProductID     int64
	Scenarios     []string
	Window        periodWindow
	Provider      string
	MaxAge        time.Duration
	GroupRule     *catalogPeriodVersion
	TrialPaused   bool
	DraftPeriodID int64
}

type catalogPeriodScenarioActivity struct {
	Code              string `json:"code"`
	Mapped            bool   `json:"mapped"`
	Payments          int    `json:"paymentCount"`
	Refunds           int    `json:"refundCount"`
	ReceiptsMinor     int64  `json:"receiptsMinor"`
	RefundsMinor      int64  `json:"refundsMinor"`
	PriorRefundsMinor int64  `json:"priorPeriodRefundsMinor"`
}

type catalogPeriodActivity struct {
	Start              time.Time                       `json:"start"`
	Through            time.Time                       `json:"through"`
	Scenarios          []catalogPeriodScenarioActivity `json:"scenarios"`
	Sources            []settlementSource              `json:"sources"`
	Issues             map[string]int                  `json:"issues"`
	Receipts           int64                           `json:"receiptsMinor"`
	Refunds            int64                           `json:"refundsMinor"`
	PriorPeriodRefunds int64                           `json:"priorPeriodRefundsMinor"`
	Grouping           *catalogPeriodGroupReport       `json:"grouping,omitempty"`
	postingIssues      []draftIssue
}

const catalogAggregateRoutes = `
 SELECT r.* FROM product_catalog_settings anchor
 JOIN product_catalog_settings c ON (c.project_code,c.source_system)=(anchor.project_code,anchor.source_system)
 JOIN catalog_business_routes r ON r.product_id=c.product_id AND r.observed_at=c.captured_at AND r.source=c.source_system
 WHERE anchor.product_id=$1 AND c.source_system='DCAI'`

const catalogActivityLatest = `NOT EXISTS (
 SELECT 1 FROM source_order_versions newer
 WHERE (newer.source,newer.order_id,newer.line_id,newer.record_key,newer.kind)=(v.source,v.order_id,v.line_id,v.record_key,v.kind)
 AND (newer.updated_at,newer.id)>(v.updated_at,v.id)
)`

const catalogActivityEventColumns = `v.id,v.source,v.kind,v.occurred_at,v.fact,
  CASE WHEN v.kind='ORDER_PAID' THEN v.fact->>'paymentId' ELSE v.fact->>'refundId' END AS event_id,
  CASE WHEN jsonb_typeof(v.fact->'amountMinor')='number' THEN (v.fact->>'amountMinor')::bigint END AS amount`

const catalogActivityAmountsSQL = `WITH routes AS MATERIALIZED (` + catalogAggregateRoutes + `),
draft_scope AS (SELECT aggregate_package_id,starts_on,ends_on FROM periods WHERE id=$6::bigint),
current_events AS MATERIALIZED (
 SELECT ` + catalogActivityEventColumns + `,true AS in_scope,coalesce(booked.changed,false) AS booked_changed
 FROM source_order_versions v
 LEFT JOIN LATERAL (
  SELECT min(a.period_id) FILTER(WHERE final.id IS NULL) AS reserved_period,
   bool_or((original.order_id,original.line_id,original.occurred_at,original.fact->'amountMinor',
   original.fact->'refundsPayment',original.fact->'sourceKey') IS DISTINCT FROM
   (v.order_id,v.line_id,v.occurred_at,v.fact->'amountMinor',v.fact->'refundsPayment',v.fact->'sourceKey'))
    FILTER(WHERE final.id IS NOT NULL) AS changed
  FROM source_order_versions original JOIN catalog_aggregate_draft_events a ON a.source_version_id=original.id
  JOIN periods p ON p.id=a.period_id LEFT JOIN settlements final ON final.period_id=p.id
  JOIN draft_scope draft ON draft.aggregate_package_id=p.aggregate_package_id
  WHERE v.kind='REFUND_SUCCEEDED' AND original.source=v.source AND original.kind='REFUND_SUCCEEDED'
   AND (original.fact->>'domain',original.fact->>'refundId')=(v.fact->>'domain',v.fact->>'refundId')
 ) booked ON true
 WHERE v.source='DCAI' AND v.kind IN ('ORDER_PAID','REFUND_SUCCEEDED','REFUND_ADJUSTED')
  AND v.occurred_at<$4 AND v.occurred_at<=CURRENT_TIMESTAMP AND booked.changed IS DISTINCT FROM false
  AND ($7::timestamptz IS NULL OR v.kind<>'ORDER_PAID' OR v.occurred_at<$7)
  AND v.occurred_at>=least($3,(
   SELECT min(earlier.starts_on)::timestamp AT TIME ZONE 'Asia/Shanghai'
   FROM draft_scope draft JOIN periods earlier USING(aggregate_package_id) JOIN settlements final ON final.period_id=earlier.id
   WHERE earlier.starts_on<draft.starts_on
  ))
  AND (v.occurred_at>=$3 OR (v.kind IN ('REFUND_SUCCEEDED','REFUND_ADJUSTED') AND EXISTS(
   SELECT 1 FROM draft_scope draft JOIN periods earlier USING(aggregate_package_id) JOIN settlements final ON final.period_id=earlier.id
   WHERE (booked.reserved_period=$6 OR (booked.reserved_period IS NULL AND
     (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN draft.starts_on AND draft.ends_on))
    AND (v.occurred_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN earlier.starts_on AND earlier.ends_on
  )))
  AND (EXISTS(SELECT 1 FROM routes m WHERE m.sku_code=ANY($2) AND ` + catalogRouteFactSQL + `)
   OR v.kind='REFUND_SUCCEEDED' AND EXISTS(
    SELECT 1 FROM source_order_versions original JOIN catalog_aggregate_draft_events a
     ON original.id=a.source_version_id AND a.source_version_id=a.payment_version_id
    JOIN settlements closed USING(period_id) JOIN periods prior ON prior.id=closed.period_id
    JOIN draft_scope draft ON draft.aggregate_package_id=prior.aggregate_package_id
    WHERE original.source=v.source AND original.order_id=v.fact#>>'{refundsPayment,0,orderId}' AND original.kind='ORDER_PAID'
     AND (original.fact->>'domain',original.fact->>'sourceKey')=(v.fact->>'domain',v.fact->>'sourceKey')
   ))
  AND ` + catalogActivityLatest + `
), refund_orders AS (
 SELECT DISTINCT fact#>>'{refundsPayment,0,orderId}' AS order_id FROM current_events WHERE kind='REFUND_SUCCEEDED'
), events AS (
 SELECT * FROM current_events
 UNION ALL
 SELECT ` + catalogActivityEventColumns + `,false,false
 FROM refund_orders original JOIN source_order_versions v
  ON v.source='DCAI' AND v.kind='REFUND_SUCCEEDED' AND v.fact#>>'{refundsPayment,0,orderId}'=original.order_id
 WHERE v.occurred_at<$3 AND NOT EXISTS(SELECT 1 FROM current_events incoming WHERE incoming.id=v.id) AND ` + catalogActivityLatest + `
), linked AS MATERIALIZED (
 SELECT e.*,e.fact->'refundsPayment'->0 AS reference,
  count(*) OVER (PARTITION BY e.kind,e.fact->>'domain',e.event_id) AS copies
 FROM events e
), origins AS MATERIALIZED (
 SELECT e.*,CASE WHEN e.kind='ORDER_PAID' THEN e.id WHEN p.matches=1 THEN p.id END AS payment_id
 FROM linked e
 LEFT JOIN LATERAL (
  SELECT count(*) AS matches,min(v.id) AS id FROM source_order_versions v
  WHERE e.kind='REFUND_SUCCEEDED' AND jsonb_array_length(CASE WHEN jsonb_typeof(e.fact->'refundsPayment')='array'
    THEN e.fact->'refundsPayment' ELSE '[]'::jsonb END)=1
   AND coalesce(e.reference->>'orderId','')<>'' AND v.source=e.source AND v.kind='ORDER_PAID'
   AND v.order_id=e.reference->>'orderId'
   AND (coalesce(e.reference->>'lineId','')='' OR v.line_id=e.reference->>'lineId')
   AND (coalesce(e.reference->>'recordId','')='' OR strpos(v.record_key,':')>0
    AND substr(v.record_key,strpos(v.record_key,':')+1)=e.reference->>'recordId')
   AND (v.fact->>'domain',v.fact->>'sourceKey')=(e.fact->>'domain',e.fact->>'sourceKey')
   AND v.occurred_at<=e.occurred_at AND ` + catalogActivityLatest + `
 ) p ON true
), mapped AS MATERIALIZED (
 SELECT v.id,v.occurred_at,c.source_version_id AS classified,
  CASE WHEN frozen.payment_version_id IS NOT NULL THEN 1 ELSE m.matches END AS matches,
  coalesce(frozen.sku_code,m.sku) AS sku,
  CASE WHEN jsonb_typeof(coalesce(frozen.amount,v.fact->'amountMinor'))='number'
   THEN (coalesce(frozen.amount,v.fact->'amountMinor')#>>'{}')::bigint END AS amount,
  v.fact->>'paymentId' AS identity,frozen.payment_version_id,frozen.period_id,frozen.group_key,
  frozen.payment_version_id IS NOT NULL AND frozen.amount IS DISTINCT FROM v.fact->'amountMinor' AS amount_changed
 FROM (SELECT DISTINCT payment_id FROM origins WHERE payment_id IS NOT NULL) ids
 JOIN source_order_versions v ON v.id=ids.payment_id
 LEFT JOIN source_order_classifications c ON c.source_version_id=v.id
 LEFT JOIN LATERAL (
  SELECT count(*) AS matches,min(m.sku_code) AS sku FROM routes m
  WHERE ` + catalogRouteFactSQL + `
   AND (m.entity_type,m.business_key,m.classifier)=(c.entity_type,c.business_key,c.classifier)
   AND m.effective_from<=v.occurred_at AND (m.effective_to IS NULL OR v.occurred_at<m.effective_to)
 ) m ON true
 LEFT JOIN LATERAL (
  SELECT a.payment_version_id,a.sku_code,a.period_id,a.group_key,original.fact->'amountMinor' AS amount
  FROM source_order_versions original JOIN catalog_aggregate_draft_events a
   ON original.id=a.source_version_id AND a.source_version_id=a.payment_version_id
  JOIN settlements closed USING(period_id) JOIN periods prior ON prior.id=closed.period_id
  JOIN draft_scope draft ON draft.aggregate_package_id=prior.aggregate_package_id
  WHERE (original.source,original.order_id,original.line_id,original.record_key,original.kind)=
   (v.source,v.order_id,v.line_id,v.record_key,v.kind)
  ORDER BY closed.id LIMIT 1
 ) frozen ON true
), assessed AS (
 SELECT e.*,CASE WHEN p.matches=1 THEN p.sku END AS sku,p.occurred_at AS paid_at,p.amount AS original_amount,
  p.payment_version_id AS frozen_payment_id,p.period_id AS frozen_period_id,p.group_key AS frozen_group_key,
  CASE
   WHEN e.kind='REFUND_ADJUSTED' OR e.booked_changed THEN 'REFUND_ADJUSTMENT'
   WHEN coalesce(e.event_id,'')='' OR e.amount IS NULL OR e.amount<0 THEN 'INVALID_EVENT'
   WHEN e.copies>1 THEN 'DUPLICATE_EVENT'
   WHEN p.id IS NULL THEN 'REFUND_ORIGIN_MISSING'
   WHEN p.amount_changed THEN 'AGGREGATE_ORIGINAL_AMOUNT_CHANGED'
   WHEN p.classified IS NULL AND p.payment_version_id IS NULL THEN 'UNCLASSIFIED'
   WHEN p.matches=0 THEN 'MAPPING_MISSING'
   WHEN p.matches>1 THEN 'MAPPING_CONFLICT'
   WHEN coalesce(p.identity,'')='' OR p.amount IS NULL OR p.amount<0 THEN 'INVALID_PAYMENT'
   ELSE '' END AS issue
 FROM origins e LEFT JOIN mapped p ON p.id=e.payment_id
 WHERE (p.matches IS DISTINCT FROM 1 OR p.sku=ANY($2) OR p.payment_version_id IS NOT NULL)
  AND ($7::timestamptz IS NULL OR p.occurred_at IS NULL OR p.occurred_at<$7 OR p.payment_version_id IS NOT NULL)
), capped AS (
 SELECT *,sum(amount) FILTER(WHERE kind='REFUND_SUCCEEDED' AND issue='') OVER (PARTITION BY payment_id) AS linked_refunds
 FROM assessed
)
SELECT id,coalesce(sku,''),kind,coalesce(amount,0),coalesce(paid_at<$3,false),
 coalesce(frozen_payment_id,payment_id,0),coalesce(paid_at,occurred_at),coalesce(frozen_period_id,0),coalesce(frozen_group_key,''),
 CASE WHEN issue='' AND kind='REFUND_SUCCEEDED' AND linked_refunds>original_amount THEN 'REFUND_EXCEEDS_PAYMENT' ELSE issue END
FROM capped WHERE in_scope LIMIT $5`

const catalogActivityMappedSQL = `WITH routes AS (` + catalogAggregateRoutes + `)
 SELECT DISTINCT sku_code FROM routes
 WHERE sku_code=ANY($2) AND effective_from<$4 AND (effective_to IS NULL OR $3<effective_to)`

const catalogActivityCoverageSQL = `WITH routes AS (` + catalogAggregateRoutes + `),
 p AS (SELECT $4::timestamptz AS from_time,$5::timestamptz AS through_time), required AS (
 SELECT DISTINCT CASE domain WHEN 'VIP' THEN 'VIP_ORDERS' WHEN 'HEALTH_IDENTITY' THEN 'HEALTH_IDENTITY_ORDERS' ELSE '' END AS stream
 FROM routes CROSS JOIN p WHERE sku_code=ANY($6) AND effective_from<p.through_time
  AND (effective_to IS NULL OR p.from_time<effective_to)
 )
SELECT r.stream,` + settlementSourceStatusSQL + `,s.initial_from,s.completed_through,s.last_success_at
 FROM p CROSS JOIN required r JOIN source_write_state ws ON ws.source='DCAI'
 LEFT JOIN dcai_order_sync s ON s.stream=r.stream LEFT JOIN dcai_sync_jobs j ON j.stream=r.stream ORDER BY r.stream`

func (s workspace) getCatalogPeriodActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var result catalogPeriodActivity
	err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rule, err := readCatalogPeriodVersion(ctx, tx, r.URL.Query().Get("ruleId"))
		if err != nil {
			return err
		}
		if actor := financeUser(ctx); actor != nil && (!actor.Workspace || !actor.permitsRule(rule.ProductID, false)) {
			return errFinanceForbidden
		}
		definition, err := parseCatalogPeriodRule(rule.raw)
		if err != nil {
			return err
		}
		rule.Cycle = definition.Cycle
		day := 1
		if definition.Cycle == "MONTHLY_25_TO_24" {
			day = 25
		}
		window, err := calendarWindow(r.URL.Query().Get("month"), day)
		if err != nil {
			return err
		}
		result, err = readCatalogPeriodActivity(ctx, tx, catalogPeriodActivityRequest{ProductID: rule.ProductID,
			Scenarios: definition.Scenarios, Window: window, Provider: s.dcaiSourceKey(), MaxAge: s.settlementSyncMaxAge, GroupRule: &rule,
			TrialPaused: r.URL.Query().Get("trialPaused") == "1"})
		return err
	})
	respond(w, result, err)
}

func readCatalogPeriodActivity(ctx context.Context, tx pgx.Tx, in catalogPeriodActivityRequest) (catalogPeriodActivity, error) {
	var rules []catalogAggregateRule
	groupPayments := in.GroupRule != nil && (!in.GroupRule.Paused || in.TrialPaused)
	if groupPayments {
		var err error
		rules, err = readCatalogAggregateVersions(ctx, tx, in.GroupRule.packageID, in.Window.Through, in.GroupRule.Cycle)
		if err != nil {
			return catalogPeriodActivity{}, err
		}
		in.Scenarios = slices.Clone(in.Scenarios)
		for _, rule := range rules {
			for _, code := range rule.Definition.Scenarios {
				if !slices.Contains(in.Scenarios, code) {
					in.Scenarios = append(in.Scenarios, code)
				}
			}
		}
	}
	result := catalogPeriodActivity{Start: in.Window.Start, Through: in.Window.Through,
		Scenarios: make([]catalogPeriodScenarioActivity, len(in.Scenarios)), Sources: []settlementSource{}, Issues: make(map[string]int)}
	byCode := make(map[string]*catalogPeriodScenarioActivity, len(in.Scenarios))
	for i, code := range in.Scenarios {
		result.Scenarios[i].Code = code
		byCode[code] = &result.Scenarios[i]
	}
	rows, err := tx.Query(ctx, catalogActivityMappedSQL, in.ProductID, in.Scenarios, in.Window.Start, in.Window.Through)
	if err != nil {
		return result, err
	}
	var code string
	if _, err := pgx.ForEachRow(rows, []any{&code}, func() error {
		byCode[code].Mapped = true
		return nil
	}); err != nil {
		return result, err
	}
	var retiredFrom *time.Time
	if in.GroupRule != nil {
		retiredFrom = in.GroupRule.retiredFrom
	}
	rows, err = tx.Query(ctx, catalogActivityAmountsSQL, in.ProductID, in.Scenarios, in.Window.Start, in.Window.Through,
		maxDraftPayments+1, in.DraftPeriodID, retiredFrom)
	if err != nil {
		return result, err
	}
	var kind, issue string
	var amount int64
	var prior bool
	var eventID, paymentID int64
	var paidAt time.Time
	var frozen catalogFrozenGroupReference
	var events []catalogActivityEvent
	selections := make(map[int64]*catalogAggregateMatch)
	firstPayment := in.Window.Start
	seen := 0
	_, err = pgx.ForEachRow(rows, []any{&eventID, &code, &kind, &amount, &prior, &paymentID, &paidAt, &frozen.PeriodID, &frozen.Key, &issue}, func() error {
		seen++
		if seen > maxDraftPayments {
			return errDraftLimit
		}
		event := catalogActivityEvent{PaymentID: paymentID, PaidAt: paidAt, Scenario: code,
			Amount: amount, Refund: kind != "ORDER_PAID", frozen: frozen}
		if in.DraftPeriodID != 0 {
			event.EventID = eventID
		}
		if groupPayments && paymentID != 0 && code != "" && frozen.PeriodID == 0 {
			selected := selections[paymentID]
			if selected == nil {
				newMatch := matchCatalogAggregateRule(rules, event)
				selected = &newMatch
				selections[paymentID] = selected
			}
			if selected.excluded {
				return nil
			}
			event.selection = selected
		}
		if issue != "" {
			result.Issues[issue]++
			if in.DraftPeriodID != 0 {
				result.postingIssues = append(result.postingIssues, draftIssue{FactID: eventID, Code: issue})
			}
			return nil
		}
		item := byCode[code]
		if item == nil && frozen.PeriodID != 0 {
			result.Scenarios = append(result.Scenarios, catalogPeriodScenarioActivity{Code: code, Mapped: true})
			for i := range result.Scenarios {
				byCode[result.Scenarios[i].Code] = &result.Scenarios[i]
			}
			item = byCode[code]
		}
		if item == nil {
			return errProductConfigConflict
		}
		if groupPayments {
			events = append(events, event)
			if paidAt.Before(firstPayment) {
				firstPayment = paidAt
			}
		}
		if kind == "ORDER_PAID" {
			item.Payments++
			return addCatalogActivityAmount(amount, &item.ReceiptsMinor, &result.Receipts)
		}
		item.Refunds++
		if err := addCatalogActivityAmount(amount, &item.RefundsMinor, &result.Refunds); err != nil {
			return err
		}
		if prior {
			return addCatalogActivityAmount(amount, &item.PriorRefundsMinor, &result.PriorPeriodRefunds)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if in.GroupRule != nil {
		if !groupPayments {
			result.Grouping = &catalogPeriodGroupReport{Paused: true, Groups: []catalogPeriodGroup{}, Issues: map[string]int{}}
		} else {
			directory, err := readCatalogAggregateDirectory(ctx, tx, rules, firstPayment, in.Window.Through)
			if err != nil {
				return result, err
			}
			original, err := readFrozenCatalogAggregateGroups(ctx, tx, events)
			if err != nil {
				return result, err
			}
			grouping, err := groupCatalogPeriodEvents(events, rules, directory, original)
			if err != nil {
				return result, err
			}
			grouping.Paused, grouping.TrialOnPaused = in.GroupRule.Paused, in.GroupRule.Paused && in.TrialPaused
			result.Grouping = &grouping
		}
	}
	if in.MaxAge == 0 {
		in.MaxAge = defaultSettlementSyncMaxAge
	}
	rows, err = tx.Query(ctx, catalogActivityCoverageSQL, in.ProductID, in.Provider, in.MaxAge,
		in.Window.Start, in.Window.Through, in.Scenarios)
	if err != nil {
		return result, err
	}
	var source settlementSource
	_, err = pgx.ForEachRow(rows, []any{&source.Stream, &source.Status, &source.From, &source.Through, &source.LastSync}, func() error {
		result.Sources = append(result.Sources, source)
		return nil
	})
	return result, err
}

func addCatalogActivityAmount(amount int64, sums ...*int64) error {
	for _, sum := range sums {
		next, err := addMinor(*sum, amount)
		if err != nil {
			return err
		}
		*sum = next
	}
	return nil
}
