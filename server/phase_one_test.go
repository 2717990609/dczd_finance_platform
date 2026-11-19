package main

import (
	"encoding/json"
	"testing"
)

func TestRuleRejectsInvalidOrRepeatedComponents(t *testing.T) {
	rules := []component{{Type: "sales", Mode: "rate", Value: 100}, {Type: "sales", Mode: "fixed", Value: 10}}
	if validateComponents(rules) == nil {
		t.Fatal("duplicate component accepted")
	}
	rules = []component{{Type: "delivery", Mode: "rate", Value: 10001}}
	if validateComponents(rules) == nil {
		t.Fatal("percentage over 100 accepted")
	}
	if err := validateComponents([]component{{Type: "sales", Mode: "rate", Value: 1500}, {Type: "delivery", Mode: "fixed", Value: 12000}}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceFactNormalizesOrderAndRejectsOtherOrder(t *testing.T) {
	payload := json.RawMessage(`{"fact_schema_version":"source.order-fact/1.0","fact_type":"ORDER_PAID","occurred_at":"2026-10-01T00:00:00Z","order":{"source_order_id":"o1","order_no":"sample","source_status":{"code":"PAID"}},"source_record":{"source_table":"orders","record_id":"r1","record_version":"v2","updated_at":"2026-10-02T00:00:00Z"},"business":{"source_domain_code":"CAMP","source_product_code":"p1"},"amounts":{"paid_minor":"9000"}}`)
	fact, err := normalizeFact("DCZD", "o1", payload)
	if err != nil || fact.AmountMinor == nil || *fact.AmountMinor != 9000 || fact.Version != "v2" || fact.OrderNo != "sample" {
		t.Fatalf("unexpected fact: %#v %v", fact, err)
	}
	if _, err = normalizeFact("DCZD", "other", payload); err == nil {
		t.Fatal("order mismatch accepted")
	}
}
