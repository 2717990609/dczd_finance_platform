package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var errCatalogRateConditions = errors.New("费率条件重复或存在重叠，请核对公司、销售关系和销售类别")

type catalogStructureOption struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Kind     string `json:"kind,omitempty"`
	UnitID   string `json:"unitId,omitempty"`
	Personal bool   `json:"personal,omitempty"`
}

type catalogStructureOptions struct {
	Companies     []catalogStructureOption `json:"companies"`
	Relationships []catalogStructureOption `json:"relationships"`
	Categories    []catalogStructureOption `json:"categories"`
	Roles         []catalogStructureOption `json:"roles"`
	Organizations []catalogStructureOption `json:"organizations"`
}

type catalogConditionInput struct {
	Company      string `json:"company"`
	Relationship string `json:"relationship"`
	Category     string `json:"category,omitempty"`
	Rate         *int64 `json:"rateBps"`
}

type catalogNewRateRow struct {
	ItemKey string `json:"itemKey"`
	catalogConditionInput
}

type catalogNewBenefitItem struct {
	Label        string                  `json:"label"`
	Target       string                  `json:"target"`
	Role         string                  `json:"role,omitempty"`
	PersonID     int64                   `json:"personId,omitempty"`
	Organization string                  `json:"organization,omitempty"`
	Rate         *int64                  `json:"rateBps,omitempty"`
	Rows         []catalogConditionInput `json:"rows,omitempty"`
}

const catalogStructureOptionsSQL = `WITH companies AS (
 SELECT company_uuid::text AS value,min(u.name) AS label
 FROM catalog_sales_company_aliases a JOIN catalog_sales_context c ON c.source=a.source AND c.captured_at=a.observed_at
 JOIN organization_units u ON u.id=a.company_unit_id AND u.kind='COMPANY' AND u.status='ACTIVE'
 WHERE a.source='DCAI' GROUP BY company_uuid HAVING count(DISTINCT u.id)=1
), organizations AS (
 SELECT b.party_uuid::text AS value,min(u.name) AS label,min(u.kind) AS kind,min(u.id::text) AS "unitId"
 FROM catalog_benefit_organizations b JOIN catalog_sales_context c ON c.source=b.source AND c.captured_at=b.observed_at
 JOIN organization_units u ON u.id=b.unit_id AND u.status='ACTIVE'
 WHERE b.source='DCAI' GROUP BY b.party_uuid HAVING count(DISTINCT u.id)=1
), configured AS (
 SELECT group_key,jsonb_agg(jsonb_build_object('value',option_key,'label',label,
  'personal',group_key='catalogAppointmentRole' AND (NOT applies_to ? 'scopes' OR applies_to->'scopes' ?| ARRAY['PRODUCT','PROJECT']))
   ORDER BY display_order,option_key) AS items
 FROM configuration_options
 WHERE enabled AND group_key IN ('catalogSalesCategory','catalogSalesRelationship','catalogAppointmentRole')
 GROUP BY group_key
)
SELECT jsonb_build_object(
 'companies',jsonb_build_array(jsonb_build_object('value','ANY','label','不限公司'))||
  coalesce((SELECT jsonb_agg(to_jsonb(c) ORDER BY label,value) FROM companies c),'[]'),
 'organizations',coalesce((SELECT jsonb_agg(to_jsonb(o) ORDER BY label,value) FROM organizations o),'[]'),
 'categories',coalesce((SELECT items FROM configured WHERE group_key='catalogSalesCategory'),'[]'),
 'relationships',coalesce((SELECT items FROM configured WHERE group_key='catalogSalesRelationship'),'[]'),
 'roles',coalesce((SELECT items FROM configured WHERE group_key='catalogAppointmentRole'),'[]'))`

