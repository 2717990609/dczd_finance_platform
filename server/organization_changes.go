package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	errAppointmentOverlap      = errors.New("该人员在此组织已有时间重叠的同类任职")
	errAppointmentChanged      = errors.New("任职结束时间已变化，或所选时间不在任职区间内，请刷新核对")
	errAppointmentUnitInactive = errors.New("目标组织已停用")
)

type appointmentTarget struct {
	UnitID          string `json:"unitId"`
	Kind            string `json:"kind"`
	Position        string `json:"position"`
	appointmentType *string
}

type addAppointmentInput struct {
	appointmentTarget
	PersonID  int64      `json:"personId"`
	StartsAt  time.Time  `json:"startsAt"`
	ThroughAt *time.Time `json:"throughAt"`
}

type endAppointmentInput struct {
	AssignmentID      string     `json:"assignmentId"`
	At                time.Time  `json:"at"`
	ExpectedThroughAt *time.Time `json:"expectedThroughAt"`
}

type transferAppointmentInput struct {
	endAppointmentInput
	appointmentTarget
}

type appointmentResult struct {
	ID string `json:"id"`
}

func (s workspace) appointmentOperator(ctx context.Context) (*financePrincipal, error) {
	operator := financeUser(ctx)
	if operator == nil {
		if s.development {
			return nil, nil
		}
		return nil, errFinanceLogin
	}
	if !operator.Workspace || !operator.OrganizationWrite {
		return nil, errFinanceForbidden
	}
	return operator, nil
}

func (s workspace) organizationPeople(w http.ResponseWriter, r *http.Request) {
	if _, err := s.appointmentOperator(r.Context()); err != nil {
		respond(w, nil, err)
		return
	}
	result, err := s.searchFinancePeople(r.Context(), r.URL.Query().Get("name"))
	respond(w, result, err)
}

func (s workspace) searchFinancePeople(ctx context.Context, name string) (json.RawMessage, error) {
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 80 {
		return nil, invalid
	}
	var result json.RawMessage
	err := s.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.name,p.id),'[]')
	 FROM (SELECT id,name FROM people WHERE directory_visible AND NOT directory_disabled AND coalesce(a2_status,'ACTIVE')='ACTIVE' AND ($1='' OR strpos(lower(name),lower($1))>0)
	  ORDER BY name,id LIMIT 25) p`, name).Scan(&result)
	return result, err
}

func (s workspace) writeAppointments(ctx context.Context, apply func(pgx.Tx, *string) error) error {
	operator, err := s.appointmentOperator(ctx)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var by *string
		if operator != nil {
			var ok int
			err := tx.QueryRow(ctx, `SELECT 1 FROM finance_accounts WHERE id=$1::uuid AND enabled AND workspace_access
			 AND organization_write FOR SHARE`, operator.ID).Scan(&ok)
			if errors.Is(err, pgx.ErrNoRows) {
				return errFinanceForbidden
			}
			if err != nil {
				return err
			}
			if err := lockFinanceOperatorSession(ctx, tx, operator); err != nil {
				return err
			}
			by = &operator.ID
		}
		// The initialization command takes this same lock exclusively.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(637619241)"); err != nil {
			return err
		}
		return apply(tx, by)
	})
}

func normalizeAppointmentTarget(target *appointmentTarget) error {
	unit, ok := canonicalSourceUUID(target.UnitID)
	target.Position = strings.TrimSpace(target.Position)
	if !ok || !textOK(target.Kind, 80) || len([]rune(target.Position)) > 100 {
		return invalid
	}
	target.UnitID = unit
	return nil
}

func lockAppointmentPerson(ctx context.Context, tx pgx.Tx, personID int64) error {
	var locked int64
	return tx.QueryRow(ctx, `SELECT id FROM people WHERE id=$1 FOR NO KEY UPDATE`, personID).Scan(&locked)
}

func matchingAppointment(ctx context.Context, tx pgx.Tx, personID int64, target appointmentTarget, from time.Time, through *time.Time) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM organization_assignments
	 WHERE person_id=$1 AND unit_id=$2::uuid AND kind=$3 AND position=$4 AND valid_from=$5
	  AND valid_to IS NOT DISTINCT FROM $6::timestamptz ORDER BY id LIMIT 1`,
		personID, target.UnitID, target.Kind, target.Position, from, through).Scan(&id)
	return id, err
}

