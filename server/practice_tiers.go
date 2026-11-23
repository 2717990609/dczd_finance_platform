package main

import (
	"slices"
	"sort"
	"time"
)

type practiceSalesTiers struct {
	IncludeRetrain    bool                       `json:"includeRetrain,omitempty"`
	CumulativeMetric  practiceAmountMetric       `json:"cumulativeMetric,omitempty"`
	CategorySchedules []practiceCategorySchedule `json:"categorySchedules,omitempty"`
	CompanySchedules  []practiceCompanySchedule  `json:"companySchedules,omitempty"`
	PersonSchedules   []practicePersonSchedule   `json:"personSchedules,omitempty"`
	OpeningID         int64                      `json:"openingId,omitempty"`
	CountFromOrdinal  int                        `json:"countFromOrdinal,omitempty"`
	CountFromAt       *time.Time                 `json:"countFromAt,omitempty"`
	Count             []practiceTier             `json:"count,omitempty"`
	Performance       []practiceTier             `json:"performance,omitempty"`
	PersonCountStarts []practicePersonCountStart `json:"personCountStarts,omitempty"`
}

type practiceAmountMetric string

func (m practiceAmountMetric) includes(member string) bool {
	return member == "new" && (m == "cumulative_new" || m == "cumulative_combined") ||
		member == "retrain" && (m == "cumulative_retrain" || m == "cumulative_combined")
}

func (t practiceSalesTiers) usesPriorTiers() bool {
	return t.CumulativeMetric != "" || t.hasCountTiers()
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
	From    int64               `json:"from"`
	Through *int64              `json:"through,omitempty"`
	Mode    string              `json:"mode"`
	Value   int64               `json:"value"`
	Retrain *practiceTierPayout `json:"retrain,omitempty"`
}

type practiceTierPayout struct {
	Mode  string `json:"mode"`
	Value int64  `json:"value"`
}

func (r practiceTier) payout(retrain bool) practiceTier {
	if retrain && r.Retrain != nil {
		r.Mode, r.Value = r.Retrain.Mode, r.Retrain.Value
	}
	return r
}

type practiceTierSchedule struct {
	Count       []practiceTier `json:"count,omitempty"`
	Performance []practiceTier `json:"performance,omitempty"`
}

type practiceCategorySchedule struct {
	SalesCategoryIDs []string `json:"salesCategoryIds"`
	practiceTierSchedule
}

type practiceCompanySchedule struct {
	CompanyID   string         `json:"companyId,omitempty"`
	CompanyIDs  []string       `json:"companyIds,omitempty"`
	Count       []practiceTier `json:"count,omitempty"`
	Performance []practiceTier `json:"performance,omitempty"`
}

func (s practiceCompanySchedule) companyIDs() []string {
	if s.CompanyID != "" {
		return []string{s.CompanyID}
	}
	return s.CompanyIDs
}

type practicePersonSchedule struct {
	PersonID int64 `json:"personId"`
	practiceTierSchedule
}

type practiceTierScheduleIndex struct {
	categories map[string]practiceTierSchedule
	companies  map[string]practiceTierSchedule
	people     map[int64]practiceTierSchedule
}

func (t practiceSalesTiers) schedules() practiceTierScheduleIndex {
	result := practiceTierScheduleIndex{
		categories: make(map[string]practiceTierSchedule, len(t.CategorySchedules)),
		companies:  make(map[string]practiceTierSchedule, len(t.CompanySchedules)+1),
		people:     make(map[int64]practiceTierSchedule, len(t.PersonSchedules)),
	}
	if len(t.Count)+len(t.Performance) != 0 {
		result.companies[""] = practiceTierSchedule{Count: t.Count, Performance: t.Performance}
	}
	for _, schedule := range t.CategorySchedules {
		for _, category := range schedule.SalesCategoryIDs {
			result.categories[category] = schedule.practiceTierSchedule
		}
	}
	for _, schedule := range t.CompanySchedules {
		tiers := practiceTierSchedule{Count: schedule.Count, Performance: schedule.Performance}
		for _, company := range schedule.companyIDs() {
			result.companies[company] = tiers
		}
	}
	for _, schedule := range t.PersonSchedules {
		result.people[schedule.PersonID] = schedule.practiceTierSchedule
	}
	return result
}

