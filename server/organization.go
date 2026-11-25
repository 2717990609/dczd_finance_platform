package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const organizationUnitsSQL = `SELECT coalesce(jsonb_agg(jsonb_build_object(
 'id',u.id,'parentId',u.parent_id,'name',u.name,'kind',u.kind,'status',u.status,
 'companyType',u.sales_company_type,'salesParentId',coalesce(parent_key.unit_id,u.parent_id))
 ORDER BY u.sort_order,u.name,u.id),'[]') FROM organization_units u
 LEFT JOIN organization_dispatch_company_keys k ON k.unit_id=u.id
 LEFT JOIN organization_dispatch_company_keys parent_key ON parent_key.wire_id=k.parent_wire_id`

const organizationAssignmentsSQL = `WITH RECURSIVE branches AS (
 SELECT $1::uuid AS id WHERE $1 IS NOT NULL
 UNION
 SELECT u.id FROM organization_units u JOIN branches b ON b.id=u.parent_id WHERE $6
), matching AS (
 SELECT a.id,p.name AS "personName",p.id AS "personId",u.id AS "unitId",u.name AS "unitName",u.kind AS "unitKind",
  (u.sales_company_type IS NOT NULL) AS "salesCompany",
  a.kind,a.position,a.appointment_type AS "appointmentType",a.valid_from AS "validFrom",a.valid_to AS "validTo"
 FROM organization_assignments a JOIN people p ON p.id=a.person_id JOIN organization_units u ON u.id=a.unit_id
 WHERE ($1::uuid IS NULL OR a.unit_id IN (SELECT id FROM branches)) AND
  ($2::timestamptz IS NULL OR (a.valid_from<=$2 AND (a.valid_to IS NULL OR a.valid_to>$2))) AND
  ($3='' OR strpos(lower(p.name),lower($3))>0) AND
  ($5::bigint IS NULL OR a.person_id=$5)
 ), page AS (
 SELECT * FROM matching ORDER BY "personName","validFrom" DESC,id LIMIT 25 OFFSET $4
 )
 SELECT jsonb_build_object('total',(SELECT count(*) FROM matching),
  'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY "personName","validFrom" DESC,id) FROM page),'[]'))`

func (s workspace) organizationAssignments(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	var unitID pgtype.UUID
	includeChildren := params.Get("includeChildren")
	if includeChildren != "" && includeChildren != "true" {
		respond(w, nil, invalid)
		return
	}
	if value := params.Get("unitId"); value != "" && unitID.Scan(value) != nil {
		respond(w, nil, invalid)
		return
	}
	var personID *int64
	if value := params.Get("personId"); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			respond(w, nil, invalid)
			return
		}
		personID = &id
	}
	offset, at, name, err := organizationAssignmentFilters(params)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), organizationAssignmentsSQL, unitID, at, name, offset, personID, includeChildren == "true").Scan(&result)
	respond(w, result, err)
}

func organizationAssignmentFilters(params url.Values) (int, *time.Time, string, error) {
	return organizationFilters(params, 25)
}

func organizationFilters(params url.Values, pageSize int) (offset int, at *time.Time, name string, err error) {
	page := 1
	if value := params.Get("page"); value != "" {
		var err error
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 || page > 100000 {
			return 0, nil, "", invalid
		}
	}
	history, on := params.Get("history"), params.Get("on")
	if history != "" && (history != "true" || on != "") {
		return 0, nil, "", invalid
	}
	if history == "" {
		date := time.Now()
		if on != "" {
			var err error
			date, err = time.ParseInLocation(time.DateOnly, on, shanghai)
			if err != nil {
				return 0, nil, "", invalid
			}
		}
		at = &date
	}
	return (page - 1) * pageSize, at, strings.TrimSpace(params.Get("name")), nil
}

type organizationSnapshot struct {
	DispatchPhoneBindings []dispatchSnapshotPhone
	DispatchVersionFloor  *int64
	DispatchCompanies     []dispatchSnapshotCompany
	DispatchReceipts      []dispatchSnapshotReceipt
	DispatchBindings      []dispatchSnapshotBinding
	CapturedAt            time.Time
	People                json.RawMessage
	Units                 json.RawMessage
	Assignments           json.RawMessage
	Companies             json.RawMessage
	Recommendations       json.RawMessage
	SalesCategories       json.RawMessage
	SalesCategoryHistory  json.RawMessage
	DutyScopes            json.RawMessage
	BusinessDuties        json.RawMessage
	AIHealthCategories    json.RawMessage
	AIHealthReceipts      json.RawMessage
}

