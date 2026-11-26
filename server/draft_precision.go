package main

import (
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
)

const draftPrecision = 10

const (
	postingModeTruncate = "TRUNCATE"
	postingModeLegacy   = "HALF_UP"
)

// The live ledger posts integer cents toward zero. A frozen class may replay
// the old arithmetic because its refund still refers to the original award.
type postingRounding bool

const (
	truncatePosting postingRounding = false
	legacyPosting   postingRounding = true
)

func supportedPostingMode(mode string, scale int) bool {
	return scale == 2 && (mode == postingModeTruncate || mode == postingModeLegacy)
}

func (r postingRounding) rate(base, rate, scale int64) int64 {
	// Rates are bounded by their scale. Split to avoid overflowing base*rate.
	var offset int64
	if r == legacyPosting {
		offset = scale / 2
	}
	return (base/scale)*rate + ((base%scale)*rate+offset)/scale
}

func (r postingRounding) proportion(amount, part, whole int64) int64 {
	if whole == 0 {
		return 0
	}
	var value big.Int
	value.Mul(big.NewInt(amount), big.NewInt(part))
	if r == legacyPosting {
		value.Add(&value, big.NewInt(whole/2))
	}
	return value.Quo(&value, big.NewInt(whole)).Int64()
}

func truncateExactMinor(amount *big.Rat) int64 {
	return new(big.Int).Quo(amount.Num(), amount.Denom()).Int64()
}

// When the payment has yet to post, apply refunds to the exact entitlement
// before taking its cents. A later refund uses the original posted cents.
func (r postingRounding) unpostedNet(award, refunded, paid int64) int64 {
	if refunded == 0 {
		return award
	}
	if r == legacyPosting {
		return award - r.proportion(award, refunded, paid)
	}
	return r.proportion(award, paid-refunded, paid)
}

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
