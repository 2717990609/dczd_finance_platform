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
	 UPDATE configuration_options SET enabled=false WHERE group_key='ruleScope' AND option_key='ordinal'`); err != nil {
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
	for _, option := range choices["ruleScope"] {
		if option.Value == "ordinal" {
			t.Fatal("disabled option is still offered")
		}
	}
}
