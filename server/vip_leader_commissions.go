package main

import "slices"

const (
	vipOrganizationAppointmentMode = "ORDER_ORGANIZATION_APPOINTMENT"
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
	standard, issue := selectVIPRateForCompany(policy.leaderSales, req.Company, req.Relationship, req.Category, "INTERNAL")
	if issue != "" {
		line.Status = issue
		return line
	}
	if standard.Rate == 0 {
		return line
	}
	if !data.BenefitReady || standard.Rate != policy.split.Total || standard.Payee == vipPayeeSalesCompany {
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
	if eligible, categoryIssue := catalogSalesCategoryMatches(a.Leader.CategoryIDs, req.Category, sourceIssue); !eligible {
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
	if conflict || holder == "" || data.People[holder] == "" {
		line.Status = "SALES_TEAM_PENDING"
		if a.Role == "GROUP_LEAD" {
			line.Status = "SALES_GROUP_PENDING"
		}
		return line
	}
	line.Rate, line.Amount, _ = a.quote(req, "")
	line.Status, line.Party, line.Name = "CALCULATED", holder, data.People[holder]
	return line
}
