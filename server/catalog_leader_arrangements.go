package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type catalogLeaderChange struct {
	ItemKey  string         `json:"itemKey"`
	Label    string         `json:"label"`
	Target   string         `json:"target"`
	PersonID int64          `json:"personId,omitempty"`
	Scope    vipLeaderScope `json:"scope"`
	catalogBenefitCalculation
}

func catalogLeaderComponents(policy vipBenefitPolicy) []catalogRateComponent {
	if len(policy.split.Teams) == 0 {
		return nil
	}
	group, team := policy.split.Group, policy.split.TeamHead
	arrangements := []catalogRateComponent{
		{ItemKey: vipGroupLeaderItem, Label: "小组负责人提成", TargetKind: "inherit", Rate: &group,
			LeaderScope: &vipLeaderScope{Orders: "members"}},
		{ItemKey: vipTeamLeaderItem, Label: "团队负责人提成", TargetKind: "inherit", Rate: &team,
			LeaderScope: &vipLeaderScope{Orders: "all"}},
	}
	for i := range arrangements {
		row := &arrangements[i]
		index := slices.IndexFunc(policy.actions, func(a vipBenefitAction) bool { return a.Item == row.ItemKey })
		if index < 0 {
			if label := policy.split.LeaderLabels[row.ItemKey]; label != "" {
				row.Label = label
			}
			continue
		}
		a := policy.actions[index]
		row.Label, row.Rate, row.Fixed, row.LeaderScope = a.Label, a.Rate, a.Fixed, a.Leader
		row.TargetKind = "appointment"
		if a.Designated != "" {
			row.TargetKind = "person"
		}
	}
	return arrangements
}

func editCatalogLeaderArrangements(ctx context.Context, tx pgx.Tx, raw json.RawMessage, changes []catalogLeaderChange) (json.RawMessage, error) {
	root, actions, err := catalogRateActions(raw)
	if err != nil {
		return nil, err
	}
	policy, _ := readVIPSalesPolicy(raw)
	prior, _ := readVIPBenefits(raw)
	categoryAdditions := make(map[string]bool)
	seen := make(map[string]bool, len(changes))
	var recipients []catalogRuleRecipientChange
	for _, change := range changes {
		if vipLeaderRole(change.ItemKey) == "" || seen[change.ItemKey] || !change.Scope.validFor(policy.Split.Teams) {
			return nil, invalid
		}
		seen[change.ItemKey] = true
		if change.Target == "person" {
			recipients = append(recipients, catalogRuleRecipientChange{PersonID: change.PersonID})
		}
		index := slices.IndexFunc(prior.actions, func(item vipBenefitAction) bool { return item.Item == change.ItemKey })
		for _, id := range change.Scope.CategoryIDs {
			if index < 0 || !slices.Contains(prior.actions[index].Leader.CategoryIDs, id) {
				categoryAdditions[id] = true
			}
		}
	}
	if err := validateNewCatalogScopeCategories(ctx, tx, categoryAdditions); err != nil {
		return nil, err
	}
	var people map[int64]string
	var inheritedLabels map[string]string
	if len(recipients) > 0 {
		people, err = catalogNewRecipientKeys(ctx, tx, recipients, nil)
		if err != nil {
			return nil, err
		}
	}
	for _, change := range changes {
		label := strings.TrimSpace(change.Label)
		if !textOK(label, 80) || len(change.Rows) > 0 {
			return nil, invalid
		}
		if change.Target == "inherit" {
			var amount int64
			var orders string
			switch change.ItemKey {
			case vipGroupLeaderItem:
				if policy.Split.independentGroup {
					return nil, invalid
				}
				amount, orders = policy.Split.Group, "members"
			case vipTeamLeaderItem:
				if policy.Split.independentTeam {
					return nil, invalid
				}
				amount, orders = policy.Split.TeamHead, "all"
			}
			if change.PersonID != 0 || change.Fixed != nil || change.Rate == nil || *change.Rate != amount ||
				change.Scope.Orders != orders || len(change.Scope.Companies)+len(change.Scope.Groups)+len(change.Scope.CategoryIDs) != 0 {
				return nil, invalid
			}
			if inheritedLabels == nil {
				inheritedLabels = policy.Split.LeaderLabels
				if inheritedLabels == nil {
					inheritedLabels = make(map[string]string)
				}
			}
			inheritedLabels[change.ItemKey] = label
			continue
		}
		action := vipBenefitAction{
			Item: change.ItemKey, Label: label, Role: vipLeaderRole(change.ItemKey), Leader: &change.Scope,
		}
		switch change.Target {
		case "appointment":
			if change.PersonID != 0 {
				return nil, invalid
			}
			action.Mode = vipOrganizationAppointmentMode
		case "person":
			action.Mode, action.Designated = "PARTY", people[change.PersonID]
		}
		if !action.validTarget() {
			return nil, invalid
		}
		if err := configureCatalogBenefitCalculation(&action, change.catalogBenefitCalculation, catalogStructureOptions{}); err != nil {
			return nil, err
		}
		body, err := json.Marshal(action)
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return nil, err
		}
		index := slices.IndexFunc(actions, func(a map[string]json.RawMessage) bool {
			var item string
			return json.Unmarshal(a["item_key"], &item) == nil && item == change.ItemKey
		})
		if index < 0 {
			actions = append(actions, fields)
		} else {
			actions[index] = fields
		}
	}
	if inheritedLabels != nil {
		var split map[string]json.RawMessage
		if err := json.Unmarshal(root["internal_sales_direct_split"], &split); err != nil {
			return nil, err
		}
		split["leader_labels"], _ = json.Marshal(inheritedLabels)
		root["internal_sales_direct_split"], _ = json.Marshal(split)
	}
	root["benefit_actions"], err = json.Marshal(actions)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}
