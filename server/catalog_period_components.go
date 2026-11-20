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
	ItemKey         string              `json:"itemKey,omitempty"`
	Label           string              `json:"label"`
	TargetKind      string              `json:"targetKind"`
	OrganizationKey string              `json:"organizationKey"`
	PersonID        int64               `json:"personId,omitempty"`
	PoolName        string              `json:"poolName,omitempty"`
	OwnerRole       string              `json:"ownerRole,omitempty"`
	MemberRole      string              `json:"memberRole,omitempty"`
	Tiers           []catalogPeriodTier `json:"tiers"`
}

func editCatalogPeriodComponents(raw json.RawMessage, edits []catalogPeriodComponentEdit, options catalogStructureOptions) (json.RawMessage, error) {
	if len(edits) == 0 {
		return nil, errCatalogPeriodComponents
	}
	period, err := parseCatalogPeriodRule(raw)
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	var actions []map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || json.Unmarshal(root["benefit_actions"], &actions) != nil {
		return nil, invalid
	}
	positions := make(map[string]int, len(period.Items))
	for i, item := range period.Items {
		positions[item.ItemKey] = i
	}
	next := make([]map[string]json.RawMessage, 0, len(edits))
	changed := len(edits) != len(actions)
	for i, edit := range edits {
		edit.Label = strings.TrimSpace(edit.Label)
		edit.PoolName = strings.TrimSpace(edit.PoolName)
		if !textOK(edit.Label, 80) {
			return nil, invalid
		}
		if !validCatalogPeriodTiers(edit.Tiers) {
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
			before, fields = period.Items[n], actions[n]
			changed = changed || i != n
		} else {
			edit.ItemKey = "period-benefit-" + strings.ToLower(rand.Text())
		}
		if before.TargetKind != edit.TargetKind || before.OrganizationKey != edit.OrganizationKey || before.PersonID != edit.PersonID ||
			before.OwnerRole != edit.OwnerRole || before.MemberRole != edit.MemberRole || before.PoolName != edit.PoolName {
			if err := setCatalogPeriodTarget(fields, before, edit, options); err != nil {
				return nil, err
			}
			changed = true
		}
		if before.Label != edit.Label {
			fields["label"], _ = json.Marshal(edit.Label)
			changed = true
		}
		if !slices.EqualFunc(before.Tiers, edit.Tiers, sameCatalogPeriodTier) {
			fields["tier_table"], err = encodeCatalogPeriodTiers(edit.Tiers)
			if err != nil {
				return nil, err
			}
			changed = true
		}
		next = append(next, fields)
	}
	if !changed {
		return raw, nil
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
	if edit.TargetKind != "person" && (org.Value == "" || edit.PersonID != 0) {
		return errCatalogPeriodRecipient
	}
	action := vipBenefitAction{Item: edit.ItemKey, Type: "TIER_RATE", Mode: "PARTY", Role: "COMPANY", Company: org.Value}
	switch edit.TargetKind {
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
		if org.UnitID == "" || !roleAllowed(edit.OwnerRole, before.OwnerRole) ||
			!roleAllowed(edit.MemberRole, before.MemberRole) || !textOK(edit.PoolName, 80) {
			return errCatalogPeriodRecipient
		}
		action.Mode, action.Company = "TEAM_POOL", ""
		action.Role, action.OwnerRole = edit.MemberRole, edit.OwnerRole
		action.PoolUnit, action.PoolParty, action.PoolName = org.UnitID, org.Value, edit.PoolName
		if _, direct := catalogPeriodUnit(org.Value); direct {
			action.PoolUnit = ""
		}
		if json.Unmarshal(fields["pool_code"], &action.PoolCode) != nil || action.PoolCode == "" {
			action.PoolCode = edit.ItemKey
		}
	default:
		return errCatalogPeriodRecipient
	}
	for _, key := range []string{"designated_party_id", "company_beneficiary_party_id", "pool_party_id", "pool_organization_unit_id",
		"pool_code", "pool_name", "allocation_owner_role", "eligibility_role"} {
		delete(fields, key)
	}
	values := map[string]string{"item_key": action.Item, "type": action.Type, "role": action.Role, "beneficiary_mode": action.Mode,
		"designated_party_id":          action.Designated,
		"company_beneficiary_party_id": action.Company, "pool_party_id": action.PoolParty, "pool_organization_unit_id": action.PoolUnit,
		"pool_code": action.PoolCode, "pool_name": action.PoolName, "allocation_owner_role": action.OwnerRole,
		"metric_code": "MONTHLY_NET_RECEIPTS", "metric_unit": "AMOUNT_MINOR"}
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
