package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const draftPersonnelSQL = `WITH selected_draft AS (
 SELECT period_id FROM calculation_drafts WHERE period_id=$1 AND version=$2
), postings AS (
 SELECT line.person_id,line.amount_minor,0::bigint AS refund,0::bigint AS correction
 FROM calculation_draft_lines line JOIN selected_draft d USING(period_id)
 UNION ALL
 SELECT original.person_id,0,refund.amount_minor,0
 FROM calculation_draft_refunds refund JOIN selected_draft d USING(period_id)
 JOIN calculation_draft_lines original ON original.id=refund.original_line_id
 UNION ALL
 SELECT correction.person_id,0,0,correction.amount_minor
 FROM calculation_draft_corrections correction JOIN selected_draft d USING(period_id)
), totals AS (
 SELECT person_id,sum(amount_minor) AS accrued_minor,sum(refund) AS refund_minor,sum(correction) AS correction_minor,
  sum(amount_minor)+sum(refund)+sum(correction) AS net_minor
 FROM postings GROUP BY person_id
), page AS (
 SELECT p.id AS "personId",p.name AS "personName",t.accrued_minor AS "accruedMinor",t.refund_minor AS "refundMinor",
  t.correction_minor AS "correctionMinor",t.net_minor AS "totalMinor"
 FROM totals t JOIN people p ON p.id=t.person_id ORDER BY t.net_minor DESC,p.id LIMIT 25 OFFSET $3
)
SELECT jsonb_build_object('total',(SELECT count(*) FROM totals),
 'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY "totalMinor" DESC,"personId") FROM page),'[]'))
FROM selected_draft`

func (s workspace) draftPersonnel(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	period, periodErr := strconv.ParseInt(query.Get("periodId"), 10, 64)
	version, versionErr := strconv.ParseInt(query.Get("version"), 10, 64)
	if periodErr != nil || versionErr != nil || period < 1 || version < 1 {
		respond(w, nil, invalid)
		return
	}
	page := 1
	if value := query.Get("page"); value != "" {
		var err error
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 || page > 100000 {
			respond(w, nil, invalid)
			return
		}
	}
	var result json.RawMessage
	err := s.db.QueryRow(r.Context(), draftPersonnelSQL, period, version, (page-1)*25).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		err = conflict
	}
	respond(w, result, err)
}
