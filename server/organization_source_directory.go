package main

import (
	"encoding/json"
	"net/http"
)

func (s workspace) organizationSourceRelations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	person, err := optionalPersonID(q.Get("personId"))
	company, valid := canonicalSourceUUID(q.Get("companyId"))
	if err != nil || (person != nil) == (q.Get("companyId") != "") || (q.Get("companyId") != "" && !valid) {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), `WITH subject AS (
 SELECT legacy_party_id AS party,NULL::uuid AS unit FROM people WHERE id=$1
 UNION ALL SELECT a2_party_id,id FROM organization_units WHERE id=nullif($2,'')::uuid
 ), ids AS (SELECT DISTINCT a.source,a.kind,a.external_id AS "externalId",a.valid_from AS "from",a.valid_to AS "to"
 FROM subject s JOIN organization_a2_identifiers a ON a.party_id=s.party OR a.unit_id=s.unit),
 roles AS (SELECT coalesce(o.label,r.role) AS role,r.scope_name AS scope,r.valid_from AS "from",r.valid_to AS "to"
 FROM subject s JOIN organization_a2_roles r ON r.party_id=s.party LEFT JOIN configuration_options o
 ON o.group_key='catalogAppointmentRole' AND o.option_key=r.role)
 SELECT jsonb_build_object('identifiers',coalesce((SELECT jsonb_agg(to_jsonb(ids) ORDER BY "from" DESC,source,kind,"externalId") FROM ids),'[]'),
 'roles',coalesce((SELECT jsonb_agg(to_jsonb(roles) ORDER BY "from" DESC,role,scope) FROM roles),'[]'))`, person, company).Scan(&result)
	respond(w, result, err)
}