func (s practiceTierScheduleIndex) forSale(person int64, company, category string) (practiceTierSchedule, bool) {
	if schedule, ok := s.people[person]; ok {
		return schedule, true
	}
	if schedule, ok := s.companies[company]; company != "" && ok {
		return schedule, true
	}
	if schedule, ok := s.categories[category]; ok {
		return schedule, true
	}
	schedule, ok := s.companies[""]
	return schedule, ok
}

func (t practiceSalesTiers) hasCountTiers() bool {
	if len(t.Count) != 0 {
		return true
	}
	for _, schedule := range t.CategorySchedules {
		if len(schedule.Count) != 0 {
			return true
		}
	}
	for _, schedule := range t.CompanySchedules {
		if len(schedule.Count) != 0 {
			return true
		}
	}
	for _, schedule := range t.PersonSchedules {
		if len(schedule.Count) != 0 {
			return true
		}
	}
	return false
}

type practiceTierPurchase struct {
	EnrollmentID    int64
	SequenceKey     string
	CompanyID       string
	SalesCategoryID string
	PaidAt          time.Time
	PersonID        int64
	Base            int64
	Retrain         bool
	MeasureOnly     bool
}

func (part component) tierIncludesRetrain() bool {
	return part.Mode == "tiered" && part.Tiers != nil && part.Tiers.IncludeRetrain
}

func (part component) matchesPracticeMember(member string) bool {
	return part.ClassMember == "" || part.ClassMember == member || member == "retrain" && part.tierIncludesRetrain()
}

func validPracticeSalesTiers(part component) bool {
	tiers := part.Tiers
	if part.Type != "sales" || part.SourceRole == "" ||
		part.Value != 0 || tiers == nil || len(tiers.Count)+len(tiers.Performance)+len(tiers.CategorySchedules)+len(tiers.CompanySchedules)+len(tiers.PersonSchedules) == 0 {
		return false
	}
	if tiers.IncludeRetrain && part.ClassMember != "new" {
		return false
	}
	switch {
	case tiers.OpeningID < 0:
		return false
	case tiers.CumulativeMetric != "":
		if !tiers.CumulativeMetric.includes("new") && !tiers.CumulativeMetric.includes("retrain") ||
			tiers.hasCountTiers() || tiers.OpeningID != 0 || !tiers.defaultCountStart().valid() {
			return false
		}
	case !tiers.usesPriorTiers():
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
	categories := make(map[string]bool)
	for _, category := range tiers.CategorySchedules {
		if len(category.SalesCategoryIDs) == 0 || len(category.Count)+len(category.Performance) == 0 ||
			!category.practiceTierSchedule.valid(tiers.IncludeRetrain) {
			return false
		}
		for _, id := range category.SalesCategoryIDs {
			normalized, ok := canonicalSourceUUID(id)
			if !ok || normalized != id || categories[id] {
				return false
			}
			categories[id] = true
		}
	}
	companies := make(map[string]bool, len(tiers.CompanySchedules))
	for _, company := range tiers.CompanySchedules {
		ids := company.companyIDs()
		schedule := practiceTierSchedule{Count: company.Count, Performance: company.Performance}
		if len(ids) == 0 || company.CompanyID != "" && len(company.CompanyIDs) != 0 ||
			len(schedule.Count)+len(schedule.Performance) == 0 || !schedule.valid(tiers.IncludeRetrain) {
			return false
		}
		for _, id := range ids {
			if !textOK(id, 128) || companies[id] {
				return false
			}
			companies[id] = true
		}
	}
	clear(seen)
	for _, person := range tiers.PersonSchedules {
		if person.PersonID < 1 || seen[person.PersonID] || len(person.Count)+len(person.Performance) == 0 ||
			!person.practiceTierSchedule.valid(tiers.IncludeRetrain) {
			return false
		}
		seen[person.PersonID] = true
	}
	return (practiceTierSchedule{Count: tiers.Count, Performance: tiers.Performance}).valid(tiers.IncludeRetrain)
}

func (s practiceTierSchedule) valid(includeRetrain bool) bool {
	return validPracticeTierRows(s.Count, includeRetrain) && validPracticeTierRows(s.Performance, includeRetrain)
}

func validPracticeTierRows(rows []practiceTier, includeRetrain bool) bool {
	for i, row := range rows {
		if row.From < 0 || row.Value < 0 || (row.Through != nil && *row.Through < row.From) {
			return false
		}
		if i > 0 && (row.From <= rows[i-1].From || (rows[i-1].Through != nil && *rows[i-1].Through >= row.From)) {
			return false
		}
		if !(practiceTierPayout{Mode: row.Mode, Value: row.Value}).valid() ||
			(row.Retrain != nil) != includeRetrain || row.Retrain != nil && !row.Retrain.valid() {
			return false
		}
	}
	return true
}

func (p practiceTierPayout) valid() bool {
	return p.Value >= 0 && (p.Mode == "rate" && p.Value <= 10000 || p.Mode == "fixed" && p.Value <= 100000000000)
}

func practiceTierUsesCategories(rule paymentRule) bool {
	return slices.ContainsFunc(rule.Components, func(part component) bool {
		return part.Tiers != nil && len(part.Tiers.CategorySchedules) != 0
	})
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
		schedule, ok := schedules.forSale(purchase.PersonID, purchase.CompanyID, purchase.SalesCategoryID)
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
		schedule, ok := schedules.forSale(purchase.PersonID, purchase.CompanyID, purchase.SalesCategoryID)
		if !ok {
			continue
		}
		metric := metrics[purchase.PersonID]
		count, err := addMinor(prior[purchase.PersonID], metric.seen)
		if err != nil {
			return nil, err
		}
		if !purchase.Retrain {
			metric.seen++
			if metric.needFull {
				metric.full, err = addMinor(metric.full, purchase.Base)
				if err != nil {
					return nil, err
				}
			}
			// The shared fallback amount uses the first eligible sale's count schedule.
			if _, covered := matchingPracticeTier(metric.firstCount, count); metric.needFallback && !covered {
				metric.fallback, err = addMinor(metric.fallback, purchase.Base)
				if err != nil {
					return nil, err
				}
			}
		}
		if purchase.MeasureOnly {
			continue
		}
		if tier, matched := matchingPracticeTier(schedule.Count, count); matched {
			awards[purchase.EnrollmentID] = tier.payout(purchase.Retrain)
		} else if len(schedule.Performance) != 0 {
			fallback = append(fallback, purchase)
		}
	}
	for _, purchase := range fallback {
		schedule, _ := schedules.forSale(purchase.PersonID, purchase.CompanyID, purchase.SalesCategoryID)
		metric := metrics[purchase.PersonID]
		performance := metric.full
		if len(schedule.Count) != 0 {
			performance = metric.fallback
		}
		if tier, matched := matchingPracticeTier(schedule.Performance, performance); matched {
			awards[purchase.EnrollmentID] = tier.payout(purchase.Retrain)
		}
	}
	return awards, nil
}

