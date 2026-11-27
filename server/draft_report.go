package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const currentReportPoolAllocationsSQL = `
 SELECT a.id,a.revision,a.period_id FROM selected_draft d JOIN monthly_pool_allocations a USING(period_id)
 WHERE ` + poolAllocationCurrentSQL

const commissionReportPostingsSQL = `
 SELECT d.period_id,l.person_id,l.company_id,l.department_id,l.group_id,0::bigint AS performance,l.amount_minor,0::bigint AS refund,0::bigint AS correction,0::bigint AS allocation,
  CASE WHEN (l.practice_pool_id IS NOT NULL OR l.catalog_pool_id IS NOT NULL OR l.allocation_owner_id IS NOT NULL) THEN 'pool' WHEN l.beneficiary_unit_id IS NOT NULL THEN 'company' ELSE 'person' END AS beneficiary_kind,
  EXISTS(SELECT FROM settlements WHERE period_id=l.period_id) AS organization_frozen
 FROM calculation_draft_lines l JOIN selected_draft d USING(period_id)
 WHERE NOT l.practice_tier_reference
 UNION ALL
 SELECT d.period_id,l.person_id,l.company_id,l.department_id,l.group_id,0,0,r.amount_minor,0,0,
  CASE WHEN (l.practice_pool_id IS NOT NULL OR l.catalog_pool_id IS NOT NULL OR l.allocation_owner_id IS NOT NULL) THEN 'pool' WHEN l.beneficiary_unit_id IS NOT NULL THEN 'company' ELSE 'person' END,
  EXISTS(SELECT FROM settlements WHERE period_id=l.period_id)
 FROM calculation_draft_refunds r JOIN selected_draft d USING(period_id)
 JOIN calculation_draft_lines l ON l.id=r.original_line_id
 UNION ALL
 SELECT d.period_id,c.person_id,c.company_id,c.department_id,c.group_id,0,0,0,c.amount_minor,0,
  CASE WHEN (l.practice_pool_id IS NOT NULL OR l.catalog_pool_id IS NOT NULL OR l.allocation_owner_id IS NOT NULL) THEN 'pool' WHEN c.beneficiary_unit_id IS NOT NULL THEN 'company' ELSE 'person' END,
  EXISTS(SELECT FROM settlements WHERE period_id=c.period_id)
 FROM calculation_draft_corrections c JOIN selected_draft d USING(period_id)
 JOIN calculation_draft_lines l ON l.id=c.original_line_id
 UNION ALL
 SELECT a.period_id,target.person_id,l.company_id,l.department_id,l.group_id,0,0,0,0,target.amount,target.kind,
  EXISTS(SELECT FROM settlements WHERE period_id=l.period_id)
 FROM current_pool_allocations a
 JOIN monthly_pool_allocation_shares sh ON sh.allocation_id=a.id
 JOIN monthly_pool_allocation_funding f ON f.share_id=sh.id
 JOIN calculation_draft_lines l ON l.id=f.source_line_id
 CROSS JOIN LATERAL (VALUES
   (sh.person_id,f.amount_minor,CASE WHEN sh.person_id IS NULL THEN 'retained' ELSE 'person' END),
   (NULL::bigint,-f.amount_minor,'pool')) target(person_id,amount,kind)`

