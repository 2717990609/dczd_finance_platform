package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errSyncInitialFrom   = errors.New("首次同步需要指定历史起点")
	errSyncOrigin        = errors.New("已保存的同步记录与当前来源不一致")
	errSyncLaterStart    = errors.New("已开始同步，历史起点可以往前补拉")
	errSyncInProgress    = errors.New("该订单流正在同步")
	errSyncJobReplaced   = errors.New("同步任务已被后续请求替换，请查看当前进度")
	errUnverifiedSource  = errors.New("库中已有未绑定来源的订单数据，请在独立库初始化同步")
	errBoundSourceImport = errors.New("此库已绑定业务来源，请通过该来源拉取订单")
)

type orderSyncState struct {
	Stream          string     `json:"stream"`
	From            time.Time  `json:"initialFrom"`
	Completed       *time.Time `json:"completedThrough"`
	Target          *time.Time `json:"targetThrough"`
	Pages           int64      `json:"pages"`
	Scanned         int64      `json:"scannedOrders"`
	Versions        int64      `json:"newVersions"`
	Failed          bool       `json:"lastFailed"`
	origin          string
	next            orderCursor
	completedOutbox *time.Time
	targetCoverage  *time.Time
}

const readOrderSyncSQL = `SELECT stream,initial_from,completed_through,target_through,pages,scanned,versions,failed,
 source_key,next_updated_at,next_id,completed_outbox_through,target_source_through FROM dcai_order_sync WHERE stream=$1`

type syncQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readOrderSync(ctx context.Context, conn syncQueryer, stream string) (orderSyncState, error) {
	var state orderSyncState
	err := conn.QueryRow(ctx, readOrderSyncSQL, stream).Scan(&state.Stream, &state.From, &state.Completed, &state.Target,
		&state.Pages, &state.Scanned, &state.Versions, &state.Failed, &state.origin, &state.next.UpdatedAt, &state.next.ID, &state.completedOutbox, &state.targetCoverage)
	return state, err
}

func (s workspace) syncOrders(ctx context.Context, stream string, from *time.Time) (orderSyncState, error) {
	job, err := s.enqueueOrderSyncAt(ctx, stream, from)
	if err != nil {
		return orderSyncState{}, err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.runOrderSyncJob(ctx, stream, 0, job.ID); err != nil {
			return orderSyncState{}, err
		}
		var current orderSyncJob
		var completed orderSyncState
		err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			var err error
			current, err = readOrderSyncJob(ctx, tx, stream)
			if err != nil {
				return err
			}
			if current.ID != job.ID {
				return errSyncJobReplaced
			}
			if current.Status == "SUCCEEDED" {
				completed, err = readOrderSync(ctx, tx, stream)
			}
			return err
		})
		if err != nil {
			return orderSyncState{}, err
		}
		switch current.Status {
		case "SUCCEEDED":
			return completed, nil
		case "FAILED":
			return orderSyncState{}, errors.New(current.Message)
		}
		select {
		case <-ctx.Done():
			return orderSyncState{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func acquireOrderSyncLock(ctx context.Context, db *pgxpool.Pool, stream string) (*pgxpool.Conn, func(), error) {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	lockKey := "finance:dcai-order-sync:" + stream
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", lockKey).Scan(&acquired); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Hijack().Close(cleanup)
		return nil, nil, err
	}
	if !acquired {
		conn.Release()
		return nil, nil, errSyncInProgress
	}
	return conn, func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,0))", lockKey); err != nil {
			_ = conn.Hijack().Close(cleanup)
			return
		}
		conn.Release()
	}, nil
}