func practiceCumulativeAmountTotals(config practiceSalesTiers, purchases []practiceTierPurchase, prior map[int64]int64) (map[int64]int64, error) {
	schedules := config.schedules()
	totals := make(map[int64]int64, len(prior))
	for _, purchase := range purchases {
		if _, configured := schedules.forSale(purchase.PersonID, purchase.CompanyID, purchase.SalesCategoryID); !configured {
			continue
		}
		person := purchase.PersonID
		if _, seen := totals[person]; !seen {
			totals[person] = prior[person]
		}
		member := "new"
		if purchase.Retrain {
			member = "retrain"
		}
		if config.CumulativeMetric.includes(member) {
			next, err := addMinor(totals[person], purchase.Base)
			if err != nil {
				return nil, err
			}
			totals[person] = next
		}
	}
	return totals, nil
}

func choosePracticeCumulativeAmountTiers(config practiceSalesTiers, purchases []practiceTierPurchase, totals map[int64]int64) map[int64]practiceTier {
	schedules := config.schedules()
	awards := make(map[int64]practiceTier)
	for _, purchase := range purchases {
		if purchase.MeasureOnly {
			continue
		}
		schedule, configured := schedules.forSale(purchase.PersonID, purchase.CompanyID, purchase.SalesCategoryID)
		if tier, matched := matchingPracticeTier(schedule.Performance, totals[purchase.PersonID]); configured && matched {
			awards[purchase.EnrollmentID] = tier.payout(purchase.Retrain)
		}
	}
	return awards
}
