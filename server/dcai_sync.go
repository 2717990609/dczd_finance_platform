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
	errSyncInitialFrom = errors.New("首次同步需要指定历史起点")
	errSyncOrigin      = errors.New("已保存的同步记录与当前来源不一致")
	errSyncLaterStart  = errors.New("已开始同步，历史起点可以往前补拉")
	errSyncInProgress  = errors.New("该订单流正在同步")
	errDCAIUnverified  = errors.New("库中已有未绑定来源的订单数据，请在独立库初始化同步")
	errDCAIFileImport  = errors.New("此库已绑定大春 AI 接口，请通过该来源拉取订单")
)

type dcaiSyncState struct {
	Stream    string     `json:"stream"`
	From      time.Time  `json:"initialFrom"`
	Completed *time.Time `json:"completedThrough"`
	Target    *time.Time `json:"targetThrough"`
	Pages     int64      `json:"pages"`
	Scanned   int64      `json:"scannedOrders"`
	Versions  int64      `json:"newVersions"`
	Failed    bool       `json:"lastFailed"`
	origin    string
	next      dcaiCursor
}

const readDCAISyncSQL = `SELECT stream,initial_from,completed_through,target_through,pages,scanned,versions,failed,
 source_key,next_updated_at,next_id FROM dcai_order_sync WHERE stream=$1`

func readDCAISync(ctx context.Context, conn *pgxpool.Conn, stream string) (dcaiSyncState, error) {
	var state dcaiSyncState
	err := conn.QueryRow(ctx, readDCAISyncSQL, stream).Scan(&state.Stream, &state.From, &state.Completed, &state.Target,
		&state.Pages, &state.Scanned, &state.Versions, &state.Failed, &state.origin, &state.next.UpdatedAt, &state.next.ID)
	return state, err
}

func (s workspace) syncDCAIOrders(ctx context.Context, stream string, from *time.Time) (dcaiSyncState, error) {
	if stream != dcaiVIPStream && stream != dcaiHealthStream || from != nil && !validDCAIWatermark(*from) {
		return dcaiSyncState{}, invalid
	}
	client, err := newDCAIOrderClient(s.sources["DCAI"])
	if err != nil {
		return dcaiSyncState{}, err
	}
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return dcaiSyncState{}, err
	}
	defer conn.Release()
	lockKey := "finance:dcai-order-sync:" + stream
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", lockKey).Scan(&acquired); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Hijack().Close(cleanup)
		return dcaiSyncState{}, err
	}
	if !acquired {
		return dcaiSyncState{}, errSyncInProgress
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,0))", lockKey); err != nil {
			_ = conn.Hijack().Close(cleanup)
		}
	}()
	state, err := readDCAISync(ctx, conn, stream)
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
	if err := runDCAISync(ctx, conn, client, &state, backfill); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if markErr := pgx.BeginFunc(cleanup, conn, func(tx pgx.Tx) error {
			return updateDCAISync(cleanup, tx, state, `UPDATE dcai_order_sync SET failed=true WHERE stream=$1`)
		}); markErr != nil {
			return state, errors.Join(err, markErr)
		}
		return state, err
	}
	return state, nil
}

func runDCAISync(ctx context.Context, conn *pgxpool.Conn, client dcaiOrderClient, state *dcaiSyncState, backfill bool) error {
	if err := client.readiness(ctx, state.Stream); err != nil {
		return err
	}
	if state.Target == nil {
		through, err := client.cutoff(ctx)
		if err != nil {
			return err
		}
		if through.Before(state.From) || state.Completed != nil && through.Before(*state.Completed) {
			return errSourceRead
		}
		// Back up to the contract's overlap boundary and include all row IDs at it.
		start := state.From
		if !backfill && state.Completed != nil && state.Completed.Add(-24*time.Hour).After(start) {
			start = state.Completed.Add(-24 * time.Hour)
		}
		state.Target, state.next = &through, dcaiCursor{UpdatedAt: start, ID: "0"}
		state.Pages, state.Scanned, state.Versions, state.Failed = 0, 0, 0, false
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			return updateDCAISync(ctx, tx, *state, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,target_through,next_updated_at,next_id)
			 VALUES($1,$2,$3,$4,$5,'0') ON CONFLICT(stream) DO UPDATE SET initial_from=excluded.initial_from,target_through=excluded.target_through,
			 next_updated_at=excluded.next_updated_at,next_id='0',pages=0,scanned=0,versions=0,failed=false`,
				state.origin, state.From, through, start)
		})
		if err != nil {
			return err
		}
	}
	for {
		page, err := client.page(ctx, state.Stream, state.next, *state.Target)
		if err != nil {
			return err
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
			updated.Completed, updated.Target = state.Target, nil
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			inserted, err := storeSourceFactsFrom(ctx, tx, facts, state.origin)
			if err != nil {
				return err
			}
			if len(facts) == 0 {
				if err := lockDCAIProvider(ctx, tx, state.origin); err != nil {
					return err
				}
			}
			if err := storeDCAIBlockers(ctx, tx, state.Stream, page.Rows); err != nil {
				return err
			}
			updated.Versions += inserted
			_, err = tx.Exec(ctx, `UPDATE dcai_order_sync SET next_updated_at=$2,next_id=$3,pages=$4,scanned=$5,versions=$6,
			 completed_through=$7,target_through=$8,failed=false WHERE stream=$1`, state.Stream, updated.next.UpdatedAt,
				updated.next.ID, updated.Pages, updated.Scanned, updated.Versions, updated.Completed, updated.Target)
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

func updateDCAISync(ctx context.Context, tx pgx.Tx, state dcaiSyncState, sql string, args ...any) error {
	if err := lockDCAIProvider(ctx, tx, state.origin); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, sql, append([]any{state.Stream}, args...)...)
	return err
}

func lockDCAIProvider(ctx context.Context, tx pgx.Tx, key string) error {
	if err := touchSourceWrites(ctx, tx, []string{"DCAI"}); err != nil {
		return err
	}
	return verifyDCAIProvider(ctx, tx, key)
}

func verifyDCAIProvider(ctx context.Context, tx pgx.Tx, key string) error {
	var bound *string
	if err := tx.QueryRow(ctx, `SELECT provider_key FROM source_write_state WHERE source='DCAI'`).Scan(&bound); err != nil {
		return err
	}
	if bound != nil {
		if key == "" {
			return errDCAIFileImport
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
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_order_versions WHERE source='DCAI')`).Scan(&existing); err != nil {
		return err
	}
	if existing {
		return errDCAIUnverified
	}
	_, err := tx.Exec(ctx, `UPDATE source_write_state SET provider_key=$1 WHERE source='DCAI'`, key)
	return err
}

func storeDCAIBlockers(ctx context.Context, tx pgx.Tx, stream string, rows []dcaiOrderPageRow) error {
	if len(rows) == 0 {
		return nil
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

func syncDCAIOrdersCLI(ctx context.Context, cfg config, args []string) error {
	if len(args) < 1 || len(args) > 2 || !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
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
	state, err := svc.syncDCAIOrders(ctx, args[0], from)
	if err != nil {
		return err
	}
	fmt.Printf("%s 已同步至 %s，扫描 %d 笔，新增 %d 个事实版本\n", state.Stream,
		state.Completed.Format(time.RFC3339Nano), state.Scanned, state.Versions)
	return nil
}
