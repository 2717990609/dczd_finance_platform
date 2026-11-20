package main

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

type catalogPeriodTier struct {
	From  int64  `json:"min_inclusive,omitempty"`
	Until *int64 `json:"max_exclusive,omitempty"`
	Rate  int64  `json:"rate_bps"`
}

type catalogPeriodTierEdit struct {
	ItemKey string              `json:"itemKey"`
	Tiers   []catalogPeriodTier `json:"tiers"`
}

type catalogPeriodItem struct {
	ItemKey         string              `json:"itemKey"`
	Label           string              `json:"label"`
	TargetKind      string              `json:"targetKind"`
	TargetName      string              `json:"targetName,omitempty"`
	OrganizationKey string              `json:"organizationKey"`
	PersonID        int64               `json:"personId,omitempty"`
	PoolName        string              `json:"poolName,omitempty"`
	OwnerRole       string              `json:"ownerRole,omitempty"`
	MemberRole      string              `json:"memberRole,omitempty"`
	Tiers           []catalogPeriodTier `json:"tiers"`
	unit            string
}

type catalogPeriodDefinition struct {
	Cycle       string
	Scenarios   []string
	Items       []catalogPeriodItem
	Performance *catalogPerformanceScope
}

const catalogPeriodUnitPrefix = "unit:"

func catalogPeriodUnit(key string) (string, bool) {
	raw, direct := strings.CutPrefix(key, catalogPeriodUnitPrefix)
	if !direct {
		return "", false
	}
	id, valid := canonicalSourceUUID(raw)
	if !valid || id != raw {
		return "", true
	}
	return id, true
}

func parseCatalogPeriodRule(raw json.RawMessage) (catalogPeriodDefinition, error) {
	var stored struct {
		Basis       string                   `json:"amount_basis"`
		Recognition string                   `json:"recognition_trigger"`
		Refund      string                   `json:"refund_policy"`
		Stacking    string                   `json:"stacking_policy"`
		Performance *catalogPerformanceScope `json:"performance_scope"`
		Rounding    struct {
			Mode  string `json:"mode"`
			Scale int    `json:"scale"`
		} `json:"rounding_policy"`
		Scope struct {
			Sources   []string `json:"sales_systems"`
			Scenarios []string `json:"business_sku_codes"`
			Events    []string `json:"financial_event_types"`
		} `json:"policy_scope"`
		Settlement struct {
			Cycle    string `json:"cycle"`
			Negative string `json:"negative_balance_policy"`
		} `json:"settlement_policy"`
		Actions []struct {
			vipBenefitAction
			Metric          string              `json:"metric_code"`
			Unit            string              `json:"metric_unit"`
			Tiers           []catalogPeriodTier `json:"tier_table"`
			EligibilityRole string              `json:"eligibility_role"`
		} `json:"benefit_actions"`
	}
	var result catalogPeriodDefinition
	if json.Unmarshal(raw, &stored) != nil || stored.Basis != "ACTUAL_RECEIVED" || stored.Recognition != "PAID" ||
		stored.Refund != "PROPORTIONAL" || stored.Stacking != "SUM" || !stored.Performance.valid() || stored.Rounding.Mode != "HALF_UP" || stored.Rounding.Scale != 2 ||
		!slices.Equal(stored.Scope.Sources, []string{"DCAI"}) || !slices.Equal(stored.Scope.Events, []string{"order.paid"}) ||
		len(stored.Scope.Scenarios) == 0 || len(stored.Actions) == 0 || stored.Settlement.Negative != "CARRY_FORWARD" ||
		(stored.Settlement.Cycle != "MONTHLY" && stored.Settlement.Cycle != "MONTHLY_25_TO_24") {
		return result, errCatalogRateUnavailable
	}
	result.Cycle, result.Scenarios = stored.Settlement.Cycle, stored.Scope.Scenarios
	result.Performance = stored.Performance
	for i, scenario := range result.Scenarios {
		if !textOK(scenario, 256) || slices.Contains(result.Scenarios[:i], scenario) {
			return result, errCatalogRateUnavailable
		}
	}
	items := make(map[string]bool)
	for _, action := range stored.Actions {
		org := action.Company
		unit := action.PoolUnit
		if action.Mode == "TEAM_POOL" {
			org = action.PoolParty
		}
		if directUnit, direct := catalogPeriodUnit(org); direct {
			if directUnit == "" || unit != "" && unit != directUnit {
				return result, errCatalogRateUnavailable
			}
			unit = directUnit
			if action.Mode == "TEAM_POOL" {
				action.PoolUnit = directUnit
			}
		}
		if action.Item == "" || items[action.Item] || action.Label == "" || !action.validTarget() ||
			action.Type != "TIER_RATE" || action.Metric != "MONTHLY_NET_RECEIPTS" || action.Unit != "AMOUNT_MINOR" ||
			!validCatalogPeriodTiers(action.Tiers) {
			return result, errCatalogRateUnavailable
		}
		items[action.Item] = true
		memberRole := ""
		var personID int64
		if action.Designated != "" {
			key, local := strings.CutPrefix(action.Designated, "person:")
			id, err := strconv.ParseInt(key, 10, 64)
			if !local || err != nil || id <= 0 {
				return result, errCatalogRateUnavailable
			}
			personID = id
		}
		if action.Mode == "TEAM_POOL" {
			memberRole = catalogAggregateMemberRole(action.Role, action.EligibilityRole)
		}
		result.Items = append(result.Items, catalogPeriodItem{ItemKey: action.Item, Label: action.Label, TargetKind: action.targetKind(),
			PoolName: action.PoolName, OwnerRole: action.OwnerRole, OrganizationKey: org, PersonID: personID, Tiers: action.Tiers,
			MemberRole: memberRole, unit: unit})
	}
	return result, nil
}