func insertAppointment(ctx context.Context, tx pgx.Tx, personID int64, target appointmentTarget, from time.Time, through *time.Time, by *string) (string, error) {
	if err := lockActiveDirectoryPerson(ctx, tx, personID); err != nil {
		return "", err
	}
	// A sales appointment may have one company per person and one owner per
	// company over its effective interval. Department appointments stay independent.
	if target.Kind == "MEMBER" || target.Kind == "MANAGER" || target.Kind == "OWNER" {
		var sales bool
		if err := tx.QueryRow(ctx, `SELECT sales_company_type IS NOT NULL FROM organization_units WHERE id=$1 FOR UPDATE`, target.UnitID).Scan(&sales); err != nil {
			return "", err
		}
		if sales {
			var collision bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM organization_assignments a JOIN organization_units u ON u.id=a.unit_id
			WHERE u.sales_company_type IS NOT NULL AND a.kind IN ('MEMBER','MANAGER','OWNER')
			AND (a.person_id=$1 OR ($3='OWNER' AND a.unit_id=$2 AND a.kind='OWNER'))
			AND tstzrange(a.valid_from,a.valid_to,'[)') && tstzrange($4,$5,'[)'))`, personID, target.UnitID, target.Kind, from, through).Scan(&collision); err != nil {
				return "", err
			}
			if collision {
				return "", dispatchProblem("所选期间已有销售公司归属或公司负责人，请先核对归属历史")
			}
		}
	}
	var status string
	var kindEnabled, overlaps bool
	err := tx.QueryRow(ctx, `SELECT u.status,
	 EXISTS(SELECT 1 FROM configuration_options WHERE group_key='appointmentKind' AND option_key=$3 AND enabled),
	 EXISTS(SELECT 1 FROM organization_assignments WHERE person_id=$1 AND unit_id=u.id AND kind=$3
	  AND tstzrange(valid_from,valid_to,'[)') && tstzrange($4,$5,'[)'))
	 FROM organization_units u WHERE u.id=$2::uuid`, personID, target.UnitID, target.Kind, from, through).Scan(&status, &kindEnabled, &overlaps)
	switch {
	case err != nil:
		return "", err
	case status != "ACTIVE":
		return "", errAppointmentUnitInactive
	case !kindEnabled:
		return "", invalid
	case overlaps:
		return "", errAppointmentOverlap
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,position,valid_from,valid_to,changed_at,changed_by,appointment_type)
	 VALUES(gen_random_uuid(),$1,$2::uuid,$3,$4,$5,$6,now(),$7::uuid,$8) RETURNING id::text`,
		personID, target.UnitID, target.Kind, target.Position, from, through, by, target.appointmentType).Scan(&id)
	return id, err
}

// A class can include payments made before it started and can depend on earlier
// classes for tier selection. Appointment dates directly bound fixed cycles.
const draftRefreshAppointment = `SELECT p.id FROM periods p JOIN products pro ON pro.id=p.product_id
 WHERE (pro.period_mode='class' AND p.calendar_month IS NULL)
 OR ($1<((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')
  AND ($2::timestamptz IS NULL OR (p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai')<$2))
 UNION SELECT c.period_id FROM calculation_draft_corrections c
 JOIN calculation_draft_lines l ON l.id=c.original_line_id
 JOIN source_order_versions original ON original.id=l.source_version_id
 WHERE c.person_id=$3 AND c.person_id<>l.person_id
  AND original.occurred_at>=$1 AND ($2 IS NULL OR original.occurred_at<$2)
 UNION SELECT a.period_id FROM calculation_draft_performance a JOIN source_order_versions paid ON paid.id=a.source_version_id
 WHERE a.completion_version_id IS NOT NULL AND paid.occurred_at>=$1 AND ($2 IS NULL OR paid.occurred_at<$2)`

