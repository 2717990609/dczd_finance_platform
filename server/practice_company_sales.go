package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type practiceCompanySales struct {
	Categories        []practiceCompanyCategory `json:"categories"`
	InternalCompanies []string                  `json:"internalCompanies,omitempty"`
}

type practiceCompanyValue struct {
	Mode  string `json:"mode"`
	Value int64  `json:"value"`
}

type practiceCompanyCategory struct {
	Label     string                    `json:"label"`
	Companies []string                  `json:"companies"`
	Overrides []practiceCompanyOverride `json:"overrides,omitempty"`
	practiceCompanyValue
}

type practiceCompanyOverride struct {
	CompanyID string `json:"companyId"`
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

func validPracticeCompanySales(c component) bool {
	if c.Type != "company_sales" || c.Mode != "company_sales" || c.ClassMember == "" ||
		c.CompanySales == nil || len(c.CompanySales.Categories) == 0 || c.Value != 0 ||
		c.Tiers != nil || c.PeriodBenefit != nil || c.RefundPolicy != "" || !validSourceRole(c.SourceRole) {
		return false
	}
	seen := make(map[string]bool)
	for _, category := range c.CompanySales.Categories {
		if !textOK(category.Label, 80) || len(category.Companies) == 0 || !category.practiceCompanyValue.valid(c.ClassMember) {
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
			if !member[override.CompanyID] || !override.practiceCompanyValue.valid(c.ClassMember) {
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
	role, key string
	internal  bool
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
			for _, company := range category.Companies {
				standards[company] = practiceCompanyAward{practiceCompanyValue: category.practiceCompanyValue, role: part.SourceRole, key: key}
			}
			for _, override := range category.Overrides {
				standards[override.CompanyID] = practiceCompanyAward{practiceCompanyValue: override.practiceCompanyValue, role: part.SourceRole, key: key}
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
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT c.source_company_id,c.company_id,c.valid_from,c.valid_to
	 FROM organization_company_sources c JOIN organization_units u ON u.id=c.company_id AND u.kind='COMPANY'
	 WHERE c.source='DCZD' AND c.source_company_id=ANY($1::text[])`, keys)
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
	recipients map[string][]companySourceAssignment, identities map[identityKey][]identityAssignment) error {
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
		amount = p.base/periodRateScale*award.Value + (p.base%periodRateScale*award.Value+periodRateScale/2)/periodRateScale
	}
	if amount > p.base {
		draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_COMPANY_AWARD_EXCEEDS_PAYMENT", p.member, rule, award.key))
		return nil
	}
	var err error
	draft.Total, err = addMinor(draft.Total, amount)
	if err != nil {
		return err
	}
	line := draftLine{RuleID: rule.ID, EnrollmentID: p.member.ID, Component: award.key, BeneficiaryUnit: &unit}
	appendPracticePeriodShare(draft, line, practiceBenefitShare{purchase: p, payments: p.payments, amount: amount})
	return nil
}
