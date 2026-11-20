package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// A payment version may explicitly identify a paid retraining signup without a seller.
// Its identifiers must still agree with the confirmed roster before excluding sales.
type sourcePracticeNoReferrer struct {
	EnrollmentID  int64 `json:"enrollmentId"`
	ParticipantID int64 `json:"participantId"`
	StrategyID    int64 `json:"strategyId"`
}

func readSourcePracticeNoReferrer(source string, doc sourceDocument) *sourcePracticeNoReferrer {
	if source != "DCZD" || doc.Kind != "ORDER_PAID" || doc.Business.Domain != "PRACTICE" ||
		doc.Record.Table != "dc_practice_total_order_t" || doc.Record.ID != doc.Order.ID || positiveSourceID(doc.Order.ID) == 0 {
		return nil
	}
	rawRevision, versioned := strings.CutPrefix(doc.Record.Version, "practice-registration/v2/")
	revision := positiveSourceID(rawRevision)
	if !versioned || revision == 0 || revision > 9_007_199_254_740_991 ||
		revision <= doc.Record.UpdatedAt.UnixMilli()*100+3 {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(doc.Business.Classification, &fields) != nil ||
		string(fields["retrain_no_sales_reason"]) != `"NO_REFERRER"` ||
		string(fields["registration_attribution_status"]) != `"PRACTICE_REGISTRATION_ATTRIBUTION_MISSING"` ||
		sourceRegistrationID(fields["order_type"]) != 3 || sourceRegistrationID(fields["registration_match_count"]) != 1 ||
		len(fields["inactive_sales_referrer_id"])+len(fields["inactive_sales_company_id"])+len(fields["inactive_sales_membership_id"]) != 0 {
		return nil
	}
	strategy := sourceRegistrationID(fields["strategy_id"])
	user := sourceRegistrationID(fields["registration_user_id"])
	enrollment := sourceRegistrationID(fields["registration_id"])
	activity := sourceRegistrationID(fields["activity_type"])
	if strategy == 0 || strategy != sourceRegistrationID(fields["registration_strategy_id"]) ||
		user == 0 || user != sourceRegistrationID(fields["user_id"]) || enrollment == 0 || (activity != 1 && activity != 2) {
		return nil
	}
	var customerMatched bool
	for _, party := range doc.Attribution.Participants {
		switch party.Role {
		case "DIRECT_REFERRER", "PROFESSOR", "DIRECT_COMPANY", "PARENT_COMPANY":
			return nil
		case "CUSTOMER":
			if positiveSourceID(party.UserID) != user {
				return nil
			}
			customerMatched = true
		}
	}
	if !customerMatched {
		return nil
	}
	return &sourcePracticeNoReferrer{EnrollmentID: enrollment, ParticipantID: user, StrategyID: strategy}
}

func (n *sourcePracticeNoReferrer) matches(member practiceEnrollment) bool {
	if n == nil || member.MembershipType != 2 || len(member.PaidOrderIDs) != 1 ||
		n.EnrollmentID != positiveSourceID(member.SourceEnrollmentID) || n.ParticipantID != positiveSourceID(member.ParticipantID) {
		return false
	}
	key := strings.Split(member.PeriodKey, ":")
	return len(key) == 4 && key[2] == strconv.FormatInt(n.StrategyID, 10)
}

func practicePaymentVersionOrder(alias string) string {
	// The materialized revision can advance even when the underlying business timestamp does not.
	return `(CASE WHEN ` + alias + `.version ~ '^practice-registration/v2/[1-9][0-9]{0,15}$'
	 THEN split_part(` + alias + `.version,'/',3)::bigint
	 ELSE floor(extract(epoch FROM ` + alias + `.updated_at)*1000)*100+3 END,` + alias + `.updated_at,` + alias + `.id)`
}
