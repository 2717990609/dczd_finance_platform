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
		{ItemKey: vipGroupLeaderItem, Label: "小组负责人提成", TargetKind: "appointment", Rate: &group,
			LeaderScope: &vipLeaderScope{Orders: "members"}},
		{ItemKey: vipTeamLeaderItem, Label: "团队负责人提成", TargetKind: "appointment", Rate: &team,
			LeaderScope: &vipLeaderScope{Orders: "all"}},
	}
	for i := range arrangements {
		row := &arrangements[i]
		index := slices.IndexFunc(policy.actions, func(a vipBenefitAction) bool { return a.Item == row.ItemKey })
		if index < 0 {
			continue
		}
		a := policy.actions[index]
		row.Label, row.Rate, row.Fixed, row.LeaderScope = a.Label, a.Rate, a.Fixed, a.Leader
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
	}
	var people map[int64]string
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
	root["benefit_actions"], err = json.Marshal(actions)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}
