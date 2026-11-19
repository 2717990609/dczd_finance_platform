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

type dcaiSyncRequest struct {
	Stream   string `json:"stream"`
	FromDate string `json:"fromDate"`
}

type dcaiSyncJob struct {
	ID       int64  `json:"id"`
	Stream   string `json:"stream"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
	from     *time.Time
	provider string
}

func (j dcaiSyncJob) active() bool { return j.Status == "QUEUED" || j.Status == "RUNNING" }

func readDCAISyncJob(ctx context.Context, db syncQueryer, stream string) (dcaiSyncJob, error) {
	job := dcaiSyncJob{Stream: stream}
	err := db.QueryRow(ctx, `SELECT id,status,initial_from,source_key,message FROM dcai_sync_jobs WHERE stream=$1`, stream).
		Scan(&job.ID, &job.Status, &job.from, &job.provider, &job.Message)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return job, err
}

func (s workspace) requestDCAISync(ctx context.Context, req dcaiSyncRequest) (any, error) {
	return s.enqueueDCAISync(ctx, req)
}

func (s workspace) enqueueDCAISync(ctx context.Context, req dcaiSyncRequest) (dcaiSyncJob, error) {
	var from *time.Time
	if req.FromDate != "" {
		date, err := time.ParseInLocation(time.DateOnly, req.FromDate, shanghai)
		if err != nil {
			return dcaiSyncJob{}, invalid
		}
		from = &date
	}
	return s.enqueueDCAISyncAt(ctx, req.Stream, from)
}

func (s workspace) enqueueDCAISyncAt(ctx context.Context, stream string, from *time.Time) (dcaiSyncJob, error) {
	if stream != dcaiVIPStream && stream != dcaiHealthStream || from != nil && (!validDCAIWatermark(*from) || from.After(time.Now())) {
		return dcaiSyncJob{}, invalid
	}
	key := s.dcaiSourceKey()
	if key == "" {
		return dcaiSyncJob{}, errSourceDisabled
	}
	var job dcaiSyncJob
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		job, err = queueDCAISyncJob(ctx, tx, stream, key, from)
		return err
	})
	return job, err
}

func queueDCAISyncJob(ctx context.Context, tx pgx.Tx, stream, key string, from *time.Time) (dcaiSyncJob, error) {
	if err := touchSourceWrites(ctx, tx, []string{"DCAI"}); err != nil {
		return dcaiSyncJob{}, err
	}
	if err := checkDCAIProvider(ctx, tx, key, false); err != nil {
		return dcaiSyncJob{}, err
	}
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM configuration_options WHERE group_key='dcaiOrderStream' AND option_key=$1 AND enabled)`, stream).Scan(&enabled); err != nil {
		return dcaiSyncJob{}, err
	}
	if !enabled {
		return dcaiSyncJob{}, invalid
	}
	job, err := readDCAISyncJob(ctx, tx, stream)
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
	state, err := readDCAISync(ctx, tx, stream)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if from == nil {
			return job, errSyncInitialFrom
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
	 RETURNING id,status,message`, stream, key, from).Scan(&job.ID, &job.Status, &job.Message)
	return job, err
}

func (s workspace) runDCAISyncJob(ctx context.Context, stream string, interval time.Duration, expectedID int64) error {
	client, err := newDCAIOrderClient(s.sources["DCAI"])
	if err != nil {
		return err
	}
	conn, release, err := acquireDCAISyncLock(ctx, s.db, stream)
	if errors.Is(err, errSyncInProgress) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	job, err := readDCAISyncJob(ctx, conn, stream)
	if err != nil {
		return err
	}
	if expectedID != 0 && job.ID != expectedID {
		return errSyncJobReplaced
	}
	if !job.active() && interval > 0 {
		var due bool
		err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dcai_order_sync d
		 JOIN configuration_options c ON c.group_key='dcaiOrderStream' AND c.option_key=d.stream AND c.enabled
		 LEFT JOIN dcai_sync_jobs j USING(stream)
		 WHERE d.stream=$1 AND d.source_key=$2
		 AND greatest(coalesce(d.last_success_at,d.completed_through),j.finished_at, '-infinity'::timestamptz)
		 <= clock_timestamp()-($3::bigint * interval '1 microsecond'))`,
			stream, client.sourceKey(), interval.Microseconds()).Scan(&due)
		if err != nil {
			return err
		}
		if due {
			var from *time.Time
			if job.Status == "FAILED" && job.provider == client.sourceKey() {
				from = job.from
			}
			if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				var err error
				job, err = queueDCAISyncJob(ctx, tx, stream, client.sourceKey(), from)
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
		_, err = syncDCAIOrdersLocked(ctx, conn, client, stream, job.from, job.ID)
	}
	if err == nil || ctx.Err() != nil {
		return ctx.Err()
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if lockErr := touchSourceWrites(ctx, tx, []string{"DCAI"}); lockErr != nil {
			return lockErr
		}
		return finishDCAISyncJob(ctx, tx, job.ID, "FAILED", dcaiSyncFailureMessage(err))
	})
}

