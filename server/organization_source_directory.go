package main

import (
	"encoding/json"
	"net/http"
)

func (s workspace) organizationSourceRelations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	person, err := optionalPersonID(q.Get("personId"))
	company, valid := canonicalSourceUUID(q.Get("companyId"))
	supplementary := q.Get("supplementary")
	if err != nil || (supplementary != "" && (supplementary != "true" || person == nil)) || (person != nil) == (q.Get("companyId") != "") || (q.Get("companyId") != "" && !valid) {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), `WITH subject AS (
 SELECT legacy_party_id AS party,NULL::uuid AS unit,id AS person FROM people WHERE id=$1
 UNION ALL SELECT a2_party_id,id,NULL::bigint FROM organization_units WHERE id=nullif($2,'')::uuid
 ), ids AS (SELECT DISTINCT a.source,a.kind,a.external_id AS "externalId",a.valid_from AS "from",a.valid_to AS "to"
 FROM subject s JOIN organization_a2_identifiers a ON a.party_id=s.party OR a.unit_id=s.unit
 WHERE NOT ($3::boolean AND a.kind='SOURCE_USER_ID' AND EXISTS (
  SELECT 1 FROM identity_mappings m WHERE m.person_id=s.person AND m.source=a.source AND m.external_id=a.external_id
   AND m.valid_from=a.valid_from AND m.valid_to IS NOT DISTINCT FROM a.valid_to
  UNION ALL
  SELECT 1 FROM catalog_sales_people_sources m JOIN catalog_sales_context c ON c.source=m.source AND c.captured_at=m.observed_at
   WHERE m.person_id=s.person AND m.source=a.source AND m.external_id=a.external_id
   AND m.valid_from=a.valid_from AND m.valid_to IS NOT DISTINCT FROM a.valid_to
 ))),
 roles AS (SELECT coalesce(o.label,r.role) AS role,r.scope_name AS scope,r.valid_from AS "from",r.valid_to AS "to"
 FROM subject s JOIN organization_a2_roles r ON r.party_id=s.party LEFT JOIN configuration_options o
 ON o.group_key='catalogAppointmentRole' AND o.option_key=r.role)
 SELECT jsonb_build_object('identifiers',coalesce((SELECT jsonb_agg(to_jsonb(ids) ORDER BY "from" DESC,source,kind,"externalId") FROM ids),'[]'),
 'roles',coalesce((SELECT jsonb_agg(to_jsonb(roles) ORDER BY "from" DESC,role,scope) FROM roles),'[]'))`, person, company, supplementary == "true").Scan(&result)
	respond(w, result, err)
}
