package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type practiceSalesOrganization struct {
	PersonID          int64                        `json:"personId"`
	SourceCompany     string                       `json:"sourceCompany,omitempty"`
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
		if benefit := part.PeriodBenefit; benefit != nil && len(benefit.SalesUnits) != 0 {
			roles[part.SourceRole] = true
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
			entry.Department, entry.Group = resolved.Department.String(), resolved.groupID().String()
			entry.ScopeAlternatives = resolved.scopeAlternatives
			byRole[role] = entry
		}
		if len(byRole) == 0 {
			delete(candidates, payment.ID)
		}
	}
	return candidates, nil
}
