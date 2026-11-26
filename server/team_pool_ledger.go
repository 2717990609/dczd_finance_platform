package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

const teamPoolLedgerPageSize = 25

// The ledger projects confirmed calculation rows and shares. A share is one
// posting even when it was funded by several original calculation lines.
const teamPoolLedgerSQL = `WITH fixed AS (
 SELECT 'pool:'||pool.id AS key,pool.id,pool.product_id,pool.unit_id,pool.name,owner.name AS owner_name
 FROM practice_pools pool LEFT JOIN people owner ON owner.id=pool.owner_person_id
 WHERE ($1::bigint=0 OR pool.product_id=$1)
), line_refs AS MATERIALIZED (
 SELECT a.*,p.product_id,l.amount_minor,l.source_version_id,l.aggregate_group_key,l.component_type,
  coalesce(fixed.key,'legacy:'||jsonb_build_array(p.product_id,a.unit_id,a.item_key,a.scenario_key)::text) AS key,
  coalesce(fixed.name,action.name,l.component_type) AS name
 FROM pool_line_accounts a JOIN calculation_draft_lines l ON l.id=a.line_id JOIN periods p ON p.id=a.period_id
 LEFT JOIN fixed ON fixed.id=coalesce(l.practice_pool_id,l.catalog_pool_id)
 LEFT JOIN product_rule_versions rule ON rule.source_id=l.catalog_rule_id
 LEFT JOIN LATERAL (
  SELECT b->>'pool_name' AS name FROM jsonb_array_elements(rule.definition->'benefit_actions') b
  WHERE b->>'item_key'=l.component_type LIMIT 1
 ) action ON fixed.id IS NULL
 WHERE ($1=0 OR p.product_id=$1)
), booked_shares AS (
 SELECT share.id,a.unit_id,p.product_id,a.item_key,a.scenario_key,a.period_id,share.person_id,share.amount_minor,
  coalesce(fixed.key,'legacy:'||jsonb_build_array(p.product_id,a.unit_id,a.item_key,a.scenario_key)::text) AS key,
  coalesce(fixed.name,a.item_key) AS name
 FROM monthly_pool_allocations a JOIN monthly_pool_allocation_shares share ON share.allocation_id=a.id
 JOIN settlements s ON (s.period_id,s.draft_version)=(a.period_id,a.draft_version)
 JOIN periods p ON p.id=a.period_id
 LEFT JOIN fixed ON fixed.id::text=a.item_key AND fixed.unit_id=a.unit_id AND fixed.product_id=p.product_id
  AND a.scenario_key IN ('class','team-pool')
 WHERE ($1=0 OR p.product_id=$1)
), postings AS (
 SELECT l.key,l.name,l.product_id,l.unit_id,l.period_id,'income' AS kind,l.line_id AS entry_id,
  l.amount_minor,l.source_version_id,l.aggregate_group_key,l.component_type AS item_key,NULL::bigint AS person_id
 FROM line_refs l JOIN settlements s ON s.period_id=l.period_id WHERE l.amount_minor>0
 UNION ALL
 SELECT l.key,l.name,l.product_id,l.unit_id,r.period_id,'refund',r.id,r.amount_minor,
  r.refund_source_version_id,l.aggregate_group_key,l.component_type,NULL
 FROM calculation_draft_refunds r JOIN settlements s ON s.period_id=r.period_id
 JOIN line_refs l ON l.line_id=r.original_line_id WHERE r.amount_minor<>0
 UNION ALL
 SELECT l.key,l.name,l.product_id,l.unit_id,c.period_id,'correction',c.id,c.amount_minor,
  c.source_version_id,l.aggregate_group_key,l.component_type,NULL
 FROM calculation_draft_corrections c JOIN settlements s ON s.period_id=c.period_id
 JOIN line_refs l ON l.line_id=c.original_line_id AND l.unit_id=c.beneficiary_unit_id WHERE c.amount_minor<>0
 UNION ALL
 SELECT key,name,product_id,unit_id,period_id,CASE WHEN person_id IS NULL THEN 'retention' ELSE 'distribution' END,
  id,-amount_minor,NULL,NULL,item_key,person_id FROM booked_shares
), entries AS MATERIALIZED (
 SELECT * FROM postings WHERE ($2::text='' OR key=$2)
), account_totals AS (
 SELECT key,product_id,unit_id,max(name) FILTER(WHERE kind IN ('income','refund','correction')) AS source_name,
  max(name) AS fallback_name,sum(amount_minor) AS balance,
  sum(greatest(amount_minor,0)) AS income,sum(greatest(-amount_minor,0)) AS spent
 FROM entries GROUP BY key,product_id,unit_id
), accounts AS (
 SELECT coalesce(f.key,t.key) AS key,f.id,coalesce(f.name,t.source_name,t.fallback_name) AS name,
  coalesce(f.product_id,t.product_id) AS product_id,coalesce(f.unit_id,t.unit_id) AS unit_id,f.owner_name,
  coalesce(t.balance,0) AS balance,coalesce(t.income,0) AS income,coalesce(t.spent,0) AS spent
 FROM fixed f FULL JOIN account_totals t USING(key)
), period_totals AS (
 SELECT period_id,sum(amount_minor) AS net,sum(greatest(amount_minor,0)) AS income,sum(greatest(-amount_minor,0)) AS spent
 FROM entries GROUP BY period_id
), period_page AS (
 SELECT p.id AS "periodId",p.name AS "periodName",product.name AS "productName",
  to_char(` + periodReportMonthSQL + `,'YYYY-MM') AS month,p.ends_on AS "endsOn",s.confirmed_at AS "confirmedAt",
  t.net AS "netMinor",t.income AS "totalIncomeMinor",t.spent AS "totalSpentMinor"
 FROM period_totals t JOIN periods p ON p.id=t.period_id JOIN products product ON product.id=p.product_id
 JOIN settlements s ON s.period_id=p.id ORDER BY p.ends_on DESC,p.ordinal DESC,p.id DESC LIMIT $4 OFFSET $5
), entry_page AS (
 SELECT e.kind||'-'||e.entry_id AS id,e.kind,e.amount_minor AS "amountMinor",
  v.order_id AS "orderId",e.aggregate_group_key AS "groupKey",e.item_key AS "itemKey",person.name AS "personName",
  s.confirmed_at AS "confirmedAt",e.entry_id AS "sortId"
 FROM entries e JOIN settlements s ON s.period_id=e.period_id
 LEFT JOIN source_order_versions v ON v.id=e.source_version_id LEFT JOIN people person ON person.id=e.person_id
 WHERE e.period_id=$3
 ORDER BY e.kind,e.entry_id LIMIT $4 OFFSET $5
)
SELECT CASE
 WHEN $2='' THEN jsonb_build_object('items',coalesce((
  SELECT jsonb_agg(jsonb_build_object('key',a.key,'id',a.id,'name',a.name,'productName',p.name,'unitName',u.name,
   'ownerName',a.owner_name,'balanceMinor',a.balance,'totalIncomeMinor',a.income,'totalSpentMinor',a.spent)
   ORDER BY p.name,a.id IS NULL,a.name,a.key)
  FROM accounts a JOIN products p ON p.id=a.product_id JOIN organization_units u ON u.id=a.unit_id
 ),'[]'))
 WHEN $3=0 THEN jsonb_build_object(
  'periods',coalesce((SELECT jsonb_agg(to_jsonb(p) ORDER BY "endsOn" DESC,"periodId" DESC) FROM period_page p),'[]'),
  'total',(SELECT count(*) FROM period_totals))
 ELSE jsonb_build_object(
  'entries',coalesce((SELECT jsonb_agg(to_jsonb(e)-'sortId' ORDER BY kind,"sortId") FROM entry_page e),'[]'),
  'total',(SELECT count(*) FROM entries WHERE period_id=$3))
 END`

func (s workspace) teamPoolLedger(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	key := q.Get("key")
	productID, productErr := optionalPoolLedgerID(q.Get("productId"))
	periodID, periodErr := optionalPoolLedgerID(q.Get("periodId"))
	offset, pageErr := reportPageOffset(q.Get("page"))
	if productErr != nil || periodErr != nil || pageErr != nil || key != "" && !textOK(key, 400) || key == "" && periodID > 0 {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err := s.db.QueryRow(r.Context(), teamPoolLedgerSQL, productID, key, periodID, teamPoolLedgerPageSize, offset).Scan(&result)
	respond(w, result, err)
}

func optionalPoolLedgerID(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, invalid
	}
	return id, nil
}
