package main

import "encoding/json"

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
	removeSplit := sales.Split.Item != "" && removeItems[sales.Split.Item]
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
		if removeSplit && vipLeaderRole(key) != "" {
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
	if len(removeItems)+len(removeRows) != 0 {
		return nil, invalid
	}
	if len(kept) == 0 {
		return nil, errCatalogRequiredEntry
	}
	if removeSplit {
		delete(root, "internal_sales_direct_split")
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