func readCatalogStructureOptions(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (catalogStructureOptions, error) {
	var result catalogStructureOptions
	err := db.QueryRow(ctx, catalogStructureOptionsSQL).Scan(&result)
	return result, err
}

func catalogStructureOptionFor(choices []catalogStructureOption, value string) catalogStructureOption {
	i := slices.IndexFunc(choices, func(option catalogStructureOption) bool { return option.Value == value })
	if i < 0 {
		return catalogStructureOption{}
	}
	return choices[i]
}

func (o catalogStructureOptions) condition(row catalogConditionInput) (json.RawMessage, error) {
	if row.Rate == nil || *row.Rate < 0 || *row.Rate > 10000 ||
		catalogStructureOptionFor(o.Companies, row.Company).Value == "" ||
		catalogStructureOptionFor(o.Relationships, row.Relationship).Value == "" ||
		row.Category != "" && catalogStructureOptionFor(o.Categories, row.Category).Value == "" {
		return nil, invalid
	}
	return json.Marshal(vipRateRow{Company: row.Company, Relationship: row.Relationship, Category: row.Category, Rate: *row.Rate})
}

func editCatalogStructure(ctx context.Context, tx pgx.Tx, product int64, at time.Time, raw json.RawMessage,
	items []catalogNewBenefitItem, rateRows []catalogNewRateRow) (json.RawMessage, error) {
	if _, err := tx.Exec(ctx, `SELECT source FROM catalog_sales_context WHERE source='DCAI' FOR SHARE`); err != nil {
		return nil, err
	}
	options, err := readCatalogStructureOptions(ctx, tx)
	if err != nil {
		return nil, err
	}
	root, actions, err := catalogRateActions(raw)
	if err != nil {
		return nil, err
	}
	positions := make(map[string]int, len(actions))
	for i, action := range actions {
		var key string
		if err := json.Unmarshal(action["item_key"], &key); err != nil {
			return nil, err
		}
		positions[key] = i
	}
	additions := make(map[string][]json.RawMessage)
	for _, row := range rateRows {
		index, found := positions[row.ItemKey]
		if !found || string(actions[index]["type"]) != `"RATE_TABLE"` {
			return nil, invalid
		}
		value, err := options.condition(row.catalogConditionInput)
		if err != nil {
			return nil, err
		}
		additions[row.ItemKey] = append(additions[row.ItemKey], value)
	}
	for key, added := range additions {
		fields := actions[positions[key]]
		var existing []json.RawMessage
		if err := json.Unmarshal(fields["rate_table"], &existing); err != nil {
			return nil, err
		}
		updated := append(slices.Clone(existing), added...)
		if err := validateCatalogNewRateConditions(existing, updated); err != nil {
			return nil, err
		}
		fields["rate_table"], err = json.Marshal(updated)
		if err != nil {
			return nil, err
		}
	}
	var people []catalogRuleRecipientChange
	for _, item := range items {
		if item.Target == "person" {
			people = append(people, catalogRuleRecipientChange{PersonID: item.PersonID})
		}
	}
	keys, err := catalogNewRecipientKeys(ctx, tx, people, nil)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		var key string
		for n := len(actions) + 1; ; n++ {
			key = "benefit-" + strconv.Itoa(n)
			if _, ok := positions[key]; !ok {
				break
			}
		}
		fields, err := newCatalogBenefitAction(ctx, tx, product, at, key, item, options, keys)
		if err != nil {
			return nil, err
		}
		positions[key] = len(actions)
		actions = append(actions, fields)
	}
	root["benefit_actions"], err = json.Marshal(actions)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	before, _ := readVIPSalesPolicy(raw)
	after, ok := readVIPSalesPolicy(body)
	if !ok {
		return nil, errCatalogRateUnavailable
	}
	if len(before.Split.Teams) != 0 && internalTeamRatesChanged(before, after) {
		return nil, errCatalogTeamRate
	}
	return body, nil
}

