package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// A source can describe the same company in both the organization directory
// and the sales catalog. Show a single active use and retain ended periods.
const companyBusinessUsesSQL = `WITH periods AS (
 SELECT source,source_company_id AS external_id,valid_from,valid_to FROM organization_company_sources WHERE company_id=$1
 UNION SELECT a.source,a.external_id,a.valid_from,a.valid_to FROM catalog_sales_company_aliases a JOIN catalog_sales_context c
 ON a.source=c.source AND a.observed_at=c.captured_at WHERE a.company_unit_id=$1
), classified AS (
 SELECT *,tstzrange(valid_from,valid_to,'[)') @> statement_timestamp() AS active FROM periods
) SELECT source,external_id AS "externalId", valid_from AS "validFrom",valid_to AS "validTo",active
 FROM classified WHERE NOT active
 UNION ALL SELECT source,external_id,max(valid_from),null,true FROM classified WHERE active GROUP BY source,external_id`

type closeCompanyUseInput struct {
	RequestID, CompanyID, Source, ExternalID string
	ExpectedFrom                             time.Time
}

func (s workspace) closeCompanyUse(ctx context.Context, in closeCompanyUseInput) (map[string]bool, error) {
	company, ok := canonicalSourceUUID(in.CompanyID)
	request, valid := canonicalSourceUUID(in.RequestID)
	if !ok || !valid || !validDispatchTarget(in.Source) || !textOK(in.ExternalID, 128) || in.ExpectedFrom.IsZero() {
		return nil, invalid
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, actor *string) error {
		// Serialize with sales-context imports, health writes and delivery receipts.
		// writeAppointments already holds the organization import lock in shared mode.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(637619250)"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT id::text FROM organization_units WHERE id=$1 AND sales_company_type IS NOT NULL FOR NO KEY UPDATE", company).Scan(&company); err != nil {
			return err
		}
		if err := lockDispatchEntity(ctx, tx, in.Source, "COMPANY", company); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, `SELECT company_id=$2 AND source=$3 AND external_id=$4 AND expected_from=$5 AND actor IS NOT DISTINCT FROM $6::uuid
		FROM organization_company_use_closures WHERE request_id=$1`, request, company, in.Source, in.ExternalID, in.ExpectedFrom, actor).Scan(&same)
		if err == nil {
			if !same {
				return conflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var activeFrom, end time.Time
		err = tx.QueryRow(ctx, `SELECT "validFrom",clock_timestamp() FROM (`+companyBusinessUsesSQL+`) u WHERE source=$2 AND "externalId"=$3 AND active`, company, in.Source, in.ExternalID).Scan(&activeFrom, &end)
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict
		}
		if err != nil {
			return err
		}
		if !activeFrom.Equal(in.ExpectedFrom) || !end.After(activeFrom) {
			return conflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO organization_company_use_closures(request_id,company_id,source,external_id,expected_from,closed_at,actor) VALUES($1,$2,$3,$4,$5,$6,$7)`, request, company, in.Source, in.ExternalID, activeFrom, end, actor); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE organization_company_sources SET valid_to=$4 WHERE company_id=$1 AND source=$2 AND source_company_id=$3 AND valid_from<$4 AND (valid_to IS NULL OR valid_to>$4)`, company, in.Source, in.ExternalID, end); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE catalog_sales_company_aliases a SET valid_to=$4 FROM catalog_sales_context c WHERE a.source=c.source AND a.observed_at=c.captured_at AND a.company_unit_id=$1 AND a.source=$2 AND a.external_id=$3 AND a.valid_from<$4 AND (a.valid_to IS NULL OR a.valid_to>$4)`, company, in.Source, in.ExternalID, end); err != nil {
			return err
		}
		return enqueueDraftConfiguration(ctx, tx, draftRefreshCompanyClassification, []string{company}, end, true)
	})
	return map[string]bool{"closed": err == nil}, err
}
