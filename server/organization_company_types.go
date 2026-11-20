package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

var errOrganizationCompanyTypeChanged = errors.New("公司类型已变化，请重新读取后再保存")

func (s workspace) organizationCompanyType(w http.ResponseWriter, r *http.Request) {
	unit, ok := canonicalSourceUUID(r.URL.Query().Get("unitId"))
	if !ok {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err := s.db.QueryRow(r.Context(), `SELECT jsonb_build_object('id',u.id,'name',u.name,'status',u.status,
	 'companyType',u.sales_company_type,'version',u.sales_company_type_version,
	 'changes',coalesce((SELECT jsonb_agg(jsonb_build_object('version',c.version,'previousType',c.previous_type,
	  'companyType',c.company_type,'changedAt',c.changed_at,'changedBy',person.name) ORDER BY c.version DESC)
	  FROM organization_sales_company_type_changes c LEFT JOIN finance_accounts a ON a.id=c.changed_by
	  LEFT JOIN people person ON person.id=a.person_id WHERE c.unit_id=u.id),'[]'))
	 FROM organization_units u WHERE u.id=$1 AND u.sales_company_type IS NOT NULL`, unit).Scan(&result)
	respond(w, result, err)
}

type updateOrganizationCompanyTypeInput struct {
	UnitID          string `json:"unitId"`
	CompanyType     string `json:"companyType"`
	ExpectedVersion int64  `json:"expectedVersion"`
}

type organizationCompanyTypeResult struct {
	CompanyType string `json:"companyType"`
	Version     int64  `json:"version"`
	Changed     bool   `json:"changed"`
}

func (s workspace) updateOrganizationCompanyType(ctx context.Context, input updateOrganizationCompanyTypeInput) (organizationCompanyTypeResult, error) {
	var result organizationCompanyTypeResult
	unit, ok := canonicalSourceUUID(input.UnitID)
	if !ok || input.ExpectedVersion < 1 || input.CompanyType != "INTERNAL" && input.CompanyType != "EXTERNAL" {
		return result, invalid
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		if err := tx.QueryRow(ctx, `SELECT sales_company_type,sales_company_type_version
		 FROM organization_units WHERE id=$1 AND sales_company_type IS NOT NULL FOR UPDATE`, unit).
			Scan(&result.CompanyType, &result.Version); err != nil {
			return err
		}
		if result.CompanyType == input.CompanyType {
			return nil
		}
		if result.Version != input.ExpectedVersion {
			return errOrganizationCompanyTypeChanged
		}
		previous := result.CompanyType
		err := tx.QueryRow(ctx, `UPDATE organization_units SET sales_company_type=$2,sales_company_type_version=sales_company_type_version+1
		 WHERE id=$1 RETURNING sales_company_type,sales_company_type_version`, unit, input.CompanyType).
			Scan(&result.CompanyType, &result.Version)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO organization_sales_company_type_changes(unit_id,version,previous_type,company_type,changed_at,changed_by)
		 VALUES($1,$2,$3,$4,clock_timestamp(),$5)`, unit, result.Version, previous, result.CompanyType, by); err != nil {
			return err
		}
		result.Changed = true
		return nil
	})
	return result, err
}
