package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type practiceSaleScope struct {
	OwnSales         bool     `json:"ownSales,omitempty"`
	SalesCategoryIDs []string `json:"salesCategoryIds,omitempty"`
	SalesUnits       []string `json:"salesUnits,omitempty"`
}

func (s practiceSaleScope) validFor(part component) bool {
	if (part.Type != "sales" && part.Type != "referral") || (part.Mode != "rate" && part.Mode != "fixed") ||
		part.SourceRole == "" || len(part.retrainSplits()) != 0 {
		return false
	}
	if s.OwnSales {
		return part.SalesPersonID > 0 && len(s.SalesCategoryIDs)+len(s.SalesUnits) == 0
	}
	return len(s.SalesCategoryIDs)+len(s.SalesUnits) != 0 && validUniqueScopeIDs(s.SalesCategoryIDs) && validUniqueScopeIDs(s.SalesUnits)
}

func (p practicePurchase) scopedSale(part component, identities map[identityKey][]identityAssignment,
	runtime practiceDraftRuntime) (practicePurchase, string) {
	scope := part.SaleScope
	if scope == nil {
		return p, ""
	}
	filter := practicePeriodBenefit{PersonID: part.SalesPersonID, OwnSales: scope.OwnSales,
		SalesCategoryIDs: scope.SalesCategoryIDs, SalesUnits: scope.SalesUnits}
	selected := p
	selected.payments, selected.base, selected.gross = nil, 0, 0
	for _, payment := range p.payments {
		matched, issue := filter.eligiblePayment(payment, part.SourceRole, nil, identities, runtime)
		if issue != "" {
			return practicePurchase{}, issue
		}
		if matched {
			selected.payments = append(selected.payments, payment)
			selected.base += p.netAmount(payment)
			selected.gross += *payment.Fact.AmountMinor
		}
	}
	return selected, ""
}

type practiceSalesOrganization struct {
	PersonID          int64                        `json:"personId"`
	SourceCompany     string                       `json:"sourceCompany,omitempty"`
	Company           string                       `json:"company,omitempty"`
	Department        string                       `json:"department,omitempty"`
	Group             string                       `json:"group,omitempty"`
	ScopeAlternatives []organizationScopeCandidate `json:"scopeAlternatives,omitempty"`
}

// The payment version and its source role identify the attribution used by
// the rule. A correction changing either the seller or the source company
// cannot reuse the original organizational attribution.
type practiceSalesOrganizations map[int64]map[string]practiceSalesOrganization

func loadPracticeSalesOrganizations(ctx context.Context, tx pgx.Tx, rule paymentRule, members []practiceEnrollment,
	orders map[string][]paymentInput, identities map[identityKey][]identityAssignment) (practiceSalesOrganizations, error) {
	roles := make(map[string]bool)
	for _, part := range rule.Components {
		if scope := part.SaleScope; scope != nil && len(scope.SalesUnits) != 0 {
			roles[part.SourceRole] = true
		}
		if benefit := part.PeriodBenefit; benefit != nil && (len(benefit.SalesUnits) != 0 || benefit.OrderAppointmentLevel != "") {
			roles[part.SourceRole] = true
		}
		for _, split := range part.retrainSplits() {
			if len(split.ServiceSalesUnits) != 0 {
				roles[part.SourceRole] = true
			}
		}
	}
	if len(roles) == 0 {
		return nil, nil
	}

	candidates := make(practiceSalesOrganizations)
	seenPayments := make(map[int64]bool)
	seenPeople := make(map[int64]bool)
	var people []int64
	var payments []paymentInput
	for _, member := range members {
		if !member.CommissionEligible {
			continue
		}
		for _, order := range member.PaidOrderIDs {
			for _, payment := range orders[order] {
				if seenPayments[payment.ID] {
					continue
				}
				seenPayments[payment.ID] = true
				payments = append(payments, payment)
				for role := range roles {
					person, issue := paymentBeneficiary(payment.Fact, role, identities)
					if issue != "" {
						continue
					}
					if !seenPeople[person.PersonID] {
						people = append(people, person.PersonID)
						seenPeople[person.PersonID] = true
					}
					if candidates[payment.ID] == nil {
						candidates[payment.ID] = make(map[string]practiceSalesOrganization)
					}
					candidates[payment.ID][role] = practiceSalesOrganization{PersonID: person.PersonID, SourceCompany: payment.Fact.SalesCompanyID}
				}
			}
		}
	}
	if len(people) == 0 {
		return nil, nil
	}
	index, err := loadReportAttribution(ctx, tx, people, payments)
	if err != nil {
		return nil, err
	}
	for _, payment := range payments {
		byRole := candidates[payment.ID]
		for role, entry := range byRole {
			company := identityKey{Source: payment.Fact.Source, ExternalID: entry.SourceCompany}
			resolved := index.resolve(entry.PersonID, payment.Fact.OccurredAt, company)
			if resolved.present() == nil && len(resolved.scopeAlternatives) == 0 || entry.SourceCompany != "" && !resolved.Company.Valid {
				delete(byRole, role)
				continue
			}
			entry.Company, entry.Department, entry.Group = resolved.Company.String(), resolved.Department.String(), resolved.groupID().String()
			entry.ScopeAlternatives = resolved.scopeAlternatives
			byRole[role] = entry
		}
		if len(byRole) == 0 {
			delete(candidates, payment.ID)
		}
	}
	return candidates, nil
}
