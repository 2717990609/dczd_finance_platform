package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const periodReportMonthSQL = `coalesce(p.report_month,p.calendar_month,date_trunc('month',p.ends_on::timestamp)::date)`

// Read exactly one current or confirmed result per registered scope. Monetary
// rows still come from the same postings used by the single-period report.
var monthlyReportTemplateSQL = `WITH RECURSIVE ` + organizationScopeCTE + `, ` + organizationDirectoryScopes + `, directory_matches AS MATERIALIZED (
 SELECT id FROM directory_scopes WHERE scope=$9
), scopes AS MATERIALIZED (
 SELECT p.id AS period_id,pro.name AS product_name,p.name,p.starts_on,p.ends_on,
  CASE WHEN p.aggregate_package_id IS NOT NULL THEN 'aggregate' WHEN pro.period_mode='calendar' THEN 'calendar'
   WHEN p.calendar_month IS NOT NULL THEN 'refund_month' ELSE 'class' END AS kind,
  d.version,d.calculated_at,d.issue_count,
  d.performance_ready AND (p.aggregate_package_id IS NOT NULL OR d.rule_id IS NOT NULL OR d.catalog_refunds_ready
   OR pro.period_mode='class' AND p.calendar_month IS NOT NULL) AS performance_ready,
  d.total_minor+d.refund_minor+d.correction_minor AS commission,st.id AS settlement_id,q.generation
 FROM periods p JOIN products pro ON pro.id=p.product_id
 LEFT JOIN calculation_drafts d ON d.period_id=p.id
 LEFT JOIN settlements st ON st.period_id=p.id
 LEFT JOIN draft_refresh_queue q ON q.period_id=p.id AND st.id IS NULL
 WHERE ` + periodReportMonthSQL + `=$1::date AND ($2::bigint IS NULL OR p.product_id=$2)
  AND ($3::text='current' OR st.id IS NOT NULL)
), selected_draft AS MATERIALIZED (
 SELECT period_id,version,performance_ready FROM scopes WHERE version IS NOT NULL
), current_pool_allocations AS MATERIALIZED (` + currentReportPoolAllocationsSQL + `
), stamp AS (
 SELECT md5((jsonb_build_array($1::date,$2::bigint,$3::text,{{unit}},$8::bigint,
  (SELECT array_agg(id ORDER BY id) FROM organization_scope),
  (SELECT jsonb_agg(jsonb_build_array(period_id,product_name,name,starts_on,ends_on,version,calculated_at,issue_count,settlement_id,generation)
   ORDER BY period_id) FROM scopes),
  (SELECT jsonb_agg(jsonb_build_array(id,revision) ORDER BY id) FROM current_pool_allocations))
  || CASE WHEN $9='' THEN '[]'::jsonb ELSE jsonb_build_array($9::text,(SELECT array_agg(id ORDER BY id) FROM directory_matches)) END
 )::text) AS snapshot
), performance_events AS MATERIALIZED (
 SELECT d.period_id,v.source,v.fact->>'domain' AS domain,v.kind,v.line_id,
  coalesce(nullif(CASE WHEN a.payment_version_id IS NULL THEN v.fact->>'paymentId' ELSE v.fact->>'refundId' END,''),'') AS financial_id,
  v.order_id,v.record_key,a.person_id,a.company_id,a.department_id,a.group_id,
  ` + performancePostingMinorSQL + ` AS principal
 FROM calculation_draft_performance a JOIN selected_draft d USING(period_id)
 JOIN source_order_versions v ON v.id=a.source_version_id
 LEFT JOIN source_order_versions prior ON prior.id=a.prior_refund_version_id
), performance_groups AS MATERIALIZED (
 SELECT min(person_id) AS person_id,min(company_id::text)::uuid AS company_id,min(department_id::text)::uuid AS department_id,
  CASE WHEN count(DISTINCT group_id)=1 THEN min(group_id::text)::uuid END AS group_id,min(period_id) AS period_id,
  min(principal) AS performance,count(*) AS entries,count(DISTINCT (person_id,company_id,department_id,principal)) AS choices
 FROM performance_events GROUP BY source,domain,kind,financial_id,
  CASE WHEN financial_id='' THEN order_id END,CASE WHEN financial_id='' THEN line_id END,CASE WHEN financial_id='' THEN record_key END
), performance_status AS (
 SELECT count(*) FILTER (WHERE entries>1) AS duplicate_facts,
  coalesce(bool_or(choices>1),false) OR EXISTS(
   SELECT 1 FROM performance_events GROUP BY source,order_id,line_id,record_key,kind
   HAVING min(financial_id)<>max(financial_id) OR count(DISTINCT domain)>1
  ) AS conflicting_performance
 FROM performance_groups
), coverage AS (
 SELECT (SELECT count(*)>0 AND bool_and(performance_ready) FROM selected_draft)
   AND NOT conflicting_performance AS performance_ready,duplicate_facts,conflicting_performance
 FROM performance_status
), postings AS (` + commissionReportPostingsSQL + `
 UNION ALL
 SELECT period_id,person_id,company_id,department_id,group_id,performance,0,0,0,0,'person'
 FROM performance_groups CROSS JOIN coverage WHERE coverage.performance_ready
), scoped_postings AS (
 SELECT * FROM postings WHERE (` + reportInOrganization + `) AND ($8::bigint IS NULL OR person_id=$8)
 AND ($9='' OR coalesce(company_id,department_id,group_id) IN (SELECT id FROM directory_matches))
), period_amounts AS (
 SELECT period_id,sum(amount_minor+refund+correction+allocation) AS commission FROM scoped_postings GROUP BY period_id
), matching AS (
 SELECT CASE $4::text
  WHEN 'person' THEN coalesce(person_id::text,nullif(beneficiary_kind,'person'))
  WHEN 'company' THEN company_id::text
  WHEN 'department' THEN coalesce(department_id::text,CASE
   WHEN company_id IS NOT NULL AND beneficiary_kind='company' THEN 'company-direct/'||company_id
   WHEN company_id IS NOT NULL AND (beneficiary_kind IN ('pool','retained') OR allocation<>0) THEN 'pool-direct/'||company_id END)
  END AS id,company_id,performance,amount_minor,refund,correction,allocation
 FROM scoped_postings WHERE performance<>0 OR amount_minor<>0 OR refund<>0 OR correction<>0 OR allocation<>0
), totals AS MATERIALIZED (
 SELECT id,CASE WHEN count(DISTINCT company_id)=1 THEN min(company_id::text)::uuid END AS company_id,
  sum(performance) AS performance_minor,sum(amount_minor) AS accrued_minor,
  sum(refund) AS refund_minor,sum(correction) AS correction_minor,sum(allocation) AS allocation_minor,
  sum(amount_minor)+sum(refund)+sum(correction)+sum(allocation) AS net_minor
 FROM matching GROUP BY id
), page AS (
 SELECT * FROM totals ORDER BY performance_minor DESC,net_minor DESC,id LIMIT 25 OFFSET $5
), labeled AS (
 SELECT coalesce(page.id,'') AS id,coalesce(person.name,unit.name,
  CASE WHEN direct.id IS NOT NULL THEN direct.name||CASE WHEN starts_with(page.id,'company-direct/') THEN ' · 直属分润' ELSE ' · 直属团队池' END
   WHEN page.id='pool' THEN '团队池归集' WHEN page.id='company' THEN '公司分润'
   WHEN page.id='retained' THEN '团队留存' ELSE '归属待核' END) AS name,
  CASE WHEN coverage.performance_ready THEN page.performance_minor END AS "performanceMinor",
  CASE WHEN person.id IS NOT NULL THEN '人员 '||person.id WHEN $4='department' AND unit.id IS NOT NULL THEN owner.name END AS subtitle,
  page.accrued_minor AS "accruedMinor",page.refund_minor AS "refundMinor",page.correction_minor AS "correctionMinor",page.allocation_minor AS "allocationMinor",
  page.net_minor AS "totalMinor"
 FROM page CROSS JOIN coverage
 LEFT JOIN people person ON person.id=CASE WHEN $4='person' AND page.id NOT IN ('pool','company','retained') THEN page.id::bigint END
 LEFT JOIN organization_units unit ON unit.id=CASE WHEN $4<>'person' AND strpos(page.id,'/')=0 THEN page.id::uuid END
 LEFT JOIN organization_units direct ON direct.id=CASE WHEN strpos(page.id,'/')>0 THEN split_part(page.id,'/',2)::uuid END
 LEFT JOIN organization_units owner ON $4='department' AND owner.id=page.company_id
)
SELECT jsonb_build_object('snapshot',stamp.snapshot,'total',(SELECT count(*) FROM totals),'performanceReady',coverage.performance_ready,
 'performanceMinor',CASE WHEN coverage.performance_ready THEN (SELECT coalesce(sum(performance_minor),0) FROM totals) END,
 'commissionMinor',(SELECT coalesce(sum(net_minor),0) FROM totals),'duplicateFacts',coverage.duplicate_facts,
 'conflictingPerformance',coverage.conflicting_performance,
 'personPerformancePending',$8::bigint IS NOT NULL AND NOT coverage.performance_ready
  AND EXISTS(SELECT 1 FROM performance_events WHERE person_id=$8),
 'periods',coalesce((SELECT jsonb_agg(jsonb_build_object('id',period_id,'productName',product_name,
  'name',name,'startsOn',starts_on,'endsOn',ends_on,'kind',kind,'version',version,'commissionMinor',CASE WHEN {{unit}} IS NULL AND $8::bigint IS NULL AND $9='' THEN scopes.commission ELSE period_amounts.commission END,'issueCount',issue_count,
  'status',CASE WHEN settlement_id IS NOT NULL THEN 'SETTLED' WHEN version IS NULL THEN 'NOT_CALCULATED'
   WHEN generation IS NOT NULL THEN 'PENDING' WHEN issue_count>0 THEN 'NEEDS_REVIEW' ELSE 'DRAFT' END)
  ORDER BY product_name,ends_on,period_id) FROM scopes LEFT JOIN period_amounts USING(period_id)
  WHERE ({{unit}} IS NULL AND $8::bigint IS NULL AND $9='') OR period_amounts.period_id IS NOT NULL),'[]'),
 'items',coalesce((SELECT jsonb_agg(to_jsonb(labeled) ORDER BY "performanceMinor" DESC,"totalMinor" DESC,id) FROM labeled),'[]'))
FROM stamp CROSS JOIN coverage WHERE $6='' OR stamp.snapshot=$6`

