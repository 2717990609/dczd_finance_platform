package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const draftDisplayFactFields = `ARRAY['version','updatedAt','orderNo']`

const draftRefreshNewCalendarPeriod = `SELECT $1::bigint UNION SELECT period_id FROM health_completion_open_periods($1)
 UNION SELECT issue.period_id FROM periods created JOIN periods open ON open.product_id=created.product_id
 JOIN calculation_draft_issues issue ON issue.period_id=open.id AND issue.code='REFUND_MONTH_MISSING'
 JOIN source_order_versions refund ON refund.id=issue.source_version_id
 WHERE created.id=$1 AND refund.occurred_at>=created.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'
  AND refund.occurred_at<(created.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'`

const draftRefreshNewAggregatePeriod = `SELECT $1::bigint
 UNION SELECT issue.period_id FROM periods created JOIN periods pending USING(aggregate_package_id)
 JOIN calculation_draft_issues issue ON issue.period_id=pending.id AND issue.code='AGGREGATE_REFUND_MONTH_MISSING'
 JOIN source_order_versions refund ON refund.id=issue.source_version_id
 WHERE created.id=$1 AND (refund.occurred_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN created.starts_on AND created.ends_on`

const draftFinancialFactChanged = `(current.fact-` + draftDisplayFactFields + `,current.occurred_at,
 cc.entity_type,cc.business_key,cc.classifier,cc.sales_claims,cv.company_id,cv.recommendation_company_id,cv.recommendation_conflict,
 current.practice_registration) IS DISTINCT FROM (previous.fact-` + draftDisplayFactFields + `,previous.occurred_at,
 pc.entity_type,pc.business_key,pc.classifier,pc.sales_claims,pv.company_id,pv.recommendation_company_id,pv.recommendation_conflict,
 previous.practice_registration)`

