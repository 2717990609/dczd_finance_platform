package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	errCatalogAppointmentContext = errors.New("请先初始化大春 AI 的核算资料")
	errCatalogAppointmentOverlap = errors.New("该人员已有时间重叠的同范围计提任职")
	errCatalogAppointmentHolder  = errors.New("适用规则要求该角色在同一时点只有一位负责人，请办理交接")
)

const catalogAppointmentTargets = `SELECT 'PROJECT' AS scope,c.project_code AS key,c.project_code AS label,true AS active
 FROM product_catalog_settings c WHERE c.source_system='DCAI' GROUP BY c.project_code
 UNION ALL SELECT 'PRODUCT',p.code,p.name,true FROM products p JOIN product_catalog_settings c ON c.product_id=p.id AND c.source_system='DCAI'
 UNION ALL SELECT 'ORGANIZATION_UNIT',u.id::text,u.name,u.status='ACTIVE' FROM organization_units u`

const catalogAppointmentScopesSQL = `WITH targets AS (` + catalogAppointmentTargets + `), options AS (
 SELECT scope,jsonb_agg(jsonb_build_object('value',key,'label',label,'active',active) ORDER BY active DESC,label,key) AS items
 FROM targets GROUP BY scope
 ) SELECT jsonb_build_object('ready',EXISTS(SELECT 1 FROM catalog_sales_context WHERE source='DCAI'),
  'options',coalesce((SELECT jsonb_object_agg(scope,items) FROM options),'{}'))`

const catalogAppointmentListSQL = `WITH targets AS (` + catalogAppointmentTargets + `), matching AS (
 SELECT a.id,p.name AS "personName",a.person_id AS "personId",a.role,a.scope,a.scope_key AS "scopeKey",
  coalesce(t.label,a.scope_key) AS "scopeName",a.valid_from AS "validFrom",a.valid_to AS "validTo"
 FROM catalog_current_sales_appointments a JOIN people p ON p.id=a.person_id
 LEFT JOIN targets t ON (t.scope,t.key)=(a.scope,a.scope_key)
 WHERE a.source='DCAI'
  AND ($1::timestamptz IS NULL OR (a.valid_from<=$1 AND (a.valid_to IS NULL OR a.valid_to>$1)))
  AND ($2='' OR strpos(lower(p.name),lower($2))>0)
  AND ($3='' OR a.role=$3) AND ($4='' OR a.scope=$4) AND ($5='' OR a.scope_key=$5)
 ), page AS (
 SELECT * FROM matching ORDER BY "personName","validFrom" DESC,id LIMIT 25 OFFSET $6
 ) SELECT jsonb_build_object('total',(SELECT count(*) FROM matching),
  'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY "personName","validFrom" DESC,id) FROM page),'[]'))`

func (s workspace) catalogAppointments(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	offset, at, name, err := organizationAssignmentFilters(params)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), catalogAppointmentListSQL, at, name, params.Get("role"), params.Get("scope"),
		params.Get("scopeKey"), offset).Scan(&result)
	respond(w, result, err)
}

type catalogAppointmentTarget struct {
	Role     string `json:"role"`
	Scope    string `json:"scope"`
	ScopeKey string `json:"scopeKey"`
}

type addCatalogAppointmentInput struct {
	catalogAppointmentTarget
	PersonID  int64      `json:"personId"`
	StartsAt  time.Time  `json:"startsAt"`
	ThroughAt *time.Time `json:"throughAt"`
}

type transferCatalogAppointmentInput struct {
	endAppointmentInput
	PersonID int64 `json:"personId"`
}

type catalogAppointmentChange struct {
	catalogAppointmentTarget
	id      string
	from    time.Time
	through *time.Time
}

const catalogAppointmentRefreshSQL = `SELECT p.id FROM periods p JOIN products pro ON pro.id=p.product_id
 JOIN product_catalog_settings c ON c.product_id=pro.id AND c.source_system='DCAI'
 WHERE ($3='ORGANIZATION_UNIT' OR ($3='PROJECT' AND c.project_code=$4) OR ($3='PRODUCT' AND pro.code=$4))
 AND (($1<((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')
  AND ($2::timestamptz IS NULL OR (p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai')<$2))
 OR EXISTS(SELECT 1 FROM calculation_draft_performance a JOIN source_order_versions paid ON paid.id=a.source_version_id
  WHERE a.period_id=p.id AND a.completion_version_id IS NOT NULL AND paid.occurred_at>=$1 AND ($2 IS NULL OR paid.occurred_at<$2)))`

