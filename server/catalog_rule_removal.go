package main

import (
	"encoding/json"
	"slices"
)

type catalogRateRowRemoval struct {
	ItemKey string `json:"itemKey"`
	Row     int    `json:"row"`
}

func removeCatalogEntries(raw json.RawMessage, items []string, rows []catalogRateRowRemoval) (json.RawMessage, error) {
	root, actions, err := catalogRateActions(raw)
	if err != nil {
		return nil, err
	}
	sales, _ := readVIPSalesPolicy(raw)
	removeItems := make(map[string]bool, len(items))
	for _, key := range items {
		if removeItems[key] {
			return nil, invalid
		}
		removeItems[key] = true
	}
	split := sales.Split
	salesRemoval := split.Item != "" && removeItems[split.Item]
	if salesRemoval && split.salesPayeeDisabled() {
		return nil, invalid
	}
	keepSplit := !removeItems[vipInternalServiceItem] && split.hasPositiveShare(vipInternalServiceItem) ||
		!removeItems[vipGroupLeaderItem] && (split.hasPositiveShare(vipGroupLeaderItem) || split.independentGroup) ||
		!removeItems[vipTeamLeaderItem] && (split.hasPositiveShare(vipTeamLeaderItem) || split.independentTeam)
	removeSplit := (salesRemoval || split.salesPayeeDisabled()) && !keepSplit
	disabled := slices.Clone(split.DisabledShares)
	var splitRemovals []string
	if len(sales.Split.Teams) != 0 {
		for _, key := range []string{vipGroupLeaderItem, vipTeamLeaderItem, vipInternalServiceItem} {
			if !removeItems[key] {
				continue
			}
			if slices.Contains(disabled, key) {
				return nil, invalid
			}
			disabled = append(disabled, key)
			splitRemovals = append(splitRemovals, key)
		}
	}
	removeRows := make(map[string]map[int]bool)
	for _, row := range rows {
		if row.Row < 1 || removeItems[row.ItemKey] || removeRows[row.ItemKey][row.Row] {
			return nil, invalid
		}
		if removeRows[row.ItemKey] == nil {
			removeRows[row.ItemKey] = make(map[int]bool)
		}
		removeRows[row.ItemKey][row.Row] = true
	}
	if salesRemoval && keepSplit {
		// Keep the original rate table and rounding weights for the surviving payees.
		delete(removeItems, split.Item)
		disabled = append(disabled, vipInternalSalesPayee)
	} else if removeSplit && split.salesPayeeDisabled() {
		removeItems[split.Item] = true
	}

	kept := make([]map[string]json.RawMessage, 0, len(actions))
	for _, action := range actions {
		var key string
		if err := json.Unmarshal(action["item_key"], &key); err != nil {
			return nil, err
		}
		if removeItems[key] {
			if key == sales.Item {
				root["sales_attribution"] = json.RawMessage(`"ORDER_SELLER"`)
			}
			delete(removeItems, key)
			continue
		}
		if selected := removeRows[key]; len(selected) != 0 {
			var table []json.RawMessage
			if err := json.Unmarshal(action["rate_table"], &table); err != nil {
				return nil, invalid
			}
			for row := range selected {
				if row > len(table) {
					return nil, invalid
				}
			}
			remaining := make([]json.RawMessage, 0, len(table)-len(selected))
			for i, row := range table {
				if !selected[i+1] {
					remaining = append(remaining, row)
				}
			}
			if len(remaining) == 0 {
				return nil, errCatalogRequiredEntry
			}
			action["rate_table"], err = json.Marshal(remaining)
			if err != nil {
				return nil, err
			}
			delete(removeRows, key)
		}
		kept = append(kept, action)
	}
	for _, key := range splitRemovals {
		delete(removeItems, key)
	}
	if len(removeItems)+len(removeRows) != 0 {
		return nil, invalid
	}
	if len(kept) == 0 {
		return nil, errCatalogRequiredEntry
	}
	if removeSplit {
		delete(root, "internal_sales_direct_split")
	} else if salesRemoval || len(splitRemovals) != 0 {
		if err := writeCatalogDisabledShares(root, disabled); err != nil {
			return nil, err
		}
	}
	root["benefit_actions"], err = json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	next, ok := readVIPSalesPolicy(body)
	if !ok {
		return nil, errCatalogRateUnavailable
	}
	if !removeSplit && len(sales.Split.Teams) != 0 && internalTeamRatesChanged(sales, next) {
		return nil, errCatalogTeamRate
	}
	return body, nil
}

func writeCatalogDisabledShares(root map[string]json.RawMessage, shares []string) error {
	var split map[string]json.RawMessage
	const key = "internal_sales_direct_split"
	if json.Unmarshal(root[key], &split) != nil || split == nil {
		return errCatalogRateUnavailable
	}
	if len(shares) == 0 {
		delete(split, "disabled_shares")
	} else {
		slices.Sort(shares)
		split["disabled_shares"], _ = json.Marshal(shares)
		if encoded := split[catalogSplitRecipientDutiesKey]; len(encoded) != 0 {
			var duties map[string]string
			if json.Unmarshal(encoded, &duties) != nil {
				return errCatalogRateUnavailable
			}
			for _, share := range shares {
				delete(duties, share)
			}
			if len(duties) == 0 {
				delete(split, catalogSplitRecipientDutiesKey)
			} else {
				split[catalogSplitRecipientDutiesKey], _ = json.Marshal(duties)
			}
		}
		if encoded := split[catalogSplitMembershipScopesKey]; len(encoded) != 0 {
			var scopes map[string]bool
			if json.Unmarshal(encoded, &scopes) != nil {
				return errCatalogRateUnavailable
			}
			for _, share := range shares {
				delete(scopes, share)
			}
			if len(scopes) == 0 {
				delete(split, catalogSplitMembershipScopesKey)
			} else {
				split[catalogSplitMembershipScopesKey], _ = json.Marshal(scopes)
			}
		}
	}
	var err error
	root[key], err = json.Marshal(split)
	return err
}