func newCatalogBenefitAction(ctx context.Context, tx pgx.Tx, product int64, at time.Time, key string,
	item catalogNewBenefitItem, options catalogStructureOptions, people map[int64]string) (map[string]json.RawMessage, error) {
	label := strings.TrimSpace(item.Label)
	if !textOK(label, 80) || (item.Rate == nil) == (len(item.Rows) == 0) || item.Rate != nil && (*item.Rate < 0 || *item.Rate > 10000) {
		return nil, invalid
	}
	action := vipBenefitAction{Item: key, Label: label, Type: "RATE", Mode: "PARTY", Rate: item.Rate}
	org := catalogStructureOptionFor(options.Organizations, item.Organization)
	role := catalogStructureOptionFor(options.Roles, item.Role)
	switch item.Target {
	case "person":
		if item.Organization != "" || !role.Personal || people[item.PersonID] == "" {
			return nil, invalid
		}
		action.Role, action.Designated = role.Value, people[item.PersonID]
	case "company":
		if item.PersonID != 0 || item.Role != "" || org.Kind != "COMPANY" {
			return nil, invalid
		}
		action.Role, action.Company = "COMPANY", org.Value
	case "pool":
		if item.PersonID != 0 || org.Value == "" || role.Value == "" {
			return nil, invalid
		}
		if err := validateCatalogPoolAppointment(ctx, tx, product, at, org.UnitID, role.Value); err != nil {
			return nil, err
		}
		action.Role, action.OwnerRole = role.Value, role.Value
		action.Mode, action.PoolParty, action.PoolUnit = "TEAM_POOL", org.Value, org.UnitID
		action.PoolCode, action.PoolName = key, label
	default:
		return nil, invalid
	}
	if len(item.Rows) != 0 {
		action.Type = "RATE_TABLE"
		for _, row := range item.Rows {
			value, err := options.condition(row)
			if err != nil {
				return nil, err
			}
			action.RateTable = append(action.RateTable, value)
		}
		if err := validateCatalogNewRateConditions(nil, action.RateTable); err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	err = json.Unmarshal(body, &fields)
	return fields, err
}

func validateCatalogPoolAppointment(ctx context.Context, tx pgx.Tx, product int64, at time.Time, unit, role string) error {
	var holders int
	err := tx.QueryRow(ctx, `SELECT count(DISTINCT a.person_id)
	 FROM products p JOIN product_catalog_settings c ON c.product_id=p.id
	 JOIN catalog_current_sales_appointments a ON a.source=c.source_system
	 WHERE p.id=$1 AND a.role=$4 AND a.valid_from<=$2 AND (a.valid_to IS NULL OR a.valid_to>$2)
	  AND ((a.scope='ORGANIZATION_UNIT' AND a.scope_key=$3) OR (a.scope='PRODUCT' AND a.scope_key=p.code)
	   OR (a.scope='PROJECT' AND a.scope_key=c.project_code))
	 GROUP BY a.scope ORDER BY CASE a.scope WHEN 'ORGANIZATION_UNIT' THEN 1 WHEN 'PRODUCT' THEN 2 ELSE 3 END LIMIT 1`,
		product, at, unit, role).Scan(&holders)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && holders != 1 {
		return errCatalogRecipientAppointment
	}
	return err
}

type catalogRateScope struct{ company, category string }

type catalogRateIndex struct {
	groups      map[catalogRateScope][]vipRateRow
	categorized bool
}

func indexCatalogRates(rows []vipRateRow) catalogRateIndex {
	index := catalogRateIndex{groups: make(map[catalogRateScope][]vipRateRow)}
	for _, row := range rows {
		scope := catalogRateScope{row.Company, row.Category}
		index.groups[scope] = append(index.groups[scope], row)
		index.categorized = index.categorized || row.Category != ""
	}
	return index
}

func (i catalogRateIndex) candidates(company string, category *string) []vipRateRow {
	if category == nil || *category == "" {
		return slices.Concat(i.groups[catalogRateScope{company, ""}], i.groups[catalogRateScope{"ANY", ""}])
	}
	return slices.Concat(i.groups[catalogRateScope{company, ""}], i.groups[catalogRateScope{"ANY", ""}],
		i.groups[catalogRateScope{company, *category}], i.groups[catalogRateScope{"ANY", *category}])
}

func validateCatalogNewRateConditions(existing, updated []json.RawMessage) error {
	var before []vipRateRow
	if len(existing) > 0 {
		var ok bool
		before, ok = parseVIPRateTable(existing)
		if !ok {
			return errCatalogRateUnavailable
		}
	}
	after, ok := parseVIPRateTable(updated)
	if !ok {
		return invalid
	}
	type condition struct{ company, relationship, category string }
	seen := make(map[condition]bool, len(after))
	companies := map[string]bool{"unlisted-company": true}
	categories := map[string]bool{"": true}
	for n, row := range after {
		key := condition{row.Company, row.Relationship, row.Category}
		if n >= len(before) && seen[key] {
			return errCatalogRateConditions
		}
		seen[key] = true
		if row.Company != "ANY" {
			companies[row.Company] = true
		}
		categories[row.Category] = true
	}
	// Compare the winner for each condition the matcher can distinguish,
	// while only examining rows that can match the current company/category.
	previous, next := indexCatalogRates(before), indexCatalogRates(after)
	probes := []*string{nil}
	for category := range categories {
		probes = append(probes, &category)
	}
	for company := range companies {
		for _, category := range probes {
			newRows := next.candidates(company, category)
			if len(newRows) < 2 {
				continue
			}
			for _, relationship := range []string{"DIRECT", "INDIRECT", "NONE"} {
				if _, issue := resolveVIPRateRows(newRows, company, relationship, category, next.categorized); issue != "SALES_RATE_CONFLICT" {
					continue
				}
				oldRows := previous.candidates(company, category)
				if _, issue := resolveVIPRateRows(oldRows, company, relationship, category, previous.categorized); issue != "SALES_RATE_CONFLICT" {
					return errCatalogRateConditions
				}
			}
		}
	}
	return nil
}
