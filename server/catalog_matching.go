package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const catalogRulePriority = `CASE WHEN jsonb_typeof(v.definition->'policy_scope'->'priority')='number'
 THEN (v.definition->'policy_scope'->>'priority')::numeric ELSE 0 END`

const catalogRuleEligible = `p.policy_kind='COMPONENT_RULE' AND p.scope_type='BUSINESS_SCENARIO' AND NOT p.paused
 AND v.source_status='PUBLISHED' AND v.definition->>'schema_version' IN ('1.1','1.2','1.3')
 AND v.definition->>'recognition_trigger'='PAID'
 AND v.definition->'policy_scope'->'financial_event_types' ? 'order.paid'`

const catalogRuleOrder = `priority DESC, (published_at IS NOT NULL) DESC,
 CASE WHEN published_at IS NOT NULL THEN effective_from END DESC NULLS LAST`

const catalogVersionVisible = `(v.observed_at=scope.captured_at OR v.published_at IS NOT NULL)`

const catalogBookedPaymentSQL = `SELECT 1 FROM source_order_versions booked
 JOIN calculation_draft_performance a ON a.source_version_id=booked.id AND a.payment_version_id IS NULL
 JOIN settlements st ON st.period_id=a.period_id
 WHERE booked.kind='ORDER_PAID' AND booked.source=f.source
  AND ((booked.order_id,booked.line_id,booked.record_key)=(f.order_id,f.line_id,f.record_key)
   OR ((booked.fact->>'domain'),(booked.fact->>'paymentId'))=(f.domain,f.payment_id))`

const catalogHealthRecognitionPlan = `v.definition->>'schema_version' IN ('1.2','1.3')
 AND CASE WHEN jsonb_typeof(v.definition->'recognition_plan')='object' THEN
  (v.definition->'recognition_plan')-'required_event_types'='{"mode":"ALL_OF","contract_version":"finance.compound-recognition/1.0"}'::jsonb
  AND v.definition->'recognition_plan'->'required_event_types' IN ('["ORDER_COMPLETED","ORDER_PAID"]'::jsonb,'["ORDER_PAID","ORDER_COMPLETED"]'::jsonb)
 ELSE false END
 AND v.definition->'policy_scope'->'financial_event_types' ? 'order.completed'`

