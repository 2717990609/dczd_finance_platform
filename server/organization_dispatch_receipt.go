package main

import (
	"encoding/json"
	"time"
)

type dispatchEnvelope struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"message_id"`
	MessageType   string          `json:"message_type"`
	SourceSystem  string          `json:"source_system"`
	TargetSystem  string          `json:"target_system"`
	EntityID      string          `json:"entity_id"`
	Version       int64           `json:"entity_version"`
	OccurredAt    time.Time       `json:"occurred_at"`
	EffectiveAt   time.Time       `json:"effective_at"`
	CorrelationID string          `json:"correlation_id"`
	Data          json.RawMessage `json:"data"`
}

type dispatchReceipt struct {
	MessageID      string            `json:"message_id"`
	Result         string            `json:"result"`
	AppliedVersion int64             `json:"applied_version"`
	ExternalID     string            `json:"external_id"`
	ReasonCode     string            `json:"reason_code"`
	Identity       *dispatchIdentity `json:"identity,omitempty"`
}

type dispatchIdentity struct {
	UserID    string `json:"user_id"`
	CompanyID string `json:"company_id"`
	MemberID  string `json:"member_id"`
	Role      string `json:"role"`
	State     string `json:"state"`
}

func dispatchDeliveryResult(msg dispatchEnvelope, r dispatchReceipt) (string, string, string) {
	if r.MessageID != msg.ID {
		return "RETRYABLE", "INVALID_RECEIPT", ""
	}
	switch r.Result {
	case "APPLIED", "DUPLICATE":
		if r.AppliedVersion != msg.Version || r.ExternalID == "" || len(r.ExternalID) > 128 {
			return "RETRYABLE", "INVALID_RECEIPT", ""
		}
		if msg.MessageType == "sale_company_membership_set" {
			var data dispatchMemberMessage
			if json.Unmarshal(msg.Data, &data) != nil {
				return "RETRYABLE", "INVALID_RECEIPT", ""
			}
			company, role, state := data.ExternalCompanyID, "MEMBER", "ACTIVE"
			switch data.Operation {
			case "TRANSFER":
				company = data.DestinationExternalID
			case "SET_OWNER":
				if r.ExternalID != data.MemberID {
					return "RETRYABLE", "INVALID_RECEIPT", ""
				}
				role = "OWNER"
			case "UNSET_OWNER":
			case "RECONCILE":
				role, state = data.DesiredRole, data.DesiredState
			default:
				return "APPLIED", "", r.ExternalID
			}
			if r.Identity == nil || r.Identity.UserID != data.UserID || r.Identity.CompanyID != company ||
				r.Identity.MemberID != r.ExternalID || r.Identity.Role != role || r.Identity.State != state {
				return "RETRYABLE", "INVALID_RECEIPT", ""
			}
		}
		return "APPLIED", "", r.ExternalID
	case "STALE":
		return "CONFLICT", "STALE_VERSION", ""
	case "CONFLICT":
		switch r.ReasonCode {
		case "OWNER_REASSIGNMENT_REQUIRED", "OWNER_ALREADY_EXISTS", "HEALTH_IDENTITY_PROTECTED", "CATEGORY_IN_USE",
			"COMPANY_MAPPING_REQUIRED", "CATEGORY_MAPPING_REQUIRED", "NAME_MAPPING_REQUIRED", "MAPPED_RECORD_MISSING",
			"MEMBERSHIP_CONFLICT", "ACCOUNT_ALREADY_HAS_IDENTITY", "ACCOUNT_NOT_FOUND", "ACCOUNT_UNAVAILABLE",
			"AI_HEALTH_NATIVE_CHANGE_PENDING", "DESTINATION_COMPANY_UNAVAILABLE", "OPEN_ORDERS", "AI_CAUSAL_CONFLICT",
			"COMPANY_MAPPING_CONFLICT", "INVALID_COMPANY_MAPPING", "PROTECTED_CATEGORY", "MESSAGE_CONTENT_CONFLICT",
			"INVALID_COMPANY", "INVALID_COMPANY_TYPE", "INVALID_NAME", "INVALID_MEMBER_NAME", "INVALID_MEMBER",
			"INVALID_STATUS", "INVALID_MESSAGE", "UNSUPPORTED_MEMBER_OPERATION", "UNSUPPORTED_MESSAGE":
			return "CONFLICT", r.ReasonCode, ""
		}
		return "CONFLICT", "TARGET_CONFLICT", ""
	case "WAIT_DEPENDENCY":
		switch r.ReasonCode {
		case "PARENT_COMPANY_NOT_SYNCED":
			return "WAIT_PARENT_COMPANY", r.ReasonCode, ""
		case "COMPANY_NOT_SYNCED":
			return "RETRYABLE", r.ReasonCode, ""
		case "CATEGORY_NOT_SYNCED":
			return "WAIT_CATEGORY", r.ReasonCode, ""
		default:
			return "RETRYABLE", "TARGET_RETRY", ""
		}
	default:
		return "RETRYABLE", "TARGET_RETRY", ""
	}
}

type dispatchMemberMessage struct {
	UserID                string `json:"external_user_id"`
	CompanyID             string `json:"company_id"`
	ExternalCompanyID     string `json:"expected_company_external_id"`
	MemberID              string `json:"external_member_id"`
	Name                  string `json:"name"`
	Operation             string `json:"operation"`
	DestinationID         string `json:"destination_company_id,omitempty"`
	DestinationExternalID string `json:"destination_company_external_id,omitempty"`
	DesiredRole           string `json:"desired_role,omitempty"`
	DesiredState          string `json:"desired_state,omitempty"`
}
