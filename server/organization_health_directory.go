package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

func resolveHealthCompany(ctx context.Context, tx pgx.Tx, external string) (string, error) {
	var ids []string
	err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT id::text),'{}') FROM (
  SELECT entity_id AS id FROM organization_dispatch_links WHERE target='DCAI' AND entity_type='COMPANY' AND external_id=$1
  UNION SELECT company_id FROM organization_company_sources WHERE source='DCAI' AND source_company_id=$1
   AND valid_from<=now() AND (valid_to IS NULL OR valid_to>now())
  UNION SELECT company_unit_id FROM catalog_sales_company_aliases WHERE source='DCAI' AND external_id=$1
   AND valid_from<=now() AND (valid_to IS NULL OR valid_to>now())
 ) sources`, external).Scan(&ids)
	if err != nil {
		return "", err
	}
	if len(ids) > 1 {
		return "", conflict
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
}

func loadHealthFinanceState(ctx context.Context, tx pgx.Tx, e organizationHealthEvent) (healthFinanceState, error) {
	a := healthFinanceState{Value: json.RawMessage("{}"), Intent: json.RawMessage("{}")}
	var err error
	if e.Kind == "COMPANY" {
		a.EntityID, err = resolveHealthCompany(ctx, tx, e.ExternalID)
		// Ambiguous mappings remain visible as an unresolvable source conflict.
		if errors.Is(err, conflict) {
			return a, nil
		}
	} else {
		err = tx.QueryRow(ctx, `SELECT entity_id::text FROM organization_dispatch_links WHERE target='DCAI' AND entity_type='MEMBER' AND external_id=$1`, e.UserID).Scan(&a.EntityID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
	}
	if err != nil || a.EntityID == "" {
		return a, err
	}
	// Finance company maintenance locks the unit before it enqueues a new version.
	if e.Kind == "COMPANY" {
		if _, err = tx.Exec(ctx, `SELECT id FROM organization_units WHERE id=$1 FOR UPDATE`, a.EntityID); err != nil {
			return a, err
		}
	}
	if err = lockDispatchEntity(ctx, tx, "DCAI", e.Kind, a.EntityID); err != nil {
		return a, err
	}
	if e.Kind == "COMPANY" {
		err = tx.QueryRow(ctx, `SELECT coalesce(k.wire_id::text,''),jsonb_build_object('name',u.name,'status',u.status,'category_id',h.category_id,
   'company_type',u.sales_company_type,'parent_company_id',parent.wire_id)
   FROM organization_units u LEFT JOIN organization_dispatch_company_keys k ON k.unit_id=u.id
   LEFT JOIN organization_dispatch_company_keys parent ON parent.unit_id=u.parent_id
   LEFT JOIN LATERAL (SELECT category_id FROM organization_sales_category_history
    WHERE unit_id=u.id AND valid_from<=now() AND (valid_to IS NULL OR valid_to>now()) ORDER BY valid_from DESC LIMIT 1) h ON true
   WHERE u.id=$1 AND u.sales_company_type IS NOT NULL FOR UPDATE OF u`, a.EntityID).Scan(&a.WireID, &a.Value)
	} else {
		err = tx.QueryRow(ctx, `SELECT l.entity_id::text,jsonb_build_object('name',coalesce(nullif(l.source_name,''),p.name),'status',l.identity_state,
   'role',l.role,'external_user_id',l.external_id,'external_member_id',l.external_member_id,
   'expected_company_external_id',l.external_company_id,'company_id',k.wire_id)
   FROM organization_dispatch_links l JOIN people p ON p.id=l.person_id
   LEFT JOIN organization_dispatch_links c ON c.target=l.target AND c.entity_type='COMPANY' AND c.external_id=l.external_company_id
   LEFT JOIN organization_dispatch_company_keys k ON k.unit_id=c.entity_id
   WHERE l.target='DCAI' AND l.entity_type='MEMBER' AND l.entity_id=$1 FOR UPDATE OF l`, a.EntityID).Scan(&a.WireID, &a.Value)
	}
	if err != nil {
		return a, err
	}
	err = tx.QueryRow(ctx, `SELECT coalesce((SELECT version FROM organization_dispatch_links WHERE target='DCAI' AND entity_type=$1 AND entity_id=$2),0)`, e.Kind, a.EntityID).Scan(&a.Applied)
	if err != nil {
		return a, err
	}
	err = tx.QueryRow(ctx, `SELECT version,state,payload FROM organization_dispatch_outbox
   WHERE target='DCAI' AND entity_type=$1 AND entity_id=$2 ORDER BY version DESC LIMIT 1 FOR UPDATE`, e.Kind, a.EntityID).Scan(&a.Version, &a.State, &a.Intent)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	a.Version = max(a.Version, a.Applied)
	return a, err
}

func applyHealthSnapshot(ctx context.Context, tx pgx.Tx, e organizationHealthEvent) (organizationHealthAck, error) {
	ack := organizationHealthAck{ID: e.ID}
	if !validHealthFields(e) {
		return ack, invalid
	}
	var entity string
	var err error
	if e.Kind == "COMPANY" {
		entity, ack.WireID, ack.Category, err = applyHealthCompany(ctx, tx, e)
	} else {
		entity, err = applyHealthMember(ctx, tx, e)
		ack.WireID = entity
	}
	if err != nil {
		return ack, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO organization_health_inbox(source_id,kind,external_id,entity_id,wire_id,category_id,finance_base_version)
  VALUES($1,$2,$3,$4,$5,nullif($6,'')::uuid,$7)`, e.ID, e.Kind, e.ExternalID, entity, ack.WireID, ack.Category, e.Base); err != nil {
		return ack, err
	}
	return ack, enqueueDraftConfiguration(ctx, tx, `SELECT id FROM periods`)
}

