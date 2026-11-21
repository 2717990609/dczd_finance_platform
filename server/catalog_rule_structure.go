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
	Value        string `json:"value"`
	Label        string `json:"label"`
	Kind         string `json:"kind,omitempty"`
	UnitID       string `json:"unitId,omitempty"`
	Personal     bool   `json:"personal,omitempty"`
	Organization bool   `json:"organization,omitempty"`
}

type catalogStructureOptions struct {
	Companies     []catalogStructureOption `json:"companies"`
	Relationships []catalogStructureOption `json:"relationships"`
	Categories    []catalogStructureOption `json:"categories"`
	Roles         []catalogStructureOption `json:"roles"`
	Organizations []catalogStructureOption `json:"organizations"`
}

type catalogConditionInput struct {
	Company      string   `json:"company"`
	CompanyIDs   []string `json:"companyIds,omitempty"`
	Relationship string   `json:"relationship"`
	Category     string   `json:"category,omitempty"`
	CategoryIDs  []string `json:"categoryIds,omitempty"`
	Rate         *int64   `json:"rateBps"`
	Payee        string   `json:"payee,omitempty"`
}

type catalogNewRateRow struct {
	ItemKey string `json:"itemKey"`
	Row     int    `json:"row,omitempty"` // One-based base row, or zero to append.
	catalogConditionInput
}

type catalogBenefitTarget struct {
	Target       string `json:"target"`
	Role         string `json:"role,omitempty"`
	PersonID     int64  `json:"personId,omitempty"`
	Organization string `json:"organization,omitempty"`
}

type catalogBenefitTargetChange struct {
	ItemKey string `json:"itemKey"`
	catalogBenefitTarget
}

type catalogNewBenefitItem struct {
	Label string `json:"label"`
	catalogBenefitTarget
	catalogBenefitCalculation
}

type catalogBenefitCalculationChange struct {
	ItemKey string `json:"itemKey"`
	catalogBenefitCalculation
}

type catalogBenefitCalculation struct {
	Rate  *int64                  `json:"rateBps,omitempty"`
	Fixed *int64                  `json:"fixedMinor,omitempty"`
	Rows  []catalogConditionInput `json:"rows,omitempty"`
}

const catalogSalesCompanyKeysSQL = `SELECT a.company_uuid AS key,a.company_unit_id AS unit
 FROM catalog_sales_company_aliases a JOIN catalog_sales_context c ON c.source=a.source AND c.captured_at=a.observed_at
 WHERE a.source='DCAI'
 UNION
 SELECT DISTINCT s.company_id,s.company_id FROM organization_company_sources s WHERE s.source='DCAI'
  AND NOT EXISTS(SELECT 1 FROM catalog_sales_company_aliases a
   JOIN catalog_sales_context c ON c.source=a.source AND c.captured_at=a.observed_at
   WHERE a.source=s.source AND a.company_unit_id=s.company_id)`

