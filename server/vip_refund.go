package main

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"math"
	"math/bits"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

type vipRefundTerms struct {
	Mode, SplitItem string
}

func vipRefundTermsByRule(definitions map[string]json.RawMessage) map[pgtype.UUID]vipRefundTerms {
	result := make(map[pgtype.UUID]vipRefundTerms, len(definitions))
	for key, raw := range definitions {
		var rule pgtype.UUID
		var definition struct {
			Mode  string `json:"refund_policy"`
			Split struct {
				Item string `json:"sales_item_key"`
			} `json:"internal_sales_direct_split"`
		}
		if rule.Scan(key) == nil && json.Unmarshal(raw, &definition) == nil {
			result[rule] = vipRefundTerms{definition.Mode, definition.Split.Item}
		}
	}
	return result
}

func (t vipRefundTerms) apply(line *refundObligation, rate int64) {
	mode := strings.ToLower(t.Mode)
	if !validRefundPolicy(mode) || mode == "" || rate < 0 || rate > 10000 {
		return
	}
	line.Policy = component{RefundPolicy: mode, Mode: "rate", Value: rate}
	if role, split := strings.CutPrefix(line.ComponentType, t.SplitItem+":"); t.SplitItem != "" && split {
		line.SplitRole = role
	}
}

type vipRefundShare struct {
	role                     string
	original, cap, remaining int64
}

func vipRefundGroupDeltas(order string, principal int64, lines []refundObligation, history map[refundLineKey]*refundHistory) (map[refundLineKey]int64, error) {
	var split []refundObligation
	for _, line := range lines {
		if line.SplitRole != "" {
			split = append(split, line)
		}
	}
	if len(split) == 0 {
		return nil, nil
	}
	first := split[0]
	previous := history[first.key()].principal
	var original, deducted int64
	shares := make([]vipRefundShare, len(split))
	roles := make(map[string]bool, len(split))
	for i, line := range split {
		h := history[line.key()]
		if line.Base != first.Base || line.Policy != first.Policy || h.principal != previous ||
			h.deducted < 0 || h.deducted > line.Accrued ||
			roles[line.SplitRole] {
			return nil, errRefundAmount
		}
		roles[line.SplitRole] = true
		var err error
		original, err = addMinor(original, line.Accrued)
		if err != nil {
			return nil, err
		}
		deducted += h.deducted
		shares[i] = vipRefundShare{role: line.SplitRole, original: line.Accrued, cap: line.Accrued - h.deducted}
	}
	groupDelta, err := refundDelta(first.Policy, first.Base, original, previous+principal, deducted)
	if err != nil {
		return nil, err
	}
	if err := allocateVIPRefundRemaining(order, original-deducted+groupDelta, shares); err != nil {
		return nil, err
	}
	result := make(map[refundLineKey]int64, len(shares))
	for i, share := range shares {
		result[split[i].key()] = share.remaining - share.cap
	}
	return result, nil
}

func allocateVIPRefundRemaining(order string, target int64, shares []vipRefundShare) error {
	var original, capacity int64
	for _, s := range shares {
		if s.cap < 0 || s.cap > s.original {
			return errRefundAmount
		}
		var err error
		original, err = addMinor(original, s.original)
		if err != nil {
			return err
		}
		capacity += s.cap
	}
	if target < 0 || target > capacity || order == "" || len(shares) == 0 {
		return errRefundAmount
	}
	if original == 0 {
		return nil
	}
	type fraction struct {
		index int
		value uint64
		tie   [32]byte
	}
	ranks := make([]fraction, len(shares))
	left := target
	for i, s := range shares {
		high, low := bits.Mul64(uint64(target), uint64(s.original))
		whole, remainder := bits.Div64(high, low, uint64(original))
		shares[i].remaining = min(int64(whole), s.cap)
		left -= shares[i].remaining
		ranks[i] = fraction{i, remainder, sha256.Sum256([]byte(order + ":" + s.role))}
	}
	slices.SortFunc(ranks, func(a, b fraction) int {
		return cmp.Or(cmp.Compare(b.value, a.value), bytes.Compare(a.tie[:], b.tie[:]))
	})
	for left > 0 {
		// Sequential one-cent rounds are equivalent to full rounds across
		// eligible shares, followed by a partial round in remainder order.
		var size int64
		room := int64(math.MaxInt64)
		for _, s := range shares {
			if free := s.cap - s.remaining; free > 0 {
				size++
				room = min(room, free)
			}
		}
		if size == 0 {
			return errRefundAmount
		}
		rounds := min(left/size, room)
		if rounds > 0 {
			for i := range shares {
				if shares[i].remaining < shares[i].cap {
					shares[i].remaining += rounds
				}
			}
			left -= rounds * size
			continue
		}
		for _, rank := range ranks {
			s := &shares[rank.index]
			if s.remaining < s.cap {
				s.remaining++
				left--
				if left == 0 {
					break
				}
			}
		}
	}
	return nil
}
