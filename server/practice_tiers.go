package main

import (
	"slices"
	"sort"
	"time"
)

type practiceSalesTiers struct {
	CountFromOrdinal int            `json:"countFromOrdinal,omitempty"`
	CountFromAt      *time.Time     `json:"countFromAt,omitempty"`
	Count            []practiceTier `json:"count,omitempty"`
	Performance      []practiceTier `json:"performance,omitempty"`
}

type practiceTier struct {
	From    int64  `json:"from"`
	Through *int64 `json:"through,omitempty"`
	Mode    string `json:"mode"`
	Value   int64  `json:"value"`
}

type practiceTierPurchase struct {
	EnrollmentID int64
	SequenceKey  string
	PaidAt       time.Time
	PersonID     int64
	Base         int64
}

func validPracticeSalesTiers(part component) bool {
	tiers := part.Tiers
	if part.Type != "sales" || part.ClassMember != "new" || part.SourceRole == "" ||
		part.Value != 0 || part.RefundPolicy != "" || tiers == nil || len(tiers.Count)+len(tiers.Performance) == 0 {
		return false
	}
	hasCountStart := tiers.CountFromOrdinal != 0 || tiers.CountFromAt != nil
	if hasCountStart != (len(tiers.Count) != 0) || tiers.CountFromOrdinal < 0 ||
		(tiers.CountFromAt != nil && (tiers.CountFromAt.IsZero() || tiers.CountFromOrdinal != 0)) {
		return false
	}
	for _, rows := range [][]practiceTier{tiers.Count, tiers.Performance} {
		for i, row := range rows {
			if row.From < 0 || row.Value < 0 || (row.Through != nil && *row.Through < row.From) {
				return false
			}
			if i > 0 && (row.From <= rows[i-1].From || (rows[i-1].Through != nil && *rows[i-1].Through >= row.From)) {
				return false
			}
			switch row.Mode {
			case "rate":
				if row.Value > 10000 {
					return false
				}
			case "fixed":
				if row.Value > 100000000000 {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

func practiceTierComponent(rule paymentRule) *component {
	for i := range rule.Components {
		if rule.Components[i].Mode == "tiered" {
			return &rule.Components[i]
		}
	}
	return nil
}

func matchingPracticeTier(rows []practiceTier, metric int64) (practiceTier, bool) {
	index := sort.Search(len(rows), func(i int) bool { return rows[i].From > metric }) - 1
	if index < 0 || (rows[index].Through != nil && *rows[index].Through < metric) {
		return practiceTier{}, false
	}
	return rows[index], true
}

func choosePracticeSalesTiers(config practiceSalesTiers, purchases []practiceTierPurchase, prior map[int64]int64) (map[int64]practiceTier, error) {
	ordered := slices.Clone(purchases)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if !a.PaidAt.Equal(b.PaidAt) {
			return a.PaidAt.Before(b.PaidAt)
		}
		if a.SequenceKey != b.SequenceKey {
			return a.SequenceKey < b.SequenceKey
		}
		return a.EnrollmentID < b.EnrollmentID
	})
	awards := make(map[int64]practiceTier, len(ordered))
	counts := make(map[int64]int64)
	performance := make(map[int64]int64)
	var fallback []practiceTierPurchase
	for _, purchase := range ordered {
		person := purchase.PersonID
		count, err := addMinor(prior[person], counts[person])
		if err != nil {
			return nil, err
		}
		counts[person]++
		if tier, ok := matchingPracticeTier(config.Count, count); ok {
			awards[purchase.EnrollmentID] = tier
			continue
		}
		performance[person], err = addMinor(performance[person], purchase.Base)
		if err != nil {
			return nil, err
		}
		fallback = append(fallback, purchase)
	}
	for _, purchase := range fallback {
		tier, ok := matchingPracticeTier(config.Performance, performance[purchase.PersonID])
		if ok {
			awards[purchase.EnrollmentID] = tier
		}
	}
	return awards, nil
}