func finishDCAISyncJob(ctx context.Context, tx pgx.Tx, id int64, status, message string) error {
	tag, err := tx.Exec(ctx, `UPDATE dcai_sync_jobs SET status=$2,finished_at=clock_timestamp(),message=$3 WHERE id=$1 AND status='RUNNING'`, id, status, message)
	if err == nil && tag.RowsAffected() != 1 {
		return conflict
	}
	return err
}

func dcaiSyncFailureMessage(err error) string {
	for _, known := range []error{errSyncOrigin, errSyncInitialFrom, errSyncLaterStart, errDCAIUnverified, errDCAIFileImport} {
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

func (s workspace) startDCAISyncWorkers(ctx context.Context, interval time.Duration) func() {
	if s.dcaiSourceKey() == "" {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	for _, stream := range []string{dcaiVIPStream, dcaiHealthStream} {
		workers.Go(func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for ctx.Err() == nil {
				if err := s.runDCAISyncJob(ctx, stream, interval, 0); err != nil && ctx.Err() == nil {
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

func (s workspace) getDCAISyncJobs(w http.ResponseWriter, r *http.Request) {
	var rows json.RawMessage
	err := s.db.QueryRow(r.Context(), `SELECT coalesce(jsonb_agg(jsonb_build_object(
	 'stream',c.option_key,'initialFrom',d.initial_from,'completedThrough',d.completed_through,'targetThrough',d.target_through,
	 'requestedFromDate',to_char(j.initial_from AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD'),
	 'scannedOrders',d.scanned,'newVersions',d.versions,'taskId',j.id,'status',coalesce(j.status,'IDLE'),'message',coalesce(j.message,''),
	 'sourceMatches',coalesce(ws.provider_key=$1,true) AND coalesce(d.source_key=$1,true)
	   AND (j.status IS NULL OR j.status NOT IN ('QUEUED','RUNNING') OR j.source_key=$1),
	 'unverifiedSource',ws.provider_key IS NULL AND EXISTS(SELECT 1 FROM source_order_versions v WHERE v.source='DCAI'),
	 'lastSyncFailed',coalesce(d.failed,false)) ORDER BY c.display_order,c.option_key),'[]')
	 FROM configuration_options c LEFT JOIN dcai_order_sync d ON d.stream=c.option_key
	 LEFT JOIN dcai_sync_jobs j ON j.stream=c.option_key
	 JOIN source_write_state ws ON ws.source='DCAI'
	 WHERE c.group_key='dcaiOrderStream' AND c.enabled AND c.option_key=ANY($2::text[])`, s.dcaiSourceKey(), []string{dcaiVIPStream, dcaiHealthStream}).Scan(&rows)
	respond(w, rows, err)
}
