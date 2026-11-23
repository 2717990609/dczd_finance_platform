package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// A source exception still has to identify the same signup in the confirmed roster.
type sourcePracticeSignup struct {
	EnrollmentID  int64 `json:"enrollmentId"`
	ParticipantID int64 `json:"participantId"`
	StrategyID    int64 `json:"strategyId"`
}

func readSourcePracticeSalesExceptions(source string, doc sourceDocument) (noReferrer, inactiveCompany *sourcePracticeSignup) {
	if source != "DCZD" || doc.Kind != "ORDER_PAID" || doc.Business.Domain != "PRACTICE" ||
		doc.Record.Table != "dc_practice_total_order_t" || doc.Record.ID != doc.Order.ID || positiveSourceID(doc.Order.ID) == 0 {
		return nil, nil
	}
	rawRevision, versioned := strings.CutPrefix(doc.Record.Version, "practice-registration/v2/")
	revision := positiveSourceID(rawRevision)
	if !versioned || revision == 0 || revision > 9_007_199_254_740_991 ||
		revision <= doc.Record.UpdatedAt.UnixMilli()*100+3 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(doc.Business.Classification, &fields) != nil ||
		string(fields["registration_attribution_status"]) != `"PRACTICE_REGISTRATION_ATTRIBUTION_MISSING"` ||
		sourceRegistrationID(fields["order_type"]) != 3 || sourceRegistrationID(fields["registration_match_count"]) != 1 {
		return nil, nil
	}
	strategy := sourceRegistrationID(fields["strategy_id"])
	user := sourceRegistrationID(fields["registration_user_id"])
	enrollment := sourceRegistrationID(fields["registration_id"])
	activity := sourceRegistrationID(fields["activity_type"])
	if strategy == 0 || strategy != sourceRegistrationID(fields["registration_strategy_id"]) ||
		user == 0 || user != sourceRegistrationID(fields["user_id"]) || enrollment == 0 || (activity != 1 && activity != 2) {
		return nil, nil
	}
	var customerMatched bool
	for _, party := range doc.Attribution.Participants {
		switch party.Role {
		case "DIRECT_REFERRER", "PROFESSOR", "DIRECT_COMPANY", "PARENT_COMPANY":
			return nil, nil
		case "CUSTOMER":
			if positiveSourceID(party.UserID) != user {
				return nil, nil
			}
			customerMatched = true
		}
	}
	if !customerMatched {
		return nil, nil
	}
	signup := &sourcePracticeSignup{EnrollmentID: enrollment, ParticipantID: user, StrategyID: strategy}
	referrer, company, membership := fields["inactive_sales_referrer_id"], fields["inactive_sales_company_id"], fields["inactive_sales_membership_id"]
	switch string(fields["retrain_no_sales_reason"]) {
	case `"NO_REFERRER"`:
		if len(referrer)+len(company)+len(membership) == 0 {
			return signup, nil
		}
	case `"COMPANY_DISABLED_BEFORE_PAYMENT"`:
		if sourceRegistrationID(referrer) > 0 && sourceRegistrationID(company) > 0 && sourceRegistrationID(membership) > 0 {
			return nil, signup
		}
	}
	return nil, nil
}

func (n *sourcePracticeSignup) matches(member practiceEnrollment) bool {
	if n == nil || member.MembershipType != 2 || len(member.PaidOrderIDs) != 1 ||
		n.EnrollmentID != positiveSourceID(member.SourceEnrollmentID) || n.ParticipantID != positiveSourceID(member.ParticipantID) {
		return false
	}
	key := strings.Split(member.PeriodKey, ":")
	return len(key) == 4 && key[2] == strconv.FormatInt(n.StrategyID, 10)
}

func practicePaymentRevision(alias string) string {
	// The materialized revision can advance even when the underlying business timestamp does not.
	return `CASE WHEN ` + alias + `.version ~ '^practice-registration/v2/[1-9][0-9]{0,15}$'
	 THEN split_part(` + alias + `.version,'/',3)::bigint
	 ELSE floor(extract(epoch FROM ` + alias + `.updated_at)*1000)*100+3 END`
}

func practicePaymentVersionOrder(alias string) string {
	return `(` + practicePaymentRevision(alias) + `,` + alias + `.updated_at,` + alias + `.id)`
}

// These rows are compared within the same source record. Other streams keep their timestamp ordering.
func sourceOrderVersionSort(alias string) string {
	return `CASE WHEN ` + alias + `.source='DCZD' AND ` + alias + `.kind='ORDER_PAID'
	 AND split_part(` + alias + `.record_key,':',1)='dc_practice_total_order_t' THEN ` + practicePaymentRevision(alias) +
		` END DESC,` + alias + `.updated_at DESC,` + alias + `.id DESC`
}
