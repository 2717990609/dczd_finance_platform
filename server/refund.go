package main

import (
	"errors"
	"math/big"
	"slices"
)

var (
	errRefundPolicy = errors.New("计提时使用的规则未配置适用的退款口径")
	errRefundAmount = errors.New("退款或累计扣减金额超出原结算")
)

func validRefundPolicy(value string) bool {
	switch value {
	case "", "proportional", "full_reversal", "non_refundable":
		return true
	default:
		return false
	}
}

func refundDelta(policy component, base, accrued, principal, alreadyDeducted int64) (int64, error) {
	if base <= 0 || accrued < 0 || principal <= 0 || principal > base || alreadyDeducted < 0 || alreadyDeducted > accrued {
		return 0, errRefundAmount
	}
	var remaining int64
	switch {
	case policy.Mode == "practice_replay":
		return 0, errPracticeRefundReplay
	case policy.Mode == "practice_rate":
		remaining = practiceCompanyRate(base-principal, policy.Value)
	case policy.Mode == "practice_fixed" || policy.Mode == "practice_company_fixed":
		if principal < base {
			if policy.Mode == "practice_company_fixed" && base-principal < accrued {
				return 0, errPracticeRefundReplay
			}
			remaining = accrued
		}
	case policy.RefundPolicy == "non_refundable":
		remaining = accrued
	case policy.RefundPolicy == "full_reversal":
		remaining = 0
	case policy.RefundPolicy == "proportional":
		// Recover the cumulative proportion of the original award, down to
		// the last cent. Historical deductions keep their posted amounts.
		return -max(0, prorateMinor(accrued, principal, base)-alreadyDeducted), nil
	default:
		return 0, errRefundPolicy
	}
	if remaining > accrued-alreadyDeducted {
		return 0, errRefundAmount
	}
	return remaining - accrued + alreadyDeducted, nil
}

// prorateMinor rounds a nonnegative proportion to cents, with halves rounded up.
func prorateMinor(amount, part, whole int64) int64 {
	var value big.Int
	value.Mul(big.NewInt(amount), big.NewInt(part))
	value.Add(&value, big.NewInt(whole/2))
	return value.Quo(&value, big.NewInt(whole)).Int64()
}

type sourceRefundCheck struct {
	Status   string `json:"status"`
	Paid     int64  `json:"paidMinor"`
	Refunded int64  `json:"refundedMinor"`
}

func checkSourceOrderRefunds(facts []sourceFact, blockers []string) *sourceRefundCheck {
	if slices.Contains(blockers, "REFUND_EVIDENCE_MISSING") {
		return &sourceRefundCheck{Status: "NEEDS_REVIEW"}
	}
	if slices.Contains(blockers, "REFUND_PROCESSING") {
		return &sourceRefundCheck{Status: "PROCESSING"}
	}
	type recordKey struct{ source, order, line, record, kind string }
	latest := make(map[recordKey]sourceFact)
	for _, fact := range facts {
		switch fact.Kind {
		case "ORDER_PAID", "REFUND_SUCCEEDED", "ORDER_REFUNDED", "REFUND_ADJUSTED":
			key := recordKey{fact.Source, fact.OrderID, fact.LineID, fact.RecordKey, fact.Kind}
			if before, ok := latest[key]; !ok || !fact.UpdatedAt.Before(before.UpdatedAt) {
				latest[key] = fact
			}
		}
	}
	if len(latest) == 0 {
		return nil
	}
	pending := &sourceRefundCheck{Status: "NEEDS_REVIEW"}
	result := &sourceRefundCheck{Status: "NO_SUCCEEDED_REFUND"}
	var payments []sourceFact
	paymentIDs := make(map[string]bool)
	for _, fact := range latest {
		if fact.Kind != "ORDER_PAID" {
			continue
		}
		if fact.AmountMinor == nil || *fact.AmountMinor < 0 || fact.PaymentID != "" && paymentIDs[fact.PaymentID] {
			return pending
		}
		if len(payments) > 0 && (fact.Source != payments[0].Source || fact.Domain != payments[0].Domain ||
			fact.OrderID != payments[0].OrderID || fact.SourceKey != payments[0].SourceKey) {
			return pending
		}
		var err error
		result.Paid, err = addMinor(result.Paid, *fact.AmountMinor)
		if err != nil {
			return pending
		}
		paymentIDs[fact.PaymentID] = true
		payments = append(payments, fact)
	}
	if len(payments) == 0 {
		return pending
	}
	refundIDs := make(map[string]bool)
	perPayment := make([]int64, len(payments))
	hasRefundStatus := false
	for _, refund := range latest {
		switch refund.Kind {
		case "REFUND_ADJUSTED":
			return pending
		case "ORDER_REFUNDED":
			hasRefundStatus = true
		case "REFUND_SUCCEEDED":
			if len(refund.RefundsPayment) != 1 || refund.RefundID == "" || refundIDs[refund.RefundID] ||
				refund.AmountMinor == nil || *refund.AmountMinor <= 0 {
				return pending
			}
			index := sourceRefundPaymentIndex(refund, payments)
			if index < 0 || *refund.AmountMinor > *payments[index].AmountMinor-perPayment[index] {
				return pending
			}
			refundIDs[refund.RefundID] = true
			perPayment[index] += *refund.AmountMinor
			result.Refunded += *refund.AmountMinor
		}
	}
	switch {
	case result.Refunded == 0 && hasRefundStatus:
		return pending
	case result.Refunded > 0 && result.Refunded == result.Paid:
		result.Status = "FULL"
	case result.Refunded > 0:
		result.Status = "PARTIAL"
	}
	return result
}

func sourceRefundPaymentIndex(refund sourceFact, payments []sourceFact) int {
	ref := refund.RefundsPayment[0]
	index := -1
	for i, payment := range payments {
		if ref.OrderID != payment.OrderID || refund.Source != payment.Source || refund.Domain != payment.Domain ||
			refund.SourceKey != payment.SourceKey || refund.OccurredAt.Before(payment.OccurredAt) ||
			!refundRefMatches(refundReference{LineID: ref.LineID, RecordID: ref.RecordID}, payment) {
			continue
		}
		if index >= 0 {
			return -1
		}
		index = i
	}
	return index
}
