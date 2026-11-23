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
	Company         string   `json:"sales_organization_party_id,omitempty"`
	CompanyIDs      []string `json:"sales_organization_party_ids,omitempty"`
	Relationship    string   `json:"sales_relationship"`
	Category        string   `json:"sales_category_id,omitempty"`
	CategoryIDs     []string `json:"sales_category_ids,omitempty"`
	SnapshotDefault bool     `json:"sales_category_snapshot_default"`
	ExternalDefault bool     `json:"external_company_default,omitempty"`
	Rate            int64    `json:"rate_bps"`
	Payee           string   `json:"sales_payee,omitempty"`
}

const vipPayeeSalesCompany = "sales_company"

func (r vipRateRow) companies() []string {
	if len(r.CompanyIDs) != 0 {
		return r.CompanyIDs
	}
	return []string{r.Company}
}

func (r vipRateRow) categories() []string {
	if len(r.CategoryIDs) != 0 {
		return r.CategoryIDs
	}
	// An empty category is the unrestricted group in the rate index.
	return []string{r.Category}
}

func (r vipRateRow) hasCategory() bool {
	return r.Category != "" || len(r.CategoryIDs) != 0
}

func (r vipRateRow) namesCompany(id string) bool {
	return r.Company == id || slices.Contains(r.CompanyIDs, id)
}

func validVIPRateGroup(single string, grouped []string) bool {
	if len(grouped) != 0 && single != "" {
		return false
	}
	for i, id := range grouped {
		if id == "" || id == "ANY" || slices.Contains(grouped[:i], id) {
			return false
		}
	}
	return true
}

func (r vipRateRow) validScope() bool {
	return (r.Company != "" || len(r.CompanyIDs) != 0) && validVIPRateGroup(r.Company, r.CompanyIDs) &&
		validVIPRateGroup(r.Category, r.CategoryIDs)
}

func (r vipRateRow) validExternalDefault() bool {
	return !r.ExternalDefault || r.Company == "ANY" && len(r.CompanyIDs) == 0 && !r.hasCategory() &&
		!r.SnapshotDefault && r.Relationship == "ANY" && r.Payee == vipPayeeSalesCompany
}

type vipInternalTeam struct {
	Company      string   `json:"sales_company_party_id"`
	TeamUnit     string   `json:"team_unit_id"`
	HeadParty    string   `json:"head_party_id"`
	ServiceParty string   `json:"service_party_id"`
	GroupUnits   []string `json:"seller_group_unit_ids"`
}

type vipInternalSplit struct {
	Mode                              string            `json:"mode"`
	Item                              string            `json:"sales_item_key"`
	Total                             int64             `json:"total_rate_bps"`
	Assistant                         int64             `json:"assistant_seller_rate_bps"`
	HeadSeller                        int64             `json:"head_seller_rate_bps"`
	Group                             int64             `json:"group_lead_rate_bps"`
	Service                           int64             `json:"service_rate_bps"`
	ServiceLabel                      string            `json:"service_label,omitempty"`
	TeamHead                          int64             `json:"team_head_rate_bps"`
	Teams                             []vipInternalTeam `json:"teams"`
	LeaderLabels                      map[string]string `json:"leader_labels,omitempty"`
	DisabledShares                    []string          `json:"disabled_shares,omitempty"`
	MembershipScopes                  map[string]bool   `json:"vip_membership_scopes,omitempty"`
	independentGroup, independentTeam bool
	Reviews                           []struct {
		Order   string `json:"source_order_id"`
		Seller  string `json:"seller_party_id"`
		Company string `json:"sales_company_party_id"`
	} `json:"order_company_reviews"`
}

type vipSalesPolicy struct {
	Item              string
	Sales             []vipRateRow
	Split             vipInternalSplit
	CommissionUnits   []string
	ScopeCategories   []string
	MembershipNewOnly *bool
}

type vipCommissionDefinition struct {
	Basis       string `json:"amount_basis"`
	Stacking    string `json:"stacking_policy"`
	Attribution string `json:"sales_attribution"`
	Rounding    struct {
		Mode  string
		Scale int
	} `json:"rounding_policy"`
	Split   *vipInternalSplit `json:"internal_sales_direct_split"`
	Actions []json.RawMessage `json:"benefit_actions"`
}

