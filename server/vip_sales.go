package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

type vipRateRow struct {
	Company         string `json:"sales_organization_party_id"`
	Relationship    string `json:"sales_relationship"`
	Category        string `json:"sales_category_id"`
	SnapshotDefault bool   `json:"sales_category_snapshot_default"`
	Rate            int64  `json:"rate_bps"`
}

type vipInternalTeam struct {
	Company      string   `json:"sales_company_party_id"`
	TeamUnit     string   `json:"team_unit_id"`
	HeadParty    string   `json:"head_party_id"`
	ServiceParty string   `json:"service_party_id"`
	GroupUnits   []string `json:"seller_group_unit_ids"`
}

type vipInternalSplit struct {
	Mode       string            `json:"mode"`
	Item       string            `json:"sales_item_key"`
	Total      int64             `json:"total_rate_bps"`
	Assistant  int64             `json:"assistant_seller_rate_bps"`
	HeadSeller int64             `json:"head_seller_rate_bps"`
	Group      int64             `json:"group_lead_rate_bps"`
	Service    int64             `json:"service_rate_bps"`
	TeamHead   int64             `json:"team_head_rate_bps"`
	Teams      []vipInternalTeam `json:"teams"`
	Reviews    []struct {
		Order   string `json:"source_order_id"`
		Seller  string `json:"seller_party_id"`
		Company string `json:"sales_company_party_id"`
	} `json:"order_company_reviews"`
}

type vipSalesPolicy struct {
	Sales []vipRateRow
	Split vipInternalSplit
}

func readVIPSalesPolicy(raw json.RawMessage) (vipSalesPolicy, bool) {
	var def struct {
		Basis    string `json:"amount_basis"`
		Stacking string `json:"stacking_policy"`
		Rounding struct {
			Mode  string
			Scale int
		} `json:"rounding_policy"`
		Split   vipInternalSplit `json:"internal_sales_direct_split"`
		Actions []struct {
			Kind       string            `json:"type"`
			Mode       string            `json:"beneficiary_mode"`
			Role       string            `json:"role"`
			Item       string            `json:"item_key"`
			Designated string            `json:"designated_party_id"`
			Rates      []json.RawMessage `json:"rate_table"`
		} `json:"benefit_actions"`
	}
	if json.Unmarshal(raw, &def) != nil || def.Basis != "ACTUAL_RECEIVED" || def.Stacking != "SUM" ||
		def.Rounding.Mode != "HALF_UP" || def.Rounding.Scale != 2 || def.Split.Mode != "DCAI_INTERNAL_DIRECT" || def.Split.Item == "" {
		return vipSalesPolicy{}, false
	}
	var match []vipRateRow
	for _, action := range def.Actions {
		if action.Item != def.Split.Item {
			continue
		}
		if match != nil || action.Kind != "RATE_TABLE" || action.Mode != "PARTY" || action.Role != "SALES" ||
			action.Designated != "" || len(action.Rates) == 0 {
			return vipSalesPolicy{}, false
		}
		var ok bool
		match, ok = parseVIPRateTable(action.Rates)
		if !ok {
			return vipSalesPolicy{}, false
		}
	}
	return vipSalesPolicy{Sales: match, Split: def.Split}, match != nil
}

func parseVIPRateTable(rawRows []json.RawMessage) ([]vipRateRow, bool) {
	if len(rawRows) == 0 {
		return nil, false
	}
	rows := make([]vipRateRow, 0, len(rawRows))
	for _, raw := range rawRows {
		var row vipRateRow
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &row) != nil || json.Unmarshal(raw, &fields) != nil ||
			len(fields["rate_bps"]) == 0 || string(fields["rate_bps"]) == "null" ||
			row.Company == "" || row.Rate < 0 || row.Rate > 10000 ||
			!slices.Contains([]string{"ANY", "DIRECT", "INDIRECT", "NONE"}, row.Relationship) {
			return nil, false
		}
		for key := range fields {
			switch key {
			case "id", "rate_bps", "sales_relationship", "sales_organization_party_id",
				"sales_category_id", "sales_category_snapshot_default":
			default:
				return nil, false
			}
		}
		rows = append(rows, row)
	}
	return rows, true
}

