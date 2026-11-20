package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

var errCatalogRecipientAppointment = errors.New("新收款人的计提任职在规则生效时缺失或冲突")

type catalogRulePerson struct {
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name"`
}

type catalogRuleTeam struct {
	Key     string             `json:"key"`
	Label   string             `json:"label"`
	Head    catalogRulePerson  `json:"head"`
	Service *catalogRulePerson `json:"service,omitempty"`
}

type catalogRuleRecipientChange struct {
	ItemKey  string `json:"itemKey"`
	PersonID int64  `json:"personId"`
}

type catalogTeamRecipientChange struct {
	TeamKey         string `json:"teamKey"`
	HeadPersonID    *int64 `json:"headPersonId,omitempty"`
	ServicePersonID *int64 `json:"servicePersonId,omitempty"`
}

func (s workspace) getCatalogRulePeople(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, err := s.financeRateActor(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	_, product, err := s.ruleRateProduct(r.Context(), r.URL.Query().Get("ruleId"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	if !actor.permits(product, true) {
		respond(w, nil, errRulePermission)
		return
	}
	result, err := s.searchFinancePeople(r.Context(), r.URL.Query().Get("name"))
	respond(w, result, err)
}

func (s workspace) catalogRuleRecipients(ctx context.Context, raw json.RawMessage, components []catalogRateComponent, companies map[string]string) ([]catalogRuleTeam, error) {
	policy, ok := readVIPBenefits(raw)
	if !ok {
		return nil, errCatalogRateUnavailable
	}
	keys := make([]string, 0, len(policy.actions)+2*len(policy.split.Teams))
	targets := make(map[string]string)
	for _, action := range policy.actions {
		if action.Designated != "" {
			targets[action.Item] = action.Designated
			keys = append(keys, action.Designated)
		}
	}
	for _, team := range policy.split.Teams {
		keys = append(keys, team.HeadParty)
		if policy.split.Service > 0 {
			keys = append(keys, team.ServiceParty)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	people := make(map[string]catalogRulePerson)
	rows, err := s.db.Query(ctx, `SELECT p.id,p.name,`+vipPersonKey+` FROM people p WHERE `+vipPersonKey+`=ANY($1::text[])`, keys)
	if err != nil {
		return nil, err
	}
	var key string
	var person catalogRulePerson
	if _, err := pgx.ForEachRow(rows, []any{&person.ID, &person.Name, &key}, func() error {
		people[key] = person
		return nil
	}); err != nil {
		return nil, err
	}
	display := func(key string) catalogRulePerson {
		if person := people[key]; person.ID != 0 {
			return person
		}
		return catalogRulePerson{Name: "未匹配人员"}
	}
	for i := range components {
		if key := targets[components[i].ItemKey]; key != "" {
			person := display(key)
			components[i].Recipient = &person
		}
	}
	teams := make([]catalogRuleTeam, 0, len(policy.split.Teams))
	for _, team := range policy.split.Teams {
		label := companies[team.Company]
		if label == "" {
			label = team.Company
		}
		row := catalogRuleTeam{Key: team.Company, Label: label, Head: display(team.HeadParty)}
		if policy.split.Service > 0 {
			person := display(team.ServiceParty)
			row.Service = &person
		}
		teams = append(teams, row)
	}
	return teams, nil
}

func catalogNewRecipientKeys(ctx context.Context, tx pgx.Tx, changes []catalogRuleRecipientChange, teams []catalogTeamRecipientChange) (map[int64]string, error) {
	var people []int64
	for _, change := range changes {
		people = append(people, change.PersonID)
	}
	for _, team := range teams {
		for _, person := range []*int64{team.HeadPersonID, team.ServicePersonID} {
			if person != nil {
				people = append(people, *person)
			}
		}
	}
	keys := make(map[int64]string)
	rows, err := tx.Query(ctx, `SELECT p.id,`+vipPersonKey+` FROM people p WHERE p.id=ANY($1::bigint[]) FOR KEY SHARE OF p`, people)
	if err != nil {
		return nil, err
	}
	var id int64
	var key string
	if _, err := pgx.ForEachRow(rows, []any{&id, &key}, func() error {
		keys[id] = key
		return nil
	}); err != nil {
		return nil, err
	}
	return keys, nil
}

func editCatalogRecipients(raw json.RawMessage, changes []catalogRuleRecipientChange, teamChanges []catalogTeamRecipientChange, people map[int64]string) (json.RawMessage, bool, error) {
	root, actions, err := catalogRateActions(raw)
	if err != nil {
		return nil, false, err
	}
	itemFields := make(map[string]map[string]json.RawMessage)
	for _, fields := range actions {
		var item, designated string
		if json.Unmarshal(fields["item_key"], &item) == nil && json.Unmarshal(fields["designated_party_id"], &designated) == nil && designated != "" {
			itemFields[item] = fields
		}
	}
	changed := false
	for _, change := range changes {
		fields := itemFields[change.ItemKey]
		key := people[change.PersonID]
		if fields == nil || change.PersonID <= 0 || key == "" {
			return nil, false, invalid
		}
		delete(itemFields, change.ItemKey)
		changed = setCatalogPerson(fields, "designated_party_id", key) || changed
	}
	if changed {
		root["benefit_actions"], err = json.Marshal(actions)
		if err != nil {
			return nil, false, err
		}
	}
	teamsChanged, err := editCatalogTeamRecipients(root, teamChanges, people)
	if err != nil {
		return nil, false, err
	}
	if !changed && !teamsChanged {
		return raw, false, nil
	}
	body, err := json.Marshal(root)
	return body, true, err
}

func editCatalogTeamRecipients(root map[string]json.RawMessage, changes []catalogTeamRecipientChange, people map[int64]string) (bool, error) {
	if len(changes) == 0 {
		return false, nil
	}
	const splitField = "internal_sales_direct_split"
	var split map[string]json.RawMessage
	var teams []map[string]json.RawMessage
	if json.Unmarshal(root[splitField], &split) != nil || json.Unmarshal(split["teams"], &teams) != nil {
		return false, invalid
	}
	byCompany := make(map[string]map[string]json.RawMessage)
	for _, row := range teams {
		var key string
		if json.Unmarshal(row["sales_company_party_id"], &key) != nil || key == "" || byCompany[key] != nil {
			return false, invalid
		}
		byCompany[key] = row
	}
	var serviceRate int64
	_ = json.Unmarshal(split["service_rate_bps"], &serviceRate)
	changed := false
	for _, change := range changes {
		row := byCompany[change.TeamKey]
		if row == nil || change.HeadPersonID == nil && change.ServicePersonID == nil || change.ServicePersonID != nil && serviceRate <= 0 {
			return false, invalid
		}
		delete(byCompany, change.TeamKey)
		for _, target := range []struct {
			field string
			id    *int64
		}{{"head_party_id", change.HeadPersonID}, {"service_party_id", change.ServicePersonID}} {
			if target.id == nil {
				continue
			}
			if *target.id <= 0 || people[*target.id] == "" {
				return false, invalid
			}
			changed = setCatalogPerson(row, target.field, people[*target.id]) || changed
		}
	}
	if !changed {
		return false, nil
	}
	var err error
	split["teams"], err = json.Marshal(teams)
	if err != nil {
		return false, err
	}
	root[splitField], err = json.Marshal(split)
	return true, err
}

func setCatalogPerson(fields map[string]json.RawMessage, field, key string) bool {
	var existing string
	if json.Unmarshal(fields[field], &existing) == nil && existing == key {
		return false
	}
	fields[field], _ = json.Marshal(key)
	return true
}

func validateCatalogRecipientAppointments(ctx context.Context, tx pgx.Tx, product int64, at time.Time, raw, edited json.RawMessage) error {
	before, wasValid := readVIPBenefits(raw)
	after, ok := readVIPBenefits(edited)
	if !wasValid || !ok || len(before.actions) > len(after.actions) || len(before.split.Teams) != len(after.split.Teams) {
		return invalid
	}
	type requiredAppointment struct {
		Label string `json:"label"`
		Key   string `json:"key"`
		Role  string `json:"role"`
		Unit  string `json:"unit"`
	}
	var requirements []requiredAppointment
	beforePeople := make(map[string]string, len(before.actions))
	for _, action := range before.actions {
		beforePeople[action.Item] = action.Designated
	}
	for _, action := range after.actions {
		if action.Designated != "" && action.Designated != beforePeople[action.Item] {
			requirements = append(requirements, requiredAppointment{Label: action.Label, Key: action.Designated, Role: action.Role})
		}
	}
	for i, team := range after.split.Teams {
		if team.HeadParty != before.split.Teams[i].HeadParty {
			if team.TeamUnit == "" {
				return invalid
			}
			requirements = append(requirements, requiredAppointment{Label: "内部团队负责人", Key: team.HeadParty, Role: "TEAM_LEAD", Unit: team.TeamUnit})
		}
	}
	if len(requirements) == 0 {
		return nil
	}
	// Appointment changes lock this row before changing the applicable terms.
	if _, err := tx.Exec(ctx, `SELECT source FROM catalog_sales_context WHERE source='DCAI' FOR SHARE`); err != nil {
		return err
	}
	targets, err := json.Marshal(requirements)
	if err != nil {
		return err
	}
	var label string
	err = tx.QueryRow(ctx, `SELECT wanted.label
	 FROM jsonb_to_recordset($3) AS wanted(label text,key text,role text,unit text)
	 JOIN products pro ON pro.id=$1 JOIN product_catalog_settings settings ON settings.product_id=pro.id
	 CROSS JOIN LATERAL (
	  SELECT count(DISTINCT a.person_id) AS holders,bool_or(`+vipPersonKey+`=wanted.key) AS chosen
	  FROM catalog_current_sales_appointments a JOIN people p ON p.id=a.person_id
	  WHERE a.source='DCAI' AND a.role=wanted.role AND a.valid_from<=$2 AND (a.valid_to IS NULL OR a.valid_to>$2)
	   AND ((wanted.unit='' AND ((a.scope='PRODUCT' AND a.scope_key=pro.code)
	    OR (a.scope='PROJECT' AND a.scope_key=settings.project_code)))
	    OR (wanted.unit<>'' AND a.scope='ORGANIZATION_UNIT' AND a.scope_key=wanted.unit))
	 ) actual
	 WHERE NOT (coalesce(actual.chosen,false) AND (wanted.unit='' OR actual.holders=1))
	 ORDER BY wanted.label LIMIT 1`, product, at, targets).Scan(&label)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w，请核对「%s」", errCatalogRecipientAppointment, label)
}
