package main

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5"
)

type dcaiOrderDetails struct {
	VIPSource         *int   `json:"vipSource,omitempty"`
	SellerAbsent      bool   `json:"sellerAbsent,omitempty"`
	AmbassadorGiftVIP string `json:"ambassadorGiftVIP,omitempty"`
}

func readDCAIOrderDetails(wire sourceDocument, classification sourceClassification) *dcaiOrderDetails {
	if wire.Kind != "ORDER_PAID" {
		return nil
	}
	var fields struct {
		OrderSource *int   `json:"order_source"`
		GiftVIP     string `json:"gift_vip_order_id"`
	}
	_ = json.Unmarshal(wire.Business.Classification, &fields)
	switch classification.Key {
	case "dcai:vip:new":
		details := dcaiOrderDetails{VIPSource: fields.OrderSource, SellerAbsent: wire.Attribution.Participants != nil}
		for _, participant := range wire.Attribution.Participants {
			if participant.Role == "PID_USER" || participant.Role == "LOCK_SALES_USER" {
				details.SellerAbsent = false
				break
			}
		}
		if details.VIPSource != nil || details.SellerAbsent {
			return &details
		}
	case "dcai:health-ambassador:new":
		// The health identity fact supplies the child VIP record ID, not its order number.
		if id, err := strconv.ParseUint(fields.GiftVIP, 10, 64); err == nil && id != 0 {
			return &dcaiOrderDetails{AmbassadorGiftVIP: strconv.FormatUint(id, 10)}
		}
	}
	return nil
}

type catalogVIPOrderTreatment struct {
	SellerAbsent bool
	GiftOpening  bool
	BackOffice   bool
}

func readCatalogVIPOrderTreatment(ctx context.Context, tx pgx.Tx, payments []int64) (map[int64]catalogVIPOrderTreatment, error) {
	rows, err := tx.Query(ctx, `SELECT c.source_version_id,c.dcai_order_details,
	 EXISTS(SELECT 1 FROM source_order_classifications parent JOIN source_order_versions fact ON fact.id=parent.source_version_id
	  WHERE parent.dcai_order_details->>'ambassadorGiftVIP'=substr(v.record_key,length('ai_vip_order:')+1)
	   AND parent.dcai_order_details ? 'ambassadorGiftVIP'
	   AND NOT EXISTS(SELECT 1 FROM source_order_versions newer
	    WHERE (newer.source,newer.order_id,newer.line_id,newer.record_key,newer.kind)=(fact.source,fact.order_id,fact.line_id,fact.record_key,fact.kind)
	     AND (newer.updated_at,newer.id)>(fact.updated_at,fact.id)))
	 FROM source_order_classifications c JOIN source_order_versions v ON v.id=c.source_version_id
	 WHERE c.source_version_id=ANY($1::bigint[]) AND c.classifier='dcai.vip-lifecycle.v1'`, payments)
	if err != nil {
		return nil, err
	}
	treatments := make(map[int64]catalogVIPOrderTreatment)
	var id int64
	var details *dcaiOrderDetails
	var ambassadorGift bool
	_, err = pgx.ForEachRow(rows, []any{&id, &details, &ambassadorGift}, func() error {
		treatment := catalogVIPOrderTreatment{GiftOpening: ambassadorGift}
		if details != nil {
			treatment.SellerAbsent = details.SellerAbsent
			if details.VIPSource != nil {
				switch *details.VIPSource {
				case 1, 2: // Back-office entry or batch import.
					treatment.BackOffice = true
				case 5, 7, 8, 14: // Health ambassador opening, share, quota usage, or claim.
					treatment.GiftOpening = true
				case 6: // VIP referral reward.
					treatment.GiftOpening = true
				}
			}
		}
		treatments[id] = treatment
		return nil
	})
	return treatments, err
}

const dcaiZeroBackOfficeAttributionAdvisory = "DCAI_VIP_ZERO_BACK_OFFICE_ATTRIBUTION"

func isVIPSalesAttributionIssue(code string) bool {
	switch code {
	case "SALES_PERSON_MISSING", "SALES_PERSON_CONFLICT", "SALES_SOURCE_MISSING", "SALES_SOURCE_CONFLICT",
		"SALES_COMPANY_MISSING", "SALES_COMPANY_CONFLICT", "AGGREGATE_SALES_ORGANIZATION_MISSING",
		"COMMISSION_SALES_ORGANIZATION_MISSING", "SALES_ORGANIZATION_SCOPE_CONFLICT", "SALES_CATEGORY_MISSING", "SALES_CATEGORY_CONFLICT":
		return true
	}
	return false
}