func resolveVIPRate(rows []vipRateRow, company, relationship, category string) (int64, string) {
	if company == "" {
		return 0, "SALES_COMPANY_MISSING"
	}
	if relationship != "" && relationship != "DIRECT" && relationship != "INDIRECT" && relationship != "NONE" {
		return 0, "SALES_RELATIONSHIP_MISSING"
	}
	categorized := slices.ContainsFunc(rows, func(r vipRateRow) bool { return r.Category != "" })
	var groups [4][]vipRateRow
	for _, row := range rows {
		if row.Company != company && row.Company != "ANY" || row.Relationship != "ANY" && relationship != "" && row.Relationship != relationship ||
			row.Category != "" && row.Category != category {
			continue
		}
		tier := 0
		if categorized {
			switch {
			case row.Company == company && !row.SnapshotDefault:
				tier = 0
			case row.Company == "ANY" && row.Category != "":
				tier = 1
			case row.Company == company:
				tier = 2
			default:
				tier = 3
			}
		}
		groups[tier] = append(groups[tier], row)
	}
	if categorized && category == "" && len(groups[0])+len(groups[2]) == 0 {
		return 0, "SALES_CATEGORY_MISSING"
	}
	for _, choices := range groups {
		if len(choices) == 0 {
			continue
		}
		best := -1
		var rate int64
		tie := false
		for _, row := range choices {
			if relationship == "" && row.Relationship != "ANY" {
				return 0, "SALES_RELATIONSHIP_MISSING"
			}
			score := 0
			if row.Relationship != "ANY" {
				score++
			}
			if row.Company != "ANY" {
				score++
			}
			switch {
			case score > best:
				best, rate, tie = score, row.Rate, false
			case score == best:
				tie = true
			}
		}
		if tie || rate < 0 || rate > 10000 {
			return 0, "SALES_RATE_CONFLICT"
		}
		return rate, ""
	}
	return 0, "SALES_RATE_MISSING"
}

type vipInterval struct {
	From time.Time
	To   *time.Time
}

func (x vipInterval) contains(at time.Time) bool {
	return !at.Before(x.From) && (x.To == nil || at.Before(*x.To))
}

type vipRole struct {
	Person, Role, Scope, Key string
	vipInterval
}

type vipMembership struct {
	Person, Unit string
	vipInterval
}

type vipSalesData struct {
	Roles       []vipRole
	Memberships []vipMembership
	People      map[string]string
}

type vipSalesRequest struct {
	Order, Product, Project, Seller, Company, Relationship, Category string
	CompanyUnit                                                      string
	Paid                                                             int64
	At                                                               time.Time
}

type vipSalesPart struct {
	Role   string `json:"role"`
	Person string `json:"-"`
	Name   string `json:"personName"`
	Rate   int64  `json:"rate"`
	Amount int64  `json:"amountMinor"`
}

func (d vipSalesData) holder(role, unit string, at time.Time) string {
	var id string
	for _, a := range d.Roles {
		if a.Role != role || a.Scope != "ORGANIZATION_UNIT" || a.Key != unit || !a.contains(at) {
			continue
		}
		if id != "" && id != a.Person {
			return ""
		}
		id = a.Person
	}
	return id
}

func calculateVIPSales(policy vipSalesPolicy, req vipSalesRequest, d vipSalesData) ([]vipSalesPart, string) {
	for _, review := range policy.Split.Reviews {
		if req.Order == review.Order && (req.Company != review.Company || req.Seller != review.Seller) {
			return nil, "SALES_COMPANY_REVIEW"
		}
	}
	if req.Seller == "" || d.People[req.Seller] == "" {
		return nil, "SALES_PERSON_MISSING"
	}
	if !slices.ContainsFunc(d.Roles, func(a vipRole) bool {
		return a.Role == "SALES" && a.Person == req.Seller && a.contains(req.At) &&
			(a.Scope == "PRODUCT" && a.Key == req.Product || a.Scope == "PROJECT" && a.Key == req.Project)
	}) {
		return nil, "SALES_APPOINTMENT_MISSING"
	}
	rate, issue := resolveVIPRate(policy.Sales, req.Company, req.Relationship, req.Category)
	if issue != "" {
		return nil, issue
	}
	if req.Order == "" || req.Paid < 0 {
		return nil, "INVALID_PAYMENT"
	}
	amount := calculatePaymentAmount(req.Paid, component{Mode: "rate", Value: rate})
	parts := []vipSalesPart{{Role: "SALES", Person: req.Seller, Name: d.People[req.Seller], Rate: rate, Amount: amount}}
	if rate == 0 {
		return parts, ""
	}
	teamIdx := slices.IndexFunc(policy.Split.Teams, func(t vipInternalTeam) bool { return t.Company == req.Company })
	if teamIdx < 0 {
		return parts, ""
	}
	p, team := policy.Split, policy.Split.Teams[teamIdx]
	if rate != p.Total || team.HeadParty == "" || d.holder("TEAM_LEAD", team.TeamUnit, req.At) != team.HeadParty {
		return nil, "SALES_TEAM_PENDING"
	}

	parts[0].Rate = p.HeadSeller
	if req.Seller != team.HeadParty {
		parts[0].Rate = p.Assistant
		if p.Group > 0 {
			var group string
			for _, m := range d.Memberships {
				if m.Person != req.Seller || !m.contains(req.At) || !slices.Contains(team.GroupUnits, m.Unit) {
					continue
				}
				if group != "" && group != m.Unit {
					return nil, "SALES_GROUP_PENDING"
				}
				group = m.Unit
			}
			parts = append(parts, vipSalesPart{Role: "DCAI_GROUP_LEAD", Person: d.holder("GROUP_LEAD", group, req.At), Rate: p.Group})
		}
	}
	if p.Service > 0 {
		parts = append(parts, vipSalesPart{Role: "DCAI_SERVICE", Person: team.ServiceParty, Rate: p.Service})
	}
	parts = append(parts, vipSalesPart{Role: "DCAI_TEAM_HEAD", Person: team.HeadParty, Rate: p.TeamHead})

	var assignedRate int64
	for i := range parts {
		part := &parts[i]
		part.Name = d.People[part.Person]
		if part.Name == "" || part.Rate < 0 || part.Rate > rate {
			return nil, "SALES_TEAM_PENDING"
		}
		assignedRate += part.Rate
	}
	if assignedRate != rate {
		return nil, "SALES_RATE_CONFLICT"
	}
	distributeVIPSales(parts, amount, rate, req.Order)
	return parts, ""
}

