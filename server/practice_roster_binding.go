package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const practiceRosterOptionsSQL = `WITH current_period AS (
 SELECT source_period_key FROM periods WHERE id=$1
), options AS (
 SELECT r.period_key,r.source_version,r.starts_at,r.attendance_count,
  (r.starts_at AT TIME ZONE 'Asia/Shanghai')::date AS starts_on,
  r.period_key=(SELECT source_period_key FROM current_period) AS current
 FROM practice_rosters r
 WHERE NOT EXISTS(SELECT 1 FROM practice_rosters newer WHERE newer.period_key=r.period_key AND newer.source_version>r.source_version)
  AND NOT EXISTS(SELECT 1 FROM periods p WHERE p.source_period_key=r.period_key AND p.id<>$1)
  AND ($2='' OR strpos(lower(r.period_key),lower($2))>0
    OR strpos(to_char(r.starts_at AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD'),$2)>0)
 ORDER BY current DESC NULLS LAST,r.starts_at DESC,r.period_key LIMIT 50
)
SELECT coalesce(jsonb_agg(jsonb_build_object('key',period_key,'version',source_version,'startsAt',starts_at,
 'startsOn',starts_on,'attendanceCount',attendance_count) ORDER BY current DESC NULLS LAST,starts_at DESC,period_key),'[]')
FROM options`

func (s workspace) listPracticeRosterOptions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var periodID int64
	if raw := query.Get("periodId"); raw != "" {
		var err error
		periodID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || periodID < 1 {
			respond(w, nil, invalid)
			return
		}
	}
	term := strings.TrimSpace(query.Get("q"))
	if term != "" && !textOK(term, 80) {
		respond(w, nil, invalid)
		return
	}
	var rows json.RawMessage
	err := s.db.QueryRow(r.Context(), practiceRosterOptionsSQL, periodID, term).Scan(&rows)
	respond(w, rows, err)
}

type periodPracticeRosterInput struct {
	PeriodID                int64  `json:"periodId"`
	SourcePeriodKey         string `json:"sourcePeriodKey"`
	StartsOn                string `json:"startsOn"`
	EndsOn                  string `json:"endsOn"`
	ExpectedSourcePeriodKey string `json:"expectedSourcePeriodKey"`
	ExpectedStartsOn        string `json:"expectedStartsOn"`
	ExpectedEndsOn          string `json:"expectedEndsOn"`
}

func (s workspace) setPeriodPracticeRoster(ctx context.Context, input periodPracticeRosterInput) (any, error) {
	key := strings.TrimSpace(input.SourcePeriodKey)
	start, startErr := time.Parse(time.DateOnly, input.StartsOn)
	end, endErr := time.Parse(time.DateOnly, input.EndsOn)
	if input.PeriodID < 1 || startErr != nil || endErr != nil || end.Before(start) || key != "" && !validPracticeKey(key) {
		return nil, invalid
	}
	changed := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var oldKey, oldStart, oldEnd string
		if err := tx.QueryRow(ctx, `SELECT coalesce(p.source_period_key,''),to_char(p.starts_on,'YYYY-MM-DD'),to_char(p.ends_on,'YYYY-MM-DD')
		 FROM periods p JOIN products product ON product.id=p.product_id
		 WHERE p.id=$1 AND product.period_mode='class' AND p.calendar_month IS NULL FOR NO KEY UPDATE OF p`,
			input.PeriodID).Scan(&oldKey, &oldStart, &oldEnd); err != nil {
			return err
		}
		if _, err := readSettlement(ctx, tx, input.PeriodID); err == nil {
			return errSettlementClosed
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if oldKey == key && oldStart == input.StartsOn && oldEnd == input.EndsOn {
			return nil
		}
		if oldKey != input.ExpectedSourcePeriodKey || oldStart != input.ExpectedStartsOn || oldEnd != input.ExpectedEndsOn {
			return conflict
		}
		if key != "" {
			var inRange *bool
			if err := tx.QueryRow(ctx, `SELECT (SELECT (starts_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN $2::text::date AND $3::text::date
			 FROM practice_rosters WHERE period_key=$1 ORDER BY source_version DESC LIMIT 1)`, key, input.StartsOn, input.EndsOn).Scan(&inRange); err != nil {
				return err
			}
			if inRange != nil && !*inRange {
				return errPracticeStart
			}
		}
		if oldKey != "" && oldKey != key {
			// Enqueue classmates sharing payments and later tiers while the old series is still linked.
			if err := enqueueDraftPeriods(ctx, tx, draftRefreshRoster, oldKey, nil); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE periods SET source_period_key=nullif($2,''),starts_on=$3::text::date,ends_on=$4::text::date WHERE id=$1`,
			input.PeriodID, key, input.StartsOn, input.EndsOn); err != nil {
			return err
		}
		changed = true
		if key == "" {
			return enqueueDraftConfiguration(ctx, tx, `SELECT $1::bigint`, input.PeriodID)
		}
		return enqueueDraftConfiguration(ctx, tx, draftRefreshRoster, key, nil)
	})
	return map[string]bool{"changed": changed}, err
}
