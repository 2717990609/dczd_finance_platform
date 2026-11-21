package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
)

const catalogCommissionScopeKey = "sales_scope_unit_ids"

var errCatalogCommissionScope = errors.New("请选择有效的销售公司、部门或小组")

type catalogCommissionScopeChange struct {
	ItemKey string   `json:"itemKey"`
	UnitIDs []string `json:"unitIds"`
}

func catalogCommissionUnitsValid(units []string, options []catalogStructureOption) bool {
	return validVIPCommissionUnits(units) && !slices.ContainsFunc(units, func(unit string) bool {
		return catalogStructureOptionFor(options, unit).Value == ""
	})
}

func editCatalogCommissionScopes(ctx context.Context, tx pgx.Tx, raw json.RawMessage, changes []catalogCommissionScopeChange) (json.RawMessage, error) {
	root, actions, err := catalogRateActions(raw)
	if err != nil {
		return nil, err
	}
	byItem := make(map[string]map[string]json.RawMessage, len(actions))
	for _, fields := range actions {
		var key string
		if err := json.Unmarshal(fields["item_key"], &key); err != nil {
			return nil, err
		}
		byItem[key] = fields
	}
	additions := make(map[string]bool)
	for _, change := range changes {
		fields := byItem[change.ItemKey]
		if fields == nil || vipLeaderRole(change.ItemKey) != "" || !validVIPCommissionUnits(change.UnitIDs) {
			return nil, errCatalogCommissionScope
		}
		delete(byItem, change.ItemKey)
		var prior []string
		if current := fields[catalogCommissionScopeKey]; len(current) > 0 && json.Unmarshal(current, &prior) != nil {
			return nil, errCatalogCommissionScope
		}
		for _, id := range change.UnitIDs {
			if !slices.Contains(prior, id) {
				additions[id] = true
			}
		}
		if len(change.UnitIDs) == 0 {
			delete(fields, catalogCommissionScopeKey)
		} else {
			fields[catalogCommissionScopeKey], _ = json.Marshal(change.UnitIDs)
		}
	}
	if len(additions) > 0 {
		ids := make([]string, 0, len(additions))
		for id := range additions {
			ids = append(ids, id)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM organization_units
		 WHERE id=ANY($1::uuid[]) AND kind IN ('COMPANY','DEPARTMENT','TEAM') AND status='ACTIVE'`, ids).Scan(&count); err != nil {
			return nil, err
		}
		if count != len(ids) {
			return nil, errCatalogCommissionScope
		}
	}
	root["benefit_actions"], err = json.Marshal(actions)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}
