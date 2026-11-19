package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const healthRefundBeforeRecognitionSQL = `
 SELECT DISTINCT q.id FROM jsonb_to_recordset($1::jsonb) AS q(id bigint,source text,order_id text,line_id text,record_id text)
 JOIN periods boundary ON boundary.id=$2
 JOIN products pro ON pro.id=boundary.product_id
 JOIN product_catalog_settings c ON c.product_id=pro.id AND c.source_system='DCAI'
 JOIN source_order_versions refund ON refund.id=q.id AND refund.source=c.source_system
  AND refund.kind='REFUND_SUCCEEDED' AND refund.fact->>'domain'='HEALTH_IDENTITY'
 JOIN LATERAL (
  SELECT v.* FROM source_order_versions v
  WHERE v.source=q.source AND v.order_id=q.order_id AND v.line_id=q.line_id AND v.kind='ORDER_PAID'
   AND v.record_key='ai_health_ambassador_order:'||q.record_id
  ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
 ) v ON v.order_id=refund.order_id AND v.record_key=refund.record_key
 JOIN source_order_classifications cl ON cl.source_version_id=v.id
 JOIN catalog_business_routes m ON m.product_id=c.product_id AND m.observed_at=c.captured_at
  AND (m.entity_type,m.business_key,m.classifier)=(cl.entity_type,cl.business_key,cl.classifier)
  AND ` + catalogRouteFactSQL + `
 LEFT JOIN dcai_order_blockers state ON state.stream='HEALTH_IDENTITY_ORDERS'
  AND state.source_row_id=q.record_id AND state.order_id=v.order_id
 LEFT JOIN source_order_versions completed ON completed.id=state.completion_version_id
 CROSS JOIN LATERAL (SELECT v.occurred_at AS paid_at,v.source AS source) payment
 WHERE v.fact->>'sourceKey'=refund.fact->>'sourceKey' AND refund.occurred_at>=v.occurred_at
  AND CASE WHEN jsonb_typeof(v.fact->'amountMinor')='number' AND jsonb_typeof(refund.fact->'amountMinor')='number'
    THEN (refund.fact->>'amountMinor')::bigint BETWEEN 1 AND (v.fact->>'amountMinor')::bigint ELSE false END
  AND m.effective_from<=payment.paid_at AND (m.effective_to IS NULL OR payment.paid_at<m.effective_to)
  AND coalesce(state.completion_observed,false)
  AND (completed.id IS NULL OR completed.occurred_at>=(boundary.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')
  AND EXISTS (` + healthRuleForPaymentSQL + `)`

func healthRefundsAwaitingRecognition(ctx context.Context, tx pgx.Tx, period int64, refs []refundReference) (map[int64]bool, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, healthRefundBeforeRecognitionSQL, refs, period)
	if err != nil {
		return nil, err
	}
	pending, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	result := make(map[int64]bool, len(pending))
	for _, id := range pending {
		result[id] = true
	}
	return result, nil
}
