package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var errSettlementSourceCoverage = errors.New("来源数据尚未覆盖本账期，请完成同步后重新核对")
var errSettlementSourceRefund = errors.New("本账期有来源退款缺少金额或时间，请在子系统核对")
var errSettlementSourceStale = errors.New("来源同步已超过关账时效，请同步后重新核对")
var errSettlementSourceClock = errors.New("来源水位晚于同步完成时间，请核对来源时钟后重新同步")

const defaultSettlementSyncMaxAge = time.Hour

type settlementSource struct {
	Stream    string     `json:"stream"`
	Status    string     `json:"status"`
	From      *time.Time `json:"initialFrom"`
	Through   *time.Time `json:"completedThrough"`
	LastSync  *time.Time `json:"lastSuccessAt"`
	RefundGap int        `json:"refundGap"`
}

func (s workspace) dcaiSourceKey() string {
	client, err := newDCAIOrderClient(s.sources["DCAI"])
	if err != nil {
		return ""
	}
	return client.sourceKey()
}

func lockDraftInputs(ctx context.Context, tx pgx.Tx, periodID int64) error {
	// Writers update these rows before changing facts or checkpoints. When a
	// writer committed after our snapshot, PostgreSQL forces a serializable retry.
	_, err := tx.Exec(ctx, `SELECT source FROM source_write_state
	 WHERE source='CONFIG' OR source IN (
	  SELECT c.source_system FROM periods p JOIN legacy_catalog_products c USING(product_id) WHERE p.id=$1
	  UNION SELECT m.source FROM periods p JOIN source_mappings m USING(product_id) WHERE p.id=$1
	 ) ORDER BY source FOR SHARE`, periodID)
	return err
}

const settlementSourceCoverageSQL = `WITH scope AS (
 SELECT p.product_id,p.ordinal,p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai' AS from_time,
  (p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai' AS through_time
 FROM periods p WHERE p.id=$1
), required AS (
 SELECT DISTINCT CASE WHEN r.source='DCAI' AND r.domain='VIP' THEN 'VIP_ORDERS'
  WHEN r.source='DCAI' AND r.domain='HEALTH_IDENTITY' THEN 'HEALTH_IDENTITY_ORDERS' ELSE '' END AS stream
 FROM scope p JOIN legacy_catalog_products c USING(product_id)
 JOIN catalog_business_routes r ON r.product_id=p.product_id AND r.observed_at=c.captured_at
 WHERE r.effective_from<p.through_time AND (r.effective_to IS NULL OR r.effective_to>p.from_time)
)
SELECT r.stream,CASE
 WHEN r.stream='' THEN 'UNSUPPORTED'
 WHEN $2='' THEN 'NOT_CONFIGURED'
 WHEN j.source_key=$2 AND j.status IN ('QUEUED','RUNNING') THEN 'INCOMPLETE'
 WHEN s.stream IS NULL THEN 'NOT_STARTED'
 WHEN s.source_key<>$2 OR ws.provider_key IS DISTINCT FROM $2 THEN 'SOURCE_CHANGED'
 WHEN s.failed THEN 'FAILED'
 WHEN s.target_through IS NOT NULL THEN 'INCOMPLETE'
 WHEN s.initial_from>p.from_time OR s.completed_through IS NULL OR s.completed_through<p.through_time THEN 'OUTSIDE_RANGE'
 WHEN s.completed_through>s.last_success_at THEN 'CLOCK_GAP'
 WHEN s.last_success_at IS NULL OR least(s.last_success_at,s.completed_through)<clock_timestamp()-$3::interval THEN 'STALE'
 ELSE 'READY' END,
 s.initial_from,s.completed_through,s.last_success_at,
 CASE WHEN r.stream='VIP_ORDERS' THEN (
  SELECT count(DISTINCT b.order_id) FROM dcai_order_blockers b
  WHERE b.stream=r.stream AND cardinality(b.reasons)>0 AND 'REFUND_EVIDENCE_MISSING'=ANY(b.reasons)
   AND (
    EXISTS(
     SELECT 1 FROM source_order_versions v
     JOIN calculation_draft_performance perf ON perf.source_version_id=v.id AND perf.payment_version_id IS NULL
     JOIN periods original ON original.id=perf.period_id
     WHERE v.source='DCAI' AND v.order_id=b.order_id AND v.kind='ORDER_PAID' AND v.fact->>'domain'='VIP'
      AND original.product_id=p.product_id AND (perf.period_id=$1 OR original.ordinal<p.ordinal
       AND EXISTS(SELECT 1 FROM settlements final WHERE final.period_id=original.id))
    )
    OR b.cursor_updated_at<p.through_time AND NOT EXISTS(
     SELECT 1 FROM source_order_versions v
     WHERE v.source='DCAI' AND v.order_id=b.order_id AND v.kind='ORDER_PAID' AND v.fact->>'domain'='VIP'
    )
   )
 ) ELSE 0 END
FROM scope p CROSS JOIN required r JOIN source_write_state ws ON ws.source='DCAI'
LEFT JOIN dcai_order_sync s ON s.stream=r.stream
LEFT JOIN dcai_sync_jobs j ON j.stream=r.stream ORDER BY r.stream`

func readSettlementSourceCoverage(ctx context.Context, tx pgx.Tx, periodID int64, provider string, maxAge time.Duration) ([]settlementSource, error) {
	if maxAge == 0 {
		maxAge = defaultSettlementSyncMaxAge
	}
	rows, err := tx.Query(ctx, settlementSourceCoverageSQL, periodID, provider, maxAge)
	if err != nil {
		return nil, err
	}
	var source settlementSource
	var result []settlementSource
	_, err = pgx.ForEachRow(rows, []any{&source.Stream, &source.Status, &source.From, &source.Through, &source.LastSync, &source.RefundGap}, func() error {
		result = append(result, source)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		result = []settlementSource{{Status: "UNSUPPORTED"}}
	}
	return result, nil
}

func settlementCoverageError(sources []settlementSource) error {
	for _, source := range sources {
		if source.Status == "STALE" {
			return errSettlementSourceStale
		}
		if source.Status == "CLOCK_GAP" {
			return errSettlementSourceClock
		}
		if source.Status != "READY" {
			return errSettlementSourceCoverage
		}
		if source.RefundGap != 0 {
			return errSettlementSourceRefund
		}
	}
	return nil
}

func verifySettlementSourceDeadline(ctx context.Context, tx pgx.Tx, sources []settlementSource, maxAge time.Duration) error {
	if len(sources) == 0 {
		return nil
	}
	if maxAge == 0 {
		maxAge = defaultSettlementSyncMaxAge
	}
	var deadline time.Time
	for _, source := range sources {
		if source.Through == nil || source.LastSync == nil {
			return errSettlementSourceStale
		}
		syncAt := *source.Through
		if source.LastSync.Before(syncAt) {
			syncAt = *source.LastSync
		}
		end := syncAt.Add(maxAge)
		if deadline.IsZero() || end.Before(deadline) {
			deadline = end
		}
	}
	// Source/checkpoint writes are locked while confirming. Only elapsed time can change this check.
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()<=$1`, deadline).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return errSettlementSourceStale
	}
	return nil
}
