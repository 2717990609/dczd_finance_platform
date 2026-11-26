package main

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
)

var errPracticeRegistrationOverlap = errors.New("总监归属范围重叠，请核对同一报名类型的归属关系")

type practiceRegistrationScope struct {
	SellerID        string   `json:"sellerId,omitempty"`
	SellerCompanyID string   `json:"sellerCompanyId,omitempty"`
	Companies       []string `json:"companies,omitempty"`
	Exclusive       bool     `json:"exclusive,omitempty"`
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
		if !left.Exclusive {
			continue
		}
		for _, other := range parts[:i] {
			if other.PeriodBenefit == nil || other.PeriodBenefit.RegistrationScope == nil ||
				part.ClassMember != "" && other.ClassMember != "" && other.ClassMember != part.ClassMember {
				continue
			}
			right := other.PeriodBenefit.RegistrationScope
			if right.Exclusive && (left.matches(right.SellerID, right.SellerCompanyID) || right.matches(left.SellerID, left.SellerCompanyID) ||
				slices.ContainsFunc(left.Companies, func(company string) bool { return slices.Contains(right.Companies, company) })) {
				return errPracticeRegistrationOverlap
			}
		}
	}
	return nil
}

func usesPracticeRegistrations(rule paymentRule) bool {
	return slices.ContainsFunc(rule.Components, func(part component) bool {
		return part.Mode == directorRateMode || part.PeriodBenefit != nil && part.PeriodBenefit.RegistrationScope != nil
	})
}

type practiceRegistrationInput struct {
	VersionID int64
	SellerID  string
	CompanyID string
	Issue     string
}

var practiceRegistrationInputsSQL = `SELECT e.id,coalesce(v.id,0),coalesce(v.fact->>'referrerId',''),coalesce(v.practice_registration->>'companyId',''),
 CASE WHEN v.id IS NULL THEN 'PRACTICE_REGISTRATION_ATTENDANCE_MISSING'
  WHEN v.fact->>'domain' IS DISTINCT FROM 'PRACTICE' OR NOT(v.order_id=ANY(e.paid_order_ids))
   THEN 'PRACTICE_REGISTRATION_ORDER_MISMATCH'
  WHEN CASE WHEN coalesce(v.practice_registration->>'periodKey','')='' THEN v.occurred_at<>e.signed_at
   ELSE v.practice_registration->>'periodKey'<>e.period_key END THEN 'PRACTICE_REGISTRATION_PERIOD_MISMATCH'
  WHEN coalesce(v.fact->>'referrerId','')='' OR coalesce(v.practice_registration->>'companyId','')=''
   THEN 'PRACTICE_REGISTRATION_ATTRIBUTION_MISSING'
  ELSE '' END
 FROM practice_roster_members member JOIN practice_enrollments e ON e.id=member.enrollment_id
 LEFT JOIN LATERAL (
  SELECT v.id,v.fact,v.order_id,v.occurred_at,v.practice_registration FROM source_order_versions v
  WHERE v.source='DCZD' AND v.kind='DELIVERY_COMPLETED' AND v.record_key='dc_offline_order_log_t:'||e.source_enrollment_id
   AND ($2 OR v.id=($3::jsonb->>e.id::text)::bigint)
  ORDER BY ` + practicePaymentRevision("v") + ` DESC,v.updated_at DESC,v.id DESC LIMIT 1
 ) v ON true
 WHERE member.roster_id=$1 AND e.commission_eligible AND ($2 OR v.id IS NOT NULL)`

func readPracticeRegistrationInputs(ctx context.Context, tx pgx.Tx, rosterID int64, latest bool, pinned map[int64]int64) (map[int64]practiceRegistrationInput, error) {
	rows, err := tx.Query(ctx, practiceRegistrationInputsSQL, rosterID, latest, pinned)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]practiceRegistrationInput)
	var enrollment int64
	var entry practiceRegistrationInput
	_, err = pgx.ForEachRow(rows, []any{&enrollment, &entry.VersionID, &entry.SellerID, &entry.CompanyID, &entry.Issue}, func() error {
		if !latest && entry.Issue != "" {
			return errPracticeRefundReplay
		}
		result[enrollment] = entry
		return nil
	})
	return result, err
}

func appendPracticeRegistrationIssues(draft *paymentDraft, ruleID int64, member string, purchases []practicePurchase,
	inputs map[int64]practiceRegistrationInput, reported map[int64]bool) bool {
	var incomplete bool
	for _, purchase := range purchases {
		enrollment := purchase.member
		if purchase.noReferrer || purchase.issue != "" || member != "" && member != classMember(enrollment.MembershipType) {
			continue
		}
		input, found := inputs[enrollment.ID]
		code := input.Issue
		if !found {
			code = "PRACTICE_REGISTRATION_ATTENDANCE_MISSING"
		}
		if code == "" {
			continue
		}
		incomplete = true
		if reported[enrollment.ID] {
			continue
		}
		reported[enrollment.ID] = true
		issue := draftIssue{RuleID: ruleID, Code: code, EnrollmentID: enrollment.ID}
		payments := purchase.payments
		if len(payments) == 0 {
			payments = purchase.originalPayments
		}
		if len(payments) != 0 {
			issue.FactID = payments[0].ID
		}
		draft.Issues = append(draft.Issues, issue)
	}
	return incomplete
}
