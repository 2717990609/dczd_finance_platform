package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"time"
)

type vipBenefitAction struct {
	Item              string            `json:"item_key"`
	Label             string            `json:"label"`
	Role              string            `json:"role"`
	Type              string            `json:"type"`
	Mode              string            `json:"beneficiary_mode"`
	Designated        string            `json:"designated_party_id"`
	Company           string            `json:"company_beneficiary_party_id"`
	PoolParty         string            `json:"pool_party_id"`
	PoolUnit          string            `json:"pool_organization_unit_id"`
	PoolCode          string            `json:"pool_code"`
	PoolName          string            `json:"pool_name"`
	OwnerRole         string            `json:"allocation_owner_role"`
	OwnerParty        string            `json:"allocation_owner_party_id,omitempty"`
	Rate              *int64            `json:"rate_bps"`
	Fixed             *int64            `json:"amount_minor,omitempty"`
	RateTable         []json.RawMessage `json:"rate_table"`
	rates             []vipRateRow
	Leader            *vipLeaderScope `json:"leader_scope,omitempty"`
	CommissionUnits   []string        `json:"sales_scope_unit_ids,omitempty"`
	ScopeCategories   []string        `json:"sales_scope_category_ids,omitempty"`
	OwnSales          bool            `json:"own_sales,omitempty"`
	RecipientDuty     string          `json:"recipient_duty,omitempty"`
	AppointmentUnit   string          `json:"appointment_organization_unit_id,omitempty"`
	AppointmentLevel  string          `json:"appointment_sales_org_level,omitempty"`
	MembershipNewOnly *bool           `json:"vip_membership_new_only,omitempty"`
}

type vipBenefitPolicy struct {
	split       vipInternalSplit
	actions     []vipBenefitAction
	leaderSales []vipRateRow
}

const vipOrderReferrerMode = "ORDER_REFERRER"

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
		sales, valid := def.directSalesPolicy()
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
		if dec.Decode(&action) != nil || action.Label == "" || action.Role == "" && action.Designated == "" || !action.validTarget() ||
			!validUniqueScopeIDs(action.CommissionUnits) || !validUniqueScopeIDs(action.ScopeCategories) {
			return vipBenefitPolicy{}, false
		}
		if action.Leader != nil {
			if !action.Leader.validFor(policy.split.Teams) || action.Type == "RATE_TABLE" || len(action.CommissionUnits)+len(action.ScopeCategories) != 0 {
				return vipBenefitPolicy{}, false
			}
			if policy.leaderSales == nil {
				sales, valid := readVIPSalesPolicy(raw)
				if !valid || len(sales.Sales) == 0 {
					return vipBenefitPolicy{}, false
				}
				policy.leaderSales = sales.Sales
			}
		}
		if (action.Type == "FIXED") != (action.Fixed != nil) {
			return vipBenefitPolicy{}, false
		}
		switch action.Type {
		case "FIXED":
			if *action.Fixed < 0 || action.Rate != nil || len(action.RateTable) != 0 {
				return vipBenefitPolicy{}, false
			}
		case "RATE":
			if action.Rate == nil || *action.Rate < 0 || *action.Rate > 10000 || len(action.RateTable) != 0 {
				return vipBenefitPolicy{}, false
			}
		case "RATE_TABLE":
			var ok bool
			action.rates, ok = parseVIPRateTable(action.RateTable)
			if !ok || action.Rate != nil || slices.ContainsFunc(action.rates, func(row vipRateRow) bool { return row.Payee != "" }) {
				return vipBenefitPolicy{}, false
			}
		default:
			return vipBenefitPolicy{}, false
		}
		policy.actions = append(policy.actions, action)
	}
	return policy, salesItem == "" || items[salesItem]
}

