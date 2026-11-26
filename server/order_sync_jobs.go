package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type orderSyncRequest struct {
	Stream   string `json:"stream"`
	FromDate string `json:"fromDate"`
}

type orderSyncJob struct {
	ID       int64  `json:"id"`
	Stream   string `json:"stream"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
	from     *time.Time
	provider string
}

func orderSyncSource(stream string) (source, optionGroup string) {
	switch stream {
	case dcaiVIPStream, dcaiHealthStream:
		return "DCAI", "dcaiOrderStream"
	case dczdOrderStream:
		return "DCZD", "dczdOrderStream"
	default:
		return "", ""
	}
}

func (j orderSyncJob) active() bool { return j.Status == "QUEUED" || j.Status == "RUNNING" }

func readOrderSyncJob(ctx context.Context, db syncQueryer, stream string) (orderSyncJob, error) {
	job := orderSyncJob{Stream: stream}
	err := db.QueryRow(ctx, `SELECT id,status,initial_from,source_key,message FROM dcai_sync_jobs WHERE stream=$1`, stream).
		Scan(&job.ID, &job.Status, &job.from, &job.provider, &job.Message)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return job, err
}

func (s workspace) requestOrderSync(ctx context.Context, req orderSyncRequest) (any, error) {
	return s.enqueueOrderSync(ctx, req)
}

func (s workspace) enqueueOrderSync(ctx context.Context, req orderSyncRequest) (orderSyncJob, error) {
	var from *time.Time
	if req.FromDate != "" {
		date, err := time.ParseInLocation(time.DateOnly, req.FromDate, shanghai)
		if err != nil {
			return orderSyncJob{}, invalid
		}
		from = &date
	}
	return s.enqueueOrderSyncAt(ctx, req.Stream, from)
}

func (s workspace) enqueueOrderSyncAt(ctx context.Context, stream string, from *time.Time) (orderSyncJob, error) {
	source, _ := orderSyncSource(stream)
	if source == "" || from != nil && (!validOrderWatermark(*from) || from.After(time.Now())) {
		return orderSyncJob{}, invalid
	}
	client, err := newOrderFeedClient(source, s.sources[source])
	if err != nil {
		return orderSyncJob{}, err
	}
	var job orderSyncJob
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		job, err = queueOrderSyncJob(ctx, tx, stream, client.sourceKey(), from)
		return err
	})
	return job, err
}

func queueOrderSyncJob(ctx context.Context, tx pgx.Tx, stream, key string, from *time.Time) (orderSyncJob, error) {
	source, group := orderSyncSource(stream)
	if err := touchSourceWrites(ctx, tx, []string{source}); err != nil {
		return orderSyncJob{}, err
	}
	if err := checkOrderProvider(ctx, tx, source, key, false); err != nil {
		return orderSyncJob{}, err
	}
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM configuration_options WHERE group_key=$1 AND option_key=$2 AND enabled)`, group, stream).Scan(&enabled); err != nil {
		return orderSyncJob{}, err
	}
	if !enabled {
		return orderSyncJob{}, invalid
	}
	job, err := readOrderSyncJob(ctx, tx, stream)
	if err != nil {
		return job, err
	}
	if job.active() {
		if job.provider != key {
			return job, errSyncOrigin
		}
		if from != nil && (job.from == nil || !from.Equal(*job.from)) {
			return job, errSyncInProgress
		}
		return job, nil
	}
	state, err := readOrderSync(ctx, tx, stream)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if from == nil {
			err := tx.QueryRow(ctx, `SELECT initial_date::timestamp AT TIME ZONE 'Asia/Shanghai'
			  FROM order_sync_settings WHERE stream=$1`, stream).Scan(&from)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return job, err
			}
			if from == nil {
				return job, errSyncInitialFrom
			}
		}
	case err != nil:
		return job, err
	case state.origin != key:
		return job, errSyncOrigin
	case from != nil && from.After(state.From):
		return job, errSyncLaterStart
	case from != nil && from.Before(state.From) && state.Target != nil:
		return job, errSyncInProgress
	}
	err = tx.QueryRow(ctx, `INSERT INTO dcai_sync_jobs(stream,source_key,initial_from,status) VALUES($1,$2,$3,'QUEUED')
	 ON CONFLICT(stream) DO UPDATE SET id=excluded.id,source_key=excluded.source_key,initial_from=excluded.initial_from,
	 status='QUEUED',requested_at=clock_timestamp(),started_at=NULL,finished_at=NULL,message=''
	 RETURNING id,status,message,source_key,initial_from`, stream, key, from).Scan(&job.ID, &job.Status, &job.Message, &job.provider, &job.from)
	return job, err
}