func readVIPCommissionDefinition(raw json.RawMessage) (vipCommissionDefinition, bool) {
	var def vipCommissionDefinition
	if json.Unmarshal(raw, &def) != nil || def.Basis != "ACTUAL_RECEIVED" || def.Stacking != "SUM" ||
		def.Rounding.Mode != "HALF_UP" || def.Rounding.Scale != 2 ||
		def.Split != nil && (def.Split.Mode != "DCAI_INTERNAL_DIRECT" || def.Split.Item == "") {
		return vipCommissionDefinition{}, false
	}
	return def, true
}

func readVIPSalesPolicy(raw json.RawMessage) (vipSalesPolicy, bool) {
	def, ok := readVIPCommissionDefinition(raw)
	if !ok {
		return vipSalesPolicy{}, false
	}
	if def.Split == nil {
		return def.directSalesPolicy()
	}
	type salesAction struct {
		Type              string            `json:"type"`
		Mode              string            `json:"beneficiary_mode"`
		Role              string            `json:"role"`
		Item              string            `json:"item_key"`
		Designated        string            `json:"designated_party_id"`
		RateTable         []json.RawMessage `json:"rate_table"`
		CommissionUnits   []string          `json:"sales_scope_unit_ids"`
		ScopeCategories   []string          `json:"sales_scope_category_ids"`
		MembershipNewOnly *bool             `json:"vip_membership_new_only"`
	}
	policy := vipSalesPolicy{Split: *def.Split}
	for _, payload := range def.Actions {
		var action salesAction
		if json.Unmarshal(payload, &action) != nil {
			return vipSalesPolicy{}, false
		}
		if action.Item != def.Split.Item {
			switch action.Item {
			case vipGroupLeaderItem:
				policy.Split.independentGroup = true
			case vipTeamLeaderItem:
				policy.Split.independentTeam = true
			}
			continue
		}
		if policy.Item != "" || action.Type != "RATE_TABLE" || action.Mode != "PARTY" || action.Role != "SALES" || action.Designated != "" {
			return vipSalesPolicy{}, false
		}
		if !validUniqueScopeIDs(action.CommissionUnits) || !validUniqueScopeIDs(action.ScopeCategories) {
			return vipSalesPolicy{}, false
		}
		policy.Item, policy.CommissionUnits = action.Item, action.CommissionUnits
		policy.ScopeCategories = action.ScopeCategories
		policy.MembershipNewOnly = action.MembershipNewOnly
		policy.Sales, ok = parseVIPRateTable(action.RateTable)
		if !ok {
			return vipSalesPolicy{}, false
		}
	}
	return policy, policy.Item != ""
}

func (d vipCommissionDefinition) directSalesPolicy() (vipSalesPolicy, bool) {
	items := make(map[string]bool, len(d.Actions))
	var policy vipSalesPolicy
	for _, payload := range d.Actions {
		var identity struct {
			Item       string `json:"item_key"`
			Role       string `json:"role"`
			Mode       string `json:"beneficiary_mode"`
			Designated string `json:"designated_party_id"`
			Company    string `json:"company_beneficiary_party_id"`
		}
		if json.Unmarshal(payload, &identity) != nil || identity.Item == "" || items[identity.Item] {
			return vipSalesPolicy{}, false
		}
		items[identity.Item] = true
		if identity.Role != "SALES" || identity.Designated != "" || identity.Company != "" || identity.Mode == "TEAM_POOL" {
			continue
		}
		var action struct {
			Item              string            `json:"item_key"`
			Role              string            `json:"role"`
			Label             string            `json:"label"`
			Type              string            `json:"type"`
			Mode              string            `json:"beneficiary_mode"`
			RateTable         []json.RawMessage `json:"rate_table"`
			CommissionUnits   []string          `json:"sales_scope_unit_ids"`
			ScopeCategories   []string          `json:"sales_scope_category_ids"`
			MembershipNewOnly *bool             `json:"vip_membership_new_only"`
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&action) != nil || policy.Item != "" || action.Type != "RATE_TABLE" || action.Mode != "PARTY" ||
			!validUniqueScopeIDs(action.CommissionUnits) || !validUniqueScopeIDs(action.ScopeCategories) {
			return vipSalesPolicy{}, false
		}
		rates, ok := parseVIPRateTable(action.RateTable)
		if !ok {
			return vipSalesPolicy{}, false
		}
		policy = vipSalesPolicy{Item: action.Item, Sales: rates, CommissionUnits: action.CommissionUnits,
			ScopeCategories: action.ScopeCategories, MembershipNewOnly: action.MembershipNewOnly}
	}
	return policy, policy.Item != "" || d.Attribution == "ORDER_SELLER" && len(d.Actions) != 0
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
			!row.validScope() || !row.validExternalDefault() || row.Rate < 0 || row.Rate > 10000 || row.Payee != "" && row.Payee != vipPayeeSalesCompany ||
			!slices.Contains([]string{"ANY", "DIRECT", "INDIRECT", "NONE"}, row.Relationship) {
			return nil, false
		}
		for key := range fields {
			switch key {
			case "id", "rate_bps", "sales_relationship", "sales_organization_party_id", "sales_organization_party_ids",
				"sales_category_id", "sales_category_ids", "sales_category_snapshot_default", "external_company_default", "sales_payee":
			default:
				return nil, false
			}
		}
		rows = append(rows, row)
	}
	return rows, true
}

