package main

import (
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
)

const draftPrecision = 10

// Exact values use minor units until storage, where the decimal is expressed in yuan.
func exactMinor(base, rate, scale int64) *big.Rat {
	return new(big.Rat).Mul(big.NewRat(base, 1), big.NewRat(rate, scale))
}

func exactProportion(amount *big.Rat, part, whole int64) *big.Rat {
	if whole == 0 {
		return new(big.Rat)
	}
	return new(big.Rat).Mul(amount, big.NewRat(part, whole))
}

func preciseYuan(amount *big.Rat) string {
	if amount == nil {
		return ""
	}
	return exactProportion(amount, 1, 100).FloatString(draftPrecision)
}

func preciseDraftAmount(value string, posted int64) (pgtype.Numeric, error) {
	if value == "" {
		value = preciseYuan(big.NewRat(posted, 1))
	}
	var result pgtype.Numeric
	err := result.Scan(value)
	return result, err
}

func preciseRefundAmount(policy component, original refundObligation, principal, deducted int64, posted int64) string {
	if policy.RefundPolicy != "proportional" {
		return ""
	}
	delta := exactMinor(original.Accrued, principal, original.Base)
	delta.Sub(big.NewRat(deducted, 1), delta)
	if delta.Sign() > 0 && posted == 0 {
		return preciseYuan(new(big.Rat))
	}
	return preciseYuan(delta)
}

func preciseLessPosted(target string, posted int64) string {
	if value, ok := new(big.Rat).SetString(target); ok {
		return value.Sub(value, big.NewRat(posted, 100)).FloatString(draftPrecision)
	}
	return ""
}
