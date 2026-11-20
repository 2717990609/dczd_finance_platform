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

type catalogActivityRecognitionWindow struct {
	From      time.Time  `json:"from_at"`
	Through   *time.Time `json:"through_at"`
	Scenarios []string   `json:"scenarios"`
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
draft_scope AS (
 SELECT id AS period_id,aggregate_package_id,starts_on,ends_on FROM periods WHERE id=$6::bigint
 UNION ALL
 SELECT opened.id,preview.aggregate_package_id,preview.starts_on,preview.ends_on
 FROM (SELECT NULLIF($8,'')::uuid AS aggregate_package_id,($3::timestamptz AT TIME ZONE 'Asia/Shanghai')::date AS starts_on,
  ($4::timestamptz AT TIME ZONE 'Asia/Shanghai')::date-1 AS ends_on WHERE $6=0 AND $8<>'') preview
 LEFT JOIN periods opened ON opened.aggregate_package_id=preview.aggregate_package_id
  AND (opened.starts_on,opened.ends_on)=(preview.starts_on,preview.ends_on)
),
closed_windows AS MATERIALIZED (
 SELECT p.id,p.starts_on,p.ends_on FROM draft_scope draft JOIN periods p USING(aggregate_package_id)
 JOIN settlements final ON final.period_id=p.id
),
recognition_rules AS MATERIALIZED (
 SELECT * FROM jsonb_to_recordset($9::jsonb) AS r(from_at timestamptz,through_at timestamptz,scenarios text[])
), completion_windows AS (
 SELECT $3::timestamptz AS from_at,$4::timestamptz AS through_at
 WHERE EXISTS(SELECT 1 FROM recognition_rules)
 UNION ALL
 SELECT prior.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai',(prior.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'
 FROM closed_windows prior CROSS JOIN draft_scope draft
 WHERE prior.ends_on<draft.starts_on AND EXISTS(SELECT 1 FROM recognition_rules)
  AND NOT EXISTS(SELECT 1 FROM closed_windows closed WHERE closed.starts_on=draft.starts_on)
  AND draft.period_id IS NOT NULL
  AND NOT EXISTS (
   SELECT 1 FROM periods earlier WHERE earlier.aggregate_package_id=draft.aggregate_package_id
    AND earlier.starts_on>prior.ends_on AND earlier.starts_on<draft.starts_on
    AND NOT EXISTS(SELECT 1 FROM settlements final WHERE final.period_id=earlier.id)
  )
), due_payments AS MATERIALIZED (
 SELECT DISTINCT v.id,v.order_id,b.source_row_id,completed.occurred_at AS completed_at
 FROM completion_windows w JOIN source_order_versions completed ON completed.source='DCAI' AND completed.kind='ORDER_COMPLETED'
  AND completed.occurred_at>=w.from_at AND completed.occurred_at<w.through_at AND completed.occurred_at<=CURRENT_TIMESTAMP
 JOIN dcai_order_blockers b ON b.stream='HEALTH_IDENTITY_ORDERS' AND b.completion_version_id=completed.id AND b.order_id=completed.order_id
 JOIN source_order_versions v ON v.source=completed.source AND v.order_id=b.order_id AND v.kind='ORDER_PAID'
  AND v.line_id='' AND v.record_key='ai_health_ambassador_order:'||b.source_row_id AND v.occurred_at<=completed.occurred_at
  AND v.fact->>'sourceKey'=completed.fact->>'sourceKey'
 JOIN source_order_classifications c ON c.source_version_id=v.id
 WHERE EXISTS(
  SELECT 1 FROM recognition_rules rule JOIN routes m ON m.sku_code=ANY(rule.scenarios)
  WHERE m.domain='HEALTH_IDENTITY' AND m.source_product_code='HEALTH_CREATOR' AND ` + catalogRouteFactSQL + `
   AND (m.entity_type,m.business_key,m.classifier)=(c.entity_type,c.business_key,c.classifier)
   AND m.effective_from<=v.occurred_at AND (m.effective_to IS NULL OR v.occurred_at<m.effective_to)
   AND rule.from_at<=v.occurred_at AND (rule.through_at IS NULL OR v.occurred_at<rule.through_at)
 ) AND ` + catalogActivityLatest + `
 AND (w.from_at>=$3 OR NOT EXISTS(
  SELECT 1 FROM catalog_aggregate_draft_events booked JOIN settlements final USING(period_id)
  JOIN periods prior ON prior.id=booked.period_id
  WHERE prior.aggregate_package_id=NULLIF($8,'')::uuid AND booked.completion_version_id=completed.id
   AND booked.source_version_id=booked.payment_version_id
 ))
), sealed_events AS MATERIALIZED (
 SELECT a.source_version_id FROM closed_windows prior JOIN draft_scope draft USING(starts_on,ends_on)
 JOIN catalog_aggregate_draft_events a ON a.period_id=prior.id AND a.completion_version_id IS NOT NULL
 WHERE EXISTS(SELECT 1 FROM recognition_rules)
), deferred_events AS (
 SELECT id FROM due_payments
 UNION
 SELECT v.id FROM due_payments due JOIN source_order_versions v
  ON v.source='DCAI' AND v.kind='REFUND_SUCCEEDED' AND v.fact#>>'{refundsPayment,0,orderId}'=due.order_id
   AND (v.fact->>'domain',v.fact->>'sourceKey')=('HEALTH_IDENTITY','HEALTH_CREATOR')
   AND (coalesce(v.fact#>>'{refundsPayment,0,recordId}','')='' OR v.fact#>>'{refundsPayment,0,recordId}'=due.source_row_id)
   AND v.occurred_at<due.completed_at
 WHERE ` + catalogActivityLatest + `
 UNION SELECT source_version_id FROM sealed_events
), dated_events AS MATERIALIZED (
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
   WHERE (booked.reserved_period=draft.period_id OR (booked.reserved_period IS NULL AND
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
), current_events AS MATERIALIZED (
 SELECT incoming.* FROM dated_events incoming
 WHERE NOT EXISTS(
  SELECT 1 FROM sealed_events saved JOIN source_order_versions original ON original.id=saved.source_version_id
  JOIN source_order_versions current ON current.id=incoming.id
  WHERE (original.source,original.order_id,original.line_id,original.record_key,original.kind)=
   (current.source,current.order_id,current.line_id,current.record_key,current.kind)
 )
 UNION ALL
 SELECT ` + catalogActivityEventColumns + `,true,false
 FROM deferred_events due JOIN source_order_versions v USING(id)
 WHERE NOT EXISTS(SELECT 1 FROM dated_events incoming WHERE incoming.id=due.id)
  OR due.id IN (SELECT source_version_id FROM sealed_events)
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
  recognition.required,completed.occurred_at AS completed_at,
  CASE WHEN frozen.payment_version_id IS NOT NULL THEN frozen.completion_version_id ELSE completed.id END AS completion_version_id,
  coalesce(frozen.completion_at,CASE WHEN frozen.payment_version_id IS NULL THEN completed.occurred_at END,v.occurred_at) AS recognized_at,
  frozen.payment_version_id IS NOT NULL AND frozen.amount IS DISTINCT FROM v.fact->'amountMinor' AS amount_changed
 FROM (SELECT DISTINCT payment_id FROM origins WHERE payment_id IS NOT NULL) ids
 JOIN source_order_versions v ON v.id=ids.payment_id
 LEFT JOIN source_order_classifications c ON c.source_version_id=v.id
 LEFT JOIN LATERAL (
  SELECT count(*) AS matches,min(m.sku_code) AS sku,
   bool_and(m.domain='HEALTH_IDENTITY' AND m.source_product_code='HEALTH_CREATOR') AS creator FROM routes m
  WHERE ` + catalogRouteFactSQL + `
   AND (m.entity_type,m.business_key,m.classifier)=(c.entity_type,c.business_key,c.classifier)
   AND m.effective_from<=v.occurred_at AND (m.effective_to IS NULL OR v.occurred_at<m.effective_to)
 ) m ON true
 CROSS JOIN LATERAL (
  SELECT coalesce(m.matches=1 AND m.creator,false) AND EXISTS(
   SELECT 1 FROM recognition_rules rule WHERE m.sku=ANY(rule.scenarios)
    AND rule.from_at<=v.occurred_at AND (rule.through_at IS NULL OR v.occurred_at<rule.through_at)
  ) AS required
 ) recognition
 LEFT JOIN dcai_order_blockers b ON recognition.required AND b.stream='HEALTH_IDENTITY_ORDERS' AND b.order_id=v.order_id
  AND v.line_id='' AND b.source_row_id=split_part(v.record_key,':',2)
 LEFT JOIN source_order_versions completed ON completed.id=b.completion_version_id AND completed.source=v.source
  AND completed.order_id=v.order_id AND completed.kind='ORDER_COMPLETED' AND completed.fact->>'sourceKey'=v.fact->>'sourceKey'
  AND completed.occurred_at>=v.occurred_at AND completed.occurred_at<=CURRENT_TIMESTAMP
 LEFT JOIN LATERAL (
  SELECT a.payment_version_id,a.sku_code,a.period_id,a.group_key,original.fact->'amountMinor' AS amount,
   a.completion_version_id,saved_completion.occurred_at AS completion_at
  FROM source_order_versions original JOIN catalog_aggregate_draft_events a
   ON original.id=a.source_version_id AND a.source_version_id=a.payment_version_id
  JOIN settlements closed USING(period_id) JOIN periods prior ON prior.id=closed.period_id
  JOIN draft_scope draft ON draft.aggregate_package_id=prior.aggregate_package_id
  LEFT JOIN source_order_versions saved_completion ON saved_completion.id=a.completion_version_id
  WHERE (original.source,original.order_id,original.line_id,original.record_key,original.kind)=
   (v.source,v.order_id,v.line_id,v.record_key,v.kind)
  ORDER BY closed.id LIMIT 1
 ) frozen ON true
), assessed AS (
 SELECT e.*,CASE WHEN p.matches=1 THEN p.sku END AS sku,p.occurred_at AS paid_at,p.amount AS original_amount,
  p.recognized_at,p.completion_version_id,
  p.payment_version_id AS frozen_payment_id,p.period_id AS frozen_period_id,p.group_key AS frozen_group_key,
  p.payment_version_id IS NULL AND due.id IS NULL AND EXISTS(
   SELECT 1 FROM closed_windows prior
   WHERE (p.recognized_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN prior.starts_on AND prior.ends_on
  ) AS origin_closed,
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
 FROM origins e LEFT JOIN mapped p ON p.id=e.payment_id LEFT JOIN due_payments due ON due.id=p.id
 WHERE (p.matches IS DISTINCT FROM 1 OR p.sku=ANY($2) OR p.payment_version_id IS NOT NULL)
  AND ($7::timestamptz IS NULL OR p.occurred_at IS NULL OR p.occurred_at<$7 OR p.payment_version_id IS NOT NULL)
  AND (p.required IS DISTINCT FROM true OR CASE WHEN p.payment_version_id IS NOT NULL THEN
    e.id IN (SELECT source_version_id FROM sealed_events)
    OR e.kind<>'ORDER_PAID' AND (NOT e.in_scope OR e.id IN (SELECT id FROM dated_events))
   ELSE p.completed_at IS NOT NULL AND (NOT e.in_scope OR e.kind<>'ORDER_PAID' AND e.occurred_at>=p.completed_at
    OR due.id IS NOT NULL) END)
), capped AS (
 SELECT *,sum(amount) FILTER(WHERE kind='REFUND_SUCCEEDED' AND issue='') OVER (PARTITION BY payment_id) AS linked_refunds
 FROM assessed
)
SELECT id,coalesce(sku,''),kind,coalesce(amount,0),
 coalesce(frozen_payment_id,payment_id,0),coalesce(completion_version_id,0),coalesce(paid_at,occurred_at),coalesce(recognized_at,occurred_at),
 coalesce(frozen_period_id,0),coalesce(frozen_group_key,''),coalesce(origin_closed,false),
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
	recognition := make([]catalogActivityRecognitionWindow, 0)
	groupPayments := in.GroupRule != nil && (!in.GroupRule.Paused || in.TrialPaused)
	if groupPayments {
		var err error
		rules, err = readCatalogAggregateVersions(ctx, tx, in.GroupRule.packageID, in.Window.Through, in.GroupRule.Cycle)
		if err != nil {
			return catalogPeriodActivity{}, err
		}
		in.Scenarios = slices.Clone(in.Scenarios)
		for _, rule := range rules {
			if rule.Definition.ProductRecognition {
				recognition = append(recognition, catalogActivityRecognitionWindow{From: rule.From, Through: rule.Through, Scenarios: rule.Definition.Scenarios})
			}
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
	var packageID string
	if in.GroupRule != nil {
		retiredFrom = in.GroupRule.retiredFrom
		packageID = in.GroupRule.packageID
	}
	rows, err = tx.Query(ctx, catalogActivityAmountsSQL, in.ProductID, in.Scenarios, in.Window.Start, in.Window.Through,
		maxDraftPayments+1, in.DraftPeriodID, retiredFrom, packageID, recognition)
	if err != nil {
		return result, err
	}
	var kind, issue string
	var amount int64
	var originClosed bool
	var eventID, paymentID, completionVersionID int64
	var paidAt, recognizedAt time.Time
	var frozen catalogFrozenGroupReference
	var events []catalogActivityEvent
	selections := make(map[int64]*catalogAggregateMatch)
	firstPayment := in.Window.Start
	seen := 0
	_, err = pgx.ForEachRow(rows, []any{&eventID, &code, &kind, &amount, &paymentID, &completionVersionID, &paidAt, &recognizedAt,
		&frozen.PeriodID, &frozen.Key, &originClosed, &issue}, func() error {
		seen++
		if seen > maxDraftPayments {
			return errDraftLimit
		}
		event := catalogActivityEvent{PaymentID: paymentID, CompletionVersionID: completionVersionID, PaidAt: paidAt,
			RecognizedAt: recognizedAt, Scenario: code,
			Amount: amount, Refund: kind != "ORDER_PAID", frozen: frozen, originClosed: originClosed}
		if in.DraftPeriodID != 0 {
			event.EventID = eventID
		}
		if groupPayments && paymentID != 0 && frozen.PeriodID == 0 && in.GroupRule.ResumeFrom != nil && paidAt.Before(*in.GroupRule.ResumeFrom) {
			return nil
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
		events = append(events, event)
		return nil
	})
	if err != nil {
		return result, err
	}
	var performance catalogPerformanceDirectory
	var decisions map[int64]catalogPerformanceDecision
	if groupPayments && slices.ContainsFunc(rules, func(rule catalogAggregateRule) bool { return rule.Definition.Performance.active() }) {
		performance, err = readCatalogPerformanceDirectory(ctx, tx, events)
		if err != nil {
			return result, err
		}
		decisions = make(map[int64]catalogPerformanceDecision)
	}
	selected := events[:0]
	for _, event := range events {
		if decisions != nil {
			decision, known := decisions[event.PaymentID]
			if !known {
				decision = performance.match(event)
				decisions[event.PaymentID] = decision
			}
			if decision.excluded {
				continue
			}
			if decision.issue != "" {
				result.Issues[decision.issue]++
				if in.DraftPeriodID != 0 {
					result.postingIssues = append(result.postingIssues, draftIssue{FactID: event.EventID, Code: decision.issue})
				}
				continue
			}
		}
		item := byCode[event.Scenario]
		if item == nil && event.frozen.PeriodID != 0 {
			result.Scenarios = append(result.Scenarios, catalogPeriodScenarioActivity{Code: event.Scenario, Mapped: true})
			for i := range result.Scenarios {
				byCode[result.Scenarios[i].Code] = &result.Scenarios[i]
			}
			item = byCode[event.Scenario]
		}
		if item == nil {
			return result, errProductConfigConflict
		}
		if groupPayments {
			selected = append(selected, event)
			if event.PaidAt.Before(firstPayment) {
				firstPayment = event.PaidAt
			}
		}
		if !event.Refund {
			item.Payments++
			err = addCatalogActivityAmount(event.Amount, &item.ReceiptsMinor, &result.Receipts)
		} else {
			item.Refunds++
			err = addCatalogActivityAmount(event.Amount, &item.RefundsMinor, &result.Refunds)
			if err == nil && event.RecognizedAt.Before(in.Window.Start) {
				err = addCatalogActivityAmount(event.Amount, &item.PriorRefundsMinor, &result.PriorPeriodRefunds)
			}
		}
		if err != nil {
			return result, err
		}
	}
	if in.GroupRule != nil {
		if !groupPayments {
			result.Grouping = &catalogPeriodGroupReport{Paused: true, Groups: []catalogPeriodGroup{}, Issues: map[string]int{}}
		} else {
			directory, err := readCatalogAggregateDirectory(ctx, tx, rules, firstPayment, in.Window.Through)
			if err != nil {
				return result, err
			}
			original, err := readFrozenCatalogAggregateGroups(ctx, tx, selected, in.DraftPeriodID == 0)
			if err != nil {
				return result, err
			}
			grouping, err := groupCatalogPeriodEvents(selected, rules, directory, original)
			if err != nil {
				return result, err
			}
			resumed := in.GroupRule.ResumeFrom != nil && in.GroupRule.ResumeFrom.Equal(in.Window.Start)
			if resumed || slices.ContainsFunc(rules, func(rule catalogAggregateRule) bool { return rule.From.Before(in.Window.Start) }) {
				if err := readCatalogAggregateCarry(ctx, tx, &grouping, packageID, in.Window.Start, in.DraftPeriodID == 0, resumed); err != nil {
					return result, err
				}
			}
			grouping.Paused, grouping.TrialOnPaused = in.GroupRule.Paused, in.GroupRule.Paused && in.TrialPaused
			result.Grouping = &grouping
		}
	}
	if in.MaxAge == 0 {
		in.MaxAge = defaultSettlementSyncMaxAge
	}
	rows, err = tx.Query(ctx, catalogActivityCoverageSQL, in.ProductID, in.Provider, in.MaxAge,
		firstPayment, in.Window.Through, in.Scenarios)
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