func resolveVIPRate(rows []vipRateRow, company, relationship string, category *string) (int64, string) {
	row, issue := selectVIPRate(rows, company, relationship, category)
	return row.Rate, issue
}

func selectVIPRate(rows []vipRateRow, company, relationship string, category *string) (vipRateRow, string) {
	return selectVIPRateForCompany(rows, company, relationship, category, "")
}

func selectVIPRateForCompany(rows []vipRateRow, company, relationship string, category *string, companyType string) (vipRateRow, string) {
	categorized := slices.ContainsFunc(rows, vipRateRow.hasCategory)
	return resolveVIPRateRows(rows, company, relationship, category, categorized, companyType)
}

func resolveVIPRateRows(rows []vipRateRow, company, relationship string, category *string, categorized bool, companyType string) (vipRateRow, string) {
	if company == "" {
		return vipRateRow{}, "SALES_COMPANY_MISSING"
	}
	if relationship != "" && relationship != "DIRECT" && relationship != "INDIRECT" && relationship != "NONE" {
		return vipRateRow{}, "SALES_RELATIONSHIP_MISSING"
	}
	const externalDefaultTier = 3
	var groups [5][]vipRateRow
	for _, row := range rows {
		if row.Company != "ANY" && !row.namesCompany(company) || row.Relationship != "ANY" && relationship != "" && row.Relationship != relationship ||
			row.hasCategory() && (category == nil || row.Category != *category && !slices.Contains(row.CategoryIDs, *category)) {
			continue
		}
		tier := 4
		switch {
		case row.ExternalDefault:
			if companyType == "INTERNAL" {
				continue
			}
			tier = externalDefaultTier
		case row.Company != "ANY" && !row.SnapshotDefault:
			tier = 0
		case row.Company == "ANY" && row.hasCategory():
			tier = 1
		case row.Company != "ANY":
			tier = 2
		}
		groups[tier] = append(groups[tier], row)
	}
	if categorized && category == nil && len(groups[0])+len(groups[2])+len(groups[externalDefaultTier]) == 0 {
		return vipRateRow{}, "SALES_CATEGORY_MISSING"
	}
	for tier, choices := range groups {
		if len(choices) == 0 {
			continue
		}
		if tier == externalDefaultTier && companyType != "EXTERNAL" {
			return vipRateRow{}, "SALES_COMPANY_TYPE_MISSING"
		}
		best := -1
		var winner vipRateRow
		tie := false
		for _, row := range choices {
			if relationship == "" && row.Relationship != "ANY" {
				return vipRateRow{}, "SALES_RELATIONSHIP_MISSING"
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
				best, winner, tie = score, row, false
			case score == best:
				tie = true
			}
		}
		if tie || winner.Rate < 0 || winner.Rate > 10000 {
			return vipRateRow{}, "SALES_RATE_CONFLICT"
		}
		return winner, ""
	}
	return vipRateRow{}, "SALES_RATE_MISSING"
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
	Person, Unit, Kind, UnitKind string
	vipInterval
}

