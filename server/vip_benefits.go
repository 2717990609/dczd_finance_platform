package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"time"
)

type vipBenefitAction struct {
	Item       string            `json:"item_key"`
	Label      string            `json:"label"`
	Role       string            `json:"role"`
	Type       string            `json:"type"`
	Mode       string            `json:"beneficiary_mode"`
	Designated string            `json:"designated_party_id"`
	Company    string            `json:"company_beneficiary_party_id"`
	PoolParty  string            `json:"pool_party_id"`
	PoolUnit   string            `json:"pool_organization_unit_id"`
	PoolCode   string            `json:"pool_code"`
	PoolName   string            `json:"pool_name"`
	OwnerRole  string            `json:"allocation_owner_role"`
	Rate       *int64            `json:"rate_bps"`
	RateTable  []json.RawMessage `json:"rate_table"`
	rates      []vipRateRow
}

type vipBenefitPolicy struct {
	split   vipInternalSplit
	actions []vipBenefitAction
}

type vipBenefitOrganization struct {
	Party, Unit, Name string
}

type vipBenefitPart struct {
	Item      string `json:"itemKey"`
	Label     string `json:"label"`
	Role      string `json:"-"`
	Kind      string `json:"kind,omitempty"`
	Party     string `json:"-"`
	Unit      string `json:"-"`
	Owner     string `json:"-"`
	Pool      string `json:"-"`
	Name      string `json:"beneficiaryName,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	Amount    int64  `json:"amountMinor"`
	Rate      int64  `json:"-"`
	Status    string `json:"status"`
}

func readVIPBenefits(raw json.RawMessage) (vipBenefitPolicy, bool) {
	def, ok := readVIPCommissionDefinition(raw)
	if !ok {
		return vipBenefitPolicy{}, false
	}
	var policy vipBenefitPolicy
	var salesItem string
	if def.Split != nil {
		policy.split = *def.Split
		salesItem = def.Split.Item
	} else {
		sales, valid := readDirectSalesPolicy(def.Actions)
		if !valid {
			return vipBenefitPolicy{}, false
		}
		salesItem = sales.Item
	}
	items := make(map[string]bool, len(def.Actions))
	for _, payload := range def.Actions {
		var head struct {
			Item string `json:"item_key"`
		}
		if json.Unmarshal(payload, &head) != nil || head.Item == "" || items[head.Item] {
			return vipBenefitPolicy{}, false
		}
		items[head.Item] = true
		if head.Item == salesItem {
			continue
		}
		var action vipBenefitAction
		dec := json.NewDecoder(bytes.NewReader(payload))
		dec.DisallowUnknownFields()
		if dec.Decode(&action) != nil || action.Label == "" || action.Role == "" || !action.validTarget() {
			return vipBenefitPolicy{}, false
		}
		switch action.Type {
		case "RATE":
			if action.Rate == nil || *action.Rate < 0 || *action.Rate > 10000 || len(action.RateTable) != 0 {
				return vipBenefitPolicy{}, false
			}
		case "RATE_TABLE":
			var ok bool
			action.rates, ok = parseVIPRateTable(action.RateTable)
			if !ok || action.Rate != nil {
				return vipBenefitPolicy{}, false
			}
		default:
			return vipBenefitPolicy{}, false
		}
		policy.actions = append(policy.actions, action)
	}
	return policy, items[salesItem]
}

func (a vipBenefitAction) validTarget() bool {
	poolFields := a.PoolParty != "" || a.PoolUnit != "" || a.PoolCode != "" || a.PoolName != "" || a.OwnerRole != ""
	switch a.Mode {
	case "PARTY":
		return !poolFields && (a.Designated == "") != (a.Company == "")
	case "TEAM_POOL":
		return a.Designated == "" && a.Company == "" && a.PoolParty != "" && a.PoolUnit != "" &&
			a.PoolCode != "" && a.PoolName != "" && a.OwnerRole != ""
	default:
		return false
	}
}

func quoteVIPBenefits(req vipSalesRequest, sourceIssue string, policy vipBenefitPolicy, data vipRuntimeData) []vipBenefitPart {
	for _, review := range policy.split.Reviews {
		if req.Order == review.Order && (req.Company != review.Company || req.Seller != review.Seller) {
			sourceIssue = "SALES_COMPANY_REVIEW"
			break
		}
	}
	out := make([]vipBenefitPart, 0, len(policy.actions))
	for _, a := range policy.actions {
		line := vipBenefitPart{Item: a.Item, Label: a.Label, Role: a.Role, Kind: a.targetKind()}
		rate, issue := a.effectiveRate(req, sourceIssue)
		switch {
		case req.Paid == 0:
			line.Status = "ZERO"
		case !data.BenefitReady:
			line.Status = "BENEFIT_CONTEXT_MISSING"
		case issue != "":
			line.Status = issue
		case rate == 0:
			line.Status = "ZERO"
		default:
			bindVIPBenefit(&line, a, req, data)
			if line.Status == "CALCULATED" {
				line.Rate = rate
				line.Amount = calculatePaymentAmount(req.Paid, component{Mode: "rate", Value: rate})
			}
		}
		out = append(out, line)
	}
	return out
}

func (a vipBenefitAction) targetKind() string {
	if a.Mode == "TEAM_POOL" {
		return "pool"
	}
	if a.Designated != "" {
		return "person"
	}
	return "company"
}

func (a vipBenefitAction) effectiveRate(req vipSalesRequest, sourceIssue string) (int64, string) {
	if a.Rate != nil {
		return *a.Rate, ""
	}
	if sourceIssue != "" {
		return 0, sourceIssue
	}
	return resolveVIPRate(a.rates, req.Company, req.Relationship, req.Category)
}

func bindVIPBenefit(line *vipBenefitPart, a vipBenefitAction, req vipSalesRequest, data vipRuntimeData) {
	line.Status = "CALCULATED"
	if a.Designated != "" {
		name := data.People[a.Designated]
		if name == "" {
			line.Status = "BENEFIT_PERSON_MISSING"
		} else if !slices.ContainsFunc(data.Roles, func(r vipRole) bool {
			return r.Person == a.Designated && r.Role == a.Role && r.contains(req.At) &&
				(r.Scope == "PRODUCT" && r.Key == req.Product || r.Scope == "PROJECT" && r.Key == req.Project)
		}) {
			line.Status = "INACTIVE"
		} else {
			line.Party, line.Name = a.Designated, name
		}
		return
	}
	party := a.Company
	if a.Mode == "TEAM_POOL" {
		party = a.PoolParty
	}
	var unit vipBenefitOrganization
	for _, o := range data.Organizations {
		if o.Party != party || a.PoolUnit != "" && a.PoolUnit != o.Unit {
			continue
		}
		if unit.Unit != "" && unit.Unit != o.Unit {
			line.Status = "BENEFIT_ORG_CONFLICT"
			return
		}
		unit = o
	}
	if unit.Unit == "" {
		line.Status = "BENEFIT_ORG_MISSING"
		return
	}
	line.Party, line.Unit, line.Name = party, unit.Unit, unit.Name
	if a.Mode != "TEAM_POOL" {
		return
	}
	line.Pool, line.Name = a.PoolCode, a.PoolName
	holder, ambiguous := vipPoolOwner(a, req, data.Roles)
	switch {
	case ambiguous:
		line.Status = "BENEFIT_POOL_OWNER_CONFLICT"
	case holder == "" || data.People[holder] == "":
		line.Status = "BENEFIT_POOL_OWNER_MISSING"
	default:
		line.Owner, line.OwnerName = holder, data.People[holder]
	}
}

func vipPoolOwner(a vipBenefitAction, req vipSalesRequest, roles []vipRole) (string, bool) {
	for _, scope := range []struct{ Type, Key string }{
		{"ORGANIZATION_UNIT", a.PoolUnit}, {"PRODUCT", req.Product}, {"PROJECT", req.Project},
	} {
		if scope.Key == "" {
			continue
		}
		holder, ambiguous := vipRoleHolder(roles, a.OwnerRole, scope.Type, scope.Key, req.At)
		if ambiguous || holder != "" {
			return holder, ambiguous
		}
	}
	return "", false
}

func vipRoleHolder(roles []vipRole, role, scope, key string, at time.Time) (holder string, ambiguous bool) {
	for _, r := range roles {
		if r.Role != role || r.Scope != scope || r.Key != key || !r.contains(at) {
			continue
		}
		if holder != "" && holder != r.Person {
			return "", true
		}
		holder = r.Person
	}
	return holder, false
}
