package main

import (
	"maps"
	"slices"
)

type practicePeriodStandardRate struct {
	SalesCategoryIDs []string `json:"salesCategoryIds,omitempty"`
	SalesCompanies   []string `json:"salesCompanies,omitempty"`
	Value            int64    `json:"value"`
}

type practicePeriodStandardRates struct {
	companies  map[string]int64
	categories map[string]int64
	fallback   *int64
}

func indexPracticeStandardRates(rows []practicePeriodStandardRate) (practicePeriodStandardRates, bool) {
	index := practicePeriodStandardRates{companies: make(map[string]int64), categories: make(map[string]int64)}
	for _, row := range rows {
		if row.Value < 0 || row.Value > periodRateScale || len(row.SalesCategoryIDs) != 0 && len(row.SalesCompanies) != 0 {
			return index, false
		}
		switch {
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
		case len(row.SalesCompanies) != 0:
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

func (s practicePeriodStandardRates) match(payment paymentInput, noSales bool, runtime practiceDraftRuntime) (int64, string) {
	if !noSales {
		company := payment.Fact.SalesCompanyID
		if company == "" && len(s.companies) != 0 {
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
