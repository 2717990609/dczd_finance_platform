package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const organizationUnitsSQL = `SELECT coalesce(jsonb_agg(jsonb_build_object(
 'id',id,'parentId',parent_id,'name',name,'kind',kind,'status',status)
 ORDER BY sort_order,name,id),'[]') FROM organization_units`

const organizationAssignmentsSQL = `WITH matching AS (
 SELECT a.id,p.name AS "personName",p.id AS "personId",u.name AS "unitName",
  a.kind,a.position,a.valid_from AS "validFrom",a.valid_to AS "validTo"
 FROM organization_assignments a JOIN people p ON p.id=a.person_id JOIN organization_units u ON u.id=a.unit_id
 WHERE ($1::uuid IS NULL OR a.unit_id=$1) AND
  ($2::timestamptz IS NULL OR (a.valid_from<=$2 AND (a.valid_to IS NULL OR a.valid_to>$2))) AND
  ($3='' OR strpos(lower(p.name),lower($3))>0)
 ), page AS (
 SELECT * FROM matching ORDER BY "personName","validFrom" DESC,id LIMIT 25 OFFSET $4
 )
 SELECT jsonb_build_object('total',(SELECT count(*) FROM matching),
  'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY "personName","validFrom" DESC,id) FROM page),'[]'))`

func (s workspace) organizationAssignments(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	var unitID pgtype.UUID
	if value := params.Get("unitId"); value != "" && unitID.Scan(value) != nil {
		respond(w, nil, invalid)
		return
	}
	page := 1
	if value := params.Get("page"); value != "" {
		var err error
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 || page > 100000 {
			respond(w, nil, invalid)
			return
		}
	}
	var at *time.Time
	history, on := params.Get("history"), params.Get("on")
	if history != "" && (history != "true" || on != "") {
		respond(w, nil, invalid)
		return
	}
	if history == "" {
		date := time.Now()
		if on != "" {
			var err error
			date, err = time.ParseInLocation(time.DateOnly, on, shanghai)
			if err != nil {
				respond(w, nil, invalid)
				return
			}
		}
		at = &date
	}
	var result json.RawMessage
	err := s.db.QueryRow(r.Context(), organizationAssignmentsSQL, unitID, at,
		strings.TrimSpace(params.Get("name")), (page-1)*25).Scan(&result)
	respond(w, result, err)
}

type organizationSnapshot struct {
	CapturedAt      time.Time
	People          json.RawMessage
	Units           json.RawMessage
	Assignments     json.RawMessage
	Companies       json.RawMessage
	Recommendations json.RawMessage
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

func storeOrganizationSnapshot(ctx context.Context, db *pgxpool.Pool, raw json.RawMessage) error {
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
	for _, rows := range []json.RawMessage{snapshot.Companies, snapshot.Recommendations} {
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

const importOrganizationPeopleSQL = `INSERT INTO people(name,legacy_party_id)
 SELECT CASE WHEN id IS NOT NULL THEN nullif(btrim(name),'') END,id
 FROM jsonb_to_recordset($1) AS incoming(id uuid,name text)
 ON CONFLICT(legacy_party_id) DO UPDATE SET name=excluded.name`

const importOrganizationUnitsSQL = `INSERT INTO organization_units(id,parent_id,name,kind,status,sort_order)
 SELECT id,"parentId",name,kind,status,coalesce("sortOrder",100)
 FROM jsonb_to_recordset($1) AS incoming(id uuid,"parentId" uuid,name text,kind text,status text,"sortOrder" integer)
 ON CONFLICT(id) DO UPDATE SET parent_id=excluded.parent_id,name=excluded.name,kind=excluded.kind,status=excluded.status,sort_order=excluded.sort_order`

const importOrganizationAssignmentsSQL = `INSERT INTO organization_assignments(id,person_id,unit_id,kind,position,valid_from,valid_to)
 SELECT i.id,p.id,i."unitId",i.kind,coalesce(i.position,''),i."from",i."to"
 FROM jsonb_to_recordset($1) AS i(id uuid,"personId" uuid,"unitId" uuid,kind text,position text,"from" timestamptz,"to" timestamptz)
 LEFT JOIN people p ON p.legacy_party_id=i."personId"
 ON CONFLICT(id) DO UPDATE SET person_id=excluded.person_id,unit_id=excluded.unit_id,kind=excluded.kind,
 position=excluded.position,valid_from=excluded.valid_from,valid_to=excluded.valid_to`

const importOrganizationCompanySourcesSQL = `INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from,valid_to)
 SELECT i.source,nullif(btrim(i."sourceCompanyId"),''),u.id,i."from",i."to"
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
