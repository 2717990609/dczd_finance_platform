package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const commissionReportPostingsSQL = `
 SELECT l.person_id,l.company_id,l.department_id,0::bigint AS performance,l.amount_minor,0::bigint AS refund,0::bigint AS correction
 FROM calculation_draft_lines l JOIN selected_draft d USING(period_id)
 UNION ALL
 SELECT l.person_id,l.company_id,l.department_id,0,0,r.amount_minor,0
 FROM calculation_draft_refunds r JOIN selected_draft d USING(period_id)
 JOIN calculation_draft_lines l ON l.id=r.original_line_id
 UNION ALL
 SELECT c.person_id,c.company_id,c.department_id,0,0,0,c.amount_minor
 FROM calculation_draft_corrections c JOIN selected_draft d USING(period_id)`

const draftReportSQL = `WITH selected_draft AS (
 SELECT period_id,performance_ready FROM calculation_drafts WHERE period_id=$1 AND version=$2
), postings AS (` + commissionReportPostingsSQL + `
 UNION ALL
 SELECT a.person_id,a.company_id,a.department_id,
  CASE WHEN a.payment_version_id IS NULL THEN 1 ELSE -1 END * (v.fact->>'amountMinor')::bigint,0,0,0
 FROM calculation_draft_performance a JOIN selected_draft d USING(period_id)
 JOIN source_order_versions v ON v.id=a.source_version_id
), matching AS (
 SELECT CASE $3::text WHEN 'person' THEN person_id::text WHEN 'company' THEN company_id::text
  WHEN 'department' THEN department_id::text END AS id,performance,amount_minor,refund,correction
 FROM postings WHERE performance<>0 OR amount_minor<>0 OR refund<>0 OR correction<>0
), totals AS (
 SELECT id,sum(performance) AS performance_minor,sum(amount_minor) AS accrued_minor,
  sum(refund) AS refund_minor,sum(correction) AS correction_minor,
  sum(amount_minor)+sum(refund)+sum(correction) AS net_minor
 FROM matching GROUP BY id
), page AS (
 SELECT * FROM totals ORDER BY performance_minor DESC,net_minor DESC,id LIMIT 25 OFFSET $4
), labeled AS (
 SELECT coalesce(page.id,'') AS id,coalesce(person.name,unit.name,'归属待核') AS name,
  CASE WHEN d.performance_ready THEN page.performance_minor END AS "performanceMinor",
  page.accrued_minor AS "accruedMinor",page.refund_minor AS "refundMinor",page.correction_minor AS "correctionMinor",
  page.net_minor AS "totalMinor"
 FROM page CROSS JOIN selected_draft d
 LEFT JOIN people person ON person.id=CASE WHEN $3='person' THEN page.id::bigint END
 LEFT JOIN organization_units unit ON unit.id=CASE WHEN $3<>'person' THEN page.id::uuid END
)
SELECT jsonb_build_object('total',(SELECT count(*) FROM totals),'performanceReady',performance_ready,
 'performanceMinor',CASE WHEN performance_ready THEN (SELECT coalesce(sum(performance_minor),0) FROM totals) END,
 'items',coalesce((SELECT jsonb_agg(to_jsonb(labeled) ORDER BY "performanceMinor" DESC,"totalMinor" DESC,id) FROM labeled),'[]'))
FROM selected_draft`

func (s workspace) draftReport(w http.ResponseWriter, r *http.Request) {
	dimension := r.URL.Query().Get("dimension")
	switch dimension {
	case "person", "company", "department":
	default:
		respond(w, nil, invalid)
		return
	}
	period, version, offset, err := draftReportPage(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), draftReportSQL, period, version, dimension, offset).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		err = conflict
	}
	respond(w, result, err)
}

func draftReportPage(r *http.Request) (period, version int64, offset int, err error) {
	query := r.URL.Query()
	period, periodErr := strconv.ParseInt(query.Get("periodId"), 10, 64)
	version, versionErr := strconv.ParseInt(query.Get("version"), 10, 64)
	if periodErr != nil || versionErr != nil || period < 1 || version < 1 {
		return 0, 0, 0, invalid
	}
	page := 1
	if value := query.Get("page"); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 || page > 100000 {
			return 0, 0, 0, invalid
		}
	}
	return period, version, (page - 1) * 25, nil
}
