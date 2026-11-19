package main

import (
	"slices"
	"sort"
	"time"
)

type practiceSalesTiers struct {
	CompanySchedules  []practiceCompanySchedule  `json:"companySchedules,omitempty"`
	OpeningID         int64                      `json:"openingId,omitempty"`
	CountFromOrdinal  int                        `json:"countFromOrdinal,omitempty"`
	CountFromAt       *time.Time                 `json:"countFromAt,omitempty"`
	Count             []practiceTier             `json:"count,omitempty"`
	Performance       []practiceTier             `json:"performance,omitempty"`
	PersonCountStarts []practicePersonCountStart `json:"personCountStarts,omitempty"`
}

type practiceCountStart struct {
	FromOrdinal int        `json:"countFromOrdinal,omitempty"`
	FromAt      *time.Time `json:"countFromAt,omitempty"`
	after       bool
}

type practicePersonCountStart struct {
	PersonID int64 `json:"personId"`
	practiceCountStart
}

func (s practiceCountStart) valid() bool {
	if s.FromAt == nil {
		return s.FromOrdinal > 0
	}
	return s.FromOrdinal == 0 && !s.FromAt.IsZero()
}

func (s practiceCountStart) includes(period practiceHistoryPeriod) bool {
	if s.FromAt == nil {
		return period.ordinal >= s.FromOrdinal
	}
	if period.rosterID != 0 {
		if s.after {
			return period.startsAt.After(*s.FromAt)
		}
		return !period.startsAt.Before(*s.FromAt)
	}
	return period.endsOn.Format(time.DateOnly) >= s.FromAt.In(shanghai).Format(time.DateOnly)
}

func (t practiceSalesTiers) defaultCountStart() practiceCountStart {
	return practiceCountStart{FromOrdinal: t.CountFromOrdinal, FromAt: t.CountFromAt}
}

type practiceTier struct {
	From    int64  `json:"from"`
	Through *int64 `json:"through,omitempty"`
	Mode    string `json:"mode"`
	Value   int64  `json:"value"`
}

type practiceTierSchedule struct {
	Count       []practiceTier `json:"count,omitempty"`
	Performance []practiceTier `json:"performance,omitempty"`
}

type practiceCompanySchedule struct {
	CompanyID   string         `json:"companyId"`
	Count       []practiceTier `json:"count,omitempty"`
	Performance []practiceTier `json:"performance,omitempty"`
}

func (t practiceSalesTiers) schedules() map[string]practiceTierSchedule {
	result := make(map[string]practiceTierSchedule, len(t.CompanySchedules)+1)
	if len(t.Count)+len(t.Performance) != 0 {
		result[""] = practiceTierSchedule{Count: t.Count, Performance: t.Performance}
	}
	for _, schedule := range t.CompanySchedules {
		result[schedule.CompanyID] = practiceTierSchedule{Count: schedule.Count, Performance: schedule.Performance}
	}
	return result
}

func companyTierSchedule(schedules map[string]practiceTierSchedule, company string) (practiceTierSchedule, bool) {
	if schedule, ok := schedules[company]; ok {
		return schedule, true
	}
	schedule, ok := schedules[""]
	return schedule, ok
}

func (t practiceSalesTiers) hasCountTiers() bool {
	if len(t.Count) != 0 {
		return true
	}
	for _, schedule := range t.CompanySchedules {
		if len(schedule.Count) != 0 {
			return true
		}
	}
	return false
}

type practiceTierPurchase struct {
	EnrollmentID int64
	SequenceKey  string
	CompanyID    string
	PaidAt       time.Time
	PersonID     int64
	Base         int64
}

func validPracticeSalesTiers(part component) bool {
	tiers := part.Tiers
	if part.Type != "sales" || part.ClassMember != "new" || part.SourceRole == "" ||
		part.Value != 0 || tiers == nil || len(tiers.Count)+len(tiers.Performance)+len(tiers.CompanySchedules) == 0 {
		return false
	}
	switch {
	case tiers.OpeningID < 0:
		return false
	case !tiers.hasCountTiers():
		if tiers.CountFromOrdinal != 0 || tiers.CountFromAt != nil || len(tiers.PersonCountStarts) != 0 || tiers.OpeningID != 0 {
			return false
		}
	case tiers.OpeningID != 0:
		if tiers.CountFromOrdinal != 0 || tiers.CountFromAt != nil {
			return false
		}
	case !tiers.defaultCountStart().valid():
		return false
	}
	seen := make(map[int64]bool, len(tiers.PersonCountStarts))
	for _, start := range tiers.PersonCountStarts {
		if start.PersonID < 1 || seen[start.PersonID] || !start.valid() {
			return false
		}
		seen[start.PersonID] = true
	}
	companies := make(map[string]bool, len(tiers.CompanySchedules))
	for _, company := range tiers.CompanySchedules {
		if !textOK(company.CompanyID, 128) || companies[company.CompanyID] || len(company.Count)+len(company.Performance) == 0 {
			return false
		}
		companies[company.CompanyID] = true
	}
	for _, schedule := range tiers.schedules() {
		if !validPracticeTierRows(schedule.Count) || !validPracticeTierRows(schedule.Performance) {
			return false
		}
	}
	return true
}

func validPracticeTierRows(rows []practiceTier) bool {
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
	type personalMetric struct {
		firstCount             []practiceTier
		seen, full, fallback   int64
		needFull, needFallback bool
	}
	schedules := config.schedules()
	metrics := make(map[int64]*personalMetric)
	for _, purchase := range ordered {
		schedule, ok := companyTierSchedule(schedules, purchase.CompanyID)
		if !ok {
			continue
		}
		person := purchase.PersonID
		metric := metrics[person]
		if metric == nil {
			metric = &personalMetric{firstCount: schedule.Count}
			metrics[person] = metric
		}
		if len(schedule.Performance) != 0 {
			metric.needFull = metric.needFull || len(schedule.Count) == 0
			metric.needFallback = metric.needFallback || len(schedule.Count) != 0
		}
	}

	awards := make(map[int64]practiceTier, len(ordered))
	var fallback []practiceTierPurchase
	for _, purchase := range ordered {
		schedule, ok := companyTierSchedule(schedules, purchase.CompanyID)
		if !ok {
			continue
		}
		metric := metrics[purchase.PersonID]
		count, err := addMinor(prior[purchase.PersonID], metric.seen)
		if err != nil {
			return nil, err
		}
		metric.seen++
		if metric.needFull {
			metric.full, err = addMinor(metric.full, purchase.Base)
			if err != nil {
				return nil, err
			}
		}
		// The old calculator defines the shared fallback amount using the first eligible sale's count schedule.
		if _, covered := matchingPracticeTier(metric.firstCount, count); metric.needFallback && !covered {
			metric.fallback, err = addMinor(metric.fallback, purchase.Base)
			if err != nil {
				return nil, err
			}
		}
		if tier, matched := matchingPracticeTier(schedule.Count, count); matched {
			awards[purchase.EnrollmentID] = tier
		} else if len(schedule.Performance) != 0 {
			fallback = append(fallback, purchase)
		}
	}
	for _, purchase := range fallback {
		schedule, _ := companyTierSchedule(schedules, purchase.CompanyID)
		metric := metrics[purchase.PersonID]
		performance := metric.full
		if len(schedule.Count) != 0 {
			performance = metric.fallback
		}
		if tier, matched := matchingPracticeTier(schedule.Performance, performance); matched {
			awards[purchase.EnrollmentID] = tier
		}
	}
	return awards, nil
}
