package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type healthContractObservation struct {
	SourceRowID string    `json:"source_row_id"`
	OrderID     string    `json:"order_id"`
	ObservedAt  time.Time `json:"observed_at"`
	HasContract bool      `json:"has_contract"`
	Reasons     []string  `json:"reasons"`
	RecordKey   string    `json:"record_key,omitempty"`
	LineID      string    `json:"line_id"`
	Version     string    `json:"version,omitempty"`
}

const storeHealthCompletionSQL = `WITH incoming AS MATERIALIZED (
 SELECT * FROM jsonb_to_recordset($2::jsonb)
 AS i(source_row_id text,order_id text,observed_at timestamptz,has_contract boolean,reasons text[],record_key text,line_id text,version text)
), prepared AS MATERIALIZED (
 SELECT i.source_row_id,coalesce(nullif(i.order_id,''),b.order_id,'') order_id,i.observed_at,coalesce(i.reasons,'{}') reasons,
  i.has_contract OR coalesce(b.completion_observed,false) observed,
  b.completion_version_id previous_id,CASE WHEN i.has_contract THEN c.id ELSE b.completion_version_id END completion_id,
  (i.record_key IS NOT NULL AND c.id IS NULL) OR coalesce(i.has_contract AND b.completion_observed
   AND i.observed_at=b.cursor_updated_at AND c.id IS DISTINCT FROM b.completion_version_id,false) inconsistent
 FROM incoming i
 LEFT JOIN dcai_order_blockers b ON b.stream=$1 AND b.source_row_id=i.source_row_id
 LEFT JOIN source_order_versions c ON c.source='DCAI' AND c.order_id=i.order_id AND c.line_id=i.line_id
  AND c.record_key=i.record_key AND c.version=i.version AND c.kind='ORDER_COMPLETED'
 WHERE b.cursor_updated_at IS NULL OR b.cursor_updated_at<=i.observed_at
), written AS (
 INSERT INTO dcai_order_blockers AS b(stream,source_row_id,order_id,cursor_updated_at,reasons,completion_version_id,completion_observed)
 SELECT $1,source_row_id,order_id,observed_at,reasons,completion_id,observed FROM prepared WHERE NOT inconsistent
 ON CONFLICT(stream,source_row_id) DO UPDATE SET completion_version_id=excluded.completion_version_id,
  completion_observed=excluded.completion_observed,order_id=excluded.order_id,cursor_updated_at=excluded.cursor_updated_at,reasons=excluded.reasons
 WHERE b.cursor_updated_at<=excluded.cursor_updated_at AND
  (b.order_id,b.cursor_updated_at,b.reasons,b.completion_version_id,b.completion_observed) IS DISTINCT FROM
  (excluded.order_id,excluded.cursor_updated_at,excluded.reasons,excluded.completion_version_id,excluded.completion_observed)
 RETURNING source_row_id,order_id,completion_version_id
)
SELECT EXISTS(SELECT 1 FROM prepared WHERE inconsistent),
 coalesce(jsonb_agg(jsonb_build_object('source_row_id',w.source_row_id,'order_id',w.order_id,
 'previous_id',p.previous_id,'current_id',w.completion_version_id)), '[]')
FROM written w JOIN prepared p USING(source_row_id)
WHERE p.previous_id IS DISTINCT FROM w.completion_version_id`

const healthCompletionRefreshSQL = `WITH changed AS MATERIALIZED (
 SELECT * FROM jsonb_to_recordset($1::jsonb) AS x(source_row_id text,order_id text,previous_id bigint,current_id bigint)
), health_changes AS (
 SELECT payment.id AS payment_version_id,x.previous_id,x.current_id FROM changed x
 JOIN LATERAL (
  SELECT v.id FROM source_order_versions v WHERE v.source='DCAI' AND v.order_id=x.order_id
   AND v.record_key='ai_health_ambassador_order:'||x.source_row_id AND v.kind='ORDER_PAID'
  ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
 ) payment ON true
)
` + healthCompletionAffectedPeriodsSQL

