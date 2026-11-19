package main

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type practiceCompanySales struct {
	Categories               []practiceCompanyCategory `json:"categories"`
	InternalCompanies        []string                  `json:"internalCompanies,omitempty"`
	RecommendationRecipients []string                  `json:"recommendationRecipients,omitempty"`
}

type practiceCompanyValue struct {
	Mode  string `json:"mode"`
	Value int64  `json:"value"`
}

type practiceCompanyCategory struct {
	Label              string                    `json:"label"`
	Companies          []string                  `json:"companies"`
	Overrides          []practiceCompanyOverride `json:"overrides,omitempty"`
	RecommendationRate *int64                    `json:"recommendationRate,omitempty"`
	practiceCompanyValue
}

type practiceCompanyOverride struct {
	CompanyID          string `json:"companyId"`
	RecommendationRate *int64 `json:"recommendationRate,omitempty"`
	practiceCompanyValue
}

func (v practiceCompanyValue) valid(member string) bool {
	switch v.Mode {
	case "rate":
		return v.Value >= 0 && v.Value <= periodRateScale
	case "fixed":
		return member == "retrain" && v.Value >= 0 && v.Value <= 100000000000
	default:
		return false
	}
}

func validCompanyRecommendation(member string, rate *int64) bool {
	return rate == nil || member == "new" && *rate >= 0 && *rate <= periodRateScale
}

func validPracticeCompanySales(c component) bool {
	if c.Type != "company_sales" || c.Mode != "company_sales" || c.ClassMember == "" ||
		c.CompanySales == nil || len(c.CompanySales.Categories) == 0 || c.Value != 0 ||
		c.Tiers != nil || c.PeriodBenefit != nil || c.RefundPolicy != "" || !validSourceRole(c.SourceRole) {
		return false
	}
	seen := make(map[string]bool)
	if c.ClassMember != "new" && len(c.CompanySales.RecommendationRecipients) != 0 {
		return false
	}
	for _, id := range c.CompanySales.RecommendationRecipients {
		if normalized, ok := canonicalSourceUUID(id); !ok || normalized != id || seen[id] {
			return false
		}
		seen[id] = true
	}
	clear(seen)
	validRate := func(rate *int64) bool {
		return validCompanyRecommendation(c.ClassMember, rate) &&
			(rate == nil || *rate == 0 || len(c.CompanySales.RecommendationRecipients) != 0)
	}
	for _, category := range c.CompanySales.Categories {
		if !textOK(category.Label, 80) || len(category.Companies) == 0 ||
			!category.practiceCompanyValue.valid(c.ClassMember) || !validRate(category.RecommendationRate) {
			return false
		}
		member := make(map[string]bool)
		for _, company := range category.Companies {
			if !textOK(company, 128) || seen[company] {
				return false
			}
			seen[company], member[company] = true, true
		}
		for _, override := range category.Overrides {
			if !member[override.CompanyID] || !override.practiceCompanyValue.valid(c.ClassMember) ||
				!validRate(override.RecommendationRate) {
				return false
			}
			delete(member, override.CompanyID)
		}
	}
	return true
}

func validPracticeCompanyScopes(items []component) bool {
	var internal, external []string
	for _, part := range items {
		for _, split := range part.retrainSplits() {
			internal = append(internal, split.CompanyID)
		}
		if s := part.CompanySales; s != nil {
			internal = append(internal, s.InternalCompanies...)
			for _, category := range s.Categories {
				external = append(external, category.Companies...)
			}
		}
	}
	companies := make(map[string]bool, len(external))
	for _, company := range external {
		companies[company] = true
	}
	for _, company := range internal {
		if !textOK(company, 128) || companies[company] {
			return false
		}
	}
	return true
}

type practiceCompanyAward struct {
	practiceCompanyValue
	role, key                string
	internal                 bool
	componentIndex           int
	recommendationRate       int64
	recommendationRecipients []string
}

type practiceCompanyOffers map[string]map[string]practiceCompanyAward

func practiceCompanyStandards(rule paymentRule) practiceCompanyOffers {
	var offers practiceCompanyOffers
	for i, part := range rule.Components {
		if part.CompanySales == nil {
			continue
		}
		if offers == nil {
			offers = practiceCompanyOffers{"new": {}, "retrain": {}}
		}
		for _, company := range part.CompanySales.InternalCompanies {
			for _, standards := range offers {
				standards[company] = practiceCompanyAward{internal: true}
			}
		}
		standards := offers[part.ClassMember]
		key := practicePeriodComponentKey(part, i)
		for _, category := range part.CompanySales.Categories {
			award := practiceCompanyAward{practiceCompanyValue: category.practiceCompanyValue, role: part.SourceRole, key: key,
				componentIndex: i, recommendationRecipients: part.CompanySales.RecommendationRecipients}
			if category.RecommendationRate != nil {
				award.recommendationRate = *category.RecommendationRate
			}
			for _, company := range category.Companies {
				standards[company] = award
			}
			for _, override := range category.Overrides {
				custom := award
				custom.practiceCompanyValue = override.practiceCompanyValue
				if override.RecommendationRate != nil {
					custom.recommendationRate = *override.RecommendationRate
				}
				standards[override.CompanyID] = custom
			}
		}
	}
	if offers != nil {
		for _, part := range rule.Components {
			for _, split := range part.retrainSplits() {
				for _, standards := range offers {
					standards[split.CompanyID] = practiceCompanyAward{internal: true}
				}
			}
		}
	}
	return offers
}