var draftRefreshFacts = `WITH keys AS (
 SELECT DISTINCT source,order_id,line_id,record_key,kind FROM source_order_versions
 WHERE id=ANY($1::bigint[]) OR id=ANY($2::bigint[])
), changed AS MATERIALIZED (
 SELECT current.id AS current_id,previous.id AS previous_id
 FROM keys k
 JOIN LATERAL (SELECT v.* FROM source_order_versions v WHERE (v.source,v.order_id,v.line_id,v.record_key,v.kind)
  =(k.source,k.order_id,k.line_id,k.record_key,k.kind) ORDER BY ` + sourceOrderVersionSort("v") + ` LIMIT 1) current ON true
 LEFT JOIN LATERAL (SELECT v.* FROM source_order_versions v WHERE (v.source,v.order_id,v.line_id,v.record_key,v.kind)
  =(k.source,k.order_id,k.line_id,k.record_key,k.kind) AND NOT(v.id=ANY($1::bigint[]))
  ORDER BY ` + sourceOrderVersionSort("v") + ` LIMIT 1) previous ON true
 LEFT JOIN source_order_classifications cc ON cc.source_version_id=current.id
 LEFT JOIN source_order_classifications pc ON pc.source_version_id=previous.id
 LEFT JOIN source_payment_companies cv ON cv.source_version_id=current.id
 LEFT JOIN source_payment_companies pv ON pv.source_version_id=previous.id
 WHERE current.id=ANY($2::bigint[]) OR (current.id=ANY($1::bigint[]) AND
  (previous.id IS NULL OR (` + draftFinancialFactChanged + `)))
), events AS MATERIALIZED (
 SELECT v.id,v.source,v.fact,v.order_id,v.kind,v.record_key,v.occurred_at FROM changed c
 JOIN source_order_versions v ON v.id=c.current_id OR v.id=c.previous_id
 WHERE v.kind IN ('ORDER_PAID','REFUND_SUCCEEDED') OR v.source='DCAI' AND v.kind='REFUND_ADJUSTED'
), refund_versions AS MATERIALIZED (
 SELECT v.id FROM events changed JOIN source_order_versions v
  ON v.source=changed.source AND v.kind='REFUND_SUCCEEDED' AND v.fact->>'domain'=changed.fact->>'domain'
  AND v.fact->>'refundId'=changed.fact->>'refundId'
 WHERE changed.kind='REFUND_SUCCEEDED' AND coalesce(changed.fact->>'refundId','')<>''
 UNION SELECT v.id FROM events changed JOIN source_order_versions v
  ON v.source='DCZD' AND v.kind='REFUND_SUCCEEDED' AND v.record_key=changed.record_key
 WHERE changed.source='DCZD' AND changed.kind='REFUND_SUCCEEDED'
), affected_events AS (
 SELECT id FROM events UNION SELECT id FROM refund_versions
), mapped AS (
 SELECT v.id,m.product_id,v.occurred_at,v.kind FROM affected_events a JOIN source_order_versions v USING(id) JOIN source_mappings m
  ON m.source=v.source AND m.domain=v.fact->>'domain' AND m.source_key=v.fact->>'sourceKey'
 WHERE NOT ` + catalogSourceReservedSQL + `
 UNION
 SELECT v.id,c.product_id,v.occurred_at,v.kind FROM affected_events a JOIN source_order_versions v USING(id)
 JOIN catalog_business_routes m ON ` + catalogRouteFactSQL + `
 JOIN product_catalog_settings c ON c.product_id=m.product_id AND c.captured_at=m.observed_at
), calendar AS (
 SELECT p.id,p.product_id,p.starts_on,p.ends_on FROM periods p JOIN products pro ON pro.id=p.product_id
 WHERE pro.period_mode='calendar' AND p.aggregate_package_id IS NULL
), origin AS (
 SELECT p.id,p.product_id,v.id AS event_id,v.kind FROM mapped v JOIN calendar p USING(product_id)
 WHERE v.occurred_at>=p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'
 AND v.occurred_at<(p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'
), practice_events AS (
 SELECT order_id,fact FROM events WHERE source='DCZD' AND fact->>'domain'='PRACTICE' AND kind='ORDER_PAID'
 UNION ALL SELECT v.order_id,v.fact FROM source_order_versions v JOIN refund_versions changed USING(id)
 WHERE v.source='DCZD' AND v.fact->>'domain'='PRACTICE'
), practice_refund_months AS (
 SELECT DISTINCT p.id FROM refund_versions changed JOIN source_order_versions v USING(id)
 JOIN source_mappings m ON m.source=v.source AND m.domain=v.fact->>'domain' AND m.source_key=v.fact->>'sourceKey'
 JOIN products pro ON pro.id=m.product_id AND pro.period_mode='class'
 JOIN periods p ON p.product_id=pro.id AND p.calendar_month IS NOT NULL
 LEFT JOIN periods origin ON origin.product_id=pro.id AND origin.calendar_month IS NOT NULL
  AND (v.occurred_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN origin.starts_on AND origin.ends_on
 WHERE v.source='DCZD' AND m.domain='PRACTICE' AND (
  p.id=origin.id OR ((origin.id IS NULL AND v.occurred_at<p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'
   OR EXISTS(SELECT 1 FROM settlements st WHERE st.period_id=origin.id))
   AND (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN p.starts_on AND p.ends_on))
), practice_order AS (
 SELECT order_id FROM practice_events
 UNION SELECT a->>'orderId' FROM practice_events v JOIN LATERAL jsonb_array_elements(v.fact->'refundsPayment') a ON true
), classes AS (
 SELECT DISTINCT p.id
 FROM practice_order o JOIN practice_enrollments e ON e.paid_order_ids @> ARRAY[o.order_id]
 JOIN periods p ON p.source_period_key=e.period_key
 JOIN LATERAL (SELECT id FROM practice_rosters r WHERE r.period_key=e.period_key ORDER BY source_version DESC LIMIT 1) latest ON true
 JOIN practice_roster_members member ON member.roster_id=latest.id AND member.enrollment_id=e.id
), practice_changed_classes AS (
 SELECT DISTINCT class.id,class.product_id FROM changed c
 JOIN source_order_versions latest ON latest.id=c.current_id
 JOIN source_order_versions previous ON previous.id=c.previous_id
 LEFT JOIN source_payment_companies next_company ON next_company.source_version_id=latest.id
 LEFT JOIN source_payment_companies prior_company ON prior_company.source_version_id=previous.id
 JOIN source_order_versions original ON original.source=latest.source AND original.order_id=latest.order_id
  AND original.line_id=latest.line_id AND original.record_key=latest.record_key AND original.kind=latest.kind
 JOIN calculation_draft_performance booked ON booked.source_version_id=original.id AND booked.payment_version_id IS NULL
 JOIN settlements final ON final.period_id=booked.period_id
 JOIN periods class ON class.id=booked.period_id AND class.calendar_month IS NULL AND class.source_period_key IS NOT NULL
 WHERE latest.source='DCZD' AND latest.kind='ORDER_PAID' AND latest.fact->>'domain'='PRACTICE'
  AND (` + practiceAttributionFields("latest.fact") + `,next_company.company_id,
   next_company.recommendation_company_id,next_company.recommendation_conflict)
  IS DISTINCT FROM (` + practiceAttributionFields("previous.fact") + `,prior_company.company_id,
   prior_company.recommendation_company_id,prior_company.recommendation_conflict)
), corrected_originals AS (
 SELECT DISTINCT line.id,prior.product_id FROM events v JOIN source_order_versions paid
  ON paid.source=v.source AND paid.order_id=v.order_id AND paid.kind='ORDER_PAID'
 JOIN calculation_draft_lines line ON line.source_version_id=paid.id AND line.aggregate_group_key IS NULL
  AND (line.component_type='referral' OR line.catalog_rule_id IS NOT NULL)
 JOIN settlements final ON final.period_id=line.period_id JOIN periods prior ON prior.id=line.period_id WHERE v.kind='ORDER_PAID'
), late AS (
 SELECT o.product_id FROM origin o JOIN settlements s ON s.period_id=o.id WHERE o.kind='REFUND_SUCCEEDED'
 UNION
 SELECT m.product_id FROM mapped m JOIN source_order_versions v ON v.id=m.id
 WHERE m.kind='REFUND_SUCCEEDED' AND m.occurred_at<=CURRENT_TIMESTAMP
  AND NOT EXISTS(SELECT 1 FROM origin o WHERE o.product_id=m.product_id AND o.event_id=m.id)
  AND ` + refundHasSettledCalendarPaymentSQL("m.product_id") + `
 UNION SELECT product_id FROM corrected_originals
), health_changes AS (
 SELECT DISTINCT v.id AS payment_version_id,NULL::bigint AS previous_id,b.completion_version_id AS current_id
 FROM affected_events a JOIN source_order_versions v USING(id)
 JOIN dcai_order_blockers b ON b.stream='HEALTH_IDENTITY_ORDERS' AND b.order_id=v.order_id
  AND b.source_row_id=split_part(v.record_key,':',2) AND b.completion_version_id IS NOT NULL
 WHERE v.source='DCAI' AND v.kind='ORDER_PAID' AND v.fact->>'domain'='HEALTH_IDENTITY'
  AND split_part(v.record_key,':',1)='ai_health_ambassador_order'
 UNION
 SELECT payment.id,NULL::bigint,b.completion_version_id
 FROM affected_events a JOIN source_order_versions v USING(id)
 JOIN dcai_order_blockers b ON b.stream='HEALTH_IDENTITY_ORDERS' AND b.order_id=v.order_id
  AND b.source_row_id=split_part(v.record_key,':',2)
 JOIN LATERAL (
  SELECT original.id FROM source_order_versions original WHERE original.source=v.source AND original.order_id=v.order_id
   AND original.line_id='' AND original.record_key=v.record_key AND original.kind='ORDER_PAID'
  ORDER BY original.updated_at DESC,original.id DESC LIMIT 1
 ) payment ON true
 WHERE v.source='DCAI' AND v.kind='REFUND_SUCCEEDED' AND v.fact->>'domain'='HEALTH_IDENTITY'
  AND split_part(v.record_key,':',1)='ai_health_ambassador_order' AND b.completion_version_id IS NOT NULL
)
SELECT id FROM origin WHERE kind<>'REFUND_ADJUSTED'
UNION SELECT p.id FROM mapped events JOIN product_catalog_settings source ON source.product_id=events.product_id
 JOIN product_catalog_settings anchor ON (anchor.project_code,anchor.source_system)=(source.project_code,source.source_system)
 JOIN periods p ON p.product_id=anchor.product_id AND p.aggregate_package_id IS NOT NULL
 WHERE (events.occurred_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN p.starts_on AND p.ends_on
  OR (events.kind IN ('REFUND_SUCCEEDED','REFUND_ADJUSTED') AND (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN p.starts_on AND p.ends_on
   AND EXISTS(SELECT 1 FROM periods earlier JOIN settlements final ON final.period_id=earlier.id
    WHERE earlier.aggregate_package_id=p.aggregate_package_id
     AND (events.occurred_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN earlier.starts_on AND earlier.ends_on))
UNION ` + healthCompletionAffectedPeriodsSQL + `
UNION SELECT p.id FROM late l JOIN calendar p USING(product_id)
 WHERE (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN p.starts_on AND p.ends_on
UNION SELECT p.id FROM refund_versions changed JOIN source_order_versions v USING(id)
 JOIN periods p ON p.aggregate_package_id IS NOT NULL
  AND (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN p.starts_on AND p.ends_on
  AND v.occurred_at<p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'
 WHERE ` + catalogAggregateMissingRefundMonthSQL("p.aggregate_package_id") + `
UNION SELECT refund.period_id FROM refund_versions v JOIN calculation_draft_refunds refund ON refund.refund_source_version_id=v.id
UNION SELECT issue.period_id FROM refund_versions v JOIN calculation_draft_issues issue ON issue.source_version_id=v.id
 WHERE issue.code IN ('REFUND_MONTH_MISSING','AGGREGATE_REFUND_MONTH_MISSING')
UNION SELECT a.period_id FROM refund_versions v JOIN catalog_aggregate_draft_events a ON a.source_version_id=v.id
UNION SELECT posted.period_id FROM refund_versions v JOIN calculation_draft_performance posted ON posted.source_version_id=v.id
 WHERE posted.payment_version_id IS NOT NULL
UNION SELECT posted.period_id FROM corrected_originals o JOIN calculation_draft_corrections posted ON posted.original_line_id=o.id
UNION SELECT issue.period_id FROM corrected_originals o JOIN calculation_draft_issues issue ON issue.correction_line_id=o.id
UNION SELECT id FROM classes
UNION SELECT p.id FROM changed c JOIN source_order_versions v ON v.id=c.current_id
 JOIN practice_enrollments e ON e.source_enrollment_id=split_part(v.record_key,':',2) AND e.commission_eligible
 JOIN periods p ON p.source_period_key=e.period_key
 JOIN LATERAL (SELECT id FROM practice_rosters r WHERE r.period_key=e.period_key ORDER BY r.source_version DESC LIMIT 1) latest ON true
 JOIN practice_roster_members member ON member.roster_id=latest.id AND member.enrollment_id=e.id
 WHERE v.source='DCZD' AND v.kind='DELIVERY_COMPLETED' AND split_part(v.record_key,':',1)='dc_offline_order_log_t'
 AND EXISTS(SELECT 1 FROM rule_versions rv WHERE ` + matchingRuleScope + ` AND rv.components @>'[{"periodBenefit":{"registrationScope":{}}}]')
UNION SELECT id FROM practice_refund_months
UNION SELECT p.id FROM practice_changed_classes c JOIN periods p USING(product_id) WHERE p.calendar_month IS NOT NULL
 AND (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN p.starts_on AND p.ends_on
UNION SELECT a.period_id FROM practice_changed_classes c JOIN calculation_draft_practice_changes a ON a.class_period_id=c.id
UNION SELECT i.period_id FROM practice_changed_classes c JOIN calculation_draft_lines l ON l.period_id=c.id
 JOIN calculation_draft_issues i ON i.correction_line_id=l.id
UNION SELECT d.period_id FROM classes c CROSS JOIN LATERAL practice_tier_dependents(c.id) d`