const catalogStructureOptionsSQL = `WITH company_keys AS (` + catalogSalesCompanyKeysSQL + `), companies AS (
 SELECT a.key::text AS value,min(u.name) AS label
 FROM company_keys a
 JOIN organization_units u ON u.id=a.unit AND u.kind='COMPANY' AND u.status='ACTIVE'
 GROUP BY a.key HAVING count(DISTINCT u.id)=1
), organizations AS (
 SELECT b.party_uuid::text AS value,min(u.name) AS label,min(u.kind) AS kind,min(u.id::text) AS "unitId"
 FROM catalog_benefit_organizations b JOIN catalog_sales_context c ON c.source=b.source AND c.captured_at=b.observed_at
 JOIN organization_units u ON u.id=b.unit_id AND u.status='ACTIVE'
 WHERE b.source='DCAI' GROUP BY b.party_uuid HAVING count(DISTINCT u.id)=1
), configured AS (
 SELECT group_key,jsonb_agg(jsonb_build_object('value',option_key,'label',label,
  'personal',group_key='catalogAppointmentRole' AND (NOT applies_to ? 'scopes' OR applies_to->'scopes' ?| ARRAY['PRODUCT','PROJECT']),
  'organization',group_key='catalogAppointmentRole' AND (NOT applies_to ? 'scopes' OR applies_to->'scopes' ? 'ORGANIZATION_UNIT'))
   ORDER BY display_order,option_key) AS items
 FROM configuration_options
 WHERE enabled AND group_key IN ('catalogSalesRelationship','catalogAppointmentRole')
 GROUP BY group_key
)
SELECT jsonb_build_object(
 'companies',jsonb_build_array(jsonb_build_object('value','ANY','label','不限公司'))||
  coalesce((SELECT jsonb_agg(to_jsonb(c) ORDER BY label,value) FROM companies c),'[]'),
 'organizations',coalesce((SELECT jsonb_agg(to_jsonb(o) ORDER BY label,value) FROM organizations o),'[]'),
 'categories',coalesce((SELECT jsonb_agg(jsonb_build_object('value',key,'label',label) ORDER BY label,key)
  FROM catalog_sales_category_options WHERE active),'[]'),
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
	rate := vipRateRow{Company: row.Company, CompanyIDs: row.CompanyIDs, Relationship: row.Relationship,
		Category: row.Category, CategoryIDs: row.CategoryIDs}
	if row.Rate == nil || *row.Rate < 0 || *row.Rate > 10000 || !rate.validScope() ||
		row.Payee != "" && row.Payee != "seller" && row.Payee != vipPayeeSalesCompany ||
		catalogStructureOptionFor(o.Relationships, row.Relationship).Value == "" {
		return nil, invalid
	}
	for _, id := range rate.companies() {
		if catalogStructureOptionFor(o.Companies, id).Value == "" {
			return nil, invalid
		}
	}
	for _, id := range rate.categories() {
		if id != "" && catalogStructureOptionFor(o.Categories, id).Value == "" {
			return nil, invalid
		}
	}
	rate.Rate = *row.Rate
	if row.Payee == vipPayeeSalesCompany {
		rate.Payee = row.Payee
	}
	return json.Marshal(rate)
}

func editCatalogStructure(ctx context.Context, tx pgx.Tx, raw json.RawMessage, input publishCatalogRatesInput) (json.RawMessage, error) {
	if _, err := tx.Exec(ctx, `SELECT source FROM catalog_sales_context WHERE source='DCAI' FOR SHARE`); err != nil {
		return nil, err
	}
	options, err := readCatalogStructureOptions(ctx, tx)
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	var actions []map[string]json.RawMessage
	if input.Create == nil {
		root, actions, err = catalogRateActions(raw)
		if err != nil {
			return nil, err
		}
	} else if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	before, _ := readVIPSalesPolicy(raw)
	positions := make(map[string]int, len(actions))
	for i, action := range actions {
		var key string
		if err := json.Unmarshal(action["item_key"], &key); err != nil {
			return nil, err
		}
		positions[key] = i
	}
	tableEdits := make(map[string][]catalogNewRateRow)
	for _, row := range input.NewRateRows {
		index, found := positions[row.ItemKey]
		if !found || string(actions[index]["type"]) != `"RATE_TABLE"` || row.Payee != "" && row.ItemKey != before.Item {
			return nil, invalid
		}
		tableEdits[row.ItemKey] = append(tableEdits[row.ItemKey], row)
	}
	removedByItem := make(map[string]map[int]bool)
	for _, row := range input.RemovedRateRows {
		if removedByItem[row.ItemKey] == nil {
			removedByItem[row.ItemKey] = make(map[int]bool)
		}
		removedByItem[row.ItemKey][row.Row] = true
	}
	for key, edits := range tableEdits {
		fields := actions[positions[key]]
		var existing []json.RawMessage
		if err := json.Unmarshal(fields["rate_table"], &existing); err != nil {
			return nil, err
		}
		updated, err := changeCatalogRateRows(existing, edits, removedByItem[key], options)
		if err != nil {
			return nil, err
		}
		fields["rate_table"], err = json.Marshal(updated)
		if err != nil {
			return nil, err
		}
	}
	for _, change := range input.CalculationChanges {
		index, found := positions[change.ItemKey]
		if !found || change.ItemKey == before.Item {
			return nil, invalid
		}
		var calculation vipBenefitAction
		if err := configureCatalogBenefitCalculation(&calculation, change.catalogBenefitCalculation, options); err != nil {
			return nil, err
		}
		fields := actions[index]
		fields["type"], _ = json.Marshal(calculation.Type)
		delete(fields, "rate_bps")
		delete(fields, "amount_minor")
		delete(fields, "rate_table")
		switch calculation.Type {
		case "RATE":
			fields["rate_bps"], _ = json.Marshal(calculation.Rate)
		case "FIXED":
			fields["amount_minor"], _ = json.Marshal(calculation.Fixed)
		case "RATE_TABLE":
			fields["rate_table"], err = json.Marshal(calculation.RateTable)
			if err != nil {
				return nil, err
			}
		}
	}
	var people []catalogRuleRecipientChange
	for _, item := range input.NewItems {
		if item.Target == "person" {
			people = append(people, catalogRuleRecipientChange{PersonID: item.PersonID})
		}
	}
	for _, target := range input.TargetChanges {
		if target.Target == "person" {
			people = append(people, catalogRuleRecipientChange{PersonID: target.PersonID})
		}
	}
	keys, err := catalogNewRecipientKeys(ctx, tx, people, nil)
	if err != nil {
		return nil, err
	}
	if err := changeCatalogBenefitTargets(actions, before.Item, input.TargetChanges, options, keys); err != nil {
		return nil, err
	}
	if len(input.NewSalesRates) != 0 {
		if before.Item != "" {
			return nil, invalid
		}
		key := "sales"
		for n := 2; ; n++ {
			if _, exists := positions[key]; !exists {
				break
			}
			key = "sales-" + strconv.Itoa(n)
		}
		rates := make([]json.RawMessage, 0, len(input.NewSalesRates))
		for _, row := range input.NewSalesRates {
			rate, err := options.condition(row)
			if err != nil {
				return nil, err
			}
			rates = append(rates, rate)
		}
		if err := validateCatalogNewRateConditions(nil, rates); err != nil {
			return nil, err
		}
		rawKey, _ := json.Marshal(key)
		label := strings.TrimSpace(input.NewSalesLabel)
		if input.NewSalesLabel == "" {
			label = "成交销售"
		}
		if !textOK(label, 80) {
			return nil, invalid
		}
		rawLabel, _ := json.Marshal(label)
		rawRates, err := json.Marshal(rates)
		if err != nil {
			return nil, err
		}
		positions[key] = len(actions)
		actions = append(actions, map[string]json.RawMessage{
			"item_key": rawKey, "label": rawLabel, "role": json.RawMessage(`"SALES"`),
			"type": json.RawMessage(`"RATE_TABLE"`), "beneficiary_mode": json.RawMessage(`"PARTY"`), "rate_table": rawRates,
		})
	}
	for _, item := range input.NewItems {
		var key string
		for n := len(actions) + 1; ; n++ {
			key = "benefit-" + strconv.Itoa(n)
			if _, ok := positions[key]; !ok {
				break
			}
		}
		fields, err := newCatalogBenefitAction(key, item, options, keys)
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
	after, ok := readVIPSalesPolicy(body)
	if !ok {
		return nil, errCatalogRateUnavailable
	}
	if _, ok := readVIPBenefits(body); !ok {
		return nil, errCatalogRateUnavailable
	}
	if len(before.Split.Teams) != 0 && internalTeamRatesChanged(before, after) {
		return nil, errCatalogTeamRate
	}
	return body, nil
}

func changeCatalogRateRows(existing []json.RawMessage, edits []catalogNewRateRow, removed map[int]bool, options catalogStructureOptions) ([]json.RawMessage, error) {
	updated := slices.Clone(existing)
	seen := make(map[int]bool)
	var changed []json.RawMessage
	for _, edit := range edits {
		value, err := options.condition(edit.catalogConditionInput)
		if err != nil {
			return nil, err
		}
		if edit.Row == 0 {
			updated = append(updated, value)
		} else {
			if edit.Row < 1 || edit.Row > len(existing) || seen[edit.Row] || removed[edit.Row] {
				return nil, invalid
			}
			var prior map[string]json.RawMessage
			var replacement map[string]json.RawMessage
			if json.Unmarshal(existing[edit.Row-1], &prior) != nil || json.Unmarshal(value, &replacement) != nil ||
				string(prior["sales_category_snapshot_default"]) == "true" {
				return nil, invalid
			}
			if id := prior["id"]; len(id) != 0 {
				replacement["id"] = id
			}
			if payee := prior["sales_payee"]; len(payee) != 0 && edit.Payee == "" {
				replacement["sales_payee"] = payee
			}
			value, err = json.Marshal(replacement)
			if err != nil {
				return nil, err
			}
			seen[edit.Row] = true
			updated[edit.Row-1] = value
		}
		changed = append(changed, value)
	}
	var unchanged []json.RawMessage
	for n, row := range existing {
		if !seen[n+1] && !removed[n+1] {
			unchanged = append(unchanged, row)
		}
	}
	if err := validateCatalogNewRateConditions(unchanged, slices.Concat(unchanged, changed)); err != nil {
		return nil, err
	}
	return updated, nil
}

func changeCatalogBenefitTargets(actions []map[string]json.RawMessage, salesKey string, edits []catalogBenefitTargetChange,
	options catalogStructureOptions, people map[int64]string) error {
	if len(edits) == 0 {
		return nil
	}
	byKey := make(map[string]map[string]json.RawMessage, len(actions))
	for _, fields := range actions {
		var key string
		if err := json.Unmarshal(fields["item_key"], &key); err != nil {
			return err
		}
		byKey[key] = fields
	}
	for _, edit := range edits {
		fields := byKey[edit.ItemKey]
		if fields == nil || edit.ItemKey == salesKey {
			return invalid
		}
		delete(byKey, edit.ItemKey)
		body, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		var action vipBenefitAction
		if err := json.Unmarshal(body, &action); err != nil {
			return err
		}
		if err := configureCatalogBenefitTarget(&action, edit.catalogBenefitTarget, options, people); err != nil {
			return err
		}
		for key, value := range map[string]string{
			"role": action.Role, "beneficiary_mode": action.Mode, "designated_party_id": action.Designated,
			"company_beneficiary_party_id": action.Company, "pool_party_id": action.PoolParty,
			"pool_organization_unit_id": action.PoolUnit, "pool_code": action.PoolCode, "pool_name": action.PoolName,
			"allocation_owner_role": action.OwnerRole,
		} {
			if value == "" {
				delete(fields, key)
			} else {
				fields[key], _ = json.Marshal(value)
			}
		}
	}
	return nil
}

func newCatalogBenefitAction(key string,
	item catalogNewBenefitItem, options catalogStructureOptions, people map[int64]string) (map[string]json.RawMessage, error) {
	label := strings.TrimSpace(item.Label)
	if !textOK(label, 80) {
		return nil, invalid
	}
	action := vipBenefitAction{Item: key, Label: label}
	if err := configureCatalogBenefitCalculation(&action, item.catalogBenefitCalculation, options); err != nil {
		return nil, err
	}
	if err := configureCatalogBenefitTarget(&action, item.catalogBenefitTarget, options, people); err != nil {
		return nil, err
	}
	body, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	err = json.Unmarshal(body, &fields)
	return fields, err
}

func configureCatalogBenefitCalculation(action *vipBenefitAction, calculation catalogBenefitCalculation, options catalogStructureOptions) error {
	rate, fixed, rows := calculation.Rate, calculation.Fixed, calculation.Rows
	switch {
	case rate != nil && fixed == nil && len(rows) == 0 && *rate >= 0 && *rate <= 10000:
		action.Type, action.Rate = "RATE", rate
	case fixed != nil && rate == nil && len(rows) == 0 && *fixed >= 0:
		action.Type, action.Fixed = "FIXED", fixed
	case rate == nil && fixed == nil && len(rows) != 0:
		for _, row := range rows {
			if row.Payee != "" {
				return invalid
			}
			value, err := options.condition(row)
			if err != nil {
				return err
			}
			action.RateTable = append(action.RateTable, value)
		}
		action.Type = "RATE_TABLE"
		return validateCatalogNewRateConditions(nil, action.RateTable)
	default:
		return invalid
	}
	return nil
}

func configureCatalogBenefitTarget(action *vipBenefitAction, target catalogBenefitTarget,
	options catalogStructureOptions, people map[int64]string) error {
	before := *action
	action.Mode, action.Role = "PARTY", ""
	action.Designated, action.Company = "", ""
	action.PoolParty, action.PoolUnit, action.PoolCode, action.PoolName, action.OwnerRole = "", "", "", "", ""
	org := catalogStructureOptionFor(options.Organizations, target.Organization)
	role := catalogStructureOptionFor(options.Roles, target.Role)
	switch target.Target {
	case "referrer":
		if target.PersonID != 0 || target.Role != "" || target.Organization != "" {
			return invalid
		}
		action.Mode, action.Role = vipOrderReferrerMode, "REFERRAL"
	case "person":
		if target.Organization != "" || !role.Personal || people[target.PersonID] == "" {
			return invalid
		}
		action.Role, action.Designated = role.Value, people[target.PersonID]
	case "company":
		if target.PersonID != 0 || target.Role != "" || org.Kind != "COMPANY" {
			return invalid
		}
		action.Role, action.Company = "COMPANY", org.Value
		if before.Company != "" {
			action.Role = before.Role
		}
	case "pool":
		if target.PersonID != 0 || org.Value == "" || role.Value == "" {
			return invalid
		}
		action.Mode, action.PoolParty, action.PoolUnit = "TEAM_POOL", org.Value, org.UnitID
		action.Role, action.OwnerRole = role.Value, role.Value
		action.PoolCode, action.PoolName = action.Item, action.Label
		if before.Mode == "TEAM_POOL" {
			action.Role, action.PoolCode, action.PoolName = before.Role, before.PoolCode, before.PoolName
		}
	default:
		return invalid
	}
	return nil
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
		for _, company := range row.companies() {
			for _, category := range row.categories() {
				scope := catalogRateScope{company, category}
				index.groups[scope] = append(index.groups[scope], row)
			}
		}
		index.categorized = index.categorized || row.hasCategory()
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
		for _, company := range row.companies() {
			if company != "ANY" {
				companies[company] = true
			}
			for _, category := range row.categories() {
				key := condition{company, row.Relationship, category}
				if n >= len(before) && seen[key] {
					return errCatalogRateConditions
				}
				seen[key] = true
				categories[category] = true
			}
		}
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
