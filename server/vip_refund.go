package main

import (
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

type vipRefundTerms struct {
	Mode string
}

func vipRefundTermsByRule(definitions map[string]json.RawMessage) map[pgtype.UUID]vipRefundTerms {
	result := make(map[pgtype.UUID]vipRefundTerms, len(definitions))
	for key, raw := range definitions {
		var rule pgtype.UUID
		var definition struct {
			Mode string `json:"refund_policy"`
		}
		if rule.Scan(key) == nil && json.Unmarshal(raw, &definition) == nil {
			result[rule] = vipRefundTerms{Mode: definition.Mode}
		}
	}
	return result
}

func (t vipRefundTerms) apply(line *refundObligation, rate int64) {
	mode := strings.ToLower(t.Mode)
	if !validRefundPolicy(mode) || mode == "" || rate < 0 || rate > 10000 {
		return
	}
	line.Policy = component{RefundPolicy: "proportional", Mode: "rate", Value: rate}
}
