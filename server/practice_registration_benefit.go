package main

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
)

var errPracticeRegistrationOverlap = errors.New("报名销售范围重复，请为同一报名类型确认唯一的收款安排")

type practiceRegistrationScope struct {
	SellerID        string   `json:"sellerId,omitempty"`
	SellerCompanyID string   `json:"sellerCompanyId,omitempty"`
	Companies       []string `json:"companies,omitempty"`
}

func (s practiceRegistrationScope) valid() bool {
	if s.SellerID == "" && s.SellerCompanyID == "" {
		if len(s.Companies) == 0 {
			return false
		}
	} else if positiveSourceID(s.SellerID) == 0 || positiveSourceID(s.SellerCompanyID) == 0 {
		return false
	}
	for i, company := range s.Companies {
		if positiveSourceID(company) == 0 || company == s.SellerCompanyID || slices.Contains(s.Companies[:i], company) {
			return false
		}
	}
	return true
}

func (s practiceRegistrationScope) matches(seller, company string) bool {
	return s.SellerID != "" && s.SellerID == seller && s.SellerCompanyID == company || slices.Contains(s.Companies, company)
}

func checkPracticeRegistrationScopes(parts []component) error {
	for i, part := range parts {
		if part.PeriodBenefit == nil || part.PeriodBenefit.RegistrationScope == nil {
			continue
		}
		left := part.PeriodBenefit.RegistrationScope
		for _, other := range parts[:i] {
			if other.PeriodBenefit == nil || other.PeriodBenefit.RegistrationScope == nil ||
				part.ClassMember != "" && other.ClassMember != "" && other.ClassMember != part.ClassMember {
				continue
			}
			right := other.PeriodBenefit.RegistrationScope
			if left.matches(right.SellerID, right.SellerCompanyID) || right.matches(left.SellerID, left.SellerCompanyID) ||
				slices.ContainsFunc(left.Companies, func(company string) bool { return slices.Contains(right.Companies, company) }) {
				return errPracticeRegistrationOverlap
			}
		}
	}
	return nil
}

func usesPracticeRegistrations(rule paymentRule) bool {
	return slices.ContainsFunc(rule.Components, func(part component) bool {
		return part.PeriodBenefit != nil && part.PeriodBenefit.RegistrationScope != nil
	})
}

type practiceRegistrationInput struct {
	VersionID int64
	SellerID  string
	CompanyID string
}

const practiceRegistrationInputsSQL = `SELECT e.id,v.id,v.fact->>'referrerId',v.practice_registration->>'companyId'
 FROM practice_roster_members member JOIN practice_enrollments e ON e.id=member.enrollment_id
 JOIN LATERAL (
  SELECT v.id,v.fact,v.order_id,v.occurred_at,v.practice_registration FROM source_order_versions v
  WHERE v.source='DCZD' AND v.kind='DELIVERY_COMPLETED' AND v.record_key='dc_offline_order_log_t:'||e.source_enrollment_id
   AND ($2 OR v.id=($3::jsonb->>e.id::text)::bigint)
  ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
 ) v ON true
 WHERE member.roster_id=$1 AND e.commission_eligible AND v.fact->>'domain'='PRACTICE'
  AND v.order_id=ANY(e.paid_order_ids) AND coalesce(v.fact->>'referrerId','')<>''
  AND coalesce(v.practice_registration->>'companyId','')<>''
  AND CASE WHEN coalesce(v.practice_registration->>'periodKey','')=''
   THEN v.occurred_at=e.signed_at ELSE v.practice_registration->>'periodKey'=e.period_key END`

func readPracticeRegistrationInputs(ctx context.Context, tx pgx.Tx, rosterID int64, latest bool, pinned map[int64]int64) (map[int64]practiceRegistrationInput, error) {
	rows, err := tx.Query(ctx, practiceRegistrationInputsSQL, rosterID, latest, pinned)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]practiceRegistrationInput)
	var enrollment int64
	var entry practiceRegistrationInput
	_, err = pgx.ForEachRow(rows, []any{&enrollment, &entry.VersionID, &entry.SellerID, &entry.CompanyID}, func() error {
		result[enrollment] = entry
		return nil
	})
	return result, err
}
