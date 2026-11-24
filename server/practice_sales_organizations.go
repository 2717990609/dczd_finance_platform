package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type practiceSaleScope struct {
	Kind              string                         `json:"kind,omitempty"`
	OwnSales          bool                           `json:"ownSales,omitempty"`
	SalesCategoryIDs  []string                       `json:"salesCategoryIds,omitempty"`
	SalesUnits        []string                       `json:"salesUnits,omitempty"`
	CompanyExceptions []practiceSaleCompanyException `json:"companyExceptions,omitempty"`
	PersonExceptions  []practiceSalePersonException  `json:"personExceptions,omitempty"`
}

type practiceSaleTerms struct {
	Mode  string `json:"mode"`
	Value int64  `json:"value"`
}

func (t practiceSaleTerms) valid() bool {
	return t.Value >= 0 && (t.Mode == "rate" && t.Value <= 10000 || t.Mode == "fixed" && t.Value <= 100000000000)
}

type practiceSaleCompanyException struct {
	CompanyID string `json:"companyId"`
	practiceSaleTerms
}

type practiceSalePersonException struct {
	PersonID int64 `json:"personId"`
	practiceSaleTerms
}

func (s *practiceSaleScope) hasExceptions() bool {
	return s != nil && len(s.CompanyExceptions)+len(s.PersonExceptions) != 0
}

func (s practiceSaleScope) validFor(part component) bool {
	tiered := part.Mode == "tiered" && part.Tiers != nil
	if (part.Type != "sales" && part.Type != "referral") || (part.Mode != "rate" && part.Mode != "fixed" && !tiered) ||
		part.SourceRole == "" || len(part.retrainSplits()) != 0 ||
		s.hasExceptions() && (s.Kind != "category" || tiered) {
		return false
	}
	categories, units := len(s.SalesCategoryIDs), len(s.SalesUnits)
	if s.OwnSales {
		return s.Kind == "" && part.SalesPersonID > 0 && categories+units == 0
	}
	companies := len(part.salesCompanies())
	switch s.Kind {
	case "company":
		return companies > 0 && categories+units == 0
	case "category":
		if categories == 0 || companies+units != 0 {
			return false
		}
	case "organization":
		if units == 0 || categories != 0 {
			return false
		}
	case "":
		// Legacy versions may contain an intersection of several filters.
	default:
		return false
	}
	if tiered && part.Tiers.OpeningID != 0 || categories+units == 0 ||
		!validUniqueScopeIDs(s.SalesCategoryIDs) || !validUniqueScopeIDs(s.SalesUnits) {
		return false
	}
	seenCompanies := make(map[string]bool, len(s.CompanyExceptions))
	for _, exception := range s.CompanyExceptions {
		if !textOK(exception.CompanyID, 128) || seenCompanies[exception.CompanyID] || !exception.valid() {
			return false
		}
		seenCompanies[exception.CompanyID] = true
	}
	seenPeople := make(map[int64]bool, len(s.PersonExceptions))
	for _, exception := range s.PersonExceptions {
		if exception.PersonID <= 0 || seenPeople[exception.PersonID] || !exception.valid() {
			return false
		}
		seenPeople[exception.PersonID] = true
	}
	return true
}

func (s practiceSaleScope) termsFor(part component, person int64, payment paymentInput) (practiceSaleTerms, string) {
	for _, exception := range s.PersonExceptions {
		if exception.PersonID == person {
			return exception.practiceSaleTerms, ""
		}
	}
	if len(s.CompanyExceptions) != 0 && payment.Fact.SalesCompanyID == "" {
		return practiceSaleTerms{}, "PRACTICE_TIER_COMPANY_MISSING"
	}
	for _, exception := range s.CompanyExceptions {
		if exception.CompanyID == payment.Fact.SalesCompanyID {
			return exception.practiceSaleTerms, ""
		}
	}
	return practiceSaleTerms{Mode: part.Mode, Value: part.Value}, ""
}

