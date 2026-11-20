package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestOrganizationCompanyTypeMaintainsIdentityAndHistory(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	const unit = "00000000-0000-4000-8000-000000000071"
	const unedited = "00000000-0000-4000-8000-000000000072"
	const snapshot = `{"capturedAt":"2025-01-01T00:00:00Z","people":[],"assignments":[],
	"units":[{"id":"` + unit + `","name":"历史公司","kind":"COMPANY","status":"INACTIVE","salesCompanyType":"UNKNOWN"},
	 {"id":"` + unedited + `","name":"来源维护公司","kind":"COMPANY","status":"ACTIVE","salesCompanyType":"INTERNAL"}]}`
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(snapshot)); err != nil {
		t.Fatal(err)
	}
	input := updateOrganizationCompanyTypeInput{UnitID: unit, CompanyType: "EXTERNAL", ExpectedVersion: 1}
	restricted := context.WithValue(ctx, financePrincipalKey{}, &financePrincipal{Workspace: true})
	if _, err := s.updateOrganizationCompanyType(restricted, input); !errors.Is(err, errFinanceForbidden) {
		t.Fatalf("unprivileged change: %v", err)
	}
	updated, err := s.updateOrganizationCompanyType(ctx, input)
	if err != nil || !updated.Changed || updated.Version != 2 {
		t.Fatalf("first change: %+v %v", updated, err)
	}
	retry, err := s.updateOrganizationCompanyType(ctx, input)
	if err != nil || retry.Changed || retry.Version != 2 {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	input.CompanyType = "INTERNAL"
	if _, err := s.updateOrganizationCompanyType(ctx, input); !errors.Is(err, errOrganizationCompanyTypeChanged) {
		t.Fatalf("stale change: %v", err)
	}
	input.ExpectedVersion = 2
	updated, err = s.updateOrganizationCompanyType(ctx, input)
	if err != nil || !updated.Changed || updated.Version != 3 {
		t.Fatalf("second change: %+v %v", updated, err)
	}
	newerSnapshot := `{"capturedAt":"2025-01-02T00:00:00Z","people":[],"assignments":[],
	"units":[{"id":"` + unit + `","name":"历史公司","kind":"COMPANY","status":"INACTIVE","salesCompanyType":"EXTERNAL"},
	 {"id":"` + unedited + `","name":"来源维护公司","kind":"COMPANY","status":"ACTIVE","salesCompanyType":"EXTERNAL"}]}`
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(newerSnapshot)); err != nil {
		t.Fatal(err)
	}
	var companyType, status string
	var changes, correctHistory int
	err = s.db.QueryRow(ctx, `SELECT sales_company_type,status,
	 (SELECT count(*) FROM organization_sales_company_type_changes t WHERE t.unit_id=u.id),
	 (SELECT count(*) FROM organization_sales_company_type_changes t WHERE t.unit_id=u.id AND
	  (t.version=2 AND t.previous_type='UNKNOWN' AND t.company_type='EXTERNAL' OR t.version=3 AND t.previous_type='EXTERNAL' AND t.company_type='INTERNAL'))
	 FROM organization_units u WHERE u.id=$1`, unit).Scan(&companyType, &status, &changes, &correctHistory)
	if err != nil || companyType != "INTERNAL" || status != "INACTIVE" || changes != 2 || correctHistory != 2 {
		t.Fatalf("company or history not preserved: %s %s %d %d %v", companyType, status, changes, correctHistory, err)
	}
	var version int64
	if err := s.db.QueryRow(ctx, "SELECT sales_company_type,sales_company_type_version FROM organization_units WHERE id=$1", unedited).
		Scan(&companyType, &version); err != nil || companyType != "EXTERNAL" || version != 2 {
		t.Fatalf("source change on unedited company: %s %d %v", companyType, version, err)
	}

}
