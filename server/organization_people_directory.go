package main

import (
	"encoding/json"
	"net/http"
)

// The roster shows finance appointments and business account identifiers side
// by side. A linked source account by itself is not a sales-company membership.
const organizationPeopleDirectorySQL = `WITH appointments AS (
 SELECT a.person_id,u.id AS unit_id,u.name,a.kind,a.position,
 (u.sales_company_type IS NOT NULL AND a.kind IN ('OWNER','MANAGER','MEMBER')) AS sales
 FROM organization_assignments a JOIN organization_units u ON u.id=a.unit_id
 WHERE tstzrange(a.valid_from,a.valid_to,'[)') @> statement_timestamp()
), accounts AS (
 SELECT person_id,source,external_id FROM identity_mappings
 WHERE tstzrange(valid_from,valid_to,'[)') @> statement_timestamp()
 UNION
 SELECT a.person_id,a.source,a.external_id FROM catalog_sales_people_sources a JOIN catalog_sales_context c
 ON c.source=a.source AND c.captured_at=a.observed_at WHERE tstzrange(a.valid_from,a.valid_to,'[)') @> statement_timestamp()
 UNION SELECT person_id,target,external_id FROM organization_dispatch_links WHERE entity_type='MEMBER'
), duties AS (
 SELECT d.person_id,s.name AS scope,coalesce(o.label,d.role) AS role FROM organization_business_duties d
 JOIN organization_duty_scopes s ON s.id=d.scope_id LEFT JOIN configuration_options o ON o.group_key='organizationDuty' AND o.option_key=d.role
 WHERE d.person_id IS NOT NULL AND tstzrange(d.valid_from,d.valid_to,'[)') @> statement_timestamp()
), matched AS (
 SELECT p.id,p.name FROM people p WHERE
 (CASE $2::text
   WHEN 'unassigned' THEN NOT EXISTS(SELECT 1 FROM appointments a WHERE a.person_id=p.id AND a.sales)
   WHEN 'conflicts' THEN p.id IN (SELECT person_id FROM appointments WHERE sales GROUP BY person_id HAVING count(DISTINCT unit_id)>1)
   WHEN 'multiple-accounts' THEN p.id IN (SELECT person_id FROM accounts GROUP BY person_id HAVING count(*)>1)
   ELSE true END)
 AND ($1='' OR p.id::text=$1 OR strpos(lower(p.name),lower($1))>0
 OR p.id IN (SELECT person_id FROM appointments WHERE strpos(lower(name||' '||position),lower($1))>0)
 OR p.id IN (SELECT person_id FROM accounts WHERE external_id=$1)
 OR p.id IN (SELECT person_id FROM duties WHERE strpos(lower(role||' '||scope),lower($1))>0))
), page AS (SELECT * FROM matched ORDER BY id DESC LIMIT 25 OFFSET $3),
rows AS (SELECT p.*,
 coalesce((SELECT jsonb_agg(DISTINCT jsonb_build_object('name',a.name,'kind',a.kind,'position',a.position,'sales',a.sales))
 FROM appointments a WHERE a.person_id=p.id),'[]') AS appointments,
 coalesce((SELECT jsonb_agg(jsonb_build_object('source',a.source,'id',a.external_id) ORDER BY a.source,a.external_id)
 FROM accounts a WHERE a.person_id=p.id),'[]') AS accounts,
 coalesce((SELECT jsonb_agg(DISTINCT(d.role||' · '||d.scope)) FROM duties d WHERE d.person_id=p.id),'[]') AS duties
 FROM page p)
SELECT jsonb_build_object('total',(SELECT count(*) FROM matched),'items',coalesce((SELECT jsonb_agg(to_jsonb(rows) ORDER BY id DESC) FROM rows),'[]'))`

func (s workspace) organizationPeopleDirectory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _, term, err := organizationAssignmentFilters(q)
	scope := q.Get("scope")
	if err != nil || len([]rune(term)) > 100 {
		respond(w, nil, invalid)
		return
	}
	switch scope {
	case "", "unassigned", "conflicts", "multiple-accounts":
	default:
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), organizationPeopleDirectorySQL, term, scope, offset).Scan(&result)
	respond(w, result, err)
}

func (s workspace) availableDispatchCompanies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	_, _, term, err := organizationAssignmentFilters(q)
	target := q.Get("target")
	if err != nil || !validDispatchTarget(target) || len([]rune(term)) > 100 {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY label,value),'[]') FROM (
	SELECT u.id AS value,u.name AS label`+readyDispatchCompanyFrom+` AND strpos(lower(u.name),lower($2))>0
	ORDER BY u.name,u.id LIMIT 50) c`, target, term).Scan(&result)
	respond(w, result, err)
}

func (s workspace) companyMemberDirectory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, at, name, err := organizationAssignmentFilters(q)
	company, ok := canonicalSourceUUID(q.Get("companyId"))
	if err != nil || !ok || at == nil || len([]rune(name)) > 100 {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), `WITH found AS (
 SELECT a.id,a.person_id AS "personId",p.name AS "personName",a.kind,a.position,a.valid_from AS "validFrom",a.valid_to AS "validTo"
 FROM organization_units u JOIN organization_assignments a ON a.unit_id=u.id JOIN people p ON p.id=a.person_id
 WHERE u.id=$1 AND u.sales_company_type IS NOT NULL AND a.kind IN ('OWNER','MEMBER','MANAGER')
 AND tstzrange(a.valid_from,a.valid_to,'[)') @> $2::timestamptz AND strpos(lower(p.name),lower($3))>0
 ), page AS (SELECT * FROM found ORDER BY (kind='OWNER') DESC,"personName",id LIMIT 25 OFFSET $4)
 SELECT jsonb_build_object('total',(SELECT count(*) FROM found),'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY (kind='OWNER') DESC,"personName",id) FROM page),'[]'))`, company, at, name, offset).Scan(&result)
	respond(w, result, err)
}
