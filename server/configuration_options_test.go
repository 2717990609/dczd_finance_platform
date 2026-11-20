package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestConfigurationOptionsAreReadFromDatabaseAndMigrationKeepsChanges(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	if _, err := db.Exec(ctx, `UPDATE configuration_options SET label='按课次核算',display_order=90 WHERE group_key='periodMode' AND option_key='class';
	 UPDATE configuration_options SET enabled=false WHERE group_key='ruleScope' AND option_key='ordinal';
	 INSERT INTO configuration_options(group_key,option_key,label,display_order) VALUES('practiceSalesCompany','30','30',10);
	 INSERT INTO organization_units(id,kind,name,status)
	  VALUES('30000000-0000-4000-8000-000000000030','COMPANY','样本公司','ACTIVE');
	 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
	  VALUES('DCZD','30','30000000-0000-4000-8000-000000000030','2020-01-01');`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	service := workspace{db: db}
	response := httptest.NewRecorder()
	service.list(configurationOptionsSQL)(response, httptest.NewRequest("GET", "/api/v1/configuration-options", nil))
	var choices map[string][]struct{ Value, Label string }
	if err := json.Unmarshal(response.Body.Bytes(), &choices); err != nil || response.Code != 200 {
		t.Fatalf("configuration options HTTP %d, error %v", response.Code, err)
	}
	modes := choices["periodMode"]
	if len(modes) != 2 || modes[0].Value != "calendar" || modes[1].Label != "按课次核算" {
		t.Fatalf("modes=%+v", modes)
	}
	companies := choices["practiceSalesCompany"]
	if len(companies) != 1 || companies[0].Value != "30" || companies[0].Label != "样本公司" {
		t.Fatalf("source company names were not resolved: %+v", companies)
	}
	for _, option := range choices["ruleScope"] {
		if option.Value == "ordinal" {
			t.Fatal("disabled option is still offered")
		}
	}
}