const catalogMatchSQL = `WITH period AS (
 SELECT p.product_id,p.starts_on,c.project_code,c.captured_at,
  NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=p.id) AS is_open,
  p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai' AS after,
  (p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai' AS before,
  CASE pro.cycle_start_day WHEN 25 THEN 'MONTHLY_25_TO_24' WHEN 1 THEN 'MONTHLY' END AS cycle
 FROM periods p JOIN products pro ON pro.id=p.product_id
 JOIN product_catalog_settings c ON c.product_id=p.product_id
 WHERE p.id=$1 AND pro.period_mode='calendar' AND p.aggregate_package_id IS NULL
), routes AS MATERIALIZED (
 SELECT m.* FROM catalog_business_routes m JOIN period p USING(product_id) WHERE m.observed_at=p.captured_at
), rules AS MATERIALIZED (
 SELECT v.source_id,v.effective_from,` + catalogRuleExecutionThrough + ` AS effective_to,v.version_no,v.published_at,p.name,p.scope_id,p.resume_from,
 v.definition->'policy_scope'->'sales_systems' AS sales_systems,
 v.definition->'policy_scope'->'business_sku_codes' AS skus,
 v.definition->'policy_scope'->'vip_new_membership_only'='true'::jsonb AND NOT jsonb_path_exists(v.definition,
  '$.benefit_actions[*].vip_membership_new_only ? (@ == true || @ == false)') AS new_membership_only,
 v.definition->>'recognition_plan' IS NOT NULL AS compound_recognition,
 coalesce((` + catalogHealthRecognitionPlan + `),false) AS health_recognition,
 ` + catalogRulePriority + ` AS priority
 FROM period scope JOIN product_rule_packages p ON p.product_id=scope.product_id AND p.project_code=scope.project_code
 JOIN product_rule_versions v ON v.package_id=p.source_id AND ` + catalogVersionVisible + `
 WHERE ` + catalogRuleEligible + ` AND v.effective_from<scope.before
  AND (` + catalogRuleExecutionThrough + ` IS NULL OR scope.after<` + catalogRuleExecutionThrough + ` OR EXISTS(
   SELECT 1 FROM routes m WHERE m.source='DCAI' AND m.domain='HEALTH_IDENTITY'
  ))
  AND v.definition->'settlement_policy'->>'cycle'=scope.cycle
), completion_windows AS (
 SELECT after,before,false AS carried FROM period
 UNION ALL
 SELECT origin.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai',
  (origin.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai',true
 FROM period scope JOIN periods origin ON origin.product_id=scope.product_id AND origin.ends_on<scope.starts_on AND origin.aggregate_package_id IS NULL
 JOIN settlements closed ON closed.period_id=origin.id
 WHERE scope.is_open AND NOT EXISTS (
  SELECT 1 FROM periods earlier WHERE earlier.product_id=scope.product_id AND earlier.aggregate_package_id IS NULL
   AND earlier.starts_on>origin.ends_on AND earlier.starts_on<scope.starts_on
   AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=earlier.id)
 )
), completion_targets AS MATERIALIZED (
 SELECT b.order_id,b.source_row_id,w.carried FROM completion_windows w
 JOIN source_order_versions completed ON completed.source='DCAI' AND completed.kind='ORDER_COMPLETED'
  AND completed.occurred_at>=w.after AND completed.occurred_at<w.before
 JOIN dcai_order_blockers b ON b.stream='HEALTH_IDENTITY_ORDERS' AND b.completion_version_id=completed.id
 WHERE EXISTS (SELECT 1 FROM rules WHERE health_recognition)
  AND (NOT w.carried OR NOT EXISTS (
   SELECT 1 FROM calculation_draft_performance booked JOIN settlements final ON final.period_id=booked.period_id
   WHERE booked.completion_version_id=completed.id
  ))
), candidates AS (
 SELECT v.id,false AS completion_due FROM period scope JOIN source_order_versions v ON v.kind='ORDER_PAID'
  AND v.occurred_at>=scope.after AND v.occurred_at<scope.before
 WHERE EXISTS (SELECT 1 FROM routes m WHERE ` + catalogRouteFactSQL + `)
 UNION ALL
 SELECT v.id,true FROM period scope CROSS JOIN completion_targets target
 JOIN source_order_versions v ON v.source='DCAI' AND v.order_id=target.order_id AND v.kind='ORDER_PAID'
  AND v.line_id='' AND v.record_key='ai_health_ambassador_order:'||target.source_row_id AND v.occurred_at<scope.before
 WHERE EXISTS (SELECT 1 FROM routes m WHERE m.source='DCAI' AND m.domain='HEALTH_IDENTITY' AND ` + catalogRouteFactSQL + `)
  AND (NOT target.carried OR v.fact->'amountMinor' IS DISTINCT FROM '0'::jsonb)
), candidate_ids AS (
 SELECT id,bool_or(completion_due) AS completion_due FROM candidates GROUP BY id
), payments AS (
 SELECT v.id,v.source,v.order_id,v.occurred_at,v.fact->>'orderNo' AS order_no,
  v.fact->>'transactionType' AS transaction_type,v.fact->>'domain' AS domain,
  v.fact->>'paymentId' AS payment_id,v.line_id,v.record_key,v.fact->>'sourceKey' AS source_product_code,
  CASE WHEN jsonb_typeof(v.fact->'amountMinor')='number' THEN (v.fact->>'amountMinor')::bigint END AS paid,
  split_part(v.record_key,':',1) AS record_table,selected.completion_due,
  count(*) OVER (PARTITION BY v.source,v.fact->>'paymentId') AS payment_count
 FROM candidate_ids selected JOIN source_order_versions v USING(id)
 WHERE NOT EXISTS (
  SELECT 1 FROM source_order_versions newer
  WHERE (newer.source,newer.order_id,newer.line_id,newer.record_key,newer.kind)=(v.source,v.order_id,v.line_id,v.record_key,v.kind)
    AND (newer.updated_at,newer.id)>(v.updated_at,v.id)
 )
), matches AS (
 SELECT f.id AS "sourceVersionId",f.order_id AS "orderId",f.order_no AS "orderNo",f.occurred_at AS "paidAt",
 f.domain AS "sourceDomain",f.transaction_type AS "sourceTransactionType",f.paid AS "paidMinor",f.occurred_at>=scope.after AS "paidInWindow",
 coalesce(f.source='DCAI' AND f.domain='VIP' AND f.record_table='ai_vip_order',false) AS "membershipOrder",
 CASE WHEN r.health_recognition OR f.occurred_at<scope.after THEN completed.id END AS "completionVersionId",
 CASE WHEN r.health_recognition OR f.occurred_at<scope.after THEN completed.occurred_at END AS "completedAt",
 coalesce(f.completion_due AND completed.occurred_at<scope.after,false) AS "completionCarried",
 CASE WHEN prior.refund_valid AND prior.refund_minor<=f.paid THEN f.paid-prior.refund_minor END AS "recognizedMinor",
 CASE WHEN prior.refund_valid AND prior.refund_minor BETWEEN 1 AND f.paid THEN prior.id END AS "priorRefundVersionId",
 CASE WHEN m.n=1 THEN m.label END AS "scenarioLabel",
 CASE WHEN m.n=1 THEN m.sku END AS "scenario",
 CASE WHEN m.n=1 THEN m.version END AS "mappingVersion",
 CASE WHEN r.n=1 THEN r.id END AS "ruleId",
 CASE WHEN r.n=1 THEN r.name END AS "ruleLabel",
 CASE WHEN r.n=1 THEN r.version END AS "ruleVersion",
 CASE WHEN f.paid IS NULL OR f.paid<0 OR coalesce(f.payment_id,'')='' THEN 'INVALID_PAYMENT'
  WHEN f.payment_count>1 THEN 'DUPLICATE_PAYMENT'
  WHEN r.n=1 AND r.health_recognition AND f.source='DCAI' AND f.domain='HEALTH_IDENTITY' AND completed.id IS NULL
   THEN 'COMPOUND_COMPLETION_PENDING'
  WHEN r.n=1 AND r.health_recognition AND f.source='DCAI' AND f.domain='HEALTH_IDENTITY'
   AND NOT f.completion_due THEN 'COMPOUND_COMPLETED_IN_OTHER_PERIOD'
  WHEN booked.other_period THEN 'PAYMENT_ALREADY_SETTLED'
  WHEN c.source_version_id IS NULL THEN 'UNCLASSIFIED'
  WHEN m.n=0 THEN 'MAPPING_MISSING'
  WHEN m.n>1 THEN 'MAPPING_CONFLICT'
  WHEN r.n=0 THEN 'RULE_MISSING'
  WHEN r.n>1 THEN 'RULE_CONFLICT'
  WHEN r.compound_recognition AND NOT (r.health_recognition AND f.source='DCAI' AND f.domain='HEALTH_IDENTITY') THEN 'COMPOUND_RECOGNITION_UNSUPPORTED'
  WHEN prior.id IS NOT NULL AND NOT coalesce(prior.refund_valid AND prior.refund_minor BETWEEN 1 AND f.paid,false)
   THEN 'COMPOUND_PRIOR_REFUND_INVALID'
  WHEN r.new_membership_only AND f.source='DCAI' AND f.domain='VIP' AND f.record_table='ai_vip_order'
   AND coalesce(f.transaction_type,'')<>'NEW' THEN
    CASE WHEN f.transaction_type='RENEWAL' THEN 'MEMBERSHIP_RENEWAL_EXCLUDED' ELSE 'MEMBERSHIP_TYPE_PENDING' END
  ELSE 'MATCHED' END AS status
 FROM payments f CROSS JOIN period scope
 LEFT JOIN source_order_classifications c ON c.source_version_id=f.id
 LEFT JOIN dcai_order_blockers b ON f.source='DCAI' AND f.domain='HEALTH_IDENTITY' AND f.record_table='ai_health_ambassador_order' AND f.line_id=''
  AND b.stream='HEALTH_IDENTITY_ORDERS' AND b.source_row_id=split_part(f.record_key,':',2) AND b.order_id=f.order_id
 LEFT JOIN source_order_versions completed ON completed.id=b.completion_version_id AND completed.source=f.source
  AND completed.order_id=f.order_id AND completed.kind='ORDER_COMPLETED'
  AND completed.fact->>'sourceKey'=f.source_product_code AND completed.occurred_at>=f.occurred_at
 CROSS JOIN LATERAL (SELECT EXISTS(` + catalogBookedPaymentSQL + ` AND a.period_id<>$1) AS other_period) booked
 LEFT JOIN LATERAL (
  SELECT count(*) n,min(sku_code) sku,min(sku_label) label,min(mapping_version) version FROM routes m
  WHERE (m.source,m.domain,m.record_table,m.entity_type,m.business_key,m.classifier)=(f.source,f.domain,f.record_table,c.entity_type,c.business_key,c.classifier)
   AND (m.source_product_code=f.source_product_code OR m.source_product_code IS NULL AND m.domain<>'HEALTH_IDENTITY')
   AND m.effective_from<=f.occurred_at AND (m.effective_to IS NULL OR f.occurred_at<m.effective_to)
 ) m ON true
 LEFT JOIN LATERAL (
  SELECT count(*) n,min(source_id::text) id,min(name) name,min(version_no) version,
   bool_or(compound_recognition) compound_recognition,bool_or(health_recognition) health_recognition,
   bool_or(new_membership_only) new_membership_only FROM (
   SELECT r.source_id,r.name,r.version_no,r.compound_recognition,r.health_recognition,r.new_membership_only FROM rules r
   WHERE m.n=1 AND r.scope_id=m.sku AND r.skus ? m.sku AND r.sales_systems ? f.source
    AND r.effective_from<=f.occurred_at AND (r.effective_to IS NULL OR f.occurred_at<r.effective_to)
    AND (r.resume_from IS NULL OR r.resume_from<=f.occurred_at)
   ORDER BY ` + catalogRuleOrder + ` FETCH FIRST 1 ROW WITH TIES
  ) best
 ) r ON true
 LEFT JOIN LATERAL (
  SELECT refund.id,
   CASE WHEN jsonb_typeof(refund.fact->'amountMinor')='number' THEN (refund.fact->>'amountMinor')::bigint END AS refund_minor,
   refund.occurred_at>=f.occurred_at AND refund.fact->>'sourceKey'=f.source_product_code
    AND coalesce(refund.fact->>'refundId','')<>'' AND refund.fact->'refundsPayment'=jsonb_build_array(jsonb_strip_nulls(
      jsonb_build_object('orderId',f.order_id,'lineId',nullif(f.line_id,''),'recordId',split_part(f.record_key,':',2)))) AS refund_valid
  FROM (
   SELECT v.id,v.fact,v.occurred_at FROM source_order_versions v
   WHERE r.health_recognition AND f.completion_due AND NOT booked.other_period
    AND (v.source,v.order_id,v.line_id,v.record_key,v.kind)=(f.source,f.order_id,f.line_id,f.record_key,'REFUND_SUCCEEDED')
   ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
  ) refund WHERE refund.occurred_at<completed.occurred_at OR (
   completed.occurred_at<scope.after AND refund.occurred_at<scope.after AND EXISTS (
    SELECT 1 FROM periods refund_period JOIN settlements closed ON closed.period_id=refund_period.id
    WHERE refund_period.product_id=scope.product_id
     AND (refund.occurred_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN refund_period.starts_on AND refund_period.ends_on
   )
  )
 ) prior ON true
 WHERE f.occurred_at>=scope.after OR ((r.n<>1 OR r.compound_recognition) AND completed.id IS NOT NULL
  AND f.completion_due AND (completed.occurred_at>=scope.after OR NOT booked.other_period))
), selected_matches AS MATERIALIZED (
 SELECT * FROM matches
 WHERE $4='' OR "sourceDomain"='VIP' AND (
  "sourceTransactionType"=$4
  OR $4='UNKNOWN' AND coalesce("sourceTransactionType",'') NOT IN ('NEW','RENEWAL')
 )
), summary AS (
 SELECT count(*) AS orders,
  count(*) FILTER(WHERE status='MATCHED') AS matched,
  count(*) FILTER(WHERE "sourceDomain"='VIP' AND coalesce("sourceTransactionType",'') NOT IN ('NEW','RENEWAL')) AS unmarked_membership,
  coalesce(sum("paidMinor") FILTER(WHERE "paidInWindow" AND status NOT IN ('INVALID_PAYMENT','DUPLICATE_PAYMENT','PAYMENT_ALREADY_SETTLED')),0) AS paid,
  coalesce(sum(coalesce("recognizedMinor","paidMinor")) FILTER(WHERE status='MATCHED'),0) AS matched_minor
 FROM selected_matches
), page AS MATERIALIZED (
 SELECT * FROM selected_matches ORDER BY "paidAt" DESC,"sourceVersionId" DESC LIMIT $3 OFFSET $2
), fixed_actions AS MATERIALIZED (
 SELECT v.source_id::text AS rule_id,a->>'item_key' AS item_key,a->>'label' AS label,
  CASE WHEN a->>'rate_bps' ~ '^[0-9]{1,5}$' THEN (a->>'rate_bps')::integer END AS rate_bps
 FROM (SELECT DISTINCT "ruleId" FROM page WHERE status='MATCHED') selected
 JOIN product_rule_versions v ON v.source_id=selected."ruleId"::uuid
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(v.definition->'benefit_actions')='array'
  THEN v.definition->'benefit_actions' ELSE '[]'::jsonb END) a
 WHERE v.definition->>'amount_basis'='ACTUAL_RECEIVED' AND v.definition->>'stacking_policy'='SUM'
  AND v.definition->'internal_sales_direct_split' IS NULL
  AND v.definition->'rounding_policy'='{"mode":"HALF_UP","scale":2}'::jsonb
  AND a->>'type'='RATE' AND a->>'beneficiary_mode'='PARTY' AND a->>'role' IN ('AI_RND_GENERAL_MANAGER','CHAIRMAN')
  AND nullif(a->>'designated_party_id','') IS NOT NULL
  AND nullif(a->>'item_key','') IS NOT NULL AND nullif(a->>'label','') IS NOT NULL
  AND CASE WHEN jsonb_typeof(a)='object' THEN a-ARRAY['type','beneficiary_mode','role','designated_party_id','item_key','label','rate_bps']='{}'::jsonb ELSE false END
), output AS (
 SELECT page.*,coalesce((
  SELECT jsonb_agg(jsonb_build_object('itemKey',a.item_key,'label',a.label,'amountMinor',round(coalesce(page."recognizedMinor",page."paidMinor")::numeric*a.rate_bps/10000)) ORDER BY a.item_key)
  FROM fixed_actions a WHERE page.status='MATCHED' AND a.rule_id=page."ruleId" AND a.rate_bps BETWEEN 0 AND 10000
 ),'[]') AS "fixedParts" FROM page
)
SELECT jsonb_build_object(
 'hasMembershipOrders',EXISTS(SELECT 1 FROM routes WHERE source='DCAI' AND domain='VIP' AND record_table='ai_vip_order'),
 'orderCount',summary.orders,
 'matchedCount',summary.matched,
 'paidMinor',summary.paid,
 'matchedMinor',summary.matched_minor,
 'unmarkedMembershipCount',summary.unmarked_membership,
 'items',coalesce((SELECT jsonb_agg(to_jsonb(output) ORDER BY "paidAt" DESC,"sourceVersionId" DESC) FROM output),'[]'))
FROM period CROSS JOIN summary WHERE EXISTS(SELECT 1 FROM routes)`

