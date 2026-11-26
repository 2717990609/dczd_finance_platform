package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestOrganizationPositionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	var defaults []organizationPosition
	if err := s.db.QueryRow(ctx, organizationPositionsSQL).Scan(&defaults); err != nil || len(defaults) == 0 {
		t.Fatalf("existing positions missing: %v", err)
	}
	active := true
	input := saveOrganizationPositionInput{Name: " 项目督导 ", Active: &active, Scopes: []string{"PROJECT", "PRODUCT"}}
	created, err := s.saveOrganizationPosition(ctx, input)
	if err != nil || created.ID == "" || created.Name != "项目督导" || !created.Active {
		t.Fatalf("unexpected creation: %+v %v", created, err)
	}
	if _, err := s.saveOrganizationPosition(ctx, input); !errors.Is(err, errOrganizationPositionName) {
		t.Fatalf("duplicate position: %v", err)
	}
	input = saveOrganizationPositionInput{ID: created.ID, Revision: created.Revision, Active: &active}
	rename := input
	rename.Name = "项目复核"
	changeScopes := input
	changeScopes.Scopes = []string{"ORGANIZATION_UNIT"}
	for _, edit := range []saveOrganizationPositionInput{rename, changeScopes} {
		if _, err := s.saveOrganizationPosition(ctx, edit); !errors.Is(err, invalid) {
			t.Fatalf("position details were editable: %+v, %v", edit, err)
		}
	}
	for _, productID := range []int64{1, 2} {
		options, err := readCatalogStructureOptions(ctx, s.db, productID)
		role := catalogStructureOptionFor(options.Roles, created.ID)
		if err != nil || role.Label != created.Name || !role.Personal || role.Organization {
			t.Fatalf("product %d received wrong position: %+v %v", productID, role, err)
		}
	}
	active = false
	disabled, err := s.saveOrganizationPosition(ctx, input)
	if err != nil || disabled.Active || disabled.Name != created.Name || !slices.Equal(disabled.Scopes, created.Scopes) || disabled.Revision == created.Revision {
		t.Fatalf("disable failed: %+v %v", disabled, err)
	}
	active = true
	if _, err := s.saveOrganizationPosition(ctx, input); !errors.Is(err, errOrganizationPositionChanged) {
		t.Fatalf("stale position was accepted: %v", err)
	}
	options, err := readCatalogStructureOptions(ctx, s.db, 2)
	if err != nil || catalogStructureOptionFor(options.Roles, disabled.ID).Value != "" {
		t.Fatalf("disabled position is selectable: %+v %v", options.Roles, err)
	}
	type optionLabel struct {
		Value, Label string
		Disabled     bool
	}
	var labels map[string][]optionLabel
	var raw json.RawMessage
	if err := s.db.QueryRow(ctx, configurationOptionsSQL).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &labels); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(labels["catalogAppointmentRole"], func(role optionLabel) bool {
		return role.Value == disabled.ID && role.Disabled && role.Label == disabled.Name
	}) {
		t.Fatal("disabled position lost its display label")
	}
	input.Revision = disabled.Revision
	enabled, err := s.saveOrganizationPosition(ctx, input)
	if err != nil || !enabled.Active {
		t.Fatalf("enable failed: %+v %v", enabled, err)
	}
}

func TestOrganizationPositionWriteRequiresOrganizationAccess(t *testing.T) {
	active := true
	ctx := context.WithValue(context.Background(), financePrincipalKey{}, &financePrincipal{Workspace: true})
	for _, input := range []saveOrganizationPositionInput{
		{Name: "项目督导", Active: &active, Scopes: []string{"PROJECT"}},
		{ID: "SALES", Active: &active, Revision: "8d930d104f0d6e53aa66efe0bb1c7389"},
	} {
		if _, err := (workspace{}).saveOrganizationPosition(ctx, input); !errors.Is(err, errFinanceForbidden) {
			t.Fatalf("write without organization permission: %v", err)
		}
	}
}
