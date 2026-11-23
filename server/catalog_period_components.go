package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
)

var errCatalogPeriodRecipient = errors.New("请核对分项的公司、团队池及适用职责")
var errCatalogPeriodComponents = errors.New("整期规则须保留至少一个分润分项")

type catalogPeriodComponentEdit struct {
	ItemKey           string                  `json:"itemKey,omitempty"`
	Label             string                  `json:"label"`
	TargetKind        string                  `json:"targetKind"`
	OrganizationKey   string                  `json:"organizationKey"`
	PersonID          int64                   `json:"personId,omitempty"`
	PoolName          string                  `json:"poolName,omitempty"`
	OwnerRole         string                  `json:"ownerRole,omitempty"`
	OwnerPersonID     int64                   `json:"ownerPersonId,omitempty"`
	PoolID            int64                   `json:"poolId,omitempty"`
	MemberRole        string                  `json:"memberRole,omitempty"`
	Tiers             []catalogPeriodTier     `json:"tiers"`
	FixedMinor        *int64                  `json:"fixedMinor,omitempty"`
	ScenarioCodes     []string                `json:"scenarioCodes"`
	MembershipNewOnly *bool                   `json:"membershipNewOnly"`
	CommissionUnits   []string                `json:"salesScopeUnitIds,omitempty"`
	ScopeCategories   []string                `json:"salesScopeCategoryIds,omitempty"`
	OwnSales          bool                    `json:"ownSales,omitempty"`
	RecipientDuty     string                  `json:"recipientDuty,omitempty"`
	Appointment       *catalogPerformanceDuty `json:"appointment,omitempty"`
	AppointmentLevel  string                  `json:"appointmentLevel,omitempty"`
	AppointmentRole   string                  `json:"appointmentRole,omitempty"`
}

func editCatalogPeriodComponents(original, raw json.RawMessage, edits []catalogPeriodComponentEdit, options catalogStructureOptions) (json.RawMessage, error) {
	period, err := parseCatalogPeriodRule(original)
	if err != nil {
		return nil, err
	}
	return writeCatalogPeriodComponents(raw, period.Items, edits, options)
}