func applyHealthCompany(ctx context.Context, tx pgx.Tx, e organizationHealthEvent) (unit, wire, category string, err error) {
	err = tx.QueryRow(ctx, `SELECT c.id::text FROM organization_dispatch_links l JOIN organization_sales_categories c ON c.id=l.entity_id
  WHERE target='DCAI' AND entity_type='CATEGORY' AND external_id=$1 AND c.active FOR SHARE OF c`, e.CategoryID).Scan(&category)
	if err != nil {
		return
	}
	unit, err = resolveHealthCompany(ctx, tx, e.ExternalID)
	if err != nil {
		return
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('health-company-name:'||lower(regexp_replace($1,'[[:space:]]','','g')),0))`, e.Name); err != nil {
		return
	}
	var duplicate bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_units WHERE sales_company_type IS NOT NULL
  AND lower(regexp_replace(name,'[[:space:]]','','g'))=lower(regexp_replace($1,'[[:space:]]','','g')) AND id IS DISTINCT FROM nullif($2,'')::uuid)`, e.Name, unit).Scan(&duplicate)
	if err != nil {
		return
	}
	if duplicate {
		err = conflict
		return
	}
	if unit == "" {
		var retired bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_company_sources WHERE source='DCAI' AND source_company_id=$1)`, e.ExternalID).Scan(&retired)
		if err != nil {
			return
		}
		if e.Status != 1 || retired {
			err = conflict
			return
		}
		err = tx.QueryRow(ctx, `INSERT INTO organization_units(id,kind,name,status,sales_company_type)
   VALUES(gen_random_uuid(),'COMPANY',$1,'ACTIVE','EXTERNAL') RETURNING id::text`, e.Name).Scan(&unit)
		if err != nil {
			return
		}
		wire = unit
		if _, err = tx.Exec(ctx, `INSERT INTO organization_dispatch_company_keys(unit_id,wire_id) VALUES($1,$1)`, unit); err != nil {
			return
		}
		if _, err = tx.Exec(ctx, `INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from,recorded_at) VALUES($1,$2,$3,now())`, unit, category, e.At); err != nil {
			return
		}
	} else {
		err = tx.QueryRow(ctx, `SELECT k.wire_id::text FROM organization_dispatch_company_keys k JOIN organization_units u ON u.id=k.unit_id
   WHERE k.unit_id=$1 AND u.sales_company_type IS NOT NULL AND EXISTS(SELECT 1 FROM organization_sales_category_history h
   WHERE h.unit_id=u.id AND h.category_id=$2 AND h.valid_from<=now() AND (h.valid_to IS NULL OR h.valid_to>now())) FOR UPDATE OF u`, unit, category).Scan(&wire)
		if err != nil {
			return
		}
		if e.Status == 0 {
			var used bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_assignments WHERE unit_id=$1 AND valid_from<=now() AND (valid_to IS NULL OR valid_to>now())
    UNION ALL SELECT 1 FROM organization_units WHERE parent_id=$1 AND status='ACTIVE'
    UNION ALL SELECT 1 FROM organization_company_sources s WHERE s.company_id=$1 AND s.valid_from<=now() AND (s.valid_to IS NULL OR s.valid_to>now())
    AND NOT EXISTS(SELECT 1 FROM organization_dispatch_links l WHERE l.target=s.source AND l.entity_type='COMPANY' AND l.entity_id=s.company_id AND l.external_id=s.source_company_id AND l.version>0)
    AND NOT EXISTS(SELECT 1 FROM organization_health_inbox i WHERE s.source='DCAI' AND i.kind='COMPANY' AND i.entity_id=s.company_id AND i.external_id=s.source_company_id AND i.acknowledged_at IS NOT NULL))`, unit).Scan(&used)
			if err != nil {
				return
			}
			if used {
				err = conflict
				return
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE organization_units SET name=$2,status=CASE WHEN $3::integer=1 THEN 'ACTIVE' ELSE 'INACTIVE' END WHERE id=$1`, unit, e.Name, e.Status); err != nil {
			return
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
  SELECT 'DCAI',$1,$2,$3 WHERE NOT EXISTS(SELECT 1 FROM organization_company_use_closures
   WHERE company_id=$2 AND source='DCAI' AND external_id=$1 AND closed_at>=$3) AND NOT EXISTS(SELECT 1 FROM organization_company_sources WHERE source='DCAI' AND source_company_id=$1
   AND company_id=$2 AND valid_from<=now() AND (valid_to IS NULL OR valid_to>now()))`, e.ExternalID, unit, e.At); err != nil {
		return
	}
	var confirmed string
	err = tx.QueryRow(ctx, `INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id) VALUES('DCAI','COMPANY',$1,$2)
  ON CONFLICT(target,entity_type,entity_id) DO UPDATE SET external_id=excluded.external_id WHERE organization_dispatch_links.external_id=excluded.external_id
  RETURNING external_id`, unit, e.ExternalID).Scan(&confirmed)
	if err != nil {
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO organization_dispatch_targets(company_id,target) VALUES($1,'DCAI') ON CONFLICT DO NOTHING`, unit)
	if err != nil {
		return
	}
	targets, err := companyTargets(ctx, tx, unit)
	if err != nil {
		return
	}
	targets = slices.DeleteFunc(targets, func(t string) bool { return t == "DCAI" })
	if len(targets) > 0 {
		err = enqueueDispatchCompanyTargets(ctx, tx, unit, targets, nil)
	}
	return
}

func applyHealthMember(ctx context.Context, tx pgx.Tx, e organizationHealthEvent) (string, error) {
	company, err := resolveHealthCompany(ctx, tx, e.CompanyID)
	if err != nil {
		return "", err
	}
	if company == "" {
		return "", conflict
	}
	// Serialize owner changes from different native users of the same company.
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM organization_units WHERE id=$1 AND sales_company_type IS NOT NULL FOR UPDATE`, company).Scan(&status)
	if err != nil {
		return "", err
	}
	if status != "ACTIVE" && e.Status == 1 {
		return "", conflict
	}

	var people []int64
	err = tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT person_id),'{}') FROM (
  SELECT person_id FROM identity_mappings WHERE source='DCAI' AND external_id=$1 AND valid_from<=now() AND (valid_to IS NULL OR valid_to>now())
  UNION SELECT person_id FROM catalog_sales_people_sources WHERE source='DCAI' AND external_id=$1 AND valid_from<=now() AND (valid_to IS NULL OR valid_to>now())
  UNION SELECT person_id FROM organization_dispatch_links WHERE target='DCAI' AND entity_type='MEMBER' AND external_id=$1 AND person_id IS NOT NULL
 ) mappings`, e.UserID).Scan(&people)
	if err != nil {
		return "", err
	}
	if len(people) > 1 || len(people) == 0 && e.Status == 0 {
		return "", conflict
	}
	var person int64
	if len(people) == 0 {
		if err = tx.QueryRow(ctx, "INSERT INTO people(name) VALUES($1) RETURNING id", e.Name).Scan(&person); err != nil {
			return "", err
		}
	} else {
		person = people[0]
	}
	if err = lockAppointmentPerson(ctx, tx, person); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity_mappings(person_id,source,external_id,valid_from)
  SELECT $1,'DCAI',$2,$3 WHERE NOT EXISTS(SELECT 1 FROM identity_mappings WHERE source='DCAI' AND external_id=$2 AND person_id=$1 AND valid_from<=now() AND (valid_to IS NULL OR valid_to>now()))`, person, e.UserID, e.At); err != nil {
		return "", err
	}
	role, state := "MEMBER", "INACTIVE"
	if e.Owner == 1 {
		role = "OWNER"
	}
	if e.Status == 1 {
		state = "ACTIVE"
	}
	var binding string
	err = tx.QueryRow(ctx, `INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id,person_id,external_company_id,external_member_id,role,identity_state,source_name)
  VALUES('DCAI','MEMBER',gen_random_uuid(),$1,$2,$3,$4,$5,$6,$7)
  ON CONFLICT(target,entity_type,external_id) DO UPDATE SET external_company_id=excluded.external_company_id,external_member_id=excluded.external_member_id,
   role=excluded.role,identity_state=excluded.identity_state,source_name=excluded.source_name
   WHERE organization_dispatch_links.person_id=excluded.person_id RETURNING entity_id::text`, e.UserID, person, e.CompanyID, e.ExternalID, role, state, e.Name).Scan(&binding)
	if err != nil {
		return "", err
	}
	return binding, applyHealthMembership(ctx, tx, person, company, role, e)
}

