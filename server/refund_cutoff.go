package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var errRefundCutoffRange = errors.New("退款截止时间须晚于本期退款起点，并早于下期退款截止时间")

type periodRefundCutoffInput struct {
	PeriodID int64      `json:"periodId"`
	CutoffAt *time.Time `json:"cutoffAt"`
}

func (s workspace) setPeriodRefundCutoff(ctx context.Context, input periodRefundCutoffInput) (any, error) {
	if input.PeriodID <= 0 || input.CutoffAt != nil && input.CutoffAt.IsZero() {
		return nil, invalid
	}
	err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var product int64
		var closed, nextClosed bool
		var previous, end time.Time
		var nextEnd *time.Time
		var nextID *int64
		var old *time.Time
		err := tx.QueryRow(ctx, `SELECT p.product_id,p.refund_cutoff_at,
		 EXISTS(SELECT 1 FROM settlements WHERE period_id=p.id),
		 CASE WHEN pro.period_mode='class' AND p.calendar_month IS NULL
		  THEN p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai' ELSE refund_period_start(p) END,
		 (p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai',
		 following.id,following.refund_end,
		 EXISTS(SELECT 1 FROM settlements WHERE period_id=following.id)
		 FROM periods p JOIN products pro ON pro.id=p.product_id
		 LEFT JOIN LATERAL (
		   SELECT n.id,refund_period_end(n) AS refund_end FROM periods n WHERE n.product_id=p.product_id AND n.starts_on=p.ends_on+1
		   AND n.aggregate_package_id IS NOT DISTINCT FROM p.aggregate_package_id
		   AND (n.calendar_month IS NULL)=(p.calendar_month IS NULL)
		   AND (pro.period_mode='calendar' OR p.calendar_month IS NOT NULL)
		   ORDER BY n.id LIMIT 1
		 ) following ON true
		 WHERE p.id=$1 FOR NO KEY UPDATE OF p,pro`, input.PeriodID).
			Scan(&product, &old, &closed, &previous, &end, &nextID, &nextEnd, &nextClosed)
		if err != nil {
			return err
		}
		if old == nil && input.CutoffAt == nil || old != nil && input.CutoffAt != nil && old.Equal(*input.CutoffAt) {
			return nil
		}
		if closed || nextClosed {
			return errSettlementClosed
		}
		deadline := end
		if input.CutoffAt != nil {
			deadline = *input.CutoffAt
		}
		if !deadline.After(previous) || nextEnd != nil && !deadline.Before(*nextEnd) {
			return errRefundCutoffRange
		}
		if _, err := tx.Exec(ctx, `UPDATE periods SET refund_cutoff_at=$2 WHERE id=$1`, input.PeriodID, input.CutoffAt); err != nil {
			return err
		}
		return enqueueDraftPeriods(ctx, tx, `SELECT $1::bigint AS id UNION SELECT $2::bigint WHERE $2::bigint IS NOT NULL
		 UNION SELECT period_id FROM practice_tier_dependents($1)
		 UNION SELECT id FROM periods WHERE product_id=$3 AND calendar_month IS NOT NULL
		  AND (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN starts_on AND ends_on`,
			input.PeriodID, nextID, product)
	})
	return input, err
}
