package main

import (
	"slices"

	"github.com/jackc/pgx/v5/pgtype"
)

const vipCommissionOutsideScope = "OUTSIDE_COMMISSION_SCOPE"

func validVIPCommissionUnits(units []string) bool {
	for index, unit := range units {
		id, ok := canonicalSourceUUID(unit)
		if !ok || id != unit || slices.Contains(units[:index], unit) {
			return false
		}
	}
	return true
}

func vipCommissionScope(units []string, req vipSalesRequest, sourceIssue string, data vipRuntimeData) (bool, string) {
	if len(units) == 0 {
		return true, ""
	}
	var org reportOrganization
	if req.commissionSalesOrg != nil {
		org = *req.commissionSalesOrg
	}
	var sourceCompany pgtype.UUID
	_ = sourceCompany.Scan(req.CompanyUnit)
	if !sourceCompany.Valid {
		sourceCompany = org.Company
	}
	if sourceCompany.Valid && slices.Contains(units, sourceCompany.String()) {
		return true, ""
	}
	inDirectory := func(match func(reportOrganization) bool) bool {
		return data.CommissionDirectory != nil && slices.ContainsFunc(units, func(id string) bool {
			var unit pgtype.UUID
			return unit.Scan(id) == nil && match(data.CommissionDirectory.ancestors(unit))
		})
	}
	if sourceCompany.Valid && data.CommissionDirectory != nil &&
		!inDirectory(func(unit reportOrganization) bool { return unit.Company == sourceCompany }) {
		return false, ""
	}
	inScope := func(id string) bool {
		if slices.Contains(units, id) {
			return true
		}
		if data.CommissionDirectory == nil || id == "" {
			return false
		}
		var unit pgtype.UUID
		if unit.Scan(id) != nil {
			return false
		}
		directory := data.CommissionDirectory.Units
		for depth := 0; unit.Valid && depth < len(directory); depth++ {
			unit = directory[unit].Parent
			if unit.Valid && slices.Contains(units, unit.String()) {
				return true
			}
		}
		return false
	}
	if inScope(org.Department.String()) || inScope(org.groupID().String()) {
		return true, ""
	}
	inside := 0
	for _, alternative := range org.scopeAlternatives {
		if inScope(alternative.Department) || inScope(alternative.Group) {
			inside++
		}
	}
	switch {
	case inside != 0 && inside != len(org.scopeAlternatives):
		return false, "SALES_ORGANIZATION_SCOPE_CONFLICT"
	case inside != 0:
		return true, ""
	case org.Group != nil || len(org.scopeAlternatives) != 0:
		return false, ""
	case org.Department.Valid && data.CommissionDirectory != nil &&
		!inDirectory(func(unit reportOrganization) bool { return unit.Department == org.Department }):
		return false, ""
	case sourceIssue != "":
		return false, sourceIssue
	default:
		return false, "COMMISSION_SALES_ORGANIZATION_MISSING"
	}
}
