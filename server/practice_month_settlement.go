package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var errPracticeMonthSettlementDependency = errors.New("有相关班次或较早退款账期待结算，请按结算核对显示的范围处理")

type practiceMonthSettlementDependency struct {
	PeriodID int64  `json:"periodId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

const practiceSettlementPaymentVersionsSQL = `SELECT DISTINCT related.source_version_id
 FROM calculation_draft_performance refund
 JOIN periods month ON month.id=refund.period_id
 JOIN calculation_draft_performance origin ON origin.source_version_id=refund.payment_version_id AND origin.payment_version_id IS NULL
 JOIN periods class_period ON class_period.id=origin.period_id AND class_period.product_id=month.product_id
 JOIN calculation_drafts d ON d.period_id=origin.period_id AND d.practice_roster_id IS NOT NULL
 JOIN calculation_draft_performance related ON related.period_id=origin.period_id AND related.payment_version_id IS NULL
  AND (d.practice_replay_inputs IS NOT NULL OR related.source_version_id=origin.source_version_id)
 WHERE refund.period_id=$1`

const practiceMonthSettlementDependenciesSQL = `WITH requested AS (
 SELECT p.id,p.product_id,p.starts_on,pro.cycle_start_day FROM periods p JOIN products pro ON pro.id=p.product_id WHERE p.id=$1
), related_orders AS MATERIALIZED (
 SELECT DISTINCT v.order_id FROM (` + practiceSettlementPaymentVersionsSQL + `) related JOIN source_order_versions v ON v.id=related.source_version_id
), candidate_refunds AS (
 SELECT target.id AS month_id,target.product_id,keys.id AS order_id,v.occurred_at
 ` + practiceRefundMonthFromSQL + `
 CROSS JOIN LATERAL (` + practiceRefundOrderKeysSQL + `) keys
 JOIN requested q ON q.product_id=target.product_id
 WHERE target.calendar_month IS NOT NULL
  AND (target.id=q.id OR target.ends_on<q.starts_on
   AND EXISTS(SELECT 1 FROM related_orders r WHERE r.order_id=keys.id)
   AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=target.id))
 ` + practiceRefundMonthRoutingSQL + `
), missing_months AS (
 SELECT DISTINCT to_char(date_trunc('month',v.occurred_at AT TIME ZONE 'Asia/Shanghai') +
   CASE WHEN extract(day FROM v.occurred_at AT TIME ZONE 'Asia/Shanghai')>=q.cycle_start_day
    THEN interval '1 month' ELSE interval '0 months' END,'YYYY-MM') AS month
 ` + practiceRefundMonthFromSQL + `
 CROSS JOIN requested q
 WHERE target.id=q.id AND origin.id IS NULL AND v.occurred_at<q.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'
  AND NOT history.booked AND ` + latestPracticeRefund + `
  AND EXISTS(SELECT 1 FROM (` + practiceRefundOrderKeysSQL + `) keys JOIN related_orders r ON r.order_id=keys.id)
)
SELECT DISTINCT coalesce(p.id,0) AS id,coalesce(p.name,e.period_key) AS name,'class' AS kind
FROM candidate_refunds v
JOIN practice_enrollments e ON e.paid_order_ids @> ARRAY[v.order_id] AND e.commission_eligible
LEFT JOIN periods p ON p.source_period_key=e.period_key
JOIN LATERAL (SELECT id FROM practice_rosters r WHERE r.period_key=e.period_key ORDER BY r.source_version DESC LIMIT 1) roster ON true
JOIN practice_roster_members m ON m.roster_id=roster.id AND m.enrollment_id=e.id
WHERE v.month_id=$1 AND (p.id IS NULL OR p.product_id=v.product_id) AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=p.id)
 AND v.occurred_at>(SELECT confirmed_at FROM practice_rosters first WHERE first.period_key=e.period_key ORDER BY source_version LIMIT 1)
UNION
SELECT DISTINCT p.id,p.name,'refund_month'
FROM candidate_refunds v JOIN periods p ON p.id=v.month_id WHERE p.id<>$1
UNION
SELECT 0,month || ' 退款账期（待建立）','missing_month' FROM missing_months
ORDER BY kind,id,name LIMIT 20`

func readPracticeMonthSettlementDependencies(ctx context.Context, tx pgx.Tx, periodID int64) ([]practiceMonthSettlementDependency, error) {
	rows, err := tx.Query(ctx, practiceMonthSettlementDependenciesSQL, periodID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceMonthSettlementDependency, error) {
		var item practiceMonthSettlementDependency
		err := row.Scan(&item.PeriodID, &item.Name, &item.Kind)
		return item, err
	})
}