func (a vipBenefitAction) validTarget() bool {
	if a.OwnSales && (a.Mode != "PARTY" || a.Designated == "" || len(a.CommissionUnits)+len(a.ScopeCategories) != 0 || a.Leader != nil) ||
		a.RecipientDuty != "" && (a.Designated == "" || !validSourceRole(a.RecipientDuty)) {
		return false
	}
	if a.AppointmentUnit != "" {
		unit, ok := canonicalSourceUUID(a.AppointmentUnit)
		if !ok || unit != a.AppointmentUnit || a.Mode != "PARTY" || a.Designated == "" || a.Role == "" || a.Leader != nil {
			return false
		}
	}
	poolFields := a.PoolParty != "" || a.PoolUnit != "" || a.PoolCode != "" || a.PoolName != "" || a.OwnerRole != "" || a.OwnerParty != ""
	if a.AppointmentLevel != "" {
		return a.Mode == vipOrganizationAppointmentMode && a.Leader == nil && vipLeaderRole(a.Item) == "" &&
			a.Designated == "" && a.Company == "" && !poolFields && validSourceRole(a.Role) && validVIPSalesOrganizationLevel(a.AppointmentLevel)
	}
	if role := vipLeaderRole(a.Item); role != "" || a.Leader != nil {
		return role != "" && a.Leader != nil && a.Role == role && !poolFields && a.Company == "" &&
			(a.Mode == vipOrganizationAppointmentMode && a.Designated == "" || a.Mode == "PARTY" && a.Designated != "")
	}
	switch a.Mode {
	case "PARTY":
		return !poolFields && (a.Designated == "") != (a.Company == "")
	case vipOrderReferrerMode:
		return !poolFields && a.Designated == "" && a.Company == ""
	case "TEAM_POOL":
		return a.Designated == "" && a.Company == "" && a.PoolParty != "" && a.PoolUnit != "" &&
			a.PoolCode != "" && a.PoolName != "" && (a.OwnerRole != "") != (a.OwnerParty != "")
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
		if a.Leader != nil {
			out = append(out, quoteVIPLeader(a, req, sourceIssue, policy, data))
			continue
		}
		line := vipBenefitPart{Item: a.Item, Label: a.Label, Role: a.Role, Kind: a.targetKind()}
		if req.Paid > 0 && (a.Rate == nil || *a.Rate > 0) && (a.Fixed == nil || *a.Fixed > 0) &&
			!(a.Mode == vipOrderReferrerMode && req.Referrer.Party == "" && req.Referrer.Issue == "") {
			if eligible, issue := a.eligibleSale(req, sourceIssue, data); !eligible {
				line.Status = vipCommissionOutsideScope
				if issue != "" {
					line.Status = issue
				}
				out = append(out, line)
				continue
			}
		}
		rate, amount, issue := a.quote(req, sourceIssue)
		switch {
		case req.Paid == 0:
			line.Status = "ZERO"
		case a.Mode == vipOrderReferrerMode && req.Referrer.Party == "" && req.Referrer.Issue == "":
			line.Status = "ZERO"
		case !data.BenefitReady && a.Mode != vipOrderReferrerMode:
			line.Status = "BENEFIT_CONTEXT_MISSING"
		case issue != "":
			line.Status = issue
		case a.Fixed == nil && rate == 0 || a.Fixed != nil && amount == 0:
			line.Status = "ZERO"
		default:
			bindVIPBenefit(&line, a, req, data)
			if line.Status == "CALCULATED" {
				line.Rate, line.Amount = rate, amount
			}
		}
		out = append(out, line)
	}
	return out
}

func (a vipBenefitAction) eligibleSale(req vipSalesRequest, sourceIssue string, data vipRuntimeData) (bool, string) {
	if a.OwnSales {
		if req.Seller != "" {
			return req.Seller == a.Designated, ""
		}
		if sourceIssue == "" {
			sourceIssue = "SALES_PERSON_MISSING"
		}
		return false, sourceIssue
	}
	eligible, issue := vipScopedSale(a.CommissionUnits, a.ScopeCategories, req, sourceIssue, data)
	if eligible && a.AppointmentLevel != "" && sourceIssue == "SALES_COMPANY_REVIEW" {
		return false, sourceIssue
	}
	return eligible, issue
}

func (a vipBenefitAction) targetKind() string {
	if a.Mode == vipOrderReferrerMode {
		return "referrer"
	}
	if a.Mode == "TEAM_POOL" {
		return "pool"
	}
	if a.Designated != "" || a.AppointmentLevel != "" {
		return "person"
	}
	return "company"
}

func (a vipBenefitAction) quote(req vipSalesRequest, sourceIssue string) (rate, amount int64, issue string) {
	if a.Fixed != nil {
		amount = *a.Fixed
		if req.Paid > 0 && req.OriginalPaid > req.Paid {
			amount -= prorateMinor(amount, req.OriginalPaid-req.Paid, req.OriginalPaid)
		}
		return 0, amount, ""
	}
	if a.Rate != nil {
		rate = *a.Rate
	} else if sourceIssue != "" {
		return 0, 0, sourceIssue
	} else {
		rate, issue = resolveVIPRate(a.rates, req.Company, req.Relationship, req.Category)
	}
	return rate, calculatePaymentAmount(req.Paid, component{Mode: "rate", Value: rate}), issue
}

func bindVIPBenefit(line *vipBenefitPart, a vipBenefitAction, req vipSalesRequest, data vipRuntimeData) {
	line.Status = "CALCULATED"
	if a.AppointmentLevel != "" {
		bindVIPSalesOrganizationAppointment(line, a, req, data)
		return
	}
	if a.Mode == vipOrderReferrerMode {
		line.Kind = "person"
		if req.Referrer.Issue != "" {
			line.Status = req.Referrer.Issue
		} else {
			line.Party = req.Referrer.Party
			line.Name = data.People[line.Party]
		}
		return
	}
	if a.Designated != "" {
		name := data.People[a.Designated]
		if name == "" {
			line.Status = "BENEFIT_PERSON_MISSING"
		} else if a.Role != "" && !slices.ContainsFunc(data.Roles, func(r vipRole) bool {
			if r.Person != a.Designated || r.Role != a.Role || !r.contains(req.At) {
				return false
			}
			if a.AppointmentUnit != "" {
				return r.Scope == "ORGANIZATION_UNIT" && r.Key == a.AppointmentUnit
			}
			return r.Scope == "PRODUCT" && r.Key == req.Product || r.Scope == "PROJECT" && r.Key == req.Project
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
	if a.OwnerParty != "" {
		return a.OwnerParty, false
	}
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