func importOrganization(ctx context.Context, cfg config, path string) error {
	if !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return errors.New("organization import requires a dedicated development database")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		return err
	}
	return storeOrganizationSnapshot(ctx, db, raw)
}

func storeOrganizationSnapshot(ctx context.Context, db transactionBeginner, raw json.RawMessage) error {
	var snapshot organizationSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if snapshot.CapturedAt.IsZero() {
		return invalid
	}
	for _, rows := range []json.RawMessage{snapshot.People, snapshot.Units, snapshot.Assignments} {
		if len(rows) == 0 || rows[0] != '[' {
			return invalid
		}
	}
	for _, rows := range []json.RawMessage{snapshot.Companies, snapshot.Recommendations, snapshot.SalesCategories, snapshot.SalesCategoryHistory, snapshot.DutyScopes, snapshot.BusinessDuties, snapshot.AIHealthCategories, snapshot.AIHealthReceipts} {
		if len(rows) != 0 && rows[0] != '[' {
			return invalid
		}
	}
	digest := sha256.Sum256(raw)
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(637619241)"); err != nil {
			return err
		}
		var previous time.Time
		var previousDigest []byte
		err := tx.QueryRow(ctx, "SELECT captured_at,digest FROM organization_import_state").Scan(&previous, &previousDigest)
		switch {
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		case snapshot.CapturedAt.Before(previous):
			return errors.New("organization snapshot is older than the one already imported")
		case snapshot.CapturedAt.Equal(previous):
			if !bytes.Equal(digest[:], previousDigest) {
				return errors.New("organization snapshot timestamp already has different content")
			}
			return nil
		}
		for _, statement := range []struct {
			query string
			rows  json.RawMessage
		}{{importOrganizationPeopleSQL, snapshot.People}, {importOrganizationUnitsSQL, snapshot.Units}, {importOrganizationAssignmentsSQL, snapshot.Assignments}} {
			if _, err := tx.Exec(ctx, statement.query, statement.rows); err != nil {
				return fmt.Errorf("write organization snapshot: %w", err)
			}
		}
		if len(snapshot.Companies) != 0 {
			if _, err := tx.Exec(ctx, importOrganizationCompanySourcesSQL, snapshot.Companies); err != nil {
				return fmt.Errorf("write organization company sources: %w", err)
			}
		}
		if len(snapshot.Recommendations) != 0 {
			if _, err := tx.Exec(ctx, importOrganizationCompanyRecommendationsSQL, snapshot.Recommendations); err != nil {
				return fmt.Errorf("write company recommendations: %w", err)
			}
		}
		if err := importOrganizationSalesCategories(ctx, tx, snapshot); err != nil {
			return fmt.Errorf("write organization sales categories: %w", err)
		}
		if err := importDispatchSnapshot(ctx, tx, snapshot); err != nil {
			return fmt.Errorf("write organization dispatch snapshot: %w", err)
		}
		if err := importOrganizationDuties(ctx, tx, snapshot); err != nil {
			return fmt.Errorf("write organization business duties: %w", err)
		}
		if err := importHealthCategoryLinks(ctx, tx, snapshot.AIHealthCategories); err != nil {
			return fmt.Errorf("write AI health category links: %w", err)
		}
		if err := importHealthReceipts(ctx, tx, snapshot.AIHealthReceipts); err != nil {
			return fmt.Errorf("write AI health receipts: %w", err)
		}
		var cyclic bool
		if err := tx.QueryRow(ctx, `WITH RECURSIVE ancestry(id,parent_id) AS (
		 SELECT id,parent_id FROM organization_units WHERE parent_id IS NOT NULL
		 UNION
		 SELECT a.id,u.parent_id FROM ancestry a JOIN organization_units u ON u.id=a.parent_id WHERE u.parent_id IS NOT NULL
		 ) SELECT EXISTS (SELECT 1 FROM ancestry WHERE id=parent_id)`).Scan(&cyclic); err != nil {
			return err
		}
		if cyclic {
			return errors.New("organization contains a parent cycle")
		}
		_, err = tx.Exec(ctx, `INSERT INTO organization_import_state(captured_at,digest) VALUES ($1,$2)
		 ON CONFLICT(singleton) DO UPDATE SET captured_at=excluded.captured_at,digest=excluded.digest`, snapshot.CapturedAt, digest[:])
		if err != nil {
			return err
		}
		return enqueueDraftConfiguration(ctx, tx, `SELECT id FROM periods`)
	})
}

