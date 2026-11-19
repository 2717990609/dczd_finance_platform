package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errDraftChanged = errors.New("当前账期有变更待试算，请更新草稿后再确认")

func draftRefreshPending(ctx context.Context, tx pgx.Tx, periodID int64) (bool, error) {
	var pending bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, periodID).Scan(&pending)
	return pending, err
}

func enqueueDraftConfiguration(ctx context.Context, tx pgx.Tx, selection string, args ...any) error {
	if err := touchSourceWrites(ctx, tx, []string{"CONFIG"}); err != nil {
		return err
	}
	return enqueueDraftPeriods(ctx, tx, selection, args...)
}

func enqueueDraftPeriods(ctx context.Context, tx pgx.Tx, selection string, args ...any) error {
	_, err := tx.Exec(ctx, `WITH affected(period_id) AS (`+selection+`)
	 INSERT INTO draft_refresh_queue(period_id)
	 SELECT DISTINCT period_id FROM affected a
	 WHERE NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=a.period_id)
	 ORDER BY period_id ON CONFLICT(period_id) DO UPDATE SET generation=excluded.generation,requested_at=excluded.requested_at,retry_on_scan=true,message=''`, args...)
	return err
}

const draftRefreshProduct = `SELECT id FROM periods WHERE product_id=$1`

const draftRefreshSalesContext = `SELECT p.id FROM periods p JOIN product_catalog_settings c USING(product_id) WHERE c.source_system=$1`

const draftRefreshIdentity = `SELECT p.id FROM periods p JOIN source_mappings m USING(product_id)
 JOIN products pro ON pro.id=p.product_id WHERE m.source=$1
 AND (pro.period_mode='class' OR $2<((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'))`

const draftRefreshRule = `SELECT id FROM periods WHERE product_id=$1
 AND ($2::bigint IS NULL OR id=$2) AND ($3::integer IS NULL OR ordinal>=$3)
 AND ($4::timestamptz IS NULL OR $4<((ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'))`

const draftRefreshClassRule = `WITH changed AS (` + draftRefreshRule + `)
 SELECT p.id FROM changed JOIN periods p USING(id) WHERE p.calendar_month IS NULL
 UNION SELECT p.id FROM changed c JOIN periods prior ON prior.id=c.id
 JOIN periods p ON p.product_id=prior.product_id AND p.ordinal>prior.ordinal
  AND split_part(p.source_period_key,':',3)=split_part(prior.source_period_key,':',3)
 WHERE EXISTS(SELECT 1 FROM rule_versions rv WHERE ` + matchingRuleScope + ` AND rv.components @>'[{"mode":"tiered"}]')`

func (s workspace) refreshPendingDrafts(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT period_id,generation FROM draft_refresh_queue WHERE retry_on_scan ORDER BY requested_at,period_id`)
	if err != nil {
		return err
	}
	type request struct{ period, generation int64 }
	work, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (request, error) {
		var next request
		err := row.Scan(&next.period, &next.generation)
		return next, err
	})
	if err != nil {
		return err
	}
	for _, next := range work {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err := s.calculateCurrentDraft(ctx, next.period, next.generation)
		if err == nil {
			continue
		}
		var dbErr *pgconn.PgError
		if errors.As(err, &dbErr) && (dbErr.Code == "40001" || dbErr.Code == "55P03") {
			continue
		}
		message, retry := draftRefreshFailure(err)
		if _, err = s.db.Exec(ctx, `UPDATE draft_refresh_queue SET message=$3,retry_on_scan=$4 WHERE period_id=$1 AND generation=$2`,
			next.period, next.generation, message, retry); err != nil {
			return err
		}
	}
	return nil
}

func draftRefreshFailure(err error) (string, bool) {
	for _, known := range []error{errDraftRule, errDraftLimit, errProductRuleUnavailable, errPracticeNotReady, errPracticeStart} {
		if errors.Is(err, known) {
			return known.Error(), false
		}
	}
	return "自动试算未完成，请核对数据后重试", true
}

func (s workspace) startDraftRefreshWorker(ctx context.Context, interval time.Duration) func() {
	if interval == 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			if err := s.refreshPendingDrafts(ctx); err != nil && ctx.Err() == nil {
				log.Printf("could not refresh calculation drafts")
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (s workspace) draftRefreshStatus(w http.ResponseWriter, r *http.Request) {
	period, err := strconv.ParseInt(r.URL.Query().Get("periodId"), 10, 64)
	if err != nil || period < 1 {
		respond(w, nil, invalid)
		return
	}
	var status json.RawMessage
	err = s.db.QueryRow(r.Context(), `SELECT jsonb_build_object('version',coalesce(d.version,0), 'pending',q.period_id IS NOT NULL AND f.id IS NULL,
	 'message',CASE WHEN f.id IS NULL THEN coalesce(q.message,'') ELSE '' END, 'settled',f.id IS NOT NULL)
	 || CASE WHEN d.practice_roster_id IS NOT NULL THEN jsonb_build_object('calculatedAt',d.calculated_at) ELSE '{}'::jsonb END
	 FROM periods p LEFT JOIN calculation_drafts d ON d.period_id=p.id LEFT JOIN draft_refresh_queue q ON q.period_id=p.id
	 LEFT JOIN settlements f ON f.period_id=p.id WHERE p.id=$1`, period).Scan(&status)
	respond(w, status, err)
}
