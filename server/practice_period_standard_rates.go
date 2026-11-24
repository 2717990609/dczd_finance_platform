package main

import (
	"maps"
	"slices"
	"strings"
)

type practicePeriodStandardRate struct {
	SalesCategoryIDs       []string `json:"salesCategoryIds,omitempty"`
	SalesCompanies         []string `json:"salesCompanies,omitempty"`
	SalesUnits             []string `json:"salesUnits,omitempty"`
	ExternalCompanyDefault bool     `json:"externalCompanyDefault,omitempty"`
	Value                  int64    `json:"value"`
}

type practicePeriodStandardRates struct {
	organizations   map[string]int64
	companies       map[string]int64
	categories      map[string]int64
	externalDefault *int64
	fallback        *int64
}

func practiceStandardRatesUseExternalDefault(rule paymentRule) bool {
	return slices.ContainsFunc(rule.Components, func(part component) bool {
		return part.PeriodBenefit != nil && slices.ContainsFunc(part.PeriodBenefit.StandardRates, func(rate practicePeriodStandardRate) bool {
			return rate.ExternalCompanyDefault
		})
	})
}

func (b practicePeriodBenefit) hasOrganizationRates() bool {
	return slices.ContainsFunc(b.StandardRates, func(rate practicePeriodStandardRate) bool { return len(rate.SalesUnits) != 0 })
}

func indexPracticeStandardRates(rows []practicePeriodStandardRate) (practicePeriodStandardRates, bool) {
	index := practicePeriodStandardRates{
		organizations: make(map[string]int64), companies: make(map[string]int64), categories: make(map[string]int64),
	}
	for _, row := range rows {
		if row.Value < 0 || row.Value > periodRateScale || len(row.SalesCategoryIDs) != 0 && len(row.SalesCompanies)+len(row.SalesUnits) != 0 {
			return index, false
		}
		switch {
		case row.ExternalCompanyDefault:
			if index.externalDefault != nil || len(row.SalesCompanies)+len(row.SalesCategoryIDs)+len(row.SalesUnits) != 0 {
				return index, false
			}
			index.externalDefault = &row.Value
		case len(row.SalesCategoryIDs) != 0:
			if !validUniqueScopeIDs(row.SalesCategoryIDs) {
				return index, false
			}
			for _, category := range row.SalesCategoryIDs {
				if _, exists := index.categories[category]; exists {
					return index, false
				}
				index.categories[category] = row.Value
			}
		case len(row.SalesCompanies)+len(row.SalesUnits) != 0:
			if !validUniqueScopeIDs(row.SalesUnits) {
				return index, false
			}
			for _, unit := range row.SalesUnits {
				if _, exists := index.organizations[unit]; exists {
					return index, false
				}
				index.organizations[unit] = row.Value
			}
			for _, company := range row.SalesCompanies {
				if _, exists := index.companies[company]; exists || !textOK(company, 128) {
					return index, false
				}
				index.companies[company] = row.Value
			}
		default:
			if index.fallback != nil {
				return index, false
			}
			index.fallback = &row.Value
		}
	}
	return index, true
}

func (s practicePeriodStandardRates) match(payment paymentInput, noSales bool, runtime practiceDraftRuntime, role string,
	identities map[identityKey][]identityAssignment) (int64, string) {
	if !noSales {
		if len(s.organizations) != 0 {
			seller, issue := paymentBeneficiary(payment.Fact, role, identities)
			if issue != "" {
				return 0, issue
			}
			org := runtime.salesOrganizations[payment.ID][role]
			if !validPracticeOrganizationAt(org, payment, seller.PersonID) {
				return 0, "PRACTICE_SALES_ORGANIZATION_MISSING"
			}
			rate, rank, conflict := matchPracticeOrganizationStandard(s.organizations, org)
			if conflict {
				return 0, "SALES_ORGANIZATION_SCOPE_CONFLICT"
			}
			if rank != 0 {
				return rate, ""
			}
		}
		company := payment.Fact.SalesCompanyID
		if company == "" && (len(s.companies) != 0 || s.externalDefault != nil) {
			return 0, "PRACTICE_TIER_COMPANY_MISSING"
		}
		if rate, ok := s.companies[company]; ok {
			return rate, ""
		}
		if len(s.categories) != 0 {
			category, issue := practicePurchaseCompanyClassification([]paymentInput{payment}, runtime.companyClassifications, runtime.internalSalesCategories)
			if issue != "" {
				return 0, issue
			}
			if rate, ok := s.categories[category]; ok {
				return rate, ""
			}
		}
		if s.externalDefault != nil {
			classification := runtime.companyClassifications[payment.Fact.PaymentID]
			if externalCompanyDefaultEligible(classification) {
				return *s.externalDefault, ""
			}
			if classification == "" || strings.HasPrefix(classification, practiceUnverifiedExternalPrefix) {
				return 0, "PRACTICE_SALES_COMPANY_TYPE_MISSING"
			}
		}
	}
	if s.fallback != nil {
		return *s.fallback, ""
	}
	return 0, ""
}

func allocatePracticeStandardRates(shares []practiceBenefitShare) ([]practiceBenefitShare, int64, error) {
	byRate := make(map[int64][]practiceBenefitShare)
	for _, share := range shares {
		for i, rate := range share.rates {
			byRate[rate] = append(byRate[rate], share.paymentShare(i))
		}
	}
	var result []practiceBenefitShare
	var total int64
	for _, rate := range slices.Sorted(maps.Keys(byRate)) {
		rows := byRate[rate]
		amount, err := allocatePracticePeriodRate(rows, rate)
		if err != nil {
			return nil, 0, err
		}
		total, err = addMinor(total, amount)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, rows...)
	}
	return result, total, nil
}