func applyHealthMembership(ctx context.Context, tx pgx.Tx, person int64, unit, role string, e organizationHealthEvent) error {
	var id, kind string
	var from time.Time
	var end *time.Time
	var source *string
	err := tx.QueryRow(ctx, `SELECT id::text,kind,valid_from,valid_to,health_member_id FROM organization_assignments
  WHERE person_id=$1 AND unit_id=$2 AND kind IN ('MEMBER','OWNER','MANAGER') AND tstzrange(valid_from,valid_to,'[)') @> $3::timestamptz
  ORDER BY valid_from DESC LIMIT 1 FOR UPDATE`, person, unit, e.At).Scan(&id, &kind, &from, &end, &source)
	if err == nil {
		if e.Status == 1 && kind != role {
			return conflict
		}
		if e.Status == 0 && source != nil && *source == e.ExternalID && end == nil {
			at := e.At
			if !at.After(from) {
				at = from.Add(time.Microsecond)
			}
			_, err = tx.Exec(ctx, `UPDATE organization_assignments SET valid_to=$2,changed_at=now() WHERE id=$1`, id, at)
		}
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if e.Status == 0 {
		return nil
	}
	var occupied bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_assignments a JOIN organization_units u ON u.id=a.unit_id
  WHERE u.sales_company_type IS NOT NULL AND a.kind IN ('OWNER','MEMBER','MANAGER') AND (a.valid_to IS NULL OR a.valid_to>$3)
  AND (a.person_id=$1 OR ($4='OWNER' AND a.unit_id=$2 AND a.kind='OWNER')))`, person, unit, e.At, role).Scan(&occupied)
	if err != nil {
		return err
	}
	if occupied {
		return conflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,changed_at,health_member_id)
  VALUES(gen_random_uuid(),$1,$2,$3,$4,now(),$5)`, person, unit, role, e.At, e.ExternalID)
	return err
}
