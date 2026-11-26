package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

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
	 'id',p.id,'name',p.name,'createdAt',p.created_at,
	 'sourceStatus',CASE WHEN p.directory_disabled THEN 'INACTIVE' ELSE p.a2_status END,
	 'statusVersion',p.directory_status_version,
	 'canRestore',p.directory_disabled AND coalesce(p.a2_status,'ACTIVE')='ACTIVE' AND p.a2_canonical_party IS NULL,
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

const personSourceAccountsSQL = `WITH source_accounts AS (
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
), matching AS (
 SELECT * FROM source_accounts
 WHERE CASE $3::text
  WHEN 'current' THEN tstzrange("validFrom","validTo",'[)') @> statement_timestamp()
  WHEN 'history' THEN NOT (tstzrange("validFrom","validTo",'[)') @> statement_timestamp())
  ELSE true END
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
	current, history := params.Get("current"), params.Get("history")
	if err != nil || (current != "" && current != "true") || (history != "" && history != "true") || (current != "" && history != "") {
		respond(w, nil, invalid)
		return
	}
	var scope string
	switch {
	case current == "true":
		scope = "current"
	case history == "true":
		scope = "history"
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), personSourceAccountsSQL, personID, offset, scope).Scan(&result)
	respond(w, result, err)
}

var errPersonSourceAccount = errors.New("账号归属未保存")

func personSourceAccountProblem(message string) error {
	return fmt.Errorf("%w，%s", errPersonSourceAccount, message)
}

type personSourceAccountInput struct {
	assignIdentityInput
	ExpectedMappingID *int64
}

func (s workspace) saveLegacyPersonSourceAccount(ctx context.Context, input assignIdentityInput) (any, error) {
	if _, err := s.appointmentOperator(ctx); err != nil {
		return nil, err
	}
	return s.assignIdentity(ctx, input)
}

func (s workspace) savePersonSourceAccount(ctx context.Context, input personSourceAccountInput) (any, error) {
	if _, err := s.appointmentOperator(ctx); err != nil {
		return nil, err
	}
	if input.ExpectedMappingID == nil || *input.ExpectedMappingID < 0 {
		return nil, invalid
	}
	return s.storeIdentity(ctx, input.assignIdentityInput, input.ExpectedMappingID)
}

func validatePersonSourceAccount(ctx context.Context, tx pgx.Tx, input assignIdentityInput, external string, at time.Time) error {
	var eligible, hasBinding, hasSourceConflict, overlaps bool
	err := tx.QueryRow(ctx, `SELECT
	 EXISTS(SELECT FROM people WHERE id=$1 AND directory_visible AND NOT directory_disabled AND coalesce(a2_status,'ACTIVE')='ACTIVE'),
	 EXISTS(SELECT FROM organization_dispatch_links WHERE entity_type='MEMBER' AND target=$2 AND external_id=$3 AND person_id<>$1),
	 EXISTS(SELECT FROM catalog_sales_people_sources a JOIN catalog_sales_context c ON c.source=a.source AND c.captured_at=a.observed_at
	  WHERE a.source=$2 AND a.external_id=$3 AND a.person_id<>$1 AND (a.valid_to IS NULL OR a.valid_to>$4)),
	 EXISTS(SELECT FROM identity_mappings WHERE source=$2 AND external_id=$3 AND valid_to>$4)`,
		input.PersonID, input.Source, external, at).Scan(&eligible, &hasBinding, &hasSourceConflict, &overlaps)
	switch {
	case err != nil:
		return err
	case !eligible:
		return personSourceAccountProblem("请选择在用的财务人员")
	case hasBinding:
		return personSourceAccountProblem("该账号已绑定另一名人员的子系统销售身份，须先核对该绑定")
	case hasSourceConflict:
		return personSourceAccountProblem("该生效时间与子系统已同步的人员归属冲突，请核对来源数据")
	case overlaps:
		return personSourceAccountProblem("该生效时间与已有账号归属区间重叠，请核对账号历史")
	}
	return nil
}
