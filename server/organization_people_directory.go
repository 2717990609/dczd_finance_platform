package main

import (
	"encoding/json"
	"net/http"
)

// Search by name, finance ID, or a source account valid at the selected date.
const directoryPersonSearchSQL = `($1='' OR p.id::text=$1 OR strpos(lower(p.name),lower($1))>0
 OR EXISTS(SELECT FROM identity_mappings m WHERE m.person_id=p.id AND m.external_id=$1
  AND tstzrange(m.valid_from,m.valid_to,'[)') @> $2::timestamptz)
 OR EXISTS(SELECT FROM catalog_sales_people_sources m JOIN catalog_sales_context c
  ON c.source=m.source AND c.captured_at=m.observed_at WHERE m.person_id=p.id AND m.external_id=$1
   AND tstzrange(m.valid_from,m.valid_to,'[)') @> $2::timestamptz)
 OR ($3::boolean AND EXISTS(SELECT FROM organization_dispatch_links l
  WHERE l.person_id=p.id AND l.entity_type='MEMBER' AND l.external_id=$1)))`

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
 SELECT p.id,p.name,CASE WHEN p.directory_disabled THEN 'INACTIVE' ELSE p.a2_status END AS "sourceStatus",p.legacy_party_id,
 CASE WHEN p.dispatch_name_managed OR p.a2_name_status='MANUAL' OR p.legacy_party_id IS NULL THEN 0
      WHEN p.a2_name_status='SOURCE_CONFIRMED' THEN 1 ELSE 2 END AS name_rank
 FROM people p WHERE p.directory_visible AND
 (CASE WHEN $2='archived' THEN p.directory_disabled OR p.a2_status='INACTIVE'
  ELSE NOT p.directory_disabled AND coalesce(p.a2_status,'ACTIVE')='ACTIVE' END) AND
 (CASE $2::text
   WHEN 'unassigned' THEN NOT EXISTS(SELECT 1 FROM appointments a WHERE a.person_id=p.id AND a.sales)
   WHEN 'conflicts' THEN p.id IN (SELECT person_id FROM appointments WHERE sales GROUP BY person_id HAVING count(DISTINCT unit_id)>1)
   WHEN 'multiple-accounts' THEN p.id IN (SELECT person_id FROM identity_mappings WHERE valid_to IS NULL GROUP BY person_id HAVING count(*)>1)
   ELSE true END)
 AND ($1='' OR p.id::text=$1 OR strpos(lower(p.name),lower($1))>0
 OR p.id IN (SELECT person_id FROM appointments WHERE strpos(lower(name||' '||position),lower($1))>0)
 OR p.id IN (SELECT person_id FROM accounts WHERE external_id=$1)
 OR p.id IN (SELECT person_id FROM duties WHERE strpos(lower(role||' '||scope),lower($1))>0))
), page AS (
 SELECT id,name,"sourceStatus",row_number() OVER (ORDER BY name_rank,name,legacy_party_id,id) AS ord
 FROM matched ORDER BY ord LIMIT 20 OFFSET $3),
rows AS (SELECT p.*,
 coalesce((SELECT jsonb_agg(DISTINCT jsonb_build_object('name',a.name,'kind',a.kind,'position',a.position,'sales',a.sales))
 FROM appointments a WHERE a.person_id=p.id),'[]') AS appointments,
 coalesce((SELECT jsonb_agg(jsonb_build_object('source',a.source,'id',a.external_id) ORDER BY a.source,a.external_id)
 FROM accounts a WHERE a.person_id=p.id),'[]') AS accounts,
 coalesce((SELECT jsonb_agg(DISTINCT(d.role||' · '||d.scope)) FROM duties d WHERE d.person_id=p.id),'[]') AS duties
 FROM page p)
SELECT jsonb_build_object('total',(SELECT count(*) FROM matched),'items',coalesce((SELECT jsonb_agg(to_jsonb(rows)-'ord' ORDER BY ord) FROM rows),'[]'))`

func (s workspace) organizationPeopleDirectory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _, term, err := organizationFilters(q, 20)
	scope := q.Get("scope")
	if err != nil || len([]rune(term)) > 100 {
		respond(w, nil, invalid)
		return
	}
	switch scope {
	case "", "unassigned", "conflicts", "multiple-accounts", "archived":
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
 WHERE u.id=$4 AND u.sales_company_type IS NOT NULL AND a.kind IN ('OWNER','MEMBER','MANAGER')
 AND tstzrange(a.valid_from,a.valid_to,'[)') @> $2::timestamptz AND `+directoryPersonSearchSQL+`
 ), page AS (SELECT * FROM found ORDER BY (kind='OWNER') DESC,"personName",id LIMIT 25 OFFSET $5)
 SELECT jsonb_build_object('total',(SELECT count(*) FROM found),'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY (kind='OWNER') DESC,"personName",id) FROM page),'[]'))`, name, at, q.Get("on") == "", company, offset).Scan(&result)
	respond(w, result, err)
}