func (s workspace) writeCatalogAppointment(ctx context.Context, apply func(pgx.Tx, *string, time.Time) (catalogAppointmentChange, error)) (appointmentResult, error) {
	var result appointmentResult
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		var baseline time.Time
		// This row both serializes edits and pins the active appointment cohort.
		err := tx.QueryRow(ctx, `SELECT coalesce(appointments_at,captured_at) FROM catalog_sales_context
		 WHERE source='DCAI' FOR UPDATE`).Scan(&baseline)
		if errors.Is(err, pgx.ErrNoRows) {
			return errCatalogAppointmentContext
		}
		if err != nil {
			return err
		}
		change, err := apply(tx, by, baseline)
		if err != nil {
			return err
		}
		result.ID = change.id
		if change.from.IsZero() {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE catalog_sales_context SET appointments_at=$1
		 WHERE source='DCAI' AND appointments_at IS NULL`, baseline); err != nil {
			return err
		}
		return enqueueDraftConfiguration(ctx, tx, catalogAppointmentRefreshSQL, change.from, change.through, change.Scope, change.ScopeKey)
	})
	return result, err
}

func findCatalogAppointment(ctx context.Context, tx pgx.Tx, baseline time.Time, person int64, target catalogAppointmentTarget, from time.Time, through *time.Time) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM catalog_sales_appointments
	 WHERE source='DCAI' AND observed_at=$1 AND person_id=$2 AND role=$3 AND scope=$4 AND scope_key=$5
	  AND valid_from=$6 AND valid_to IS NOT DISTINCT FROM $7::timestamptz ORDER BY id LIMIT 1`,
		baseline, person, target.Role, target.Scope, target.ScopeKey, from, through).Scan(&id)
	return id, err
}

// Only rule components that resolve a single holder make an otherwise valid
// overlapping appointment ambiguous. Ordinary membership roles can coexist.
const catalogAppointmentRequiresHolder = `EXISTS(
 SELECT 1 FROM product_catalog_settings scope JOIN products pro ON pro.id=scope.product_id
 JOIN product_rule_packages p ON p.product_id=scope.product_id AND p.project_code=scope.project_code
 JOIN product_rule_versions v ON v.package_id=p.source_id AND ` + catalogVersionVisible + `
 WHERE scope.source_system='DCAI' AND ` + catalogRuleEligible + `
  AND ($3='ORGANIZATION_UNIT' OR $3='PROJECT' AND scope.project_code=$4 OR $3='PRODUCT' AND pro.code=$4)
  AND v.effective_from<coalesce(least($7::timestamptz,other.valid_to),'infinity')
  AND greatest($6,other.valid_from)<coalesce(` + catalogDisplayedThrough + `,'infinity')
  AND v.definition->'internal_sales_direct_split'->>'mode'='DCAI_INTERNAL_DIRECT'
  AND (
   EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(v.definition->'benefit_actions')='array'
    THEN v.definition->'benefit_actions' ELSE '[]'::jsonb END) a
    WHERE a->>'beneficiary_mode'='TEAM_POOL' AND a->>'allocation_owner_role'=$2
     AND ($3<>'ORGANIZATION_UNIT' OR a->>'pool_organization_unit_id'=$4))
   OR $3='ORGANIZATION_UNIT' AND $2 IN ('TEAM_LEAD','GROUP_LEAD') AND EXISTS(
    SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(v.definition->'internal_sales_direct_split'->'teams')='array'
     THEN v.definition->'internal_sales_direct_split'->'teams' ELSE '[]'::jsonb END) team
     WHERE ($2='TEAM_LEAD' AND team->>'team_unit_id'=$4)
      OR ($2='GROUP_LEAD' AND team->'seller_group_unit_ids'?$4
       AND ((NOT coalesce(v.definition->'internal_sales_direct_split'->'disabled_shares' ? 'internal-group-leader',false)
        AND CASE WHEN jsonb_typeof(v.definition->'internal_sales_direct_split'->'group_lead_rate_bps')='number'
        THEN (v.definition->'internal_sales_direct_split'->>'group_lead_rate_bps')::numeric>0 ELSE false END)
        OR EXISTS(SELECT 1 FROM jsonb_path_query(v.definition,'$.benefit_actions[*] ? (@.item_key == "internal-group-leader")') leader
         WHERE (NOT leader->'leader_scope' ? 'companies' OR leader->'leader_scope'->'companies' ? (team->>'sales_company_party_id'))
          AND (NOT leader->'leader_scope' ? 'groups' OR leader->'leader_scope'->'groups' ? $4))))
   )
  )
)`

