package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func optionalPersonID(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return nil, invalid
	}
	return &id, nil
}

func (s workspace) personDetail(w http.ResponseWriter, r *http.Request) {
	personID, err := optionalPersonID(r.URL.Query().Get("personId"))
	if err != nil || personID == nil {
		respond(w, nil, invalid)
		return
	}
	operator := financeUser(r.Context())
	var operatorID string
	canReadAccount := s.development && operator == nil
	if operator != nil {
		operatorID = operator.ID
		canReadAccount = operator.OrganizationWrite
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), `SELECT jsonb_strip_nulls(jsonb_build_object(
	 'id',p.id,'name',p.name,'createdAt',p.created_at,'sourceStatus',p.a2_status,
	 'canonicalId',(SELECT canonical.id FROM people canonical WHERE canonical.legacy_party_id=p.a2_canonical_party),
	 'accountStatus',CASE WHEN $2::boolean OR a.id::text=$3 THEN
	  CASE WHEN a.id IS NULL THEN 'UNCONFIGURED' WHEN a.enabled THEN 'ACTIVE' ELSE 'DISABLED' END END,
	 'ownLogin',CASE WHEN a.id::text=$3 THEN a.login END))
	 FROM people p LEFT JOIN finance_accounts a ON a.person_id=p.id WHERE p.id=$1`,
		personID, canReadAccount, operatorID).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteJson(w, http.StatusNotFound, map[string]string{"message": "未找到该人员档案"})
		return
	}
	respond(w, result, err)
}

const personSourceAccountsSQL = `WITH matching AS (
 SELECT i.id::text AS id,i.source,i.external_id AS "externalId",i.valid_from AS "validFrom",
  i.valid_to AS "validTo",'DIRECTORY' AS origin
 FROM identity_mappings i WHERE i.person_id=$1
 UNION ALL
 SELECT i.id::text,i.source,i.external_id,i.valid_from,i.valid_to,'CATALOG'
 FROM catalog_sales_people_sources i JOIN catalog_sales_context c ON c.source=i.source AND c.captured_at=i.observed_at
 WHERE i.person_id=$1 AND NOT EXISTS (
  SELECT 1 FROM identity_mappings m WHERE m.person_id=i.person_id AND m.source=i.source AND m.external_id=i.external_id
   AND m.valid_from=i.valid_from AND m.valid_to IS NOT DISTINCT FROM i.valid_to
 )
), page AS (
 SELECT * FROM matching ORDER BY "validFrom" DESC,source,id LIMIT 25 OFFSET $2
)
SELECT jsonb_build_object('total',(SELECT count(*) FROM matching),
 'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY "validFrom" DESC,source,id) FROM page),'[]'))`

func (s workspace) personSourceAccounts(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	personID, err := optionalPersonID(params.Get("personId"))
	if err != nil || personID == nil {
		respond(w, nil, invalid)
		return
	}
	offset, _, _, err := organizationAssignmentFilters(params)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), personSourceAccountsSQL, personID, offset).Scan(&result)
	respond(w, result, err)
}
