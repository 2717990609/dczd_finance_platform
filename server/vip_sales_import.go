package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type vipSalesSnapshot struct {
	Source          string
	CapturedAt      time.Time
	People          json.RawMessage
	PersonLinks     json.RawMessage
	CompanyLinks    json.RawMessage
	Categories      json.RawMessage
	CategoryOptions json.RawMessage
	Appointments    json.RawMessage
	Organizations   json.RawMessage
}

func importVIPSalesContext(ctx context.Context, cfg config, path string) error {
	if !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return errors.New("sales context import requires a development database")
	}
	var result json.RawMessage
	if err := readImportFile(path, &result, 8<<20); err != nil {
		return err
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("could not initialize the development database")
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		return err
	}
	return storeVIPSalesContext(ctx, db, result)
}

func storeVIPSalesContext(ctx context.Context, db *pgxpool.Pool, raw json.RawMessage) error {
	var s vipSalesSnapshot
	if err := json.Unmarshal(raw, &s); err != nil || s.Source != "DCAI" || s.CapturedAt.IsZero() {
		return invalid
	}
	statements := []struct {
		label, query string
		rows         json.RawMessage
	}{
		{"people", importOrganizationPeopleSQL, s.People},
		{"person links", importVIPSalesPersonLinks, s.PersonLinks},
		{"company links", importVIPSalesCompanyLinks, s.CompanyLinks},
		{"categories", importVIPSalesCategories, s.Categories},
		{"appointments", importVIPSalesAppointments, s.Appointments},
	}
	if len(s.Organizations) != 0 {
		statements = append(statements, struct {
			label, query string
			rows         json.RawMessage
		}{"beneficiary organizations", importVIPBenefitOrganizations, s.Organizations})
	}
	if len(s.CategoryOptions) != 0 && s.CategoryOptions[0] != '[' {
		return invalid
	}
	for _, item := range statements {
		if len(item.rows) == 0 || item.rows[0] != '[' {
			return invalid
		}
	}
	sum := sha256.Sum256(raw)
	s.CapturedAt = s.CapturedAt.UTC().Truncate(time.Microsecond)
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(637619250)"); err != nil {
			return err
		}
		var at time.Time
		var prior []byte
		err := tx.QueryRow(ctx, "SELECT captured_at,snapshot_digest FROM catalog_sales_context WHERE source=$1", s.Source).Scan(&at, &prior)
		switch {
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		case s.CapturedAt.Before(at):
			return errors.New("sales context snapshot is older than the imported version")
		case s.CapturedAt.Equal(at):
			if !bytes.Equal(sum[:], prior) {
				return errors.New("sales context snapshot timestamp has different content")
			}
			return nil
		}
		var appointmentsManaged bool
		err = tx.QueryRow(ctx, `INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest,benefit_ready) VALUES($1,$2,$3,$4)
		 ON CONFLICT(source) DO UPDATE SET captured_at=excluded.captured_at,snapshot_digest=excluded.snapshot_digest,benefit_ready=excluded.benefit_ready
		 RETURNING appointments_at IS NOT NULL`, s.Source, s.CapturedAt, sum[:], len(s.Organizations) != 0).Scan(&appointmentsManaged)
		if err != nil {
			return err
		}
		for i, item := range statements {
			if appointmentsManaged && item.label == "appointments" {
				continue
			}
			args := []any{item.rows}
			if i != 0 {
				args = append(args, s.Source, s.CapturedAt)
			}
			if _, err = tx.Exec(ctx, item.query, args...); err != nil {
				return fmt.Errorf("invalid sales context %s", item.label)
			}
		}
		if len(s.CategoryOptions) != 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO configuration_options(group_key,option_key,label,display_order)
			 SELECT 'catalogSalesCategory',id::text,name,coalesce("sortOrder",100)
			 FROM jsonb_to_recordset($1) AS item(id uuid,name text,"sortOrder" int)
			 ON CONFLICT(group_key,option_key) DO NOTHING`, s.CategoryOptions); err != nil {
				return fmt.Errorf("invalid sales category options: %w", err)
			}
		}
		return enqueueDraftConfiguration(ctx, tx, draftRefreshSalesContext, s.Source)
	})
}

const importVIPSalesPersonLinks = `INSERT INTO catalog_sales_people_sources(id,source,external_id,person_id,valid_from,valid_to,observed_at)
 SELECT i.id,$2,i."externalId",p.id,i."from",i."to",$3
 FROM jsonb_to_recordset($1) AS i(id uuid,"externalId" text,"partyId" uuid,"from" timestamptz,"to" timestamptz)
 LEFT JOIN people p ON p.legacy_party_id=i."partyId"
 ON CONFLICT(id) DO UPDATE SET person_id=excluded.person_id,valid_from=excluded.valid_from,valid_to=excluded.valid_to,observed_at=excluded.observed_at`

const importVIPSalesCompanyLinks = `INSERT INTO catalog_sales_company_aliases(id,source,external_id,company_uuid,company_unit_id,valid_from,valid_to,observed_at)
 SELECT id,$2,"externalId","companyId","unitId","from","to",$3
 FROM jsonb_to_recordset($1) AS i(id uuid,"externalId" text,"companyId" uuid,"unitId" uuid,"from" timestamptz,"to" timestamptz)
 ON CONFLICT(id) DO UPDATE SET company_uuid=excluded.company_uuid,company_unit_id=excluded.company_unit_id,
 valid_from=excluded.valid_from,valid_to=excluded.valid_to,observed_at=excluded.observed_at`

const importVIPSalesCategories = `INSERT INTO catalog_sales_categories(source,company_uuid,category_uuid,valid_from,valid_to,observed_at)
 SELECT $2,"companyId","categoryId","from","to",$3
 FROM jsonb_to_recordset($1) AS i("companyId" uuid,"categoryId" uuid,"from" timestamptz,"to" timestamptz)
 ON CONFLICT(source,company_uuid,valid_from) DO UPDATE SET category_uuid=excluded.category_uuid,
 valid_to=excluded.valid_to,observed_at=excluded.observed_at`

const importVIPSalesAppointments = `INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,valid_to,observed_at)
 SELECT i.id,$2,p.id,i.role,i.scope,i.key,i."from",i."to",$3
 FROM jsonb_to_recordset($1) AS i(id uuid,"partyId" uuid,role text,scope text,key text,"from" timestamptz,"to" timestamptz)
 LEFT JOIN people p ON p.legacy_party_id=i."partyId"
 ON CONFLICT(id) DO UPDATE SET person_id=excluded.person_id,role=excluded.role,scope=excluded.scope,scope_key=excluded.scope_key,
 valid_from=excluded.valid_from,valid_to=excluded.valid_to,observed_at=excluded.observed_at`

const importVIPBenefitOrganizations = `INSERT INTO catalog_benefit_organizations(source,party_uuid,unit_id,observed_at)
 SELECT $2,"partyId","unitId",$3
 FROM jsonb_to_recordset($1) AS i("partyId" uuid,"unitId" uuid)
 ON CONFLICT(source,party_uuid,unit_id) DO UPDATE SET observed_at=excluded.observed_at`