func enqueueDraftFactChanges(ctx context.Context, tx pgx.Tx, inserted, metadata []int64) error {
	if len(inserted) == 0 && len(metadata) == 0 {
		return nil
	}
	// PostgreSQL's ANY(NULL) is unknown, rather than false.
	if inserted == nil {
		inserted = []int64{}
	}
	if metadata == nil {
		metadata = []int64{}
	}
	return enqueueDraftPeriods(ctx, tx, draftRefreshFacts, inserted, metadata)
}

const draftRefreshAfterClose = `WITH closing AS (
 SELECT p.*,product.period_mode FROM periods p JOIN products product ON product.id=p.product_id WHERE p.id=$1
), affected_classes AS (
 SELECT DISTINCT original.period_id FROM closing
 JOIN calculation_draft_performance booked ON booked.period_id=closing.id AND booked.payment_version_id IS NOT NULL
 JOIN calculation_draft_performance original ON original.source_version_id=booked.payment_version_id AND original.payment_version_id IS NULL
 JOIN calculation_drafts class ON class.period_id=original.period_id AND class.practice_replay_inputs IS NOT NULL
 JOIN periods original_period ON original_period.id=class.period_id AND original_period.product_id=closing.product_id
 WHERE closing.period_mode='class'
 UNION SELECT c.class_period_id FROM calculation_draft_practice_changes c WHERE c.period_id=$1
), payments AS MATERIALIZED (
 SELECT post.source_version_id FROM calculation_draft_performance post
 WHERE post.payment_version_id IS NULL AND (post.period_id=$1 OR post.period_id IN (SELECT period_id FROM affected_classes))
), paid AS (
 SELECT DISTINCT v.source,v.order_id FROM payments JOIN source_order_versions v ON v.id=payments.source_version_id
), related_refunds AS (
 SELECT DISTINCT v.occurred_at FROM paid JOIN source_order_versions v ON v.source=paid.source AND v.kind='REFUND_SUCCEEDED'
 AND (v.order_id=paid.order_id OR v.fact->'refundsPayment' @> jsonb_build_array(jsonb_build_object('orderId',paid.order_id)))
), closed_adjustments AS (
 SELECT original_line_id FROM calculation_draft_corrections WHERE period_id=$1
 UNION SELECT original_line_id FROM calculation_draft_refunds WHERE period_id=$1
)
SELECT p.id FROM closing original JOIN periods p ON p.product_id=original.product_id AND p.id<>original.id
 AND p.aggregate_package_id IS NOT DISTINCT FROM original.aggregate_package_id
WHERE (original.period_mode='class' AND p.calendar_month IS NOT NULL OR original.period_mode<>'class' AND p.ordinal>original.ordinal)
 AND (
 (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN p.starts_on AND p.ends_on
 OR EXISTS(SELECT 1 FROM related_refunds r WHERE r.occurred_at>=p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'
  AND r.occurred_at<(p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')
 OR original.period_mode='class' AND EXISTS(SELECT 1 FROM calculation_draft_performance refund
  JOIN payments paid ON paid.source_version_id=refund.payment_version_id WHERE refund.period_id=p.id))
UNION SELECT period_id FROM practice_payment_dependents($1)
UNION SELECT p.id FROM practice_refund_dependents($1) dep JOIN periods p ON p.id=dep.period_id WHERE p.calendar_month IS NULL
UNION SELECT p.id FROM closing original JOIN periods p ON p.aggregate_package_id=original.aggregate_package_id
 AND p.starts_on=original.ends_on+1
UNION SELECT c.period_id FROM closed_adjustments a JOIN calculation_draft_corrections c USING(original_line_id)
UNION SELECT i.period_id FROM closed_adjustments a JOIN calculation_draft_issues i ON i.correction_line_id=a.original_line_id
UNION SELECT r.period_id FROM closed_adjustments a JOIN calculation_draft_refunds r USING(original_line_id)
UNION SELECT c.period_id FROM affected_classes a JOIN calculation_draft_practice_changes c ON c.class_period_id=a.period_id
UNION SELECT i.period_id FROM affected_classes a JOIN calculation_draft_lines l ON l.period_id=a.period_id
 JOIN calculation_draft_issues i ON i.correction_line_id=l.id
UNION SELECT c.period_id FROM closing later
 JOIN periods earlier ON earlier.product_id=later.product_id AND earlier.ordinal<later.ordinal
  AND earlier.aggregate_package_id IS NOT DISTINCT FROM later.aggregate_package_id
  AND split_part(earlier.source_period_key,':',3)=split_part(later.source_period_key,':',3)
 JOIN calculation_draft_practice_changes c ON c.class_period_id=earlier.id
 WHERE later.period_mode='class' AND later.source_period_key IS NOT NULL`