func (p *practicePurchase) classifyCompany(offers practiceCompanyOffers) {
	standards := offers[classMember(p.member.MembershipType)]
	if p.issue != "" || standards == nil {
		return
	}
	var company string
	company, p.companyRoutingIssue = practicePurchaseCompany(p.payments)
	if p.companyRoutingIssue != "" {
		return
	}
	terms, exists := standards[company]
	if !exists {
		p.companyRoutingIssue = "PRACTICE_COMPANY_SCOPE_MISSING"
	} else if !terms.internal {
		p.companyAward = &terms
	}
}

func loadPracticeCompanyRecipients(ctx context.Context, tx pgx.Tx, offers practiceCompanyOffers) (map[string][]companySourceAssignment, error) {
	var keys []string
	for _, group := range offers {
		for key, terms := range group {
			if !terms.internal {
				keys = append(keys, key)
			}
		}
	}
	return loadPracticeCompanySources(ctx, tx, keys, false)
}

func loadPracticeCompanySources(ctx context.Context, tx pgx.Tx, keys []string, activeOnly bool) (map[string][]companySourceAssignment, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT c.source_company_id,c.company_id,c.valid_from,c.valid_to
	 FROM organization_company_sources c JOIN organization_units u ON u.id=c.company_id AND u.kind='COMPANY'
	 WHERE c.source='DCZD' AND c.source_company_id=ANY($1::text[]) AND (NOT $2 OR u.status='ACTIVE')`, keys, activeOnly)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]companySourceAssignment)
	var id string
	var a companySourceAssignment
	_, err = pgx.ForEachRow(rows, []any{&id, &a.Company, &a.From, &a.To}, func() error {
		out[id] = append(out[id], a)
		return nil
	})
	return out, err
}

func practiceCompanyRecipient(assignments []companySourceAssignment, at time.Time) uniqueUnit {
	var result uniqueUnit
	for _, a := range assignments {
		if !at.Before(a.From) && (a.To == nil || at.Before(*a.To)) {
			result.add(a.Company)
		}
	}
	return result
}

func appendPracticeCompanySale(draft *paymentDraft, rule paymentRule, p practicePurchase,
	recipients map[string][]companySourceAssignment, identities map[identityKey][]identityAssignment,
	recommendations practiceCompanyRecommendations) error {
	award := p.companyAward
	if award == nil {
		return nil
	}
	if _, issue := practiceBeneficiaries(p.payments, award.role, identities); issue != "" {
		draft.Issues = append(draft.Issues, practiceIssue(issue, p.member, rule, award.key))
		return nil
	}
	var unit pgtype.UUID
	for _, payment := range p.payments {
		f := payment.Fact
		mapping := practiceCompanyRecipient(recipients[f.SalesCompanyID], f.OccurredAt)
		switch {
		case mapping.Ambiguous || unit.Valid && mapping.ID.Valid && unit != mapping.ID:
			draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_COMPANY_RECIPIENT_CONFLICT", p.member, rule, award.key))
			return nil
		case !mapping.ID.Valid:
			draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_COMPANY_RECIPIENT_MISSING", p.member, rule, award.key))
			return nil
		}
		unit = mapping.ID
	}
	amount := award.Value
	if award.Mode == "rate" {
		amount = practiceCompanyRate(p.base, award.Value)
	}
	if amount > p.base {
		draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_COMPANY_AWARD_EXCEEDS_PAYMENT", p.member, rule, award.key))
		return nil
	}
	if err := appendPracticeCompanyShare(draft, rule, p, unit, award.key, amount); err != nil {
		return err
	}
	if award.recommendationRate > 0 {
		key := "period:company_referral:" + strconv.Itoa(award.componentIndex)
		parent, issue := recommendations.resolve(p.payments, unit)
		if issue == "" && parent.Valid && !slices.Contains(award.recommendationRecipients, parent.String()) {
			issue = "PRACTICE_RECOMMENDATION_ROUTE_MISSING"
		}
		if issue != "" {
			pending := practiceIssue(issue, p.member, rule, key)
			pending.FactID = p.payments[0].ID
			draft.Issues = append(draft.Issues, pending)
		} else if parent.Valid {
			return appendPracticeCompanyShare(draft, rule, p, parent, key, practiceCompanyRate(p.base, award.recommendationRate))
		}
	}
	return nil
}

func practiceCompanyRate(base, rate int64) int64 {
	return base/periodRateScale*rate + (base%periodRateScale*rate+periodRateScale/2)/periodRateScale
}

func appendPracticeCompanyShare(draft *paymentDraft, rule paymentRule, p practicePurchase, unit pgtype.UUID, key string, amount int64) error {
	var err error
	draft.Total, err = addMinor(draft.Total, amount)
	if err != nil {
		return err
	}
	line := draftLine{RuleID: rule.ID, EnrollmentID: p.member.ID, Component: key, BeneficiaryUnit: &unit}
	appendPracticePeriodShare(draft, line, practiceBenefitShare{purchase: p, payments: p.payments, amount: amount})
	return nil
}
