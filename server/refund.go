package main

import (
	"errors"
	"math/big"
)

var (
	errRefundPolicy = errors.New("原规则未配置适用的退款口径")
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
	switch policy.RefundPolicy {
	case "non_refundable":
		remaining = accrued
	case "full_reversal":
		remaining = 0
	case "proportional":
		if policy.Mode == "rate" {
			remaining = calculatePaymentAmount(base-principal, policy)
		} else {
			remaining = prorateMinor(accrued, base-principal, base)
		}
	default:
		return 0, errRefundPolicy
	}
	if remaining > accrued-alreadyDeducted {
		return 0, errRefundAmount
	}
	return remaining - accrued + alreadyDeducted, nil
}

// prorateMinor rounds the cumulative share to cents before adjacent shares are subtracted.
func prorateMinor(amount, part, whole int64) int64 {
	var value big.Int
	value.Mul(big.NewInt(amount), big.NewInt(part))
	value.Add(&value, big.NewInt(whole/2))
	return value.Quo(&value, big.NewInt(whole)).Int64()
}