func (s workspace) addOrganizationAssignment(ctx context.Context, input addAppointmentInput) (appointmentResult, error) {
	var result appointmentResult
	from := input.StartsAt.UTC().Truncate(time.Microsecond)
	through := canonicalConfigTime(input.ThroughAt)
	if input.PersonID < 1 || from.IsZero() || through != nil && !through.After(from) {
		return result, invalid
	}
	if err := normalizeAppointmentTarget(&input.appointmentTarget); err != nil {
		return result, err
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		if err := lockAppointmentPerson(ctx, tx, input.PersonID); err != nil {
			return err
		}
		id, err := matchingAppointment(ctx, tx, input.PersonID, input.appointmentTarget, from, through)
		if err == nil {
			result.ID = id
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result.ID, err = insertAppointment(ctx, tx, input.PersonID, input.appointmentTarget, from, through, by)
		if err != nil {
			return err
		}
		return enqueueDraftConfiguration(ctx, tx, draftRefreshAppointment, from, through, input.PersonID)
	})
	return result, err
}

func (s workspace) endOrganizationAssignment(ctx context.Context, input endAppointmentInput) (appointmentResult, error) {
	return s.changeOrganizationAssignment(ctx, input, nil)
}

func (s workspace) transferOrganizationAssignment(ctx context.Context, input transferAppointmentInput) (appointmentResult, error) {
	if err := normalizeAppointmentTarget(&input.appointmentTarget); err != nil {
		return appointmentResult{}, err
	}
	return s.changeOrganizationAssignment(ctx, input.endAppointmentInput, &input.appointmentTarget)
}

func (s workspace) changeOrganizationAssignment(ctx context.Context, input endAppointmentInput, target *appointmentTarget) (appointmentResult, error) {
	var result appointmentResult
	id, ok := canonicalSourceUUID(input.AssignmentID)
	at := input.At.UTC().Truncate(time.Microsecond)
	expected := canonicalConfigTime(input.ExpectedThroughAt)
	if !ok || at.IsZero() || expected != nil && !at.Before(*expected) {
		return result, invalid
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		var person int64
		if err := tx.QueryRow(ctx, `SELECT person_id FROM organization_assignments WHERE id=$1::uuid`, id).Scan(&person); err != nil {
			return err
		}
		if err := lockAppointmentPerson(ctx, tx, person); err != nil {
			return err
		}
		var from time.Time
		var through *time.Time
		var unit, kind, position string
		var appointmentType *string
		if err := tx.QueryRow(ctx, `SELECT valid_from,valid_to,unit_id::text,kind,position,appointment_type
		 FROM organization_assignments WHERE id=$1::uuid`, id).Scan(&from, &through, &unit, &kind, &position, &appointmentType); err != nil {
			return err
		}
		if through != nil && through.Equal(at) {
			result.ID = id
			if target == nil {
				return nil
			}
			replacement, err := matchingAppointment(ctx, tx, person, *target, at, expected)
			if errors.Is(err, pgx.ErrNoRows) {
				return errAppointmentChanged
			}
			result.ID = replacement
			return err
		}
		if !at.After(from) || (through == nil) != (expected == nil) ||
			through != nil && (!through.Equal(*expected) || !at.Before(*through)) {
			return errAppointmentChanged
		}
		if target != nil && target.UnitID == unit && target.Kind == kind && target.Position == position {
			return invalid
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_assignments SET valid_to=$2,changed_at=now(),changed_by=$3::uuid
		 WHERE id=$1::uuid`, id, at, by); err != nil {
			return err
		}
		result.ID = id
		if target != nil {
			switch target.Kind {
			case "OWNER", "MANAGER", "MEMBER":
				target.appointmentType = appointmentType
				if kind == "PRIMARY" || kind == "CONCURRENT" {
					target.appointmentType = &kind
				}
			}
			replacement, err := insertAppointment(ctx, tx, person, *target, at, through, by)
			if err != nil {
				return err
			}
			result.ID = replacement
		}
		return enqueueDraftConfiguration(ctx, tx, draftRefreshAppointment, at, through, person)
	})
	return result, err
}