const draftRefreshRoster = `WITH base AS (
 SELECT p.id,p.product_id,split_part(p.source_period_key,':',3) AS series FROM periods p
 WHERE p.source_period_key=$1 AND ($2::bigint IS NULL OR NOT EXISTS(
  SELECT 1 FROM practice_rosters r WHERE r.period_key=$1 AND r.source_version>$2) OR NOT EXISTS(
  SELECT 1 FROM practice_rosters r WHERE r.period_key=$1 AND r.source_version<$2))
), relatives AS (
 SELECT DISTINCT sibling.period_key FROM practice_enrollments e JOIN practice_enrollments sibling
  ON sibling.paid_order_ids&&e.paid_order_ids AND split_part(sibling.period_key,':',3)=split_part(e.period_key,':',3)
 WHERE e.period_key=$1 AND sibling.period_key<>$1 AND e.commission_eligible AND sibling.commission_eligible
), affected AS (
 SELECT id FROM base
 UNION SELECT p.id FROM base b
 JOIN periods p ON p.product_id=b.product_id AND split_part(p.source_period_key,':',3)=b.series
 JOIN relatives r ON r.period_key=p.source_period_key
)
SELECT id FROM affected
UNION SELECT d.period_id FROM affected a CROSS JOIN LATERAL practice_tier_dependents(a.id) d`
