package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type practiceClassRosterReview struct {
	Version       int64     `json:"version"`
	LatestVersion int64     `json:"latestVersion"`
	Attendance    int       `json:"attendance"`
	StartsAt      time.Time `json:"startsAt"`
	ConfirmedAt   time.Time `json:"confirmedAt"`
	RefundCutoff  time.Time `json:"refundCutoff"`
}

func readPracticeClassRosterReview(ctx context.Context, tx pgx.Tx, periodID int64) (practiceClassRosterReview, error) {
	var result practiceClassRosterReview
	err := tx.QueryRow(ctx, `SELECT r.source_version,latest.source_version,r.attendance_count,r.starts_at,r.confirmed_at,
	 (SELECT confirmed_at FROM practice_rosters WHERE period_key=r.period_key ORDER BY source_version LIMIT 1)
	 FROM calculation_drafts d JOIN practice_rosters r ON r.id=d.practice_roster_id
	 JOIN LATERAL (SELECT source_version FROM practice_rosters
	  WHERE period_key=r.period_key ORDER BY source_version DESC LIMIT 1) latest ON true
	 WHERE d.period_id=$1`, periodID).Scan(&result.Version, &result.LatestVersion, &result.Attendance, &result.StartsAt, &result.ConfirmedAt, &result.RefundCutoff)
	return result, err
}

const practiceClassSettlementSourceSQL = `WITH scope AS (
 SELECT least(p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai',(
   SELECT min(v.occurred_at) FROM calculation_draft_performance a JOIN source_order_versions v ON v.id=a.source_version_id
   WHERE a.period_id=p.id AND a.payment_version_id IS NULL
  )) AS from_time,greatest((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai',roster.confirmed_at) AS through_time
 FROM periods p JOIN calculation_drafts d ON d.period_id=p.id JOIN practice_rosters roster ON roster.id=d.practice_roster_id
 WHERE p.id=$1 AND EXISTS(
  SELECT 1 FROM source_mappings m WHERE m.product_id=p.product_id AND m.source='DCZD' AND m.domain='PRACTICE'
 )
)` + practiceSettlementCoverageSQL