// Distribute the rounded transaction total; ties are stable for an order and role.
func distributeVIPSales(parts []vipSalesPart, total, rate int64, order string) {
	type remainder struct {
		index int
		value int64
		tie   [32]byte
	}
	residuals := make([]remainder, len(parts))
	unassigned := total
	for i := range parts {
		p := &parts[i]
		product := (total % rate) * p.Rate
		p.Amount = (total/rate)*p.Rate + product/rate
		unassigned -= p.Amount
		residuals[i] = remainder{i, product % rate, sha256.Sum256([]byte("DCAI:" + order + ":" + p.Role))}
	}
	slices.SortFunc(residuals, func(a, b remainder) int {
		if a.value != b.value {
			return int(b.value - a.value)
		}
		return bytes.Compare(a.tie[:], b.tie[:])
	})
	for _, r := range residuals[:unassigned] {
		parts[r.index].Amount++
	}
}

type vipClaim struct {
	Role         string `json:"role"`
	User         string `json:"user,omitempty"`
	Company      string `json:"company,omitempty"`
	Relationship string `json:"relationship,omitempty"`
}

func vipSourceClaims(participants []sourceAttributionParticipant) []vipClaim {
	claims := make([]vipClaim, 0, 3)
	for _, p := range participants {
		switch p.Role {
		case "PID_USER", "LOCK_SALES_USER":
			claims = append(claims, vipClaim{p.Role, p.UserID, p.OrganizationID, p.Relationship})
		case "SALES_COMPANY":
			claims = append(claims, vipClaim{Role: p.Role, Company: p.CompanyID})
		}
	}
	slices.SortFunc(claims, func(a, b vipClaim) int {
		if a.Role != b.Role {
			return strings.Compare(a.Role, b.Role)
		}
		if a.User != b.User {
			return strings.Compare(a.User, b.User)
		}
		if a.Company != b.Company {
			return strings.Compare(a.Company, b.Company)
		}
		return strings.Compare(a.Relationship, b.Relationship)
	})
	return claims
}

func vipSellerClaim(claims []vipClaim) (vipClaim, string) {
	var pid, lock, company vipClaim
	for _, c := range claims {
		var slot *vipClaim
		switch c.Role {
		case "PID_USER":
			slot = &pid
		case "LOCK_SALES_USER":
			slot = &lock
		case "SALES_COMPANY":
			slot = &company
		default:
			continue
		}
		if slot.Role != "" {
			return vipClaim{}, "SALES_SOURCE_CONFLICT"
		}
		*slot = c
	}
	switch {
	case pid.Role == "" && lock.Role == "":
		return vipClaim{}, "SALES_PERSON_MISSING"
	case pid.Role == "":
		return lock, ""
	case lock.Role == "":
		return pid, ""
	case pid.User == "" || pid.User != lock.User || pid.Company == "" || lock.Company == "" ||
		(pid.Company != lock.Company && company.Company != pid.Company) ||
		(pid.Relationship != "" && lock.Relationship != "" && pid.Relationship != lock.Relationship):
		return vipClaim{}, "SALES_SOURCE_CONFLICT"
	default:
		return pid, ""
	}
}