func syncOrdersLocked(ctx context.Context, conn *pgxpool.Conn, client orderFeedClient, stream string, from *time.Time, jobID int64) (orderSyncState, error) {
	state, err := readOrderSync(ctx, conn, stream)
	if errors.Is(err, pgx.ErrNoRows) {
		if from == nil {
			return state, errSyncInitialFrom
		}
		state.Stream, state.From, state.origin = stream, from.UTC(), client.sourceKey()
	} else if err != nil {
		return state, err
	} else if state.origin != client.sourceKey() {
		return state, errSyncOrigin
	}
	backfill := from != nil && from.Before(state.From)
	if from != nil && from.After(state.From) {
		return state, errSyncLaterStart
	}
	if backfill && state.Target != nil {
		return state, errSyncInProgress
	}
	if backfill {
		state.From = from.UTC()
	}
	if err := runOrderSync(ctx, conn, client, &state, backfill, jobID); err != nil {
		if ctx.Err() != nil {
			return state, ctx.Err()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if markErr := pgx.BeginFunc(cleanup, conn, func(tx pgx.Tx) error {
			return updateOrderSync(cleanup, tx, client, state, `UPDATE dcai_order_sync SET failed=true WHERE stream=$1`)
		}); markErr != nil {
			return state, errors.Join(err, markErr)
		}
		return state, err
	}
	return state, nil
}

func runOrderSync(ctx context.Context, conn *pgxpool.Conn, client orderFeedClient, state *orderSyncState, backfill bool, jobID int64) error {
	limit, err := client.readiness(ctx, state.Stream)
	if err != nil {
		return err
	}
	if state.Target == nil {
		cutoff, err := client.cutoff(ctx)
		if err != nil {
			return err
		}
		previousOutbox := state.completedOutbox
		if previousOutbox == nil {
			previousOutbox = state.Completed
		}
		if cutoff.Through.Before(state.From) || previousOutbox != nil && cutoff.Through.Before(*previousOutbox) ||
			state.Completed != nil && cutoff.SourceThrough.Before(*state.Completed) {
			return errSourceRead
		}
		// Page by outbox time. Only a completed source sweep establishes business coverage.
		start := state.From
		if !backfill && previousOutbox != nil && previousOutbox.Add(-24*time.Hour).After(start) {
			start = previousOutbox.Add(-24 * time.Hour)
		}
		state.Target, state.targetCoverage = &cutoff.Through, &cutoff.SourceThrough
		state.next = orderCursor{UpdatedAt: start, ID: "0"}
		state.Pages, state.Scanned, state.Versions, state.Failed = 0, 0, 0, false
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			return updateOrderSync(ctx, tx, client, *state, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,target_through,next_updated_at,next_id,target_source_through)
			 VALUES($1,$2,$3,$4,$5,'0',$6) ON CONFLICT(stream) DO UPDATE SET initial_from=excluded.initial_from,target_through=excluded.target_through,
			 target_source_through=excluded.target_source_through,next_updated_at=excluded.next_updated_at,next_id='0',pages=0,scanned=0,versions=0,failed=false`,
				state.origin, state.From, cutoff.Through, start, cutoff.SourceThrough)
		})
		if err != nil {
			return err
		}
	}
	if state.targetCoverage == nil {
		if client.practiceOnly {
			return errSourceRead
		}
		state.targetCoverage = state.Target
	}
	for {
		page, err := client.page(ctx, state.Stream, state.next, *state.Target, limit)
		if err != nil {
			return err
		}
		if !page.HasMore && client.source == "DCZD" {
			// The source can suppress conflicting rows from a page. Check its
			// reported health again before promoting the completed watermark.
			if _, err := client.readiness(ctx, state.Stream); err != nil {
				return err
			}
		}
		var facts []sourceFact
		for _, row := range page.Rows {
			facts = append(facts, row.Facts...)
		}
		updated := *state
		updated.Pages++
		updated.Scanned += int64(len(page.Rows))
		updated.next, updated.Failed = page.Next, false
		if !page.HasMore {
			updated.completedOutbox, updated.Target, updated.targetCoverage = state.Target, nil, nil
			if !state.targetCoverage.Before(state.From) {
				updated.Completed = state.targetCoverage
			}
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			inserted, err := storeSourceFactsFrom(ctx, tx, facts, state.origin)
			if err != nil {
				return err
			}
			if len(facts) == 0 {
				if err := lockOrderProvider(ctx, tx, client.source, state.origin); err != nil {
					return err
				}
			}
			if client.source == "DCAI" {
				if err := storeDCAIBlockers(ctx, tx, state.Stream, page.Rows); err != nil {
					return err
				}
			}
			updated.Versions += inserted
			_, err = tx.Exec(ctx, `UPDATE dcai_order_sync SET next_updated_at=$2,next_id=$3,pages=$4,scanned=$5,versions=$6,
			 completed_through=$7,target_through=$8,completed_outbox_through=$9,target_source_through=$10,failed=false,
			 last_success_at=CASE WHEN $8::timestamptz IS NULL THEN clock_timestamp() ELSE last_success_at END WHERE stream=$1`, state.Stream, updated.next.UpdatedAt,
				updated.next.ID, updated.Pages, updated.Scanned, updated.Versions, updated.Completed, updated.Target, updated.completedOutbox, updated.targetCoverage)
			if err == nil && !page.HasMore && jobID != 0 {
				return finishOrderSyncJob(ctx, tx, jobID, "SUCCEEDED", "")
			}
			return err
		})
		if err != nil {
			return err
		}
		*state = updated
		if !page.HasMore {
			return nil
		}
	}
}

func updateOrderSync(ctx context.Context, tx pgx.Tx, client orderFeedClient, state orderSyncState, sql string, args ...any) error {
	if err := lockOrderProvider(ctx, tx, client.source, state.origin); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, sql, append([]any{state.Stream}, args...)...)
	return err
}

func lockOrderProvider(ctx context.Context, tx pgx.Tx, source, key string) error {
	if err := touchSourceWrites(ctx, tx, []string{source}); err != nil {
		return err
	}
	return checkOrderProvider(ctx, tx, source, key, true)
}

func checkOrderProvider(ctx context.Context, tx pgx.Tx, source, key string, bind bool) error {
	var bound *string
	if err := tx.QueryRow(ctx, `SELECT provider_key FROM source_write_state WHERE source=$1`, source).Scan(&bound); err != nil {
		return err
	}
	if bound != nil {
		if key == "" {
			return errBoundSourceImport
		}
		if *bound != key {
			return errSyncOrigin
		}
		return nil
	}
	if key == "" {
		return nil
	}
	var existing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_order_versions WHERE source=$1)`, source).Scan(&existing); err != nil {
		return err
	}
	if existing {
		return errUnverifiedSource
	}
	if !bind {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE source_write_state SET provider_key=$1 WHERE source=$2`, key, source)
	return err
}

func storeDCAIBlockers(ctx context.Context, tx pgx.Tx, stream string, rows []orderPageRow) error {
	if len(rows) == 0 {
		return nil
	}
	if stream == dcaiHealthStream {
		return storeHealthCompletionState(ctx, tx, rows)
	}
	type update struct {
		ID        string    `json:"id"`
		OrderID   string    `json:"order_id"`
		UpdatedAt time.Time `json:"updated_at"`
		Reasons   []string  `json:"reasons"`
	}
	payload := make([]update, len(rows))
	for i, row := range rows {
		payload[i] = update{ID: row.Cursor.ID, OrderID: row.OrderID, UpdatedAt: row.Cursor.UpdatedAt, Reasons: row.Blockers}
	}
	_, err := tx.Exec(ctx, `WITH incoming AS MATERIALIZED (SELECT * FROM jsonb_to_recordset($2::jsonb)
	 AS i(id text,order_id text,updated_at timestamptz,reasons text[])), cleared AS (
	  UPDATE dcai_order_blockers b SET reasons='{}',cursor_updated_at=i.updated_at FROM incoming i
	  WHERE b.stream=$1 AND b.source_row_id=i.id AND b.cursor_updated_at<=i.updated_at AND cardinality(i.reasons)=0
	 ) INSERT INTO dcai_order_blockers(stream,source_row_id,order_id,cursor_updated_at,reasons)
	 SELECT $1,id,order_id,updated_at,reasons FROM incoming WHERE cardinality(reasons)>0
	 ON CONFLICT(stream,source_row_id) DO UPDATE SET order_id=excluded.order_id,
	 cursor_updated_at=excluded.cursor_updated_at,reasons=excluded.reasons WHERE dcai_order_blockers.cursor_updated_at<=excluded.cursor_updated_at`,
		stream, payload)
	return err
}

func syncOrdersCLI(ctx context.Context, cfg config, source string, args []string) error {
	if len(args) < 1 || len(args) > 2 || !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return invalid
	}
	if streamSource, _ := orderSyncSource(args[0]); streamSource != source {
		return invalid
	}
	var from *time.Time
	if len(args) == 2 {
		parsed, err := time.Parse(time.RFC3339Nano, args[1])
		if err != nil {
			return invalid
		}
		from = &parsed
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		return err
	}
	svc := workspace{db: db, sources: readSources()}
	state, err := svc.syncOrders(ctx, args[0], from)
	if err != nil {
		return err
	}
	fmt.Printf("%s 已同步至 %s，扫描 %d 笔，新增 %d 个事实版本\n", state.Stream,
		state.Completed.Format(time.RFC3339Nano), state.Scanned, state.Versions)
	return nil
}
