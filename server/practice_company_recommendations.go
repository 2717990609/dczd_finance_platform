package main

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type practiceCompanyRecommendations struct {
	directory map[pgtype.UUID]pgtype.UUID
	explicit  map[string][]companySourceAssignment
}

func loadPracticeCompanyRecommendations(ctx context.Context, tx pgx.Tx, offers *practiceCompanyOffers,
	recipients map[string][]companySourceAssignment, orders map[string][]paymentInput, categories map[string]string) (practiceCompanyRecommendations, error) {
	var out practiceCompanyRecommendations
	if offers == nil {
		return out, nil
	}
	companies := make(map[pgtype.UUID]bool)
	for sourceID, award := range offers.companies["new"] {
		if award.recommendationRate > 0 {
			for _, assignment := range recipients[sourceID] {
				companies[assignment.Company] = true
			}
		}
	}
	var explicit []string
	for _, matches := range orders {
		for _, payment := range matches {
			f := payment.Fact
			if offers.newCompanyAward(f.SalesCompanyID, categories[f.PaymentID]).recommendationRate <= 0 {
				continue
			}
			for _, assignment := range recipients[f.SalesCompanyID] {
				companies[assignment.Company] = true
			}
			if f.ReferrerCompanyID != "" {
				explicit = append(explicit, f.ReferrerCompanyID)
			}
		}
	}
	if len(companies) == 0 {
		return out, nil
	}
	ids := make([]pgtype.UUID, 0, len(companies))
	for id := range companies {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx, `SELECT r.company_id,r.referrer_company_id FROM organization_company_recommendations r
	 JOIN organization_units company ON company.id=r.company_id AND company.kind='COMPANY' AND company.status='ACTIVE'
	 LEFT JOIN organization_units parent ON parent.id=r.referrer_company_id AND parent.kind='COMPANY' AND parent.status='ACTIVE'
	 WHERE r.company_id=ANY($1::uuid[]) AND (r.referrer_company_id IS NULL OR parent.id IS NOT NULL)`, ids)
	if err != nil {
		return out, err
	}
	out.directory = make(map[pgtype.UUID]pgtype.UUID)
	var company, parent pgtype.UUID
	_, err = pgx.ForEachRow(rows, []any{&company, &parent}, func() error {
		out.directory[company] = parent
		return nil
	})
	if err != nil {
		return out, err
	}
	out.explicit, err = loadPracticeCompanySources(ctx, tx, explicit, true)
	return out, err
}

func (r practiceCompanyRecommendations) resolve(payments []paymentInput, company pgtype.UUID) (pgtype.UUID, string) {
	sourceID := payments[0].Fact.ReferrerCompanyID
	var parent pgtype.UUID
	for _, payment := range payments {
		fact := payment.Fact
		if fact.ReferrerConflict || fact.ReferrerCompanyID != sourceID {
			return pgtype.UUID{}, "PRACTICE_RECOMMENDATION_CONFLICT"
		}
		if sourceID == "" {
			continue
		}
		match := practiceCompanyRecipient(r.explicit[sourceID], fact.OccurredAt)
		switch {
		case match.Ambiguous || parent.Valid && match.ID.Valid && parent != match.ID:
			return pgtype.UUID{}, "PRACTICE_RECOMMENDATION_CONFLICT"
		case !match.ID.Valid:
			return pgtype.UUID{}, "PRACTICE_RECOMMENDATION_COMPANY_MISSING"
		}
		parent = match.ID
	}
	if sourceID == "" {
		var known bool
		parent, known = r.directory[company]
		if !known {
			return pgtype.UUID{}, "PRACTICE_RECOMMENDATION_RELATION_MISSING"
		}
	}
	if parent == company {
		return pgtype.UUID{}, "PRACTICE_RECOMMENDATION_CONFLICT"
	}
	return parent, ""
}
