package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

const draftPersonnelSQL = `WITH selected_draft AS (
 SELECT period_id,version FROM calculation_drafts WHERE period_id=$1 AND version=$2
), current_pool_allocations AS MATERIALIZED (` + currentReportPoolAllocationsSQL + `
), postings AS (` + commissionReportPostingsSQL + `
), totals AS (
 SELECT person_id,sum(amount_minor) AS accrued_minor,sum(refund) AS refund_minor,sum(correction) AS correction_minor,
  sum(allocation) AS allocation_minor,sum(amount_minor)+sum(refund)+sum(correction)+sum(allocation) AS net_minor
 FROM postings WHERE person_id IS NOT NULL GROUP BY person_id
), page AS (
 SELECT p.id AS "personId",p.name AS "personName",t.accrued_minor AS "accruedMinor",t.refund_minor AS "refundMinor",
  t.correction_minor AS "correctionMinor",t.allocation_minor AS "allocationMinor",t.net_minor AS "totalMinor"
 FROM totals t JOIN people p ON p.id=t.person_id ORDER BY t.net_minor DESC,p.id LIMIT 25 OFFSET $3
)
SELECT jsonb_build_object('total',(SELECT count(*) FROM totals),
 'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY "totalMinor" DESC,"personId") FROM page),'[]'))
FROM selected_draft`

func (s workspace) draftPersonnel(w http.ResponseWriter, r *http.Request) {
	period, version, offset, err := draftReportPage(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), draftPersonnelSQL, period, version, offset).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		err = conflict
	}
	respond(w, result, err)
}
