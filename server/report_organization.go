package main

// A draft can resolve missing ancestors from a known group. Confirmed
// postings and refunds of confirmed payments use the organization on file.
const reportOrganizationCTEs = `
 report_organization_units AS (
  SELECT coalesce(department_id,group_id) AS id FROM postings
  WHERE NOT organization_frozen AND (company_id IS NULL OR department_id IS NULL)
 ), report_organization_ancestry(unit_id,id,parent_id,kind,depth) AS (
  SELECT u.id,u.id,u.parent_id,u.kind,0 FROM organization_units u
  WHERE u.id IN (SELECT id FROM report_organization_units)
  UNION ALL
  SELECT path.unit_id,u.id,u.parent_id,u.kind,path.depth+1
  FROM report_organization_ancestry path JOIN organization_units u ON u.id=path.parent_id
  WHERE path.kind<>'COMPANY'
 ) CYCLE id SET cycle USING path,
 report_organization_paths AS (
  SELECT unit_id,
   (array_agg(id ORDER BY depth) FILTER (WHERE kind='COMPANY'))[1] AS company_id,
   (array_agg(id ORDER BY depth) FILTER (WHERE kind='DEPARTMENT'))[1] AS department_id
  FROM report_organization_ancestry WHERE NOT cycle GROUP BY unit_id
 ), organized_postings AS (
  SELECT p.period_id,p.person_id,coalesce(p.company_id,path.company_id) AS company_id,
   coalesce(p.department_id,path.department_id) AS department_id,p.group_id,
   p.performance,p.amount_minor,p.refund,p.correction,p.allocation,p.beneficiary_kind,p.organization_frozen
  FROM postings p LEFT JOIN report_organization_paths path ON NOT p.organization_frozen
   AND path.unit_id=coalesce(p.department_id,p.group_id)
   AND (p.company_id IS NULL OR p.company_id=path.company_id)
 )`

const reportPerformanceOrganizationFrozenSQL = `(
 EXISTS(SELECT FROM settlements WHERE period_id=a.period_id)
 OR EXISTS(
  SELECT FROM calculation_draft_performance original JOIN settlements USING(period_id)
  WHERE original.source_version_id=a.payment_version_id AND original.payment_version_id IS NULL
 )
)`
