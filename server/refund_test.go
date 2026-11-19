package main

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeSuccessfulRefundRetainsPaymentRelationAndRefundDate(t *testing.T) {
	item, err := normalizeFact("DCAI", "sample", []byte(`{
  "fact_schema_version":"source.order-fact/1.0", "fact_type":"REFUND_SUCCEEDED", "occurred_at":"2026-11-05T03:00:00Z",
  "order":{"source_order_id":"sample"}, "source_record":{"source_table":"refunds", "record_id":"1", "record_version":"2", "updated_at":"2026-11-05T03:00:00Z"},
  "business":{"source_domain_code":"VIP","source_product_code":"VIP"}, "amounts":{"refund_minor":"400"},
  "refund":{"source_refund_id":"r1","refunded_at":"2026-08-05T03:00:00Z"},
  "relations":[{"relation":"REFUNDS_PAYMENT","target_source_order_id":"original","target_source_line_id":"line","target_source_record_id":"payment"}]
}`))
	if err != nil || item.RefundID != "r1" || item.OccurredAt.Format(time.DateOnly) != "2026-08-05" ||
		item.AmountMinor == nil || *item.AmountMinor != 400 || len(item.RefundsPayment) != 1 ||
		item.RefundsPayment[0] != (sourcePaymentReference{OrderID: "original", LineID: "line", RecordID: "payment"}) {
		t.Fatalf("refund=%+v, error=%v", item, err)
	}
}

func TestRefundDeltaUsesFrozenPolicyAndAccumulatesUntilFull(t *testing.T) {
	for _, test := range []struct {
		name                                      string
		policy                                    component
		base, gross, cumulative, previously, want int64
	}{
		{"first partial refund", component{Mode: "rate", Value: 3333, RefundPolicy: "proportional"}, 105, 35, 34, 0, -11},
		{"residual on full refund", component{Mode: "rate", Value: 3333, RefundPolicy: "proportional"}, 105, 35, 105, 11, -24},
		{"same accumulated refund", component{Mode: "rate", Value: 3333, RefundPolicy: "proportional"}, 105, 35, 105, 35, 0},
		{"reversal on partial", component{Mode: "fixed", Value: 501, RefundPolicy: "full_reversal"}, 1000, 501, 1, 0, -501},
		{"fixed amount prorated on remaining base", component{Mode: "fixed", Value: 501, RefundPolicy: "proportional"}, 1000, 501, 500, 0, -250},
		{"explicitly no recovery", component{Mode: "rate", Value: 1000, RefundPolicy: "non_refundable"}, 1000, 100, 1000, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := refundDelta(test.policy, test.base, test.gross, test.cumulative, test.previously)
			if err != nil || got != test.want {
				t.Fatalf("delta=%d error=%v", got, err)
			}
		})
	}
	if _, err := refundDelta(component{Mode: "rate", Value: 1000}, 1000, 100, 200, 0); !errors.Is(err, errRefundPolicy) {
		t.Fatalf("legacy rule inferred a refund policy: %v", err)
	}
	if _, err := refundDelta(component{Mode: "rate", Value: 1000, RefundPolicy: "proportional"}, 1000, 100, 1001, 0); !errors.Is(err, errRefundAmount) {
		t.Fatalf("excess refund accepted: %v", err)
	}
}
