package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

var errBusinessDutyOverlap = errors.New("该主体在相同职责和适用范围已有重叠任职")

const organizationDutyScopesSQL = `SELECT jsonb_build_object(
 'scopes',coalesce((SELECT jsonb_agg(jsonb_build_object('value',id,'label',name,'kind',kind,'code',code,'active',active)
 ORDER BY kind,name,id) FROM organization_duty_scopes),'[]'),
 'roles',coalesce((SELECT jsonb_agg(jsonb_build_object('value',option_key,'label',label,'active',enabled,
 'scopes',applies_to->'scopes','subjects',applies_to->'subjects') ORDER BY display_order,option_key)
 FROM configuration_options WHERE group_key='organizationDuty'),'[]'))`

const organizationBusinessDutiesSQL = `WITH matching AS (
 SELECT d.id,d.role,d.scope_id AS "scopeId",s.kind AS "scopeKind",s.name AS "scopeName",s.code AS "scopeCode",
  d.person_id AS "personId",d.unit_id AS "unitId",coalesce(p.name,u.name) AS "subjectName",
  d.valid_from AS "validFrom",d.valid_to AS "validTo"
 FROM organization_business_duties d JOIN organization_duty_scopes s ON s.id=d.scope_id
 LEFT JOIN people p ON p.id=d.person_id LEFT JOIN organization_units u ON u.id=d.unit_id
 WHERE ($1::timestamptz IS NULL OR (d.valid_from<=$1 AND (d.valid_to IS NULL OR d.valid_to>$1)))
 AND ($2='' OR strpos(lower(coalesce(p.name,u.name)),lower($2))>0)
 AND ($3='' OR d.role=$3) AND ($4::uuid IS NULL OR d.scope_id=$4)
 AND ($5::bigint=0 OR d.person_id=$5) AND ($6::uuid IS NULL OR d.unit_id=$6)
), page AS (
 SELECT * FROM matching ORDER BY "subjectName","validFrom" DESC,id LIMIT 25 OFFSET $7
) SELECT jsonb_build_object('total',(SELECT count(*) FROM matching),
 'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY "subjectName","validFrom" DESC,id) FROM page),'[]'))`

func (s workspace) organizationBusinessDuties(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	offset, at, name, err := organizationAssignmentFilters(params)
	scope, scopeErr := parseOrganizationScope(params.Get("scopeId"))
	unit, unitErr := parseOrganizationScope(params.Get("unitId"))
	var person int64
	if value := params.Get("personId"); value != "" {
		var personErr error
		person, personErr = strconv.ParseInt(value, 10, 64)
		if personErr != nil || person <= 0 {
			respond(w, nil, invalid)
			return
		}
	}
	if err != nil || scopeErr != nil || unitErr != nil || len([]rune(name)) > 80 {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), organizationBusinessDutiesSQL, at, name, params.Get("role"), scope, person, unit, offset).Scan(&result)
	respond(w, result, err)
}

type dutySubject struct {
	PersonID int64  `json:"personId"`
	UnitID   string `json:"unitId"`
}

func (owner *dutySubject) normalize() bool {
	if owner.UnitID == "" {
		return owner.PersonID > 0
	}
	unit, ok := canonicalSourceUUID(owner.UnitID)
	owner.UnitID = unit
	return ok && owner.PersonID == 0
}

func (owner dutySubject) kind() string {
	if owner.PersonID > 0 {
		return "PERSON"
	}
	return "ORGANIZATION"
}

type createBusinessDutyInput struct {
	dutySubject
	Role      string     `json:"role"`
	ScopeID   string     `json:"scopeId"`
	StartsAt  time.Time  `json:"startsAt"`
	ThroughAt *time.Time `json:"throughAt"`
}

type transferBusinessDutyInput struct {
	endAppointmentInput
	dutySubject
}