const importOrganizationPeopleSQL = `INSERT INTO people(name,legacy_party_id,a2_status,a2_name_status)
 SELECT CASE WHEN id IS NOT NULL THEN nullif(btrim(name),'') END,id,status,display_name_status
 FROM jsonb_to_recordset($1) AS incoming(id uuid,name text,status text,display_name_status text)
 ON CONFLICT(legacy_party_id) DO UPDATE SET
 name=CASE WHEN people.dispatch_name_managed THEN people.name ELSE excluded.name END,
 a2_status=coalesce(excluded.a2_status,people.a2_status),
 a2_name_status=coalesce(excluded.a2_name_status,people.a2_name_status)`

const importedCompanyTypeChange = `excluded.sales_company_type IS NOT NULL
 AND organization_units.sales_company_type IS DISTINCT FROM excluded.sales_company_type
 AND NOT EXISTS(SELECT 1 FROM organization_sales_company_type_changes ch WHERE ch.unit_id=organization_units.id)`

const importOrganizationUnitsSQL = `INSERT INTO organization_units(id,parent_id,name,kind,status,sort_order,sales_company_type)
 SELECT id,"parentId",name,kind,status,coalesce("sortOrder",100),"salesCompanyType"
 FROM jsonb_to_recordset($1) AS incoming(id uuid,"parentId" uuid,name text,kind text,status text,"sortOrder" integer,"salesCompanyType" text)
 ON CONFLICT(id) DO UPDATE SET parent_id=CASE WHEN organization_units.dispatch_profile_version=0 THEN excluded.parent_id ELSE organization_units.parent_id END,
 name=CASE WHEN organization_units.dispatch_profile_version=0 THEN excluded.name ELSE organization_units.name END,kind=excluded.kind,
 status=CASE WHEN organization_units.dispatch_profile_version=0 THEN excluded.status ELSE organization_units.status END,
 sort_order=excluded.sort_order,
 sales_company_type=CASE WHEN ` + importedCompanyTypeChange + `
  THEN excluded.sales_company_type ELSE organization_units.sales_company_type END,
 sales_company_type_version=organization_units.sales_company_type_version +
  CASE WHEN ` + importedCompanyTypeChange + ` THEN 1 ELSE 0 END`

const importOrganizationAssignmentsSQL = `INSERT INTO organization_assignments(id,person_id,unit_id,kind,position,valid_from,valid_to,appointment_type,health_member_id)
 SELECT i.id,p.id,i."unitId",i.kind,coalesce(i.position,''),i."from",i."to",i."appointmentType",i."healthMemberId"
 FROM jsonb_to_recordset($1) AS i(id uuid,"personId" uuid,"unitId" uuid,kind text,position text,"from" timestamptz,"to" timestamptz,"appointmentType" text,"healthMemberId" text)
 LEFT JOIN people p ON p.legacy_party_id=i."personId"
 ON CONFLICT(id) DO UPDATE SET person_id=excluded.person_id,unit_id=excluded.unit_id,kind=excluded.kind,
 position=excluded.position,valid_from=excluded.valid_from,valid_to=excluded.valid_to,
 appointment_type=coalesce(excluded.appointment_type,organization_assignments.appointment_type),
 health_member_id=coalesce(excluded.health_member_id,organization_assignments.health_member_id)
 WHERE organization_assignments.changed_at IS NULL`

const importOrganizationCompanySourcesSQL = `INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from,valid_to)
 SELECT i.source,nullif(btrim(i."sourceCompanyId"),''),u.id,i."from",least(i."to",(
  SELECT min(c.closed_at) FROM organization_company_use_closures c WHERE c.company_id=u.id AND c.source=i.source
  AND c.external_id=btrim(i."sourceCompanyId") AND c.closed_at>i."from"))
 FROM jsonb_to_recordset($1) AS i(source text,"sourceCompanyId" text,"companyId" uuid,"from" timestamptz,"to" timestamptz)
 LEFT JOIN organization_units u ON u.id=i."companyId" AND u.kind='COMPANY'
 ON CONFLICT(source,source_company_id,valid_from) DO UPDATE SET company_id=excluded.company_id,valid_to=excluded.valid_to`

const importOrganizationCompanyRecommendationsSQL = `INSERT INTO organization_company_recommendations(company_id,referrer_company_id)
 SELECT CASE WHEN i.value?'referrerCompanyId' AND (i.value->>'referrerCompanyId' IS NULL OR parent.id IS NOT NULL)
  THEN direct.id END,parent.id
 FROM jsonb_array_elements($1) AS i(value)
 LEFT JOIN organization_units direct ON direct.id=(i.value->>'companyId')::uuid AND direct.kind='COMPANY'
 LEFT JOIN organization_units parent ON parent.id=(i.value->>'referrerCompanyId')::uuid AND parent.kind='COMPANY'
 ON CONFLICT(company_id) DO UPDATE SET referrer_company_id=excluded.referrer_company_id`
