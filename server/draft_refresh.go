package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
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
	 SELECT DISTINCT a.period_id FROM affected a JOIN periods p ON p.id=a.period_id
	 JOIN products pro ON pro.id=p.product_id AND pro.code`+activeFinanceProductCodeSQL+`
	 WHERE NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=a.period_id)
	 ORDER BY period_id ON CONFLICT(period_id) DO UPDATE SET generation=excluded.generation,requested_at=excluded.requested_at,retry_on_scan=true,message=''`, args...)
	return err
}

const draftRefreshProduct = `SELECT id FROM periods WHERE product_id=$1`

const draftRefreshSalesContext = `SELECT p.id FROM periods p JOIN product_catalog_settings c USING(product_id) WHERE c.source_system=$1`

const draftRefreshIdentity = `SELECT p.id FROM periods p JOIN products pro ON pro.id=p.product_id
 WHERE (EXISTS(SELECT 1 FROM source_mappings m WHERE m.product_id=p.product_id AND m.source=$1)
    OR EXISTS(SELECT 1 FROM product_catalog_settings c WHERE c.product_id=p.product_id AND c.source_system=$1))
 AND (pro.period_mode='class' OR $2<((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'))`

const draftRefreshRule = `SELECT id FROM periods WHERE product_id=$1
 AND ($2::bigint IS NULL OR id=$2) AND ($3::integer IS NULL OR ordinal>=$3)
 AND ($4::timestamptz IS NULL OR $4<((ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'))`

const draftRefreshClassRule = `WITH changed AS (` + draftRefreshRule + `
 AND ($5::integer=0 OR $5::integer::text=split_part(source_period_key,':',2)))
 SELECT p.id FROM changed JOIN periods p USING(id) WHERE p.calendar_month IS NULL
 UNION SELECT d.period_id FROM changed c CROSS JOIN LATERAL practice_tier_dependents(c.id) d`

const (
	manualDraftPollInterval = time.Second
	manualDraftTimeout      = 5 * time.Minute
)

func (s workspace) requestDraftGeneration(ctx context.Context, input calculateDraftInput) (map[string]int64, error) {
	if input.PeriodID < 1 {
		return nil, invalid
	}
	var settled bool
	var productCode string
	err := s.db.QueryRow(ctx, `WITH target AS (
	 SELECT p.id,pro.code,EXISTS(SELECT 1 FROM settlements f WHERE f.period_id=p.id) AS settled
	 FROM periods p JOIN products pro ON pro.id=p.product_id WHERE p.id=$1
	), queued AS (
	 INSERT INTO draft_refresh_queue(period_id,manual_requested)
	 SELECT id,true FROM target WHERE NOT settled AND code`+activeFinanceProductCodeSQL+`
	  AND NOT EXISTS(SELECT 1 FROM draft_refresh_queue q WHERE q.period_id=target.id AND q.manual_requested)
	 ON CONFLICT(period_id) DO UPDATE SET generation=excluded.generation,requested_at=excluded.requested_at,
	  manual_requested=true,retry_on_scan=true,message='' WHERE NOT draft_refresh_queue.manual_requested
	)
	SELECT settled,code FROM target`, input.PeriodID).Scan(&settled, &productCode)
	if err != nil {
		return nil, err
	}
	if productCode == retiredHealthCreatorProduct {
		return nil, errHealthCreatorRetired
	}
	if settled {
		return nil, errSettlementClosed
	}
	return map[string]int64{"periodId": input.PeriodID}, nil
}

func (s workspace) refreshPendingDrafts(ctx context.Context) error {
	return s.refreshDrafts(ctx, false)
}

func (s workspace) refreshDrafts(ctx context.Context, manual bool) error {
	rows, err := s.db.Query(ctx, `SELECT q.period_id,q.generation FROM draft_refresh_queue q
	 JOIN periods p ON p.id=q.period_id JOIN products pro ON pro.id=p.product_id
	 WHERE q.manual_requested=$1 AND ($1 OR q.retry_on_scan) AND pro.code`+activeFinanceProductCodeSQL+` ORDER BY q.requested_at,q.period_id`, manual)
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
		if err := s.runDraftRefresh(ctx, next.period, next.generation, manual); err != nil {
			return err
		}
	}
	return nil
}

func (s workspace) runDraftRefresh(ctx context.Context, period, generation int64, manual bool) error {
	conn, release, err := tryFinanceAdvisoryLock(ctx, s.db, "finance:draft-refresh:"+strconv.FormatInt(period, 10))
	if err != nil || conn == nil {
		return err
	}
	defer release()

	// Another instance may have completed the request after this worker read the queue.
	var eligible bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1 AND generation=$2
	 AND manual_requested=$3 AND ($3 OR retry_on_scan))`, period, generation, manual).Scan(&eligible); err != nil || !eligible {
		return err
	}
	workCtx := ctx
	if manual {
		var cancel context.CancelFunc
		workCtx, cancel = context.WithTimeout(ctx, manualDraftTimeout)
		defer cancel()
	}
	_, err = s.calculateDraftOn(workCtx, conn, period, generation)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var dbErr *pgconn.PgError
	if errors.As(err, &dbErr) && (dbErr.Code == "40001" || dbErr.Code == "55P03") {
		return nil
	}
	message, retry := draftRefreshFailure(err)
	if manual {
		if errors.Is(workCtx.Err(), context.DeadlineExceeded) {
			message = "试算超时，请重试"
		} else if retry {
			message = "试算未完成，请重试"
		}
		retry = false
	}
	const failed = `UPDATE draft_refresh_queue SET message=$3,retry_on_scan=$4,manual_requested=false
	 WHERE period_id=$1 AND generation=$2`
	// pgx can close the connection on deadline. Fence the fallback update by
	// generation so it cannot overwrite a newer request or a completed draft.
	if conn.Conn().IsClosed() {
		_, err = s.db.Exec(ctx, failed, period, generation, message, retry)
	} else {
		_, err = conn.Exec(ctx, failed, period, generation, message, retry)
	}
	return err
}

func draftRefreshFailure(err error) (string, bool) {
	for _, known := range []error{errDraftRule, errDraftLimit, errProductRuleUnavailable, errPracticeNotReady, errPracticeRefundCutoffRequired, errPracticeRegistrationPayment, errPracticeRosterProduct, errCatalogAggregatePaused, errCatalogAggregatePeriod} {
		if errors.Is(err, known) {
			return known.Error(), false
		}
	}
	return "自动试算未完成，请核对数据后重试", true
}

func (s workspace) startDraftRefreshWorker(ctx context.Context, interval time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	run := func(interval time.Duration, manual bool) {
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			if err := s.refreshDrafts(ctx, manual); err != nil && ctx.Err() == nil {
				log.Printf("could not refresh calculation drafts (manual=%t)", manual)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}
	workers.Go(func() { run(manualDraftPollInterval, true) })
	if interval != 0 {
		workers.Go(func() { run(interval, false) })
	}
	return func() { cancel(); workers.Wait() }
}

func (s workspace) draftRefreshStatus(w http.ResponseWriter, r *http.Request) {
	period, err := strconv.ParseInt(r.URL.Query().Get("periodId"), 10, 64)
	if err != nil || period < 1 {
		respond(w, nil, invalid)
		return
	}
	var status json.RawMessage
	err = s.db.QueryRow(r.Context(), `SELECT jsonb_build_object('version',coalesce(d.version,0), 'pending',q.period_id IS NOT NULL AND f.id IS NULL,
	 'message',CASE WHEN f.id IS NULL THEN coalesce(q.message,'') ELSE '' END,
	 'generating',coalesce(q.manual_requested,false) AND f.id IS NULL, 'settled',f.id IS NOT NULL)
	 || CASE WHEN d.practice_roster_id IS NOT NULL THEN jsonb_build_object('calculatedAt',d.calculated_at) ELSE '{}'::jsonb END
	 FROM periods p LEFT JOIN calculation_drafts d ON d.period_id=p.id LEFT JOIN draft_refresh_queue q ON q.period_id=p.id
	 LEFT JOIN settlements f ON f.period_id=p.id WHERE p.id=$1`, period).Scan(&status)
	respond(w, status, err)
}
