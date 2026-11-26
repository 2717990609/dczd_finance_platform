package main

import "slices"

const (
	vipOrganizationAppointmentMode = "ORDER_ORGANIZATION_APPOINTMENT"
	vipRegisteredTeamLeaderMode    = "RULE_REGISTERED_TEAM_LEADER"
	vipGroupLeaderItem             = "internal-group-leader"
	vipTeamLeaderItem              = "internal-team-leader"
	vipInternalServiceItem         = "internal-service"
)

type vipLeaderScope struct {
	Orders      string   `json:"orders"`
	Companies   []string `json:"companies,omitempty"`
	Groups      []string `json:"groups,omitempty"`
	CategoryIDs []string `json:"categoryIds,omitempty"`
}

func vipLeaderRole(item string) string {
	switch item {
	case vipGroupLeaderItem:
		return "GROUP_LEAD"
	case vipTeamLeaderItem:
		return "TEAM_LEAD"
	default:
		return ""
	}
}

func validVIPSalesOrganizationLevel(level string) bool {
	return level == "COMPANY" || level == "DEPARTMENT" || level == "TEAM"
}

func vipSalesAppointmentOrganization(req vipSalesRequest, level string) (string, string) {
	var org reportOrganization
	if req.commissionSalesOrg != nil {
		org = *req.commissionSalesOrg
	}
	company := req.CompanyUnit
	if company == "" {
		company = org.Company.String()
	}
	return salesAppointmentOrganization(company, org.Department.String(), org.groupID().String(), org.scopeAlternatives, level)
}

func salesAppointmentOrganization(company, department, group string, alternatives []organizationScopeCandidate, level string) (string, string) {
	const missing = "COMMISSION_SALES_ORGANIZATION_MISSING"
	if level == "COMPANY" {
		if company != "" {
			return company, ""
		}
		return "", missing
	}
	unit := department
	if level == "TEAM" {
		unit = group
	}
	for index, candidate := range alternatives {
		possible := candidate.Department
		if level == "TEAM" {
			possible = candidate.Group
		}
		if index == 0 && unit == "" {
			unit = possible
		} else if possible != unit {
			return "", "SALES_ORGANIZATION_SCOPE_CONFLICT"
		}
	}
	if unit == "" {
		return "", missing
	}
	return unit, ""
}

func bindVIPSalesOrganizationAppointment(line *vipBenefitPart, a vipBenefitAction, req vipSalesRequest, data vipRuntimeData) {
	unit, issue := vipSalesAppointmentOrganization(req, a.AppointmentLevel)
	if issue != "" {
		line.Status = issue
		return
	}
	holder, ambiguous := vipRoleHolder(data.Roles, a.Role, "ORGANIZATION_UNIT", unit, req.At)
	switch {
	case ambiguous:
		line.Status = "BENEFIT_APPOINTMENT_CONFLICT"
	case holder == "":
		line.Status = "INACTIVE"
	case data.People[holder] == "":
		line.Status = "BENEFIT_PERSON_MISSING"
	default:
		line.Party, line.Name = holder, data.People[holder]
	}
}

func (scope vipLeaderScope) validFor(teams []vipInternalTeam) bool {
	if len(teams) == 0 || scope.Orders != "all" && scope.Orders != "members" && scope.Orders != "head" {
		return false
	}
	companies := make(map[string]bool, len(teams))
	groups := make(map[string]bool)
	for _, team := range teams {
		companies[team.Company] = true
		if len(scope.Companies) == 0 || slices.Contains(scope.Companies, team.Company) {
			for _, group := range team.GroupUnits {
				groups[group] = true
			}
		}
	}
	validIDs := func(ids []string, allowed map[string]bool) bool {
		seen := make(map[string]bool, len(ids))
		for _, id := range ids {
			if id == "" || !allowed[id] || seen[id] {
				return false
			}
			seen[id] = true
		}
		return true
	}
	return validIDs(scope.Companies, companies) && validIDs(scope.Groups, groups) && validUniqueScopeIDs(scope.CategoryIDs)
}

func vipSellerGroup(team vipInternalTeam, req vipSalesRequest, memberships []vipMembership) (string, bool) {
	var group string
	for _, m := range memberships {
		if m.Person != req.Seller || !m.contains(req.At) || !slices.Contains(team.GroupUnits, m.Unit) {
			continue
		}
		if group != "" && group != m.Unit {
			return "", true
		}
		group = m.Unit
	}
	return group, false
}

