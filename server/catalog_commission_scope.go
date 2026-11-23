package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
)

const (
	catalogCommissionScopeKey = "sales_scope_unit_ids"
	catalogCategoryScopeKey   = "sales_scope_category_ids"
)

var errCatalogCommissionScope = errors.New("请选择有效的销售类别、公司、部门或小组")

type catalogCommissionScopeChange struct {
	ItemKey     string   `json:"itemKey"`
	UnitIDs     []string `json:"unitIds"`
	CategoryIDs []string `json:"categoryIds,omitempty"`
	OwnSales    bool     `json:"ownSales,omitempty"`
}

func catalogScopeIDsValid(ids, prior []string, options []catalogStructureOption) bool {
	return validUniqueScopeIDs(ids) && !slices.ContainsFunc(ids, func(id string) bool {
		return !slices.Contains(prior, id) && catalogStructureOptionFor(options, id).Value == ""
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
	categoryAdditions := make(map[string]bool)
	for _, change := range changes {
		fields := byItem[change.ItemKey]
		if fields == nil || vipLeaderRole(change.ItemKey) != "" || !validUniqueScopeIDs(change.UnitIDs) || !validUniqueScopeIDs(change.CategoryIDs) {
			return nil, errCatalogCommissionScope
		}
		delete(fields, "own_sales")
		if change.OwnSales {
			var designated string
			if len(change.UnitIDs)+len(change.CategoryIDs) != 0 || json.Unmarshal(fields["designated_party_id"], &designated) != nil || designated == "" {
				return nil, errCatalogCommissionScope
			}
			fields["own_sales"] = json.RawMessage("true")
		}
		delete(byItem, change.ItemKey)
		if !replaceCatalogScopeIDs(fields, catalogCommissionScopeKey, change.UnitIDs, additions) ||
			!replaceCatalogScopeIDs(fields, catalogCategoryScopeKey, change.CategoryIDs, categoryAdditions) {
			return nil, errCatalogCommissionScope
		}
	}
	if err := validateNewCatalogScopeCategories(ctx, tx, categoryAdditions); err != nil {
		return nil, err
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

func validateNewCatalogScopeCategories(ctx context.Context, tx pgx.Tx, additions map[string]bool) error {
	if len(additions) == 0 {
		return nil
	}
	ids := make([]string, 0, len(additions))
	for id := range additions {
		ids = append(ids, id)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM catalog_sales_category_options WHERE key=ANY($1::text[]) AND active`, ids).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return errCatalogCommissionScope
	}
	return nil
}

func replaceCatalogScopeIDs(fields map[string]json.RawMessage, field string, ids []string, additions map[string]bool) bool {
	var prior []string
	if current := fields[field]; len(current) > 0 && json.Unmarshal(current, &prior) != nil {
		return false
	}
	for _, id := range ids {
		if !slices.Contains(prior, id) {
			additions[id] = true
		}
	}
	if len(ids) == 0 {
		delete(fields, field)
	} else {
		fields[field], _ = json.Marshal(ids)
	}
	return true
}