func (s workspace) runOrderSyncJob(ctx context.Context, stream string, interval time.Duration, expectedID int64) error {
	source, group := orderSyncSource(stream)
	client, err := newOrderFeedClient(source, s.sources[source])
	if err != nil {
		return err
	}
	conn, release, err := acquireOrderSyncLock(ctx, s.db, stream)
	if errors.Is(err, errSyncInProgress) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	job, err := readOrderSyncJob(ctx, conn, stream)
	if err != nil {
		return err
	}
	if expectedID != 0 && job.ID != expectedID {
		return errSyncJobReplaced
	}
	if !job.active() && expectedID == 0 {
		due, from, err := scheduledOrderSync(ctx, conn, stream, group, client.sourceKey(), interval)
		if err != nil {
			return err
		}
		if due {
			if from == nil && job.Status == "FAILED" && job.provider == client.sourceKey() {
				from = job.from
			}
			if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				var err error
				job, err = queueOrderSyncJob(ctx, tx, stream, client.sourceKey(), from)
				return err
			}); err != nil {
				return err
			}
		}
	}
	if !job.active() {
		return nil
	}
	if _, err := conn.Exec(ctx, `UPDATE dcai_sync_jobs SET status='RUNNING',started_at=coalesce(started_at,clock_timestamp()) WHERE id=$1`, job.ID); err != nil {
		return err
	}
	if job.provider != client.sourceKey() {
		err = errSyncOrigin
	} else {
		_, err = syncOrdersLocked(ctx, conn, client, stream, job.from, job.ID)
	}
	if err == nil || ctx.Err() != nil {
		return ctx.Err()
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if lockErr := touchSourceWrites(ctx, tx, []string{source}); lockErr != nil {
			return lockErr
		}
		return finishOrderSyncJob(ctx, tx, job.ID, "FAILED", orderSyncFailureMessage(err))
	})
}

func finishOrderSyncJob(ctx context.Context, tx pgx.Tx, id int64, status, message string) error {
	tag, err := tx.Exec(ctx, `UPDATE dcai_sync_jobs SET status=$2,finished_at=clock_timestamp(),message=$3 WHERE id=$1 AND status='RUNNING'`, id, status, message)
	if err == nil && tag.RowsAffected() != 1 {
		return conflict
	}
	return err
}

func orderSyncFailureMessage(err error) string {
	for _, known := range []error{errSyncOrigin, errSyncInitialFrom, errSyncLaterStart, errUnverifiedSource, errBoundSourceImport} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, errSourceRead) {
		return "读取来源失败，请检查来源后继续同步"
	}
	if errors.Is(err, conflict) {
		return "来源版本内容冲突，请核对原始数据"
	}
	return "同步未完成，请稍后重试"
}

func (s workspace) startOrderSyncWorkers(ctx context.Context, dcaiInterval, dczdInterval time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	for _, stream := range []string{dcaiVIPStream, dcaiHealthStream, dczdOrderStream} {
		source, _ := orderSyncSource(stream)
		if s.orderSourceKey(source) == "" {
			continue
		}
		interval := dcaiInterval
		if source == "DCZD" {
			interval = dczdInterval
		}
		workers.Go(func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for ctx.Err() == nil {
				if err := s.runOrderSyncJob(ctx, stream, interval, 0); err != nil && ctx.Err() == nil {
					log.Printf("order sync worker %s could not update task state", stream)
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
				}
			}
		})
	}
	return func() { cancel(); workers.Wait() }
}

func (s workspace) getOrderSyncJobs(w http.ResponseWriter, r *http.Request) {
	var rows json.RawMessage
	err := s.db.QueryRow(r.Context(), `WITH streams(source,group_key,stream,provider) AS (
	 VALUES ('DCAI','dcaiOrderStream',$1::text,$4::text),('DCAI','dcaiOrderStream',$2,$4),('DCZD','dczdOrderStream',$3,$5)
	 ) SELECT coalesce(jsonb_agg(jsonb_build_object(
	 'source',s.source,'stream',s.stream,'initialFrom',d.initial_from,'completedThrough',d.completed_through,'targetThrough',d.target_through,
	 'requestedFromDate',to_char(j.initial_from AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD'),
	 'lastSuccessAt',d.last_success_at,'scannedOrders',d.scanned,'newVersions',d.versions,'taskId',j.id,'status',coalesce(j.status,'IDLE'),'message',coalesce(j.message,''),
	 'sourceMatches',coalesce(ws.provider_key=s.provider,true) AND coalesce(d.source_key=s.provider,true)
	   AND (j.status IS NULL OR j.status NOT IN ('QUEUED','RUNNING') OR j.source_key=s.provider),
	 'unverifiedSource',ws.provider_key IS NULL AND EXISTS(SELECT 1 FROM source_order_versions v WHERE v.source=s.source),
	 'lastSyncFailed',coalesce(d.failed,false)) ORDER BY s.source,c.display_order,s.stream),'[]')
	 FROM streams s JOIN configuration_options c ON (c.group_key,c.option_key)=(s.group_key,s.stream) AND c.enabled
	 LEFT JOIN dcai_order_sync d ON d.stream=s.stream
	 LEFT JOIN dcai_sync_jobs j ON j.stream=s.stream
	 JOIN source_write_state ws ON ws.source=s.source WHERE s.source='DCAI' OR s.provider<>''`,
		dcaiVIPStream, dcaiHealthStream, dczdOrderStream, s.dcaiSourceKey(), s.orderSourceKey("DCZD")).Scan(&rows)
	respond(w, rows, err)
}