var draftReportTemplateSQL = `WITH RECURSIVE ` + organizationScopeCTE + `, selected_draft AS (
 SELECT period_id,performance_ready,version FROM calculation_drafts WHERE period_id=$1 AND version=$2
), current_pool_allocations AS MATERIALIZED (` + currentReportPoolAllocationsSQL + `
), postings AS (` + commissionReportPostingsSQL + `
 UNION ALL
 SELECT d.period_id,a.person_id,a.company_id,a.department_id,a.group_id,
  ` + performancePostingMinorSQL + `,0,0,0,0,'person',` + reportPerformanceOrganizationFrozenSQL + `
 FROM calculation_draft_performance a JOIN selected_draft d USING(period_id)
 JOIN source_order_versions v ON v.id=a.source_version_id
 LEFT JOIN source_order_versions prior ON prior.id=a.prior_refund_version_id
), ` + reportOrganizationCTEs + `, matching AS (
 SELECT CASE $3::text WHEN 'person' THEN coalesce(person_id::text,nullif(beneficiary_kind,'person')) WHEN 'company' THEN company_id::text
  WHEN 'department' THEN coalesce(department_id::text,CASE
   WHEN company_id IS NOT NULL AND beneficiary_kind='company' THEN 'company-direct'
   WHEN company_id IS NOT NULL AND (beneficiary_kind IN ('pool','retained') OR allocation<>0) THEN 'pool-direct'
   WHEN company_id IS NOT NULL THEN 'direct/'||company_id ELSE group_id::text END)
  END AS id,performance,amount_minor,refund,correction,allocation
 FROM organized_postings WHERE ` + reportInOrganization + `
 AND (performance<>0 OR amount_minor<>0 OR refund<>0 OR correction<>0 OR allocation<>0)
), totals AS (
 SELECT id,sum(performance) AS performance_minor,sum(amount_minor) AS accrued_minor,
  sum(refund) AS refund_minor,sum(correction) AS correction_minor,sum(allocation) AS allocation_minor,
  sum(amount_minor)+sum(refund)+sum(correction)+sum(allocation) AS net_minor
 FROM matching GROUP BY id
), page AS (
 SELECT * FROM totals ORDER BY performance_minor DESC,net_minor DESC,id LIMIT 25 OFFSET $4
), labeled AS (
 SELECT coalesce(page.id,'') AS id,coalesce(person.name,unit.name,direct.name||' · 直属',
  CASE page.id WHEN 'pool' THEN '团队池归集' WHEN 'company' THEN '公司分润' WHEN 'company-direct' THEN '公司直属分润'
   WHEN 'pool-direct' THEN '公司直属团队池' WHEN 'retained' THEN '团队留存' ELSE '归属待核' END) AS name,
  CASE WHEN d.performance_ready THEN page.performance_minor END AS "performanceMinor",
  page.accrued_minor AS "accruedMinor",page.refund_minor AS "refundMinor",page.correction_minor AS "correctionMinor",page.allocation_minor AS "allocationMinor",
  page.net_minor AS "totalMinor"
 FROM page CROSS JOIN selected_draft d
 LEFT JOIN people person ON person.id=CASE WHEN $3='person' AND page.id NOT IN ('pool','company','retained') THEN page.id::bigint END
 LEFT JOIN organization_units unit ON unit.id=CASE WHEN $3<>'person' AND page.id NOT IN ('company-direct','pool-direct') AND strpos(page.id,'/')=0 THEN page.id::uuid END
 LEFT JOIN organization_units direct ON direct.id=CASE WHEN starts_with(page.id,'direct/') THEN split_part(page.id,'/',2)::uuid END
)
SELECT jsonb_build_object('total',(SELECT count(*) FROM totals),'performanceReady',performance_ready,
 'performanceMinor',CASE WHEN performance_ready THEN (SELECT coalesce(sum(performance_minor),0) FROM totals) END,
 'items',coalesce((SELECT jsonb_agg(to_jsonb(labeled) ORDER BY "performanceMinor" DESC,"totalMinor" DESC,id) FROM labeled),'[]'))
FROM selected_draft`

var draftReportSQL = organizationScopeQuery(draftReportTemplateSQL, "NULL")
var scopedDraftReportSQL = organizationScopeQuery(draftReportTemplateSQL, "$5")

func (s workspace) draftReport(w http.ResponseWriter, r *http.Request) {
	dimension := r.URL.Query().Get("dimension")
	if !validReportDimension(dimension) {
		respond(w, nil, invalid)
		return
	}
	period, version, offset, err := draftReportPage(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	unit, err := parseOrganizationScope(r.URL.Query().Get("unitId"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), scopedDraftReportSQL, period, version, dimension, offset, unit).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		err = conflict
	}
	respond(w, result, err)
}

func validReportDimension(value string) bool {
	return value == "person" || value == "company" || value == "department"
}

func draftReportPage(r *http.Request) (period, version int64, offset int, err error) {
	query := r.URL.Query()
	period, periodErr := strconv.ParseInt(query.Get("periodId"), 10, 64)
	version, versionErr := strconv.ParseInt(query.Get("version"), 10, 64)
	if periodErr != nil || versionErr != nil || period < 1 || version < 1 {
		return 0, 0, 0, invalid
	}
	offset, err = reportPageOffset(query.Get("page"))
	return period, version, offset, err
}

func reportPageOffset(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	page, err := strconv.Atoi(value)
	if err != nil || page < 1 || page > 100000 {
		return 0, invalid
	}
	return (page - 1) * 25, nil
}
