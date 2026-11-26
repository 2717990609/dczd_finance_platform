package main

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const orderDisplayInclude = "finance_order_display"

func canRequestOrderDisplay(endpoint *url.URL) bool {
	return endpoint.Scheme == "https" || isLoopbackHost(endpoint.Hostname())
}

type orderDisplayFields map[string]any

var orderDisplayContactKeys = map[string]string{
	"customer_nickname": "customerNickname", "customer_name": "customerName", "customer_phone": "customerPhone",
	"referrer_nickname": "referrerNickname", "referrer_name": "referrerName", "referrer_phone": "referrerPhone",
	"referrer_company_name": "referrerCompanyName",
	"seller_nickname":       "sellerNickname", "seller_name": "sellerName", "seller_phone": "sellerPhone",
}

func (fields orderDisplayFields) addText(key string, raw json.RawMessage, limit int) {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		value = strings.TrimSpace(value)
		if textOK(value, limit) {
			fields[key] = value
		}
	}
}

func (fields orderDisplayFields) addTime(key string, raw json.RawMessage) {
	var value time.Time
	if json.Unmarshal(raw, &value) == nil && !value.IsZero() {
		fields[key] = value
	}
}

func (fields orderDisplayFields) addMinor(key string, raw json.RawMessage) {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		if minor, err := strconv.ParseInt(value, 10, 64); err == nil && minor >= 0 {
			fields[key] = minor
		}
	}
}

func (fields orderDisplayFields) addContacts(raw map[string]json.RawMessage) {
	for sourceKey, displayKey := range orderDisplayContactKeys {
		fields.addText(displayKey, raw[sourceKey], 200)
	}
}

func readOrderDisplay(source string, wire sourceDocument, raw json.RawMessage) orderDisplayFields {
	var optional struct {
		Amounts, Payment, Refund map[string]json.RawMessage
		Extensions               struct {
			Contacts   map[string]json.RawMessage `json:"finance_order_display"`
			Attributes map[string]json.RawMessage
		}
	}
	fields := make(orderDisplayFields)
	// Optional display fields cannot invalidate an otherwise valid financial fact.
	_ = json.Unmarshal(raw, &optional)
	fields.addMinor("originalAmountMinor", optional.Amounts["gross_minor"])
	fields.addMinor("sourceSettlementBasisMinor", optional.Amounts["source_settlement_basis_minor"])
	fields.addText("paymentMethod", optional.Payment["method_code"], 80)
	fields.addTime("paidAt", optional.Payment["paid_at"])
	fields.addText("paymentTrace", optional.Payment["provider_transaction_id"], 200)
	if fields["paymentTrace"] == nil {
		fields.addText("paymentTrace", optional.Payment["provider_txn_id"], 200)
	}
	fields.addText("refundNo", optional.Refund["refund_no"], 200)
	fields.addTime("refundedAt", optional.Refund["refunded_at"])
	if source == "DCAI" {
		fields.addText("correctionItemId", optional.Extensions.Attributes["correction_item_id"], 128)
		fields.addText("correctionBatchNo", optional.Extensions.Attributes["correction_batch_no"], 200)
		fields.addTime("correctionCreatedAt", optional.Extensions.Attributes["correction_created_at"])
	}
	for _, related := range wire.Relations {
		if related.Kind == "SETTLES_DEPOSIT" && textOK(related.OrderID, 128) {
			fields["settlesDepositOrderId"] = related.OrderID
		}
	}
	fields.addContacts(optional.Extensions.Contacts)
	return fields
}

const orderDisplayJoinsSQL = `
 LEFT JOIN source_order_display display ON display.source_version_id=v.id
 LEFT JOIN LATERAL (
  SELECT details - ARRAY['correctionItemId','correctionBatchNo','correctionCreatedAt'] AS details
  FROM (
   SELECT d.details,count(*) OVER () AS matches
   FROM (
    SELECT DISTINCT ON (paid.line_id,paid.record_key) paid.id
    FROM source_order_versions paid
    WHERE v.kind='REFUND_SUCCEEDED' AND v.source='DCZD'
     AND jsonb_array_length(coalesce(v.fact->'refundsPayment','[]'::jsonb))=1
     AND paid.source=v.source AND paid.kind='ORDER_PAID' AND paid.order_id=v.fact#>>'{refundsPayment,0,orderId}'
     AND paid.occurred_at<=v.occurred_at AND paid.updated_at<=v.updated_at
     AND paid.fact->>'sourceKey'=v.fact->>'sourceKey' AND paid.fact->>'domain'=v.fact->>'domain'
     AND (coalesce(v.fact#>>'{refundsPayment,0,lineId}','')='' OR paid.line_id=v.fact#>>'{refundsPayment,0,lineId}')
     AND (coalesce(v.fact#>>'{refundsPayment,0,recordId}','')='' OR strpos(paid.record_key,':')>0
      AND substr(paid.record_key,strpos(paid.record_key,':')+1)=v.fact#>>'{refundsPayment,0,recordId}')
    ORDER BY paid.line_id,paid.record_key,paid.updated_at DESC,paid.id DESC
   ) linked
   LEFT JOIN source_order_display d ON d.source_version_id=linked.id
  ) candidates WHERE matches=1
 ) original ON true
`

// Keep order JSON values lossless while applying the presentation policy at the API boundary.
type orderHTTPResult map[string]json.RawMessage

var orderPhoneFields = [...]string{"customerPhone", "referrerPhone", "sellerPhone"}

func orderText(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func maskOrderPhone(phone string) string {
	if strings.TrimSpace(phone) == "" {
		return ""
	}
	var digits []rune
	for _, r := range phone {
		if unicode.IsDigit(r) {
			digits = append(digits, r)
		}
	}
	if len(digits) < 8 {
		return "****"
	}
	head, tail := 3, 4
	if len(digits) < 11 {
		head, tail = 2, 2
	}
	return string(digits[:head]) + strings.Repeat("*", len(digits)-head-tail) + string(digits[len(digits)-tail:])
}

func (order orderHTTPResult) maskPhones() error {
	var display map[string]json.RawMessage
	if err := json.Unmarshal(order["display"], &display); err != nil {
		return err
	}
	for _, field := range orderPhoneFields {
		if value, ok := display[field]; ok {
			phone := maskOrderPhone(orderText(value))
			if phone == "" {
				delete(display, field)
			} else {
				display[field], _ = json.Marshal(phone)
			}
		}
	}
	var err error
	order["display"], err = json.Marshal(display)
	return err
}