var scopedMonthlyReportSQL = organizationScopeQuery(monthlyReportTemplateSQL, "$7")

func parseReportMonth(value string) (time.Time, error) {
	month, err := time.Parse("2006-01", value)
	if err != nil || month.Year() < 1 {
		return time.Time{}, invalid
	}
	return month, nil
}

type periodReportMonthInput struct {
	PeriodID int64  `json:"periodId"`
	Month    string `json:"month"`
}

func (s workspace) setPeriodReportMonth(ctx context.Context, input periodReportMonthInput) (any, error) {
	var month *time.Time
	if input.PeriodID < 1 {
		return nil, invalid
	}
	if input.Month != "" {
		parsed, err := parseReportMonth(input.Month)
		if err != nil {
			return nil, err
		}
		month = &parsed
	}
	var reportMonth string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT p.id FROM periods p JOIN products pro ON pro.id=p.product_id
		 WHERE p.id=$1 AND pro.period_mode='class' AND p.calendar_month IS NULL FOR NO KEY UPDATE OF p`, input.PeriodID).Scan(&id); err != nil {
			return err
		}
		if _, err := readSettlement(ctx, tx, id); err == nil {
			return errSettlementClosed
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE periods p SET report_month=$2 WHERE p.id=$1
		 RETURNING to_char(`+periodReportMonthSQL+`,'YYYY-MM')`, id, month).Scan(&reportMonth)
	})
	return map[string]string{"reportMonth": reportMonth}, err
}

func (s workspace) monthlyReport(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	month, err := parseReportMonth(query.Get("month"))
	view, dimension, scope := query.Get("view"), query.Get("dimension"), query.Get("scope")
	if err != nil || view != "current" && view != "settled" || !validReportDimension(dimension) || !validOrganizationDirectoryScope(scope) {
		respond(w, nil, invalid)
		return
	}
	var product *int64
	if value := query.Get("productId"); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			respond(w, nil, invalid)
			return
		}
		product = &id
	}
	offset, err := reportPageOffset(query.Get("page"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	snapshot := query.Get("snapshot")
	if offset > 0 && snapshot == "" {
		respond(w, nil, invalid)
		return
	}
	if snapshot != "" {
		decoded, err := hex.DecodeString(snapshot)
		if err != nil || len(decoded) != 16 {
			respond(w, nil, invalid)
			return
		}
	}
	unit, err := parseOrganizationScope(query.Get("unitId"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	personID, err := optionalPersonID(query.Get("personId"))
	if err != nil || personID != nil && dimension != "person" {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), scopedMonthlyReportSQL, month, product, view, dimension, offset, snapshot, unit, personID, scope).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		err = conflict
	}
	respond(w, result, err)
}
