package main

import "encoding/json"

func isEnrollmentPracticeFact(doc sourceDocument) bool {
	if doc.Business.Domain != "PRACTICE" || (doc.Business.Code != "1" && doc.Business.Code != "4") {
		return false
	}
	switch doc.Kind {
	case "ORDER_PAID", "REFUND_SUCCEEDED", "DELIVERY_COMPLETED":
	default:
		return false
	}
	var fields struct {
		Basis string `json:"registration_attribution_basis"`
	}
	return json.Unmarshal(doc.Business.Classification, &fields) == nil && fields.Basis == "BUSINESS_ENROLLMENT"
}