func findBusinessDuty(ctx context.Context, tx pgx.Tx, input createBusinessDutyInput) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM organization_business_duties WHERE scope_id=$1::uuid AND role=$2
	 AND person_id IS NOT DISTINCT FROM nullif($3::bigint,0)
	 AND unit_id IS NOT DISTINCT FROM nullif($4,'')::uuid AND valid_from=$5
	 AND valid_to IS NOT DISTINCT FROM $6::timestamptz`, input.ScopeID, input.Role, input.PersonID,
		input.UnitID, input.StartsAt, input.ThroughAt).Scan(&id)
	return id, err
}

func registerBusinessDuty(ctx context.Context, tx pgx.Tx, input createBusinessDutyInput, by *string) (string, error) {
	var kind string
	var scopeActive bool
	if err := tx.QueryRow(ctx, "SELECT kind,active FROM organization_duty_scopes WHERE id=$1::uuid FOR UPDATE", input.ScopeID).
		Scan(&kind, &scopeActive); err != nil {
		return "", err
	}
	if id, err := findBusinessDuty(ctx, tx, input); !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	var configured, overlap bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM configuration_options WHERE group_key='organizationDuty'
	 AND enabled AND option_key=$2 AND applies_to->'scopes'?$7 AND applies_to->'subjects'?$8)
	 AND CASE WHEN $3::bigint>0 THEN EXISTS(SELECT 1 FROM people WHERE id=$3)
	 ELSE EXISTS(SELECT 1 FROM organization_units WHERE id=nullif($4,'')::uuid AND status='ACTIVE') END,
	 EXISTS(SELECT 1 FROM organization_business_duties WHERE scope_id=$1::uuid AND role=$2
	 AND person_id IS NOT DISTINCT FROM nullif($3::bigint,0) AND unit_id IS NOT DISTINCT FROM nullif($4,'')::uuid
	 AND tstzrange(valid_from,valid_to,'[)') && tstzrange($5,$6,'[)'))`,
		input.ScopeID, input.Role, input.PersonID, input.UnitID, input.StartsAt, input.ThroughAt, kind, input.kind()).
		Scan(&configured, &overlap)
	switch {
	case err != nil:
		return "", err
	case !scopeActive || !configured:
		return "", invalid
	case overlap:
		return "", errBusinessDutyOverlap
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO organization_business_duties
	 (id,role,scope_id,person_id,unit_id,valid_from,valid_to,changed_at,changed_by)
	 VALUES(gen_random_uuid(),$2,$1::uuid,nullif($3::bigint,0),nullif($4,'')::uuid,$5,$6,now(),$7::uuid) RETURNING id::text`,
		input.ScopeID, input.Role, input.PersonID, input.UnitID, input.StartsAt, input.ThroughAt, by).Scan(&id)
	return id, err
}

func (s workspace) addBusinessDuty(ctx context.Context, input createBusinessDutyInput) (appointmentResult, error) {
	var result appointmentResult
	scope, scopeOK := canonicalSourceUUID(input.ScopeID)
	input.ScopeID = scope
	input.StartsAt = input.StartsAt.UTC().Truncate(time.Microsecond)
	input.ThroughAt = canonicalConfigTime(input.ThroughAt)
	if !scopeOK || !input.dutySubject.normalize() || !textOK(input.Role, 80) || input.StartsAt.IsZero() ||
		input.ThroughAt != nil && !input.ThroughAt.After(input.StartsAt) {
		return result, invalid
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		var err error
		result.ID, err = registerBusinessDuty(ctx, tx, input, by)
		return err
	})
	return result, err
}

func (s workspace) endBusinessDuty(ctx context.Context, input endAppointmentInput) (appointmentResult, error) {
	return s.changeBusinessDuty(ctx, input, nil)
}

func (s workspace) transferBusinessDuty(ctx context.Context, input transferBusinessDutyInput) (appointmentResult, error) {
	if !input.dutySubject.normalize() {
		return appointmentResult{}, invalid
	}
	return s.changeBusinessDuty(ctx, input.endAppointmentInput, &input.dutySubject)
}

func (s workspace) changeBusinessDuty(ctx context.Context, input endAppointmentInput, replacement *dutySubject) (appointmentResult, error) {
	var result appointmentResult
	id, ok := canonicalSourceUUID(input.AssignmentID)
	at := input.At.UTC().Truncate(time.Microsecond)
	expected := canonicalConfigTime(input.ExpectedThroughAt)
	if !ok || at.IsZero() || expected != nil && !at.Before(*expected) {
		return result, invalid
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		var old createBusinessDutyInput
		err := tx.QueryRow(ctx, `SELECT role,scope_id::text,coalesce(person_id,0),coalesce(unit_id::text,''),valid_from,valid_to
		 FROM organization_business_duties WHERE id=$1::uuid FOR UPDATE`, id).Scan(&old.Role, &old.ScopeID,
			&old.PersonID, &old.UnitID, &old.StartsAt, &old.ThroughAt)
		if err != nil {
			return err
		}
		next := createBusinessDutyInput{Role: old.Role, ScopeID: old.ScopeID, StartsAt: at, ThroughAt: expected}
		if replacement != nil {
			if old.dutySubject == *replacement {
				return invalid
			}
			next.dutySubject = *replacement
		}
		if old.ThroughAt != nil && old.ThroughAt.Equal(at) {
			result.ID = id
			if replacement != nil {
				result.ID, err = findBusinessDuty(ctx, tx, next)
				if errors.Is(err, pgx.ErrNoRows) {
					return errAppointmentChanged
				}
			}
			return err
		}
		if !at.After(old.StartsAt) || (old.ThroughAt == nil) != (expected == nil) ||
			old.ThroughAt != nil && !old.ThroughAt.Equal(*expected) {
			return errAppointmentChanged
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_business_duties SET valid_to=$2,changed_at=now(),changed_by=$3::uuid
		 WHERE id=$1::uuid`, id, at, by); err != nil {
			return err
		}
		result.ID = id
		if replacement != nil {
			result.ID, err = registerBusinessDuty(ctx, tx, next, by)
		}
		return err
	})
	return result, err
}

