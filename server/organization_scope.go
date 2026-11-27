package main

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

const organizationScopeCTE = `organization_scope(id,root_kind) AS (
 SELECT id,kind FROM organization_units WHERE id={{unit}}
 UNION
 SELECT child.id,parent.root_kind FROM organization_units child JOIN organization_scope parent ON parent.id=child.parent_id
)`

func organizationScopeQuery(sql, parameter string) string {
	return strings.ReplaceAll(sql, "{{unit}}", parameter+"::uuid")
}

func parseOrganizationScope(value string) (unit pgtype.UUID, err error) {
	if value != "" && unit.Scan(value) != nil {
		return unit, invalid
	}
	return unit, nil
}

// The stored company and department take priority over the current parent
// chain of a smaller unit, so moving a team cannot reclassify booked rows.
const organizationPostingMatch = `org.id=CASE org.root_kind
 WHEN 'COMPANY' THEN coalesce({{posting}}company_id,{{posting}}department_id,{{posting}}group_id)
 WHEN 'DEPARTMENT' THEN coalesce({{posting}}department_id,{{posting}}group_id)
 ELSE {{posting}}group_id END`

func organizationPostingInScope(alias string) string {
	return strings.ReplaceAll(organizationPostingMatch, "{{posting}}", alias)
}

// Saved company-direct postings remain outside department filters if the
// current tree later moves their group under a department.
var reportInOrganization = `({{unit}} IS NULL OR EXISTS (
 SELECT 1 FROM organization_scope org WHERE ` + organizationPostingInScope("") + `
 AND NOT (org.root_kind='DEPARTMENT' AND organization_frozen AND company_id IS NOT NULL AND department_id IS NULL)
))`

const orderPostingKey = `observed.source,observed.order_id,observed.line_id,observed.record_key,observed.kind`
const orderPostingFields = `a.company_id,a.department_id,a.group_id,true AS has_posting`
const orderPostingPriority = `s.id DESC NULLS LAST,observed.updated_at DESC,observed.id DESC,a.period_id DESC`
const orderPostingSources = `
  FROM source_order_versions observed
  JOIN calculation_draft_performance a ON a.source_version_id=observed.id
  LEFT JOIN settlements s ON s.period_id=a.period_id`

// Make the same posting choice once per order when a page requires a full scan.
const groupedOrderPostings = `order_postings AS MATERIALIZED (
 SELECT DISTINCT ON (` + orderPostingKey + `) ` + orderPostingKey + `,` + orderPostingFields + orderPostingSources + `
 WHERE ($1='' OR observed.source=$1) AND ($2='' OR observed.order_id=$2)
 ORDER BY ` + orderPostingKey + `,` + orderPostingPriority + `
)`

var orderScopeMatch = `CASE WHEN recognized.has_posting THEN EXISTS(SELECT 1 FROM organization_scope org
  WHERE ` + organizationPostingInScope("recognized.") + `)
 WHEN latest.kind='ORDER_PAID' AND EXISTS(
  SELECT 1 FROM source_payment_companies c WHERE c.source_version_id=latest.id
  UNION ALL SELECT 1 FROM source_order_classifications c WHERE c.source_version_id=latest.id AND c.sales_claims IS NOT NULL
  UNION ALL SELECT 1 FROM source_mappings m
   WHERE (m.source,m.domain,m.source_key)=(latest.source,latest.fact->>'domain',latest.fact->>'sourceKey')
 ) THEN ` + uncalculatedOrderInOrganization + ` ELSE false END`

var orderInOrganization = `EXISTS (
 SELECT 1 FROM (VALUES (1)) seed(n) LEFT JOIN LATERAL (
  SELECT ` + orderPostingFields + orderPostingSources + `
  WHERE (` + orderPostingKey + `)=
   (latest.source,latest.order_id,latest.line_id,latest.record_key,latest.kind)
  ORDER BY ` + orderPostingPriority + ` LIMIT 1
 ) recognized ON true
 WHERE ` + orderScopeMatch + `
)`

