package main

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5"
)

type sourcePracticeRegistration struct {
	CompanyID string `json:"companyId"`
	PeriodKey string `json:"periodKey,omitempty"`
}

func readSourcePracticeRegistration(source string, doc sourceDocument, seller string) *sourcePracticeRegistration {
	if source != "DCZD" || doc.Kind != "DELIVERY_COMPLETED" || doc.Business.Domain != "PRACTICE" ||
		doc.Record.Table != "dc_offline_order_log_t" || doc.Delivery.ID != doc.Record.ID ||
		positiveSourceID(doc.Delivery.ID) == 0 || positiveSourceID(seller) == 0 {
		return nil
	}
	linked := false
	for _, relation := range doc.Relations {
		if relation.Kind == "DELIVERS_ORDER" {
			if relation.OrderID != doc.Order.ID || relation.LineID != doc.Order.Line {
				return nil
			}
			linked = true
		}
	}
	if !linked {
		return nil
	}
	var company string
	for _, party := range doc.Attribution.Participants {
		if party.Role != "DIRECT_COMPANY" {
			continue
		}
		if positiveSourceID(party.CompanyID) == 0 || company != "" && party.CompanyID != company {
			return nil
		}
		company = party.CompanyID
	}
	if company == "" {
		return nil
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(doc.Business.Classification, &fields)
	return &sourcePracticeRegistration{CompanyID: company, PeriodKey: sourceRegistrationPeriodKey(fields)}
}

func sourceRegistrationID(raw json.RawMessage) int64 {
	id := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &id) != nil {
			return 0
		}
	}
	return positiveSourceID(id)
}

func sourceRegistrationPeriodKey(fields map[string]json.RawMessage) string {
	deliveryID, attendanceID := sourceRegistrationID(fields["delivery_activity_id"]), sourceRegistrationID(fields["attendance_activity_id"])
	deliveryType, attendanceType := sourceRegistrationID(fields["delivery_activity_type"]), sourceRegistrationID(fields["attendance_activity_type"])
	if deliveryID != 0 && attendanceID != 0 && deliveryID != attendanceID ||
		deliveryType != 0 && attendanceType != 0 && deliveryType != attendanceType {
		return ""
	}
	id, activityType := max(deliveryID, attendanceID), max(deliveryType, attendanceType)
	strategy := sourceRegistrationID(fields["strategy_id"])
	if id == 0 || strategy == 0 || (activityType != 1 && activityType != 2) {
		return ""
	}
	return "PRACTICE:" + strconv.FormatInt(activityType, 10) + ":" + strconv.FormatInt(strategy, 10) + ":" + strconv.FormatInt(id, 10)
}

func fillSourcePracticeRegistration(ctx context.Context, tx pgx.Tx, version int64, registration *sourcePracticeRegistration) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE source_order_versions SET practice_registration=$2
	 WHERE id=$1 AND practice_registration IS NULL AND NOT source_version_is_settled(id,kind)`, version, registration)
	if err != nil || tag.RowsAffected() != 0 {
		return err == nil, err
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(practice_registration=$2::jsonb,source_version_is_settled(id,kind)) FROM source_order_versions WHERE id=$1`,
		version, registration).Scan(&same); err != nil {
		return false, err
	}
	if !same {
		return false, conflict
	}
	return false, nil
}
