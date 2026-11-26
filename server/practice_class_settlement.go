package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type practiceClassRosterReview struct {
	Version              int64      `json:"version"`
	LatestVersion        int64      `json:"latestVersion"`
	Attendance           int        `json:"attendance"`
	StartsAt             time.Time  `json:"startsAt"`
	ConfirmedAt          time.Time  `json:"confirmedAt"`
	RefundCutoff         *time.Time `json:"refundCutoff,omitempty"`
	SourceStatus         string     `json:"sourceStatus"`
	SourceVersion        int64      `json:"sourceVersion,omitempty"`
	id                   int64
	key                  string
	hash                 string
	firstVersion         int64
	firstConfirmedAt     time.Time
	fromRegistrationSync bool
}

func readPracticeClassRosterReview(ctx context.Context, tx pgx.Tx, periodID int64) (practiceClassRosterReview, error) {
	var result practiceClassRosterReview
	err := tx.QueryRow(ctx, `SELECT r.id,r.period_key,r.source_hash,r.source_version,latest.source_version,r.attendance_count,
	 r.starts_at,r.confirmed_at,first.source_version,first.confirmed_at,
	 coalesce(p.refund_cutoff_at,CASE WHEN NOT first.from_registration_sync THEN first.confirmed_at END),r.from_registration_sync
	 FROM calculation_drafts d JOIN practice_rosters r ON r.id=d.practice_roster_id
	 JOIN periods p ON p.id=d.period_id
	 JOIN LATERAL (SELECT source_version FROM practice_rosters
	  WHERE period_key=r.period_key ORDER BY source_version DESC LIMIT 1) latest ON true
	 JOIN LATERAL (SELECT source_version,confirmed_at,from_registration_sync FROM practice_rosters
	  WHERE period_key=r.period_key ORDER BY source_version LIMIT 1) first ON true
	 WHERE d.period_id=$1`, periodID).Scan(&result.id, &result.key, &result.hash, &result.Version, &result.LatestVersion, &result.Attendance,
		&result.StartsAt, &result.ConfirmedAt, &result.firstVersion, &result.firstConfirmedAt, &result.RefundCutoff, &result.fromRegistrationSync)
	return result, err
}

func (r practiceClassRosterReview) reviewToken(base string) string {
	var cutoff int64
	if r.RefundCutoff != nil {
		cutoff = r.RefundCutoff.UnixMicro()
	}
	value := fmt.Sprintf("%s|%d|%s|%d|%s|%d|%d|%d", base, r.id, r.key, r.Version, r.hash,
		r.StartsAt.UnixMicro(), r.ConfirmedAt.UnixMicro(), cutoff)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func (s workspace) checkPracticeClassRoster(ctx context.Context, r *practiceClassRosterReview) error {
	if r.Version != r.LatestVersion {
		r.SourceStatus = "NEEDS_DRAFT"
		return errDraftChanged
	}
	if r.fromRegistrationSync {
		if r.RefundCutoff == nil {
			r.SourceStatus = "REFUND_CUTOFF_REQUIRED"
			return errPracticeRefundCutoffRequired
		}
		client, err := newOrderFeedClient("DCZD", s.sources["DCZD"])
		if err != nil {
			r.SourceStatus = "NOT_CONFIGURED"
			return errPracticeSettlementUnavailable
		}
		snapshot, err := client.practiceRegistrations(ctx, r.key)
		if err != nil {
			r.SourceStatus = "UNAVAILABLE"
			return errPracticeRegistrationSync
		}
		startsAt, _ := time.Parse(time.RFC3339Nano, snapshot.PeriodStartAt)
		if snapshot.signature() != r.hash || !startsAt.Truncate(time.Microsecond).Equal(r.StartsAt) {
			r.SourceStatus = "NEEDS_PULL"
			return errPracticeRosterNeedsPull
		}
		r.SourceStatus = "READY"
		return nil
	}
	snapshot, err := s.fetchPracticeRoster(ctx, r.key, false)
	if err != nil {
		switch {
		case errors.Is(err, errPracticeSettlementUnavailable):
			r.SourceStatus = "NOT_CONFIGURED"
		case errors.Is(err, errPracticeRosterRoute):
			r.SourceStatus = "ROUTE_MISSING"
		case errors.Is(err, errPracticeRosterAccess):
			r.SourceStatus = "ACCESS_DENIED"
		case errors.Is(err, errPracticeRosterUnconfirmed):
			r.SourceStatus = "UNCONFIRMED"
		default:
			r.SourceStatus = "UNAVAILABLE"
		}
		return err
	}
	r.SourceVersion = snapshot.Version
	if *snapshot.PaymentConflict {
		r.SourceStatus = "PAYMENT_CONFLICT"
		return errPracticeRosterOverlap
	}
	if r.firstVersion != 1 || snapshot.Version != r.Version || snapshot.Hash != r.hash ||
		!r.StartsAt.Equal(snapshot.StartsAt.Truncate(time.Microsecond)) ||
		!r.ConfirmedAt.Equal(snapshot.ConfirmedAt.Truncate(time.Microsecond)) ||
		!r.firstConfirmedAt.Equal(snapshot.FirstConfirmed.Truncate(time.Microsecond)) {
		r.SourceStatus = "NEEDS_PULL"
		return errPracticeRosterNeedsPull
	}
	r.SourceStatus = "READY"
	return nil
}

const practiceClassSettlementSourceSQL = `WITH scope AS (
 SELECT least(p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai',(
   SELECT min(v.occurred_at) FROM calculation_draft_performance a JOIN source_order_versions v ON v.id=a.source_version_id
   WHERE a.period_id=p.id AND a.payment_version_id IS NULL
  )) AS from_time,greatest((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai',CASE WHEN NOT roster.from_registration_sync THEN roster.confirmed_at END,p.refund_cutoff_at) AS through_time
 FROM periods p JOIN calculation_drafts d ON d.period_id=p.id JOIN practice_rosters roster ON roster.id=d.practice_roster_id
 WHERE p.id=$1 AND EXISTS(
  SELECT 1 FROM source_mappings m WHERE m.product_id=p.product_id AND m.source='DCZD' AND m.domain='PRACTICE'
 )
)` + practiceSettlementCoverageSQL