func writeCatalogPeriodComponents(raw json.RawMessage, original []catalogPeriodItem, edits []catalogPeriodComponentEdit,
	options catalogStructureOptions) (json.RawMessage, error) {
	if len(edits) == 0 {
		return nil, errCatalogPeriodComponents
	}
	var root map[string]json.RawMessage
	var actions []map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || json.Unmarshal(root["benefit_actions"], &actions) != nil {
		return nil, invalid
	}
	positions := make(map[string]int, len(original))
	for i, item := range original {
		positions[item.ItemKey] = i
	}
	next := make([]map[string]json.RawMessage, 0, len(edits))
	changed := len(edits) != len(actions)
	var err error
	for i, edit := range edits {
		edit.Label = strings.TrimSpace(edit.Label)
		edit.PoolName = strings.TrimSpace(edit.PoolName)
		if !textOK(edit.Label, 80) {
			return nil, invalid
		}
		if edit.FixedMinor != nil && (!validCatalogPeriodFixed(edit.FixedMinor) || len(edit.Tiers) != 0) {
			return nil, invalid
		}
		if edit.FixedMinor == nil && !validCatalogPeriodTiers(edit.Tiers) {
			return nil, errCatalogPeriodTiers
		}
		var before catalogPeriodItem
		fields := make(map[string]json.RawMessage)
		if edit.ItemKey != "" {
			n, found := positions[edit.ItemKey]
			if !found {
				return nil, invalid
			}
			delete(positions, edit.ItemKey)
			before, fields = original[n], actions[n]
			changed = changed || i != n
		} else {
			edit.ItemKey = "period-benefit-" + strings.ToLower(rand.Text())
		}
		beforeOrg, beforeName := before.OrganizationKey, before.PoolName
		if before.PoolID != 0 {
			beforeOrg, beforeName = "", ""
		}
		if before.TargetKind != edit.TargetKind || beforeOrg != edit.OrganizationKey || before.PersonID != edit.PersonID ||
			before.OwnerRole != edit.OwnerRole || before.OwnerPersonID != edit.OwnerPersonID ||
			before.MemberRole != edit.MemberRole || beforeName != edit.PoolName || before.PoolID != edit.PoolID ||
			before.AppointmentLevel != edit.AppointmentLevel || before.AppointmentRole != edit.AppointmentRole {
			if err := setCatalogPeriodTarget(fields, before, edit, options); err != nil {
				return nil, err
			}
			changed = true
		}
		if before.Label != edit.Label {
			fields["label"], _ = json.Marshal(edit.Label)
			changed = true
		}
		if edit.ScenarioCodes != nil && !slices.Equal(before.ScenarioCodes, edit.ScenarioCodes) {
			if len(edit.ScenarioCodes) == 0 {
				delete(fields, "business_sku_codes")
			} else {
				fields["business_sku_codes"], _ = json.Marshal(edit.ScenarioCodes)
			}
			changed = true
		}
		if edit.MembershipNewOnly != nil && *edit.MembershipNewOnly != before.MembershipNewOnly {
			if *edit.MembershipNewOnly {
				fields["vip_new_membership_only"] = json.RawMessage("true")
			} else {
				delete(fields, "vip_new_membership_only")
			}
			changed = true
		}
		if edit.TargetKind != "person" && edit.OwnSales ||
			edit.OwnSales && len(edit.CommissionUnits)+len(edit.ScopeCategories) != 0 ||
			!catalogScopeIDsValid(edit.CommissionUnits, before.CommissionUnits, options.SalesUnits) ||
			!catalogScopeIDsValid(edit.ScopeCategories, before.ScopeCategories, options.Categories) ||
			edit.RecipientDuty != "" && (!catalogRecipientIsPerson(edit.TargetKind) ||
				catalogStructureOptionFor(options.Duties, edit.RecipientDuty).Value == "") {
			return nil, errCatalogCommissionScope
		}
		if edit.Appointment != nil && (edit.TargetKind != "person" || edit.PersonID != edit.Appointment.PersonID ||
			!(&catalogPerformanceScope{Appointment: edit.Appointment}).valid()) {
			return nil, errCatalogPerformanceScope
		}
		if before.Appointment != nil || edit.Appointment != nil {
			switch {
			case edit.Appointment == nil:
				delete(fields, "recipient_appointment")
				changed = true
			case before.Appointment == nil || *edit.Appointment != *before.Appointment:
				fields["recipient_appointment"], _ = json.Marshal(edit.Appointment)
				changed = true
			}
		}
		if !slices.Equal(before.CommissionUnits, edit.CommissionUnits) || !slices.Equal(before.ScopeCategories, edit.ScopeCategories) || before.OwnSales != edit.OwnSales ||
			before.RecipientDuty != edit.RecipientDuty {
			for _, field := range []string{catalogCommissionScopeKey, catalogCategoryScopeKey, "own_sales", "recipient_duty"} {
				delete(fields, field)
			}
			if len(edit.CommissionUnits) != 0 {
				fields[catalogCommissionScopeKey], _ = json.Marshal(edit.CommissionUnits)
			}
			if len(edit.ScopeCategories) != 0 {
				fields[catalogCategoryScopeKey], _ = json.Marshal(edit.ScopeCategories)
			}
			if edit.OwnSales {
				fields["own_sales"] = json.RawMessage("true")
			}
			if edit.RecipientDuty != "" {
				fields["recipient_duty"], _ = json.Marshal(edit.RecipientDuty)
			}
			changed = true
		}
		if edit.FixedMinor != nil {
			if before.FixedMinor == nil || *before.FixedMinor != *edit.FixedMinor {
				fields["type"] = json.RawMessage(`"FIXED"`)
				fields["amount_minor"], _ = json.Marshal(edit.FixedMinor)
				delete(fields, "tier_table")
				delete(fields, "rate_bps")
				delete(fields, "metric_code")
				delete(fields, "metric_unit")
				changed = true
			}
		} else if before.FixedMinor != nil || !slices.EqualFunc(before.Tiers, edit.Tiers, sameCatalogPeriodTier) {
			fields["type"] = json.RawMessage(`"TIER_RATE"`)
			fields["metric_code"] = json.RawMessage(`"MONTHLY_NET_RECEIPTS"`)
			fields["metric_unit"] = json.RawMessage(`"AMOUNT_MINOR"`)
			delete(fields, "amount_minor")
			fields["tier_table"], err = encodeCatalogPeriodTiers(edit.Tiers)
			if err != nil {
				return nil, err
			}
			changed = true
		}
		fixedPerOrder := edit.FixedMinor != nil && edit.TargetKind == "appointment"
		if fixedPerOrder != before.FixedPerOrder {
			if fixedPerOrder {
				fields["fixed_per_order"] = json.RawMessage("true")
			} else {
				delete(fields, "fixed_per_order")
			}
			changed = true
		}
		next = append(next, fields)
	}
	if !changed {
		_, err := parseCatalogPeriodRule(raw)
		return raw, err
	}
	root["benefit_actions"], err = json.Marshal(next)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	_, err = parseCatalogPeriodRule(body)
	return body, err
}

