package main

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type orderSyncSettings struct {
	Stream          string `json:"stream"`
	Auto            bool   `json:"auto"`
	IntervalMinutes int    `json:"intervalMinutes"`
	FromDate        string `json:"fromDate"`
}

type orderSyncSettingsView struct {
	orderSyncSettings
	Configured bool `json:"configured"`
}

func (s workspace) getOrderSyncSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT stream,auto,interval_minutes,coalesce(initial_date::text,'')
	 FROM order_sync_settings`)
	if err != nil {
		respond(w, nil, err)
		return
	}
	saved, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (orderSyncSettings, error) {
		var settings orderSyncSettings
		err := row.Scan(&settings.Stream, &settings.Auto, &settings.IntervalMinutes, &settings.FromDate)
		return settings, err
	})
	if err != nil {
		respond(w, nil, err)
		return
	}
	byStream := make(map[string]orderSyncSettings, len(saved))
	for _, settings := range saved {
		byStream[settings.Stream] = settings
	}
	result := make([]orderSyncSettingsView, 0, 3)
	for _, stream := range []string{dcaiVIPStream, dcaiHealthStream, dczdOrderStream} {
		source, _ := orderSyncSource(stream)
		settings, exists := byStream[stream]
		if !exists {
			interval := s.dcaiOrderSyncInterval
			if source == "DCZD" {
				interval = s.dczdOrderSyncInterval
			}
			minutes := 30
			if interval > 0 {
				minutes = int((interval-1)/time.Minute) + 1
			}
			settings = orderSyncSettings{Stream: stream, Auto: interval > 0, IntervalMinutes: minutes}
		}
		result = append(result, orderSyncSettingsView{settings, s.orderSourceKey(source) != ""})
	}
	respond(w, result, nil)
}

func (s workspace) saveOrderSyncSettings(ctx context.Context, settings orderSyncSettings) (any, error) {
	source, _ := orderSyncSource(settings.Stream)
	if source == "" || settings.IntervalMinutes < 1 || settings.IntervalMinutes > 10080 {
		return settings, invalid
	}
	if settings.FromDate != "" {
		date, err := time.ParseInLocation(time.DateOnly, settings.FromDate, shanghai)
		if err != nil || date.After(time.Now()) {
			return settings, invalid
		}
	}
	_, err := s.db.Exec(ctx, `INSERT INTO order_sync_settings(stream,auto,interval_minutes,initial_date)
	 VALUES($1,$2,$3,nullif($4,'')::date) ON CONFLICT(stream) DO UPDATE
	 SET auto=excluded.auto,interval_minutes=excluded.interval_minutes,initial_date=excluded.initial_date`,
		settings.Stream, settings.Auto, settings.IntervalMinutes, settings.FromDate)
	return settings, err
}

func scheduledOrderSync(ctx context.Context, db syncQueryer, stream, group, key string, defaultInterval time.Duration) (bool, *time.Time, error) {
	var due bool
	var firstDate *time.Time
	err := db.QueryRow(ctx, `SELECT coalesce(os.auto,$4::bigint>0) AND
	 (d.stream IS NOT NULL OR os.initial_date IS NOT NULL) AND
	 (d.stream IS NULL OR d.source_key=$3) AND
	 greatest(d.last_success_at,d.completed_through,j.finished_at,'-infinity'::timestamptz) <= clock_timestamp()
	  - coalesce(os.interval_minutes*interval '1 minute',$4::bigint*interval '1 microsecond'),
	 CASE WHEN d.stream IS NULL THEN os.initial_date::timestamp AT TIME ZONE 'Asia/Shanghai' END
	 FROM configuration_options c
	 LEFT JOIN order_sync_settings os ON os.stream=c.option_key
	 LEFT JOIN dcai_order_sync d ON d.stream=c.option_key
	 LEFT JOIN dcai_sync_jobs j ON j.stream=c.option_key
	 WHERE c.option_key=$1 AND c.group_key=$2 AND c.enabled`, stream, group, key, defaultInterval.Microseconds()).Scan(&due, &firstDate)
	if err == pgx.ErrNoRows {
		return false, nil, nil
	}
	return due, firstDate, err
}