func registerCatalogAppointment(ctx context.Context, tx pgx.Tx, baseline time.Time, by *string, person int64, target catalogAppointmentTarget, from time.Time, through *time.Time) (string, error) {
	var configured, overlaps, holderConflict bool
	err := tx.QueryRow(ctx, `WITH targets AS (`+catalogAppointmentTargets+`), overlapping_roles AS (
	 SELECT person_id,valid_from,valid_to FROM catalog_sales_appointments WHERE source='DCAI' AND observed_at=$1 AND role=$2
	  AND scope=$3 AND scope_key=$4 AND tstzrange(valid_from,valid_to,'[)') && tstzrange($6,$7,'[)')
	 )
	 SELECT EXISTS(SELECT 1 FROM targets WHERE scope=$3 AND key=$4 AND active)
	  AND EXISTS(SELECT 1 FROM configuration_options WHERE group_key='catalogAppointmentScope' AND option_key=$3 AND enabled)
	  AND EXISTS(SELECT 1 FROM configuration_options WHERE group_key='catalogAppointmentRole' AND option_key=$2 AND enabled
	   AND (NOT applies_to?'scopes' OR applies_to->'scopes'?$3)),
	 EXISTS(SELECT 1 FROM overlapping_roles WHERE person_id=$5),
	 EXISTS(SELECT 1 FROM overlapping_roles other WHERE other.person_id<>$5 AND `+catalogAppointmentRequiresHolder+`)`,
		baseline, target.Role, target.Scope, target.ScopeKey, person, from, through).Scan(&configured, &overlaps, &holderConflict)
	switch {
	case err != nil:
		return "", err
	case !configured:
		return "", invalid
	case overlaps:
		return "", errCatalogAppointmentOverlap
	case holderConflict:
		return "", errCatalogAppointmentHolder
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,valid_to,observed_at,changed_at,changed_by)
	 SELECT gen_random_uuid(),'DCAI',p.id,$2,$3,$4,$6,$7,$1,now(),$8::uuid FROM people p WHERE p.id=$5 RETURNING id::text`,
		baseline, target.Role, target.Scope, target.ScopeKey, person, from, through, by).Scan(&id)
	return id, err
}

func (s workspace) addCatalogAppointment(ctx context.Context, input addCatalogAppointmentInput) (appointmentResult, error) {
	from := input.StartsAt.UTC().Truncate(time.Microsecond)
	through := canonicalConfigTime(input.ThroughAt)
	if input.PersonID < 1 || from.IsZero() || through != nil && !through.After(from) ||
		!textOK(input.Role, 80) || !textOK(input.Scope, 32) || !textOK(input.ScopeKey, 128) {
		return appointmentResult{}, invalid
	}
	target := input.catalogAppointmentTarget
	return s.writeCatalogAppointment(ctx, func(tx pgx.Tx, by *string, baseline time.Time) (catalogAppointmentChange, error) {
		id, err := findCatalogAppointment(ctx, tx, baseline, input.PersonID, target, from, through)
		if !errors.Is(err, pgx.ErrNoRows) {
			return catalogAppointmentChange{id: id}, err
		}
		id, err = registerCatalogAppointment(ctx, tx, baseline, by, input.PersonID, target, from, through)
		return catalogAppointmentChange{catalogAppointmentTarget: target, id: id, from: from, through: through}, err
	})
}

func (s workspace) endCatalogAppointment(ctx context.Context, input endAppointmentInput) (appointmentResult, error) {
	return s.changeCatalogAppointment(ctx, input, 0)
}

func (s workspace) transferCatalogAppointment(ctx context.Context, input transferCatalogAppointmentInput) (appointmentResult, error) {
	if input.PersonID < 1 {
		return appointmentResult{}, invalid
	}
	return s.changeCatalogAppointment(ctx, input.endAppointmentInput, input.PersonID)
}

func (s workspace) changeCatalogAppointment(ctx context.Context, input endAppointmentInput, newPerson int64) (appointmentResult, error) {
	id, ok := canonicalSourceUUID(input.AssignmentID)
	at := input.At.UTC().Truncate(time.Microsecond)
	expected := canonicalConfigTime(input.ExpectedThroughAt)
	if !ok || at.IsZero() || expected != nil && !at.Before(*expected) {
		return appointmentResult{}, invalid
	}
	return s.writeCatalogAppointment(ctx, func(tx pgx.Tx, by *string, baseline time.Time) (catalogAppointmentChange, error) {
		var result catalogAppointmentChange
		var person int64
		var from time.Time
		var through *time.Time
		err := tx.QueryRow(ctx, `SELECT person_id,role,scope,scope_key,valid_from,valid_to FROM catalog_sales_appointments
		 WHERE source='DCAI' AND observed_at=$1 AND id=$2::uuid`, baseline, id).
			Scan(&person, &result.Role, &result.Scope, &result.ScopeKey, &from, &through)
		if err != nil {
			return result, err
		}
		if newPerson == person {
			return result, invalid
		}
		if through != nil && through.Equal(at) {
			if newPerson == 0 {
				return catalogAppointmentChange{id: id}, nil
			}
			replacement, err := findCatalogAppointment(ctx, tx, baseline, newPerson, result.catalogAppointmentTarget, at, expected)
			if errors.Is(err, pgx.ErrNoRows) {
				return result, errAppointmentChanged
			}
			return catalogAppointmentChange{id: replacement}, err
		}
		if !at.After(from) || (through == nil) != (expected == nil) ||
			through != nil && (!through.Equal(*expected) || !at.Before(*through)) {
			return result, errAppointmentChanged
		}
		if _, err := tx.Exec(ctx, `UPDATE catalog_sales_appointments SET valid_to=$2,changed_at=now(),changed_by=$3::uuid
		 WHERE id=$1::uuid`, id, at, by); err != nil {
			return result, err
		}
		result.id, result.from, result.through = id, at, through
		if newPerson != 0 {
			result.id, err = registerCatalogAppointment(ctx, tx, baseline, by, newPerson, result.catalogAppointmentTarget, at, through)
		}
		return result, err
	})
}