type vipSalesData struct {
	Roles       []vipRole
	Memberships []vipMembership
	People      map[string]string
}

type vipSalesRequest struct {
	Order, Product, Project, Seller, Company, Relationship string
	CompanyUnit, CompanyName, CompanyType                  string
	Category                                               *string // Empty when explicitly unclassified, nil without a history record.
	Paid, OriginalPaid                                     int64
	At                                                     time.Time
	Referrer                                               vipOrderReferrer
	commissionSalesOrg                                     *reportOrganization
}

type vipSalesPart struct {
	Role        string `json:"role"`
	Person      string `json:"-"`
	Name        string `json:"personName"`
	Rate        int64  `json:"rate"`
	Amount      int64  `json:"amountMinor"`
	CompanyUnit string `json:"-"`
	CompanyName string `json:"companyName,omitempty"`
	Status      string `json:"status,omitempty"`
}

const vipSalesAppointmentInactive = "SALES_APPOINTMENT_INACTIVE"

const vipSalesPrimaryRole = "SALES"

// These are the registered arrangements backed by the same rounded allocation.
var vipSplitRoles = [...]struct{ item, role string }{
	{vipGroupLeaderItem, "DCAI_GROUP_LEAD"},
	{vipInternalServiceItem, "DCAI_SERVICE"},
	{vipTeamLeaderItem, "DCAI_TEAM_HEAD"},
}

func (p vipInternalSplit) membershipOverride(item string) *bool {
	if override, found := p.MembershipScopes[item]; found {
		return &override
	}
	return nil
}

