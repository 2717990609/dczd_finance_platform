package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

var errPracticeRosterProduct = errors.New("签到名单属于其他产品，请核对所选班次和来源映射")

// Strategy is the source product code on DCZD practice payments. Existing
// imports with descriptive product keys still work where no numeric route exists.
const practiceRosterProductAllowed = `(scope.product_id IS NULL
 OR EXISTS(SELECT 1 FROM source_mappings m WHERE m.source='DCZD' AND m.domain='PRACTICE'
   AND m.product_id=scope.product_id AND m.source_key=strategy.source_key)
 OR NOT EXISTS(SELECT 1 FROM source_mappings m WHERE m.source='DCZD' AND m.domain='PRACTICE'
   AND (m.source_key=strategy.source_key OR m.product_id=scope.product_id AND m.source_key~'^[1-9][0-9]*$')))`

func checkPracticeRosterProduct(ctx context.Context, db catalogPeriodReader, productID int64, key string) error {
	var allowed bool
	err := db.QueryRow(ctx, `SELECT `+practiceRosterProductAllowed+`
	 FROM (SELECT $1::bigint AS product_id) scope CROSS JOIN (SELECT split_part($2,':',3) AS source_key) strategy`,
		productID, key).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return errPracticeRosterProduct
	}
	return nil
}

const practiceRosterOptionsSQL = `WITH current_period AS (
 SELECT product_id,source_period_key FROM periods WHERE id=$1 AND calendar_month IS NULL
), scope AS (
 SELECT product_id,source_period_key FROM current_period
 UNION ALL SELECT nullif($3::bigint,0),NULL::text
  WHERE $1=0 AND ($3=0 OR EXISTS(SELECT 1 FROM products p WHERE p.id=$3 AND p.period_mode='class'))
), options AS (
 SELECT r.period_key,r.source_version,r.starts_at,r.attendance_count,
  (r.starts_at AT TIME ZONE 'Asia/Shanghai')::date AS starts_on,
  r.period_key=scope.source_period_key AS current
 FROM practice_rosters r CROSS JOIN scope
 CROSS JOIN LATERAL (SELECT split_part(r.period_key,':',3) AS source_key) strategy
 WHERE NOT EXISTS(SELECT 1 FROM practice_rosters newer WHERE newer.period_key=r.period_key AND newer.source_version>r.source_version)
  AND NOT EXISTS(SELECT 1 FROM periods p WHERE p.source_period_key=r.period_key AND p.id<>$1)
  AND ` + practiceRosterProductAllowed + `
  AND ($2='' OR strpos(lower(r.period_key),lower($2))>0
    OR strpos(to_char(r.starts_at AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD'),$2)>0)
 ORDER BY current DESC NULLS LAST,r.starts_at DESC,r.period_key LIMIT 50
)
SELECT coalesce(jsonb_agg(jsonb_build_object('key',period_key,'version',source_version,'startsAt',starts_at,
 'startsOn',starts_on,'attendanceCount',attendance_count) ORDER BY current DESC NULLS LAST,starts_at DESC,period_key),'[]')
FROM options`

func (s workspace) listPracticeRosterOptions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var periodID, productID int64
	for _, filter := range []struct {
		name string
		id   *int64
	}{{"periodId", &periodID}, {"productId", &productID}} {
		raw := query.Get(filter.name)
		if raw == "" {
			continue
		}
		var err error
		*filter.id, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || *filter.id < 1 {
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
	err := s.db.QueryRow(r.Context(), practiceRosterOptionsSQL, periodID, term, productID).Scan(&rows)
	respond(w, rows, err)
}

type periodPracticeRosterInput struct {
	PeriodID                int64  `json:"periodId"`
	SourcePeriodKey         string `json:"sourcePeriodKey"`
	ExpectedSourcePeriodKey string `json:"expectedSourcePeriodKey"`
	// Older clients still send these fields when changing the association.
	StartsOn         string `json:"startsOn"`
	EndsOn           string `json:"endsOn"`
	ExpectedStartsOn string `json:"expectedStartsOn"`
	ExpectedEndsOn   string `json:"expectedEndsOn"`
}

func (s workspace) setPeriodPracticeRoster(ctx context.Context, input periodPracticeRosterInput) (any, error) {
	key := strings.TrimSpace(input.SourcePeriodKey)
	if input.PeriodID < 1 || !validPracticeKey(key) {
		return nil, invalid
	}
	changed := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var oldKey string
		var productID int64
		if err := tx.QueryRow(ctx, `SELECT p.product_id,coalesce(p.source_period_key,'')
		 FROM periods p JOIN products product ON product.id=p.product_id
		 WHERE p.id=$1 AND product.period_mode='class' AND p.calendar_month IS NULL FOR NO KEY UPDATE OF p`,
			input.PeriodID).Scan(&productID, &oldKey); err != nil {
			return err
		}
		if _, err := readSettlement(ctx, tx, input.PeriodID); err == nil {
			return errSettlementClosed
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if oldKey == key {
			return nil
		}
		if oldKey != "" || input.ExpectedSourcePeriodKey != "" {
			return conflict
		}
		if err := checkPracticeRosterProduct(ctx, tx, productID, key); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE periods SET source_period_key=$2 WHERE id=$1`,
			input.PeriodID, key); err != nil {
			return err
		}
		changed = true
		return enqueueDraftConfiguration(ctx, tx, draftRefreshRoster, key, nil)
	})
	return map[string]bool{"changed": changed}, err
}