func quoteVIPLeader(a vipBenefitAction, req vipSalesRequest, sourceIssue string, policy vipBenefitPolicy, data vipRuntimeData) vipBenefitPart {
	line := vipBenefitPart{Item: a.Item, Label: a.Label, Role: a.Role, Kind: "person", Status: "ZERO"}
	if req.Paid == 0 || a.Rate != nil && *a.Rate == 0 || a.Fixed != nil && *a.Fixed == 0 {
		return line
	}
	teamIndex := slices.IndexFunc(policy.split.Teams, func(t vipInternalTeam) bool { return t.Company == req.Company })
	switch {
	case req.Company != "" && (teamIndex < 0 || len(a.Leader.Companies) > 0 && !slices.Contains(a.Leader.Companies, req.Company)):
		return line
	case sourceIssue != "":
		line.Status = sourceIssue
		return line
	case req.Company == "":
		line.Status = "SALES_COMPANY_MISSING"
		return line
	}
	if !policy.split.Independent {
		standard, issue := req.rateRow(policy.leaderSales, "INTERNAL")
		if issue != "" {
			line.Status = issue
			return line
		}
		if standard.Rate == 0 {
			return line
		}
		if standard.Rate != policy.split.Total || standard.Payee == vipPayeeSalesCompany {
			line.Status = "SALES_TEAM_PENDING"
			return line
		}
	}
	if !data.BenefitReady {
		line.Status = "SALES_TEAM_PENDING"
		return line
	}
	if req.Seller == "" || data.People[req.Seller] == "" {
		line.Status = "SALES_PERSON_MISSING"
		return line
	}
	if !data.hasSalesAppointment(req) {
		return line
	}
	team := policy.split.Teams[teamIndex]
	if a.Leader.Orders != "all" {
		head := data.holder("TEAM_LEAD", team.TeamUnit, req.At)
		if head == "" {
			line.Status = "SALES_TEAM_PENDING"
			return line
		}
		if (a.Leader.Orders == "head") != (req.Seller == head) {
			return line
		}
	}
	unit := team.TeamUnit
	if a.Role == "GROUP_LEAD" || len(a.Leader.Groups) > 0 {
		group, conflict := vipSellerGroup(team, req, data.Memberships)
		if conflict || group == "" && len(a.Leader.Groups) == 0 {
			line.Status = "SALES_GROUP_PENDING"
			return line
		}
		if len(a.Leader.Groups) > 0 && !slices.Contains(a.Leader.Groups, group) {
			return line
		}
		if a.Role == "GROUP_LEAD" {
			unit = group
		}
	}
	if eligible, categoryIssue := catalogSalesCategoryMatches(a.Leader.CategoryIDs, req.Category, req.categoryIssue); !eligible {
		if categoryIssue != "" {
			line.Status = categoryIssue
		}
		return line
	}
	holder, conflict := vipRoleHolder(data.Roles, a.Role, "ORGANIZATION_UNIT", unit, req.At)
	if !conflict && a.Designated != "" && a.Designated != holder {
		line.Status = "INACTIVE"
		return line
	}
	if conflict || holder == "" || data.People[holder] == "" || a.Mode == vipRegisteredTeamLeaderMode && holder != team.HeadParty {
		line.Status = "SALES_TEAM_PENDING"
		if a.Role == "GROUP_LEAD" {
			line.Status = "SALES_GROUP_PENDING"
		}
		return line
	}
	line.Rate, line.Amount, _ = a.quote(req, "")
	line.preciseAmount = a.preciseAmount(req, line.Rate)
	line.Status, line.Party, line.Name = "CALCULATED", holder, data.People[holder]
	return line
}

func quoteVIPIndependentTeamShares(req vipSalesRequest, sourceIssue string, policy vipBenefitPolicy, data vipRuntimeData) []vipBenefitPart {
	split := policy.split
	var parts []vipBenefitPart
	for _, share := range []struct {
		key, label, orders string
		rate               int64
	}{
		{vipGroupLeaderItem, "小组负责人提成", "members", split.Group},
		{vipTeamLeaderItem, "团队负责人提成", "all", split.TeamHead},
	} {
		if !split.hasPositiveShare(share.key) {
			continue
		}
		label := split.LeaderLabels[share.key]
		if label == "" {
			label = share.label
		}
		mode := vipOrganizationAppointmentMode
		if share.key == vipTeamLeaderItem {
			mode = vipRegisteredTeamLeaderMode
		}
		action := vipBenefitAction{Item: share.key, Label: label, Role: vipLeaderRole(share.key),
			Mode: mode, Rate: &share.rate, Leader: &vipLeaderScope{Orders: share.orders}}
		part := quoteVIPLeader(action, req, sourceIssue, policy, data)
		if part.Status != "ZERO" && part.Status != "INACTIVE" {
			parts = append(parts, part)
		}
	}
	if !split.hasPositiveShare(vipInternalServiceItem) || req.Paid == 0 {
		return parts
	}
	index := slices.IndexFunc(split.Teams, func(team vipInternalTeam) bool { return team.Company == req.Company })
	if index < 0 && req.Company != "" {
		return parts
	}
	label := split.ServiceLabel
	if label == "" {
		label = "内部团队服务提成"
	}
	part := vipBenefitPart{Item: vipInternalServiceItem, Label: label, Role: "DCAI_SERVICE", Kind: "person"}
	switch {
	case sourceIssue != "":
		part.Status = sourceIssue
	case index < 0:
		part.Status = "SALES_COMPANY_MISSING"
	case !data.hasSalesAppointment(req):
		return parts
	default:
		part.Party = split.Teams[index].ServiceParty
		part.Name = data.People[part.Party]
		part.Status = "SALES_TEAM_PENDING"
		if part.Name != "" {
			part.Status = "CALCULATED"
			part.Rate = split.Service
			part.Amount = calculatePaymentAmount(req.Paid, component{Mode: "rate", Value: split.Service})
			part.preciseAmount = preciseYuan(exactMinor(req.Paid, part.Rate, 10000))
		}
	}
	return append(parts, part)
}