const healthCompletionAffectedPeriodsSQL = `SELECT target.id FROM health_changes x
 JOIN source_order_versions v ON v.id=x.payment_version_id
 JOIN catalog_business_routes m ON ` + catalogRouteFactSQL + `
 JOIN product_catalog_settings c ON c.product_id=m.product_id AND c.captured_at=m.observed_at
 JOIN products pro ON pro.id=m.product_id AND pro.period_mode='calendar'
 LEFT JOIN source_order_classifications cl ON cl.source_version_id=v.id AND (cl.entity_type,cl.business_key,cl.classifier)=
  (m.entity_type,m.business_key,m.classifier)
 CROSS JOIN LATERAL (SELECT v.occurred_at AS paid_at,v.source AS source) payment
 CROSS JOIN LATERAL (
  SELECT payment.paid_at AS occurred_at,false AS completion
  UNION SELECT occurred_at,true FROM source_order_versions WHERE id IN(x.previous_id,x.current_id)
 ) dates
 JOIN periods boundary ON boundary.product_id=m.product_id
  AND (dates.occurred_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN boundary.starts_on AND boundary.ends_on
 JOIN LATERAL (
  SELECT candidate.id FROM periods candidate
  WHERE candidate.product_id=boundary.product_id AND candidate.starts_on>=boundary.starts_on
   AND (dates.completion OR candidate.id=boundary.id)
   AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=candidate.id)
  ORDER BY candidate.starts_on LIMIT 1
 ) target ON true
 CROSS JOIN LATERAL (SELECT cl.source_version_id IS NOT NULL AND m.effective_from<=payment.paid_at
  AND (m.effective_to IS NULL OR payment.paid_at<m.effective_to) AS mapped) route
 WHERE (route.mapped AND EXISTS (` + healthRuleForPaymentSQL + `))
 OR (dates.completion AND NOT (route.mapped AND EXISTS (SELECT 1 FROM (` + healthRulesAtPaymentSQL + `) selected
   HAVING count(*)=1 AND NOT bool_or(has_plan))) AND EXISTS (
    SELECT 1 FROM product_rule_packages p JOIN product_rule_versions v ON v.package_id=p.source_id
    WHERE p.product_id=m.product_id AND p.project_code=c.project_code
     AND (v.observed_at=c.captured_at OR v.published_at IS NOT NULL) AND ` + catalogRuleEligible + `
     AND ` + catalogHealthRecognitionPlan + `
 ))`

const healthRuleForPaymentSQL = `SELECT 1 FROM (` + healthRulesAtPaymentSQL + `) selected
 HAVING count(*)=1 AND bool_or(is_compound)`

const healthRulesAtPaymentSQL = `
   SELECT coalesce((` + catalogHealthRecognitionPlan + `),false) AS is_compound,v.definition->>'recognition_plan' IS NOT NULL AS has_plan,
    ` + catalogRulePriority + ` AS priority,v.published_at,v.effective_from
   FROM product_rule_packages p JOIN product_rule_versions v ON v.package_id=p.source_id
   WHERE p.product_id=m.product_id AND p.project_code=c.project_code AND p.scope_id=m.sku_code
    AND (v.observed_at=c.captured_at OR v.published_at IS NOT NULL) AND ` + catalogRuleEligible + `
    AND v.definition->'settlement_policy'->>'cycle'=CASE pro.cycle_start_day WHEN 25 THEN 'MONTHLY_25_TO_24' WHEN 1 THEN 'MONTHLY' END
    AND v.definition->'policy_scope'->'sales_systems' ? payment.source AND v.definition->'policy_scope'->'business_sku_codes' ? m.sku_code
    AND v.effective_from<=payment.paid_at AND (v.effective_to IS NULL OR payment.paid_at<v.effective_to)
    AND (p.resume_from IS NULL OR p.resume_from<=payment.paid_at)
   ORDER BY ` + catalogRuleOrder + ` FETCH FIRST 1 ROW WITH TIES`

func storeHealthCompletionState(ctx context.Context, tx pgx.Tx, rows []orderPageRow) error {
	observations := make([]healthContractObservation, 0, len(rows))
	for _, row := range rows {
		observation := healthContractObservation{SourceRowID: row.Cursor.ID, OrderID: row.OrderID, ObservedAt: row.Cursor.UpdatedAt,
			HasContract: row.healthContractObserved, Reasons: row.Blockers}
		if completion := row.healthCompletion; completion != nil {
			observation.RecordKey, observation.LineID, observation.Version = completion.RecordKey, completion.LineID, completion.Version
		}
		observations = append(observations, observation)
	}
	if len(observations) == 0 {
		return nil
	}
	var unresolved bool
	var changed json.RawMessage
	if err := tx.QueryRow(ctx, storeHealthCompletionSQL, dcaiHealthStream, observations).Scan(&unresolved, &changed); err != nil {
		return err
	}
	if unresolved {
		return errSourceRead
	}
	if string(changed) == "[]" {
		return nil
	}
	return enqueueDraftPeriods(ctx, tx, healthCompletionRefreshSQL, changed)
}