func validCatalogPeriodTiers(tiers []catalogPeriodTier) bool {
	if len(tiers) == 0 || tiers[0].From != 0 {
		return false
	}
	for i, row := range tiers {
		if row.Rate < 0 || row.Rate > 10000 {
			return false
		}
		if i == len(tiers)-1 {
			return row.Until == nil
		}
		if row.Until == nil || *row.Until <= row.From || *row.Until != tiers[i+1].From {
			return false
		}
	}
	return false
}

func editCatalogPeriodTiers(raw json.RawMessage, edits []catalogPeriodTierEdit) (json.RawMessage, error) {
	period, err := parseCatalogPeriodRule(raw)
	if err != nil {
		return nil, err
	}
	replacements := make(map[string][]catalogPeriodTier)
	for _, e := range edits {
		if replacements[e.ItemKey] != nil || !validCatalogPeriodTiers(e.Tiers) {
			return nil, errCatalogPeriodTiers
		}
		replacements[e.ItemKey] = e.Tiers
	}
	var root map[string]json.RawMessage
	var actions []map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || json.Unmarshal(root["benefit_actions"], &actions) != nil {
		return nil, invalid
	}
	changed := false
	for i, item := range period.Items {
		tiers, found := replacements[item.ItemKey]
		if !found {
			continue
		}
		delete(replacements, item.ItemKey)
		if slices.EqualFunc(tiers, item.Tiers, sameCatalogPeriodTier) {
			continue
		}
		actions[i]["tier_table"], err = encodeCatalogPeriodTiers(tiers)
		if err != nil {
			return nil, err
		}
		changed = true
	}
	if len(replacements) != 0 {
		return nil, invalid
	}
	if !changed {
		return raw, nil
	}
	root["benefit_actions"], err = json.Marshal(actions)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func encodeCatalogPeriodTiers(tiers []catalogPeriodTier) (json.RawMessage, error) {
	rows := make([]struct {
		ID string `json:"id"`
		catalogPeriodTier
	}, len(tiers))
	for n, tier := range tiers {
		rows[n].ID, rows[n].catalogPeriodTier = "tier-"+strconv.Itoa(n+1), tier
	}
	return json.Marshal(rows)
}

func sameCatalogPeriodTier(a, b catalogPeriodTier) bool {
	if a.From != b.From || a.Rate != b.Rate {
		return false
	}
	if a.Until == nil || b.Until == nil {
		return a.Until == nil && b.Until == nil
	}
	return *a.Until == *b.Until
}

type catalogPeriodAward struct {
	ItemKey string `json:"itemKey"`
	Label   string `json:"label"`
	Rate    int64  `json:"rateBps"`
	Amount  int64  `json:"amountMinor"`
}

type catalogPeriodQuote struct {
	Basis        int64                `json:"basisMinor"`
	CarryForward int64                `json:"carryForwardMinor"`
	Total        int64                `json:"totalMinor"`
	Awards       []catalogPeriodAward `json:"awards"`
}

func quoteCatalogPeriod(items []catalogPeriodItem, received, refunds, carry int64) (catalogPeriodQuote, error) {
	result := catalogPeriodQuote{Awards: make([]catalogPeriodAward, 0, len(items))}
	if received < 0 || refunds < 0 || carry > 0 {
		return result, invalid
	}
	net, err := addMinor(received-refunds, carry)
	if err != nil {
		return result, err
	}
	result.Basis, result.CarryForward = max(net, 0), min(net, 0)
	for _, item := range items {
		i := slices.IndexFunc(item.Tiers, func(t catalogPeriodTier) bool {
			return result.Basis >= t.From && (t.Until == nil || result.Basis < *t.Until)
		})
		if i < 0 {
			return result, errCatalogPeriodTiers
		}
		rate := item.Tiers[i].Rate
		amount := calculatePaymentAmount(result.Basis, component{Mode: "rate", Value: rate})
		result.Total, err = addMinor(result.Total, amount)
		if err != nil {
			return result, err
		}
		result.Awards = append(result.Awards, catalogPeriodAward{item.ItemKey, item.Label, rate, amount})
	}
	return result, nil
}
