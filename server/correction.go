package main

import (
	"math"
	"slices"
)

type recipientDelta struct {
	PersonID int64
	Amount   int64
}

func addMinor(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right || right < 0 && left < math.MinInt64-right {
		return 0, errCalculationOverflow
	}
	return left + right, nil
}

func recommendationDeltas(recorded, desired map[int64]int64, recovery map[int64]string) ([]recipientDelta, bool, error) {
	people := make([]int64, 0, len(recorded)+len(desired))
	for person := range recorded {
		people = append(people, person)
	}
	for person := range desired {
		if _, exists := recorded[person]; !exists {
			people = append(people, person)
		}
	}
	slices.Sort(people)
	var pending bool
	var deltas []recipientDelta
	for _, person := range people {
		amount := recorded[person]
		want := desired[person]
		if amount == math.MinInt64 {
			return nil, false, errCalculationOverflow
		}
		delta, err := addMinor(want, -amount)
		if err != nil {
			return nil, false, err
		}
		if delta < 0 && recovery[person] != "recover" {
			pending = pending || recovery[person] != "departed_unreachable"
			continue
		}
		if delta != 0 {
			deltas = append(deltas, recipientDelta{PersonID: person, Amount: delta})
		}
	}
	return deltas, pending, nil
}
