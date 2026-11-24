package main

import (
	"context"
	"encoding/json"
	"errors"
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
	input.ID, input.Revision, input.Name = created.ID, created.Revision, "项目复核"
	updated, err := s.saveOrganizationPosition(ctx, input)
	if err != nil || updated.ID != created.ID || updated.Name != "项目复核" || updated.Revision == created.Revision {
		t.Fatalf("unexpected update: %+v %v", updated, err)
	}
	for _, productID := range []int64{1, 2} {
		options, err := readCatalogStructureOptions(ctx, s.db, productID)
		role := catalogStructureOptionFor(options.Roles, updated.ID)
		if err != nil || role.Label != updated.Name || !role.Personal || role.Organization {
			t.Fatalf("product %d received wrong position: %+v %v", productID, role, err)
		}
	}
	active = false
	if _, err := s.saveOrganizationPosition(ctx, input); !errors.Is(err, errOrganizationPositionChanged) {
		t.Fatalf("stale position was accepted: %v", err)
	}
	input.Revision = updated.Revision
	disabled, err := s.saveOrganizationPosition(ctx, input)
	if err != nil || disabled.Active {
		t.Fatalf("disable failed: %+v %v", disabled, err)
	}
	options, err := readCatalogStructureOptions(ctx, s.db, 2)
	if err != nil || catalogStructureOptionFor(options.Roles, disabled.ID).Value != "" {
		t.Fatalf("disabled position is selectable: %+v %v", options.Roles, err)
	}
	var labels map[string][]struct {
		Value, Label string
		Disabled     bool
	}
	var raw json.RawMessage
	if err := s.db.QueryRow(ctx, configurationOptionsSQL).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &labels); err != nil {
		t.Fatal(err)
	}
	for _, role := range labels["catalogAppointmentRole"] {
		if role.Value == disabled.ID && role.Disabled && role.Label == disabled.Name {
			return
		}
	}
	t.Fatal("disabled position lost its display label")
}

func TestOrganizationPositionWriteRequiresOrganizationAccess(t *testing.T) {
	active := true
	input := saveOrganizationPositionInput{Name: "项目督导", Active: &active, Scopes: []string{"PROJECT"}}
	ctx := context.WithValue(context.Background(), financePrincipalKey{}, &financePrincipal{Workspace: true})
	if _, err := (workspace{}).saveOrganizationPosition(ctx, input); !errors.Is(err, errFinanceForbidden) {
		t.Fatalf("write without organization permission: %v", err)
	}
}