func (p practicePurchase) saleTerms(sale practicePurchase, part component, seller int64, identities map[identityKey][]identityAssignment,
	runtime practiceDraftRuntime) (practiceSaleTerms, string) {
	if len(p.originalPayments) != 0 {
		p.payments = p.originalPayments
		original, issue := p.scopedSale(part, identities, runtime)
		if issue != "" {
			return practiceSaleTerms{}, issue
		}
		sale = original
		// Replay can drop a fully refunded payment. Its attribution still
		// determines the original standard for the enrollment.
		assignments, issue := practiceBeneficiaries(sale.payments, part.SourceRole, identities)
		if issue != "" {
			return practiceSaleTerms{}, issue
		}
		seller = assignments[0].PersonID
	}
	scope := part.SaleScope
	terms, issue := scope.termsFor(part, seller, sale.payments[0])
	if issue != "" {
		return practiceSaleTerms{}, issue
	}
	for _, payment := range sale.payments[1:] {
		other, issue := scope.termsFor(part, seller, payment)
		if issue != "" {
			return practiceSaleTerms{}, issue
		}
		if other != terms {
			return practiceSaleTerms{}, "PRACTICE_SALE_STANDARD_CONFLICT"
		}
	}
	return terms, ""
}

func (p practicePurchase) scopedSale(part component, identities map[identityKey][]identityAssignment,
	runtime practiceDraftRuntime) (practicePurchase, string) {
	scope := part.SaleScope
	if scope == nil {
		return p, ""
	}
	filter := practicePeriodBenefit{PersonID: part.SalesPersonID, OwnSales: scope.OwnSales,
		SalesCategoryIDs: scope.SalesCategoryIDs, SalesUnits: scope.SalesUnits}
	if scope.Kind == "organization" && len(part.salesCompanies()) != 0 {
		filter.SalesCompanies, filter.JoinSalesOrganizations = part.salesCompanies(), true
	}
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
	history practiceTierHistory, orders map[string][]paymentInput, identities map[identityKey][]identityAssignment) (practiceSalesOrganizations, error) {
	roles := make(map[string]bool)
	loadPrior := false
	for _, part := range rule.Components {
		if part.CompanySales.usesOrganizationStandards() {
			roles[part.SourceRole] = true
		}
		if scope := part.SaleScope; scope != nil && len(scope.SalesUnits) != 0 {
			roles[part.SourceRole] = true
			loadPrior = loadPrior || part.Tiers != nil && part.Tiers.usesPriorTiers()
		}
		if benefit := part.PeriodBenefit; benefit != nil && (len(benefit.SalesUnits) != 0 || benefit.hasOrganizationRates() || benefit.OrderAppointmentLevel != "") {
			roles[part.SourceRole] = true
		}
		for _, split := range part.retrainSplits() {
			if len(split.ServiceSalesUnits) != 0 {
				roles[part.SourceRole] = true
			}
		}
	}
	for _, period := range history.periods {
		if period.companyOffers != nil {
			for _, byRole := range period.companyOffers.organizationStandards {
				for role := range byRole {
					roles[role] = true
					loadPrior = true
				}
			}
		}
	}
	if len(roles) == 0 {
		return nil, nil
	}
	if loadPrior {
		for _, period := range history.periods {
			for _, role := range []string{period.personalRole, period.retrainRole} {
				if role != "" {
					roles[role] = true
				}
			}
		}
		members = append(members[:len(members):len(members)], history.members...)
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

func validPracticeOrganizationAt(organization practiceSalesOrganization, payment paymentInput, seller int64) bool {
	return organization.PersonID == seller && organization.SourceCompany == payment.Fact.SalesCompanyID
}

// Prefer the closest configured organization. Ambiguous affiliations are
// usable only when every candidate would select the same standard.
func matchPracticeOrganizationStandard[T comparable](standards map[string]T, org practiceSalesOrganization) (T, int, bool) {
	match := func(department, group string) (T, int) {
		for i, id := range [...]string{group, department, org.Company} {
			if value, found := standards[id]; found {
				return value, 3 - i
			}
		}
		var unmatched T
		return unmatched, 0
	}
	if len(org.ScopeAlternatives) == 0 {
		value, rank := match(org.Department, org.Group)
		return value, rank, false
	}
	first := org.ScopeAlternatives[0]
	value, rank := match(first.Department, first.Group)
	for _, alternative := range org.ScopeAlternatives[1:] {
		next, otherRank := match(alternative.Department, alternative.Group)
		if (rank == 0) != (otherRank == 0) || next != value {
			return value, 0, true
		}
		rank = min(rank, otherRank)
	}
	return value, rank, false
}