func (p vipInternalSplit) hasShare(item string) bool {
	if len(p.Teams) == 0 || slices.Contains(p.DisabledShares, item) {
		return false
	}
	switch item {
	case vipGroupLeaderItem:
		return !p.independentGroup
	case vipInternalServiceItem:
		return p.Service > 0
	case vipTeamLeaderItem:
		return !p.independentTeam
	}
	return false
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

func (d vipSalesData) hasMatchingSalesCompanyMembership(req vipSalesRequest) bool {
	if req.CompanyUnit == "" {
		return false
	}
	var unit string
	for _, membership := range d.Memberships {
		if membership.Person != req.Seller || membership.UnitKind != "COMPANY" || !membership.contains(req.At) {
			continue
		}
		switch membership.Kind {
		case "MEMBER", "MANAGER", "OWNER":
			if unit != "" {
				return false
			}
			unit = membership.Unit
		}
	}
	return unit == req.CompanyUnit
}

func (d vipSalesData) hasSalesAppointment(req vipSalesRequest) bool {
	return slices.ContainsFunc(d.Roles, func(a vipRole) bool {
		return a.Role == "SALES" && a.Person == req.Seller && a.contains(req.At) &&
			(a.Scope == "PRODUCT" && a.Key == req.Product || a.Scope == "PROJECT" && a.Key == req.Project)
	})
}

func calculateVIPSales(policy vipSalesPolicy, req vipSalesRequest, d vipSalesData) ([]vipSalesPart, string) {
	return calculateVIPSalesWithMembership(policy, req, d, nil)
}

func calculateVIPSalesWithMembership(policy vipSalesPolicy, req vipSalesRequest, d vipSalesData, membership map[string]string) ([]vipSalesPart, string) {
	if req.Order == "" || req.Paid < 0 {
		return nil, "INVALID_PAYMENT"
	}
	for _, review := range policy.Split.Reviews {
		if req.Order == review.Order && (req.Company != review.Company || req.Seller != review.Seller) {
			return nil, "SALES_COMPANY_REVIEW"
		}
	}
	standard, issue := selectVIPRateForCompany(policy.Sales, req.Company, req.Relationship, req.Category, req.CompanyType)
	rate := standard.Rate
	sellerName := d.People[req.Seller]
	if issue == "" && rate == 0 {
		// A known zero rate has no payout to qualify or split.
		if req.Seller == "" || sellerName == "" {
			return nil, ""
		}
		return []vipSalesPart{{Role: "SALES", Person: req.Seller, Name: sellerName}}, ""
	}
	if req.Seller == "" || sellerName == "" {
		return nil, "SALES_PERSON_MISSING"
	}
	if !d.hasSalesAppointment(req) {
		if issue == "" && d.hasMatchingSalesCompanyMembership(req) {
			return nil, vipSalesAppointmentInactive
		}
		return nil, "SALES_APPOINTMENT_MISSING"
	}
	if issue != "" {
		return nil, issue
	}
	amount := calculatePaymentAmount(req.Paid, component{Mode: "rate", Value: rate})
	parts := []vipSalesPart{{Role: "SALES", Person: req.Seller, Name: sellerName, Rate: rate, Amount: amount}}
	teamIdx := slices.IndexFunc(policy.Split.Teams, func(t vipInternalTeam) bool { return t.Company == req.Company })
	if standard.Payee == vipPayeeSalesCompany {
		if teamIdx >= 0 {
			return nil, "SALES_TEAM_PENDING"
		}
		if req.CompanyUnit == "" || req.CompanyName == "" {
			return nil, "SALES_COMPANY_MISSING"
		}
		parts[0].CompanyUnit, parts[0].CompanyName = req.CompanyUnit, req.CompanyName
	}
	if teamIdx < 0 {
		return parts, ""
	}
	p, team := policy.Split, policy.Split.Teams[teamIdx]
	replaceGroup := !p.hasShare(vipGroupLeaderItem)
	replaceTeam := !p.hasShare(vipTeamLeaderItem)
	removeService := !p.hasShare(vipInternalServiceItem)
	groupExcluded := membership["DCAI_GROUP_LEAD"] != ""
	teamExcluded := membership["DCAI_TEAM_HEAD"] != ""
	head := d.holder("TEAM_LEAD", team.TeamUnit, req.At)
	needsHead := !replaceTeam && !teamExcluded || p.HeadSeller != p.Assistant
	if rate != p.Total || head == "" && needsHead || !replaceTeam && !teamExcluded && head != team.HeadParty {
		return nil, "SALES_TEAM_PENDING"
	}

	parts[0].Rate = p.HeadSeller
	if req.Seller != head {
		parts[0].Rate = p.Assistant
		if p.Group > 0 {
			part := vipSalesPart{Role: "DCAI_GROUP_LEAD", Rate: p.Group}
			if !replaceGroup && !groupExcluded {
				group, conflict := vipSellerGroup(team, req, d.Memberships)
				if conflict {
					return nil, "SALES_GROUP_PENDING"
				}
				part.Person = d.holder("GROUP_LEAD", group, req.At)
			}
			parts = append(parts, part)
		}
	}
	if p.Service > 0 {
		parts = append(parts, vipSalesPart{Role: "DCAI_SERVICE", Person: team.ServiceParty, Rate: p.Service})
	}
	parts = append(parts, vipSalesPart{Role: "DCAI_TEAM_HEAD", Person: head, Rate: p.TeamHead})

	replaced := func(part vipSalesPart) bool {
		return part.Role == "DCAI_GROUP_LEAD" && replaceGroup || part.Role == "DCAI_TEAM_HEAD" && replaceTeam ||
			part.Role == "DCAI_SERVICE" && removeService
	}
	// Allocate against the original shares so removing a payee does not
	// redistribute any cents to the other payees.
	var assignedRate int64
	for i := range parts {
		part := &parts[i]
		part.Name = d.People[part.Person]
		part.Status = membership[part.Role]
		if part.Name == "" && part.Status == "" && !replaced(*part) || part.Rate < 0 || part.Rate > rate {
			return nil, "SALES_TEAM_PENDING"
		}
		assignedRate += part.Rate
	}
	if assignedRate != rate {
		return nil, "SALES_RATE_CONFLICT"
	}
	distributeVIPSales(parts, amount, rate, req.Order)
	for i := range parts {
		part := &parts[i]
		if part.Status != "" {
			if part.Amount == 0 {
				part.Status = vipCommissionOutsideScope
			}
			// Keep the slot and original share so posting keys and other cents stay stable.
			part.Person, part.Name, part.Amount = "", "", 0
		}
	}
	return slices.DeleteFunc(parts, replaced), ""
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
