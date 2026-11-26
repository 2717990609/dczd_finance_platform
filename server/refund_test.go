package main

import (
	"errors"
	"testing"
	"time"
)

func TestSourceRefundCheckUsesLatestSuccessfulAmountsAndOriginalPayment(t *testing.T) {
	at := time.Date(2026, 8, 5, 0, 0, 0, 0, shanghai)
	amount := func(n int64) *int64 { return &n }
	paid := sourceFact{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: "PRACTICE",
		OrderID: "sample-order", RecordKey: "orders:501", OccurredAt: at, UpdatedAt: at, AmountMinor: amount(100000)}
	first := sourceFact{Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: paid.Domain, SourceKey: paid.SourceKey,
		OrderID: paid.OrderID, RecordKey: "refund:1001", RefundID: "first", AmountMinor: amount(50000),
		OccurredAt: at.Add(time.Hour), UpdatedAt: at.Add(time.Hour),
		RefundsPayment: []sourcePaymentReference{{OrderID: paid.OrderID, RecordID: "501"}}}
	old := first
	old.AmountMinor, old.UpdatedAt = amount(20000), at
	second := first
	second.RefundID, second.RecordKey, second.OccurredAt = "second", "refund:1002", at.Add(2*time.Hour)
	unlinked := first
	unlinked.RefundsPayment = []sourcePaymentReference{{OrderID: paid.OrderID, RecordID: "502"}}
	anotherPayment := paid
	anotherPayment.RecordKey, anotherPayment.AmountMinor = "orders:502", amount(20000)
	anotherRefund := second
	anotherRefund.RefundsPayment, anotherRefund.AmountMinor = unlinked.RefundsPayment, amount(20000)
	ambiguous := first
	ambiguous.RefundsPayment = []sourcePaymentReference{{OrderID: paid.OrderID}}
	for _, tc := range []struct {
		name     string
		facts    []sourceFact
		blockers []string
		status   string
		refunded int64
	}{
		{"partial latest", []sourceFact{paid, first, old}, nil, "PARTIAL", 50000},
		{"full after two successful refunds", []sourceFact{paid, first, second}, nil, "FULL", 100000},
		{"refund still processing", []sourceFact{paid}, []string{"REFUND_PROCESSING"}, "PROCESSING", 0},
		{"source refund details incomplete", []sourceFact{paid}, []string{"REFUND_EVIDENCE_MISSING"}, "NEEDS_REVIEW", 0},
		{"no matching payment record", []sourceFact{paid, unlinked}, nil, "NEEDS_REVIEW", 0},
		{"separate paid legs", []sourceFact{paid, anotherPayment, first, anotherRefund}, nil, "PARTIAL", 70000},
		{"multiple legs require a unique match", []sourceFact{paid, anotherPayment, ambiguous}, nil, "NEEDS_REVIEW", 0},
		{"no successful refund returned", []sourceFact{paid}, nil, "NO_SUCCEEDED_REFUND", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := checkSourceOrderRefunds(tc.facts, tc.blockers)
			if result == nil || result.Status != tc.status || result.Refunded != tc.refunded {
				t.Fatalf("source refund check = %+v", result)
			}
		})
	}
}

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
		{"sales on half of 1000 yuan", component{Mode: "rate", Value: 1000, RefundPolicy: "proportional"}, 100000, 10000, 50000, 0, -5000},
		{"delivery on half of 1000 yuan", component{Mode: "rate", Value: 100, RefundPolicy: "proportional"}, 100000, 1000, 50000, 0, -500},
		{"half-cent waits for the next cumulative deduction", component{Mode: "rate", Value: 1000, RefundPolicy: "proportional"}, 10, 1, 5, 0, 0},
		{"residual cents already deducted", component{Mode: "rate", Value: 1000, RefundPolicy: "proportional"}, 10, 1, 10, 1, 0},
		{"residual on full refund", component{Mode: "rate", Value: 3333, RefundPolicy: "proportional"}, 105, 35, 105, 11, -24},
		{"same accumulated refund", component{Mode: "rate", Value: 3333, RefundPolicy: "proportional"}, 105, 35, 105, 35, 0},
		{"reversal on partial", component{Mode: "fixed", Value: 501, RefundPolicy: "full_reversal"}, 1000, 501, 1, 0, -501},
		{"fixed amount prorated on frozen award", component{Mode: "fixed", Value: 501, RefundPolicy: "proportional"}, 1000, 501, 500, 0, -250},
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
		t.Fatalf("inferred a refund policy for a rule without one: %v", err)
	}
	if _, err := refundDelta(component{Mode: "rate", Value: 1000, RefundPolicy: "proportional"}, 1000, 100, 1001, 0); !errors.Is(err, errRefundAmount) {
		t.Fatalf("excess refund accepted: %v", err)
	}
}