// For an order awaiting its first trial, use the same dated source mappings
// used by finance. A refund with a saved performance row keeps that row's
// original attribution.
const uncalculatedOrderInOrganization = `EXISTS (
 WITH RECURSIVE catalog_claims AS (
  SELECT c.sales_claims FROM source_order_classifications c WHERE c.source_version_id=latest.id
 ), mapped AS (
  SELECT product.id,product.period_mode FROM source_mappings m JOIN products product ON product.id=m.product_id
  WHERE m.source=latest.source AND m.domain=latest.fact->>'domain' AND m.source_key=latest.fact->>'sourceKey'
 ), class_registration AS (
  SELECT p.id,p.ordinal,r.starts_at,p.source_period_key,
   CASE e.membership_type WHEN 3 THEN 'new' WHEN 2 THEN 'retrain' ELSE '' END AS member
  FROM mapped m JOIN periods p ON p.product_id=m.id
  JOIN LATERAL (SELECT id,starts_at FROM practice_rosters WHERE period_key=p.source_period_key ORDER BY source_version DESC LIMIT 1) r ON true
  JOIN practice_roster_members roster ON roster.roster_id=r.id
  JOIN practice_enrollments e ON e.id=roster.enrollment_id AND e.paid_order_ids@>ARRAY[latest.order_id]
  WHERE m.period_mode='class'
 ), financial_scope AS (
  SELECT p.id,p.ordinal,latest.occurred_at AS as_of,p.source_period_key,'' AS member
  FROM mapped m JOIN periods p ON p.product_id=m.id
  WHERE m.period_mode='calendar' AND (latest.occurred_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN p.starts_on AND p.ends_on
  UNION ALL SELECT id,ordinal,starts_at,source_period_key,member FROM class_registration
 ), role AS (
  SELECT min(chosen.role) AS code FROM mapped m
  LEFT JOIN financial_scope p ON true
  JOIN LATERAL (
   SELECT rv.components FROM rule_versions rv CROSS JOIN LATERAL (SELECT ` + componentRulePeriodEndSQL + ` AS at) stopped WHERE rv.product_id=m.id
    AND (rv.period_id=p.id OR rv.period_id IS NULL AND (rv.from_ordinal IS NULL OR rv.from_ordinal<=p.ordinal))
    AND ` + matchingPracticeActivity + `
    AND (rv.effective_from IS NULL OR rv.effective_from<=coalesce(p.as_of,latest.occurred_at))
    AND (stopped.at IS NULL OR coalesce(p.as_of,latest.occurred_at)<stopped.at)
   ORDER BY ` + matchingRuleOrder + ` LIMIT 1
  ) rule ON true
  CROSS JOIN LATERAL (
   SELECT nullif(part->>'sourceRole','') AS role FROM jsonb_array_elements(rule.components) part
    WHERE part->>'type' IN ('sales','company_sales') AND (coalesce(part->>'classMember','')='' OR part->>'classMember'=coalesce(p.member,'')
      OR p.member='retrain' AND part->>'mode'='tiered' AND part->'tiers' @> '{"includeRetrain":true}')
   ORDER BY (part->>'type'='sales') DESC LIMIT 1
  ) chosen
  HAVING count(DISTINCT chosen.role)<=1
 ), merchant_claims AS (
  SELECT max(candidate::text) FILTER (WHERE candidate->>'role'='PID_USER')::jsonb AS pid,
   max(candidate::text) FILTER (WHERE candidate->>'role'='LOCK_SALES_USER')::jsonb AS lock,
   max(candidate->>'company') FILTER (WHERE candidate->>'role'='SALES_COMPANY') AS known_company
  FROM catalog_claims CROSS JOIN LATERAL jsonb_array_elements(sales_claims) candidate
  HAVING count(*) FILTER (WHERE candidate->>'role'='PID_USER')<=1
   AND count(*) FILTER (WHERE candidate->>'role'='LOCK_SALES_USER')<=1
   AND count(*) FILTER (WHERE candidate->>'role'='SALES_COMPANY')<=1
 ), actor AS (
  SELECT coalesce(pid,lock)->>'user' AS external_id,coalesce(pid,lock)->>'company' AS company_key,true AS catalog
  FROM merchant_claims WHERE (pid IS NOT NULL OR lock IS NOT NULL)
   AND (pid IS NULL OR lock IS NULL OR (
    pid->>'user'=lock->>'user' AND coalesce(pid->>'company','')<>'' AND coalesce(lock->>'company','')<>''
    AND (pid->>'company'=lock->>'company' OR pid->>'company'=known_company)
    AND (coalesce(pid->>'relationship','')='' OR coalesce(lock->>'relationship','')=''
     OR pid->>'relationship'=lock->>'relationship')
   ))
  UNION ALL
  SELECT coalesce(p.external_id,CASE WHEN r.code='DIRECT_REFERRER' THEN latest.fact->>'referrerId' END),NULL,false
  FROM role r CROSS JOIN LATERAL (
   SELECT min(p->>'userId') AS external_id FROM jsonb_array_elements(latest.fact->'participants') p
   WHERE p->>'role'=r.code AND nullif(p->>'userId','') IS NOT NULL
   HAVING count(DISTINCT p->>'userId')<=1
  ) p WHERE r.code IS NOT NULL AND NOT EXISTS(SELECT 1 FROM catalog_claims WHERE sales_claims IS NOT NULL)
 ), catalog_people_at_payment AS (
  SELECT i.person_id FROM actor x JOIN catalog_sales_context c ON x.catalog AND c.source=latest.source
  JOIN catalog_sales_people_sources i ON i.source=c.source AND i.observed_at=c.captured_at AND i.external_id=x.external_id
  WHERE i.valid_from<=latest.occurred_at AND (i.valid_to IS NULL OR i.valid_to>latest.occurred_at)
 ), people_at_payment AS (
  SELECT person_id FROM catalog_people_at_payment
  UNION
  SELECT i.person_id FROM actor x JOIN identity_mappings i ON i.source=latest.source AND i.external_id=x.external_id
  WHERE i.valid_from<=latest.occurred_at AND (i.valid_to IS NULL OR i.valid_to>latest.occurred_at)
   AND (NOT x.catalog OR NOT EXISTS(SELECT 1 FROM catalog_people_at_payment))
 ), organization_at_payment AS (
  SELECT a.unit_id,u.kind,a.kind='PRIMARY' AS is_primary
  FROM organization_assignments a JOIN organization_units u ON u.id=a.unit_id
  WHERE a.person_id=(SELECT min(person_id) FROM people_at_payment HAVING count(*)=1)
   AND a.valid_from<=latest.occurred_at AND (a.valid_to IS NULL OR a.valid_to>latest.occurred_at)
 ), catalog_company_at_payment AS (
  SELECT a.company_unit_id AS unit_id FROM actor x JOIN catalog_sales_context c ON x.catalog AND c.source=latest.source
  JOIN catalog_sales_company_aliases a ON a.source=c.source AND a.observed_at=c.captured_at AND a.external_id=x.company_key
  WHERE a.valid_from<=latest.occurred_at AND (a.valid_to IS NULL OR a.valid_to>latest.occurred_at)
 ), explicit_company AS (
  SELECT o.company_id AS unit_id FROM source_payment_companies c
  JOIN organization_company_sources o ON o.source=latest.source AND o.source_company_id=c.company_id
  WHERE c.source_version_id=latest.id AND NOT EXISTS(SELECT 1 FROM actor WHERE catalog)
   AND o.valid_from<=latest.occurred_at AND (o.valid_to IS NULL OR o.valid_to>latest.occurred_at)
  UNION
  SELECT unit_id FROM catalog_company_at_payment
  UNION
  SELECT o.company_id FROM actor x JOIN organization_company_sources o
   ON o.source=latest.source AND o.source_company_id=x.company_key
  JOIN organization_units u ON u.id=o.company_id AND u.kind='COMPANY'
  WHERE x.catalog AND NOT EXISTS(SELECT 1 FROM catalog_company_at_payment)
   AND o.valid_from<=latest.occurred_at AND (o.valid_to IS NULL OR o.valid_to>latest.occurred_at)
 ), organization_ancestry(unit_id,node_id,parent_id) AS (
  SELECT a.unit_id,u.id,u.parent_id FROM organization_at_payment a JOIN organization_units u ON u.id=a.unit_id
  WHERE a.kind<>'COMPANY' AND EXISTS(SELECT 1 FROM explicit_company)
  UNION
  SELECT a.unit_id,u.id,u.parent_id FROM organization_ancestry a JOIN organization_units u ON u.id=a.parent_id
 )
 SELECT 1 FROM organization_scope org WHERE org.id IN (
  SELECT (min(unit_id::text))::uuid FROM explicit_company HAVING count(DISTINCT unit_id)=1
  UNION
  SELECT (min(unit_id::text))::uuid FROM organization_at_payment
  WHERE kind<>'COMPANY' AND (is_primary OR NOT EXISTS(SELECT 1 FROM organization_at_payment WHERE is_primary AND kind<>'COMPANY'))
   AND (NOT EXISTS(SELECT 1 FROM explicit_company) OR
    (SELECT count(DISTINCT unit_id)=1 FROM explicit_company) AND EXISTS (
     SELECT 1 FROM organization_ancestry branch JOIN explicit_company company ON company.unit_id=branch.node_id
     WHERE branch.unit_id=organization_at_payment.unit_id
    ))
  HAVING count(DISTINCT unit_id)=1
  UNION
  SELECT (min(unit_id::text))::uuid FROM organization_at_payment
  WHERE kind='COMPANY' AND NOT EXISTS(SELECT 1 FROM explicit_company)
  HAVING count(DISTINCT unit_id)=1
 )
)`