func (s workspace) catalogMatches(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	periodID, err := strconv.ParseInt(query.Get("periodId"), 10, 64)
	if err != nil || periodID < 1 {
		respond(w, nil, invalid)
		return
	}
	page := 1
	if value := query.Get("page"); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 || page > 100000 {
			respond(w, nil, invalid)
			return
		}
	}
	memberType := query.Get("memberTransactionType")
	switch memberType {
	case "", "NEW", "RENEWAL", "UNKNOWN":
	default:
		respond(w, nil, invalid)
		return
	}
	ctx := r.Context()
	var report catalogMatchReport
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		report, err = readCatalogMatchPage(ctx, tx, periodID, (page-1)*25, 25, memberType)
		if err != nil {
			return err
		}
		data, err := loadVIPRuntimeData(ctx, tx, periodID, report.Items, false)
		if err != nil {
			return err
		}
		applyVIPQuotes(report.Items, data)
		return nil
	})
	respond(w, report, err)
}

func readCatalogMatches(ctx context.Context, tx pgx.Tx, periodID int64, offset, limit int) (catalogMatchReport, error) {
	return readCatalogMatchPage(ctx, tx, periodID, offset, limit, "")
}

func readCatalogMatchPage(ctx context.Context, tx pgx.Tx, periodID int64, offset, limit int, memberType string) (catalogMatchReport, error) {
	var report catalogMatchReport
	var raw json.RawMessage
	if err := tx.QueryRow(ctx, catalogMatchSQL, periodID, offset, limit, memberType).Scan(&raw); err != nil {
		return report, err
	}
	err := json.Unmarshal(raw, &report)
	return report, err
}