func importOrganizationDuties(ctx context.Context, tx pgx.Tx, snapshot organizationSnapshot) error {
	if len(snapshot.DutyScopes) != 0 {
		var complete bool
		err := tx.QueryRow(ctx, `WITH incoming AS (
		 SELECT * FROM jsonb_to_recordset($1) AS i(id uuid,kind text,code text,name text,active boolean)
		), stored AS (
		 INSERT INTO organization_duty_scopes(id,kind,code,name,active) SELECT id,kind,code,name,active FROM incoming
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name,active=excluded.active
		 WHERE (organization_duty_scopes.kind,organization_duty_scopes.code)=(excluded.kind,excluded.code)
		 RETURNING id
		) SELECT (SELECT count(*) FROM incoming)=(SELECT count(*) FROM stored)`, snapshot.DutyScopes).Scan(&complete)
		if err != nil {
			return err
		}
		if !complete {
			return invalid
		}
	}
	if len(snapshot.BusinessDuties) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organization_business_duties(id,role,scope_id,person_id,unit_id,valid_from,valid_to)
	 SELECT a.id,r.option_key,a."scopeId",p.id,a."unitId",a."from",a."to"
	 FROM jsonb_to_recordset($1) AS a(id uuid, role text, "scopeId" uuid, "personId" uuid, "unitId" uuid,
	  "from" timestamptz,"to" timestamptz)
	 LEFT JOIN people p ON p.legacy_party_id=a."personId"
	 LEFT JOIN organization_duty_scopes s ON s.id=a."scopeId"
	 LEFT JOIN configuration_options r ON r.group_key='organizationDuty' AND r.option_key=a.role
	  AND (a."personId" IS NULL)<>(a."unitId" IS NULL)
	  AND r.applies_to->'scopes'?s.kind AND r.applies_to->'subjects'?
	   CASE WHEN a."unitId" IS NULL THEN 'PERSON' ELSE 'ORGANIZATION' END
	 ON CONFLICT(id) DO UPDATE SET role=excluded.role,scope_id=excluded.scope_id,person_id=excluded.person_id,unit_id=excluded.unit_id,
	  valid_from=excluded.valid_from,valid_to=excluded.valid_to
	 WHERE organization_business_duties.changed_at IS NULL`, snapshot.BusinessDuties); err != nil {
		return err
	}
	var overlap bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM (
	 SELECT valid_to,lead(valid_from) OVER (PARTITION BY role,scope_id,person_id,unit_id ORDER BY valid_from,id) AS next_start
	 FROM organization_business_duties) ranges WHERE next_start IS NOT NULL AND (valid_to IS NULL OR valid_to>next_start))`).Scan(&overlap)
	if err == nil && overlap {
		return errBusinessDutyOverlap
	}
	return err
}