func setCatalogPeriodTarget(fields map[string]json.RawMessage, before catalogPeriodItem,
	edit catalogPeriodComponentEdit, options catalogStructureOptions) error {
	org := catalogStructureOptionFor(options.Organizations, edit.OrganizationKey)
	pool, registered := catalogRegisteredPool(options.TeamPools, edit.PoolID)
	if edit.TargetKind != "pool" && (edit.OwnerPersonID != 0 || edit.PoolID != 0) {
		return errCatalogPeriodRecipient
	}
	if edit.TargetKind == "appointment" {
		if !validVIPSalesOrganizationLevel(edit.AppointmentLevel) || edit.AppointmentRole == "" ||
			edit.AppointmentRole != before.AppointmentRole && !catalogStructureOptionFor(options.Roles, edit.AppointmentRole).Organization ||
			edit.PersonID != 0 || edit.OrganizationKey != "" || edit.PoolName != "" || edit.OwnerRole != "" || edit.MemberRole != "" {
			return errCatalogPeriodRecipient
		}
	} else if edit.AppointmentLevel != "" || edit.AppointmentRole != "" ||
		edit.TargetKind != "person" && (org.Value == "" && !registered || edit.PersonID != 0) {
		return errCatalogPeriodRecipient
	}
	action := vipBenefitAction{Item: edit.ItemKey, Mode: "PARTY", Role: "COMPANY", Company: org.Value}
	switch edit.TargetKind {
	case "appointment":
		action.Mode, action.Role, action.Company = vipOrganizationAppointmentMode, edit.AppointmentRole, ""
		action.AppointmentLevel = edit.AppointmentLevel
	case "person":
		if edit.PersonID <= 0 || edit.OrganizationKey != "" || edit.PoolName != "" || edit.OwnerRole != "" || edit.MemberRole != "" {
			return errCatalogPeriodRecipient
		}
		action.Role, action.Company, action.Designated = "DIRECT_BENEFIT", "", "person:"+strconv.FormatInt(edit.PersonID, 10)
	case "company":
		if org.Kind != "COMPANY" || edit.PoolName != "" || edit.OwnerRole != "" || edit.MemberRole != "" {
			return errCatalogPeriodRecipient
		}
	case "pool":
		roleAllowed := func(role, previous string) bool {
			return role != "" && (role == previous || catalogStructureOptionFor(options.Roles, role).Organization)
		}
		validDestination := registered && edit.OrganizationKey == "" && edit.PoolName == "" ||
			edit.PoolID == 0 && org.UnitID != "" && textOK(edit.PoolName, 80)
		if !validDestination || edit.OwnerPersonID < 0 || (edit.OwnerPersonID > 0) == (edit.OwnerRole != "") ||
			edit.OwnerRole != "" && !roleAllowed(edit.OwnerRole, before.OwnerRole) ||
			!roleAllowed(edit.MemberRole, before.MemberRole) {
			return errCatalogPeriodRecipient
		}
		action.Mode, action.Company = "TEAM_POOL", ""
		action.Role, action.OwnerRole = edit.MemberRole, edit.OwnerRole
		if edit.OwnerPersonID != 0 {
			action.OwnerParty = "person:" + strconv.FormatInt(edit.OwnerPersonID, 10)
		}
		action.PoolUnit, action.PoolParty, action.PoolName = org.UnitID, org.Value, edit.PoolName
		if _, direct := catalogPeriodUnit(org.Value); direct {
			action.PoolUnit = ""
		}
		if json.Unmarshal(fields["pool_code"], &action.PoolCode) != nil || action.PoolCode == "" || catalogPoolID(action.PoolCode) != 0 {
			action.PoolCode = edit.ItemKey
		}
		if registered {
			pool.bind(&action)
		}
	default:
		return errCatalogPeriodRecipient
	}
	for _, key := range []string{"designated_party_id", "company_beneficiary_party_id", "pool_party_id", "pool_organization_unit_id",
		"pool_code", "pool_name", "allocation_owner_role", "allocation_owner_party_id", "eligibility_role", "appointment_sales_org_level"} {
		delete(fields, key)
	}
	values := map[string]string{"item_key": action.Item, "role": action.Role, "beneficiary_mode": action.Mode,
		"designated_party_id":          action.Designated,
		"company_beneficiary_party_id": action.Company, "pool_party_id": action.PoolParty, "pool_organization_unit_id": action.PoolUnit,
		"pool_code": action.PoolCode, "pool_name": action.PoolName,
		"allocation_owner_role": action.OwnerRole, "allocation_owner_party_id": action.OwnerParty,
		"appointment_sales_org_level": action.AppointmentLevel}
	for key, value := range values {
		if value != "" {
			fields[key], _ = json.Marshal(value)
		}
	}
	if action.Mode == "TEAM_POOL" {
		fields["eligibility_role"], _ = json.Marshal(edit.MemberRole)
	}
	return nil
}
