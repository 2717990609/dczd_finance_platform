package main

import "time"

type practiceHistoryPeriod struct {
	rosterID      int64
	ordinal       int
	startsAt      *time.Time
	endsOn        time.Time
	companyOffers practiceCompanyOffers
	personalRole  string
	ruleKnown     bool
}

type practiceTierHistory struct {
	current     practiceHistoryPeriod
	periods     []practiceHistoryPeriod
	members     []practiceEnrollment
	opening     *practiceCountOpening
	replaying   bool
	frozenPrior map[int64]int64
}

func (h practiceTierHistory) defaultStart(config practiceSalesTiers) practiceCountStart {
	if h.opening != nil {
		return practiceCountStart{FromAt: &h.opening.throughAt, after: true}
	}
	return config.defaultCountStart()
}

func (h practiceTierHistory) window(start practiceCountStart) (map[int64]bool, bool) {
	if !start.includes(h.current) {
		return nil, false
	}
	rosters := make(map[int64]bool)
	for _, period := range h.periods {
		if !start.includes(period) {
			continue
		}
		if period.rosterID == 0 {
			return nil, false
		}
		rosters[period.rosterID] = true
	}
	if start.FromAt == nil && len(rosters) != h.current.ordinal-start.FromOrdinal {
		return nil, false
	}
	return rosters, true
}

type practicePriorIndex struct {
	counts         map[int64]map[int64]int64
	incomplete     map[int64]bool
	enrollmentFrom map[string]int64
	paymentFrom    map[string]int64
}

func indexPracticePrior(history practiceTierHistory, role string, orders map[string][]paymentInput,
	identities map[identityKey][]identityAssignment, blocked map[int64]string, refunds map[int64][]practiceRefundMatch,
	strictCompanyScope bool) (practicePriorIndex, error) {
	index := practicePriorIndex{
		counts: make(map[int64]map[int64]int64), incomplete: make(map[int64]bool),
		enrollmentFrom: make(map[string]int64), paymentFrom: make(map[string]int64),
	}
	past, err := preparePracticePurchases(history.members, orders, blocked, refunds, false)
	if err != nil {
		return index, err
	}
	historical := make(map[int64]practiceHistoryPeriod, len(history.periods))
	for _, period := range history.periods {
		historical[period.rosterID] = period
	}
	for _, purchase := range past {
		member := purchase.member
		if classMember(member.MembershipType) != "new" || purchase.issue == practicePaymentAlreadySettled {
			continue
		}
		roster := member.RosterID
		period := historical[roster]
		purchase.classifyCompany(period.companyOffers)
		if purchase.companyAward != nil {
			continue
		}
		periodRole := period.personalRole
		if periodRole == "" {
			if strictCompanyScope && (!period.ruleKnown || period.companyOffers != nil) {
				index.incomplete[roster] = true
				continue
			}
			periodRole = role
		}
		person := practicePurchaseOwner(purchase, periodRole, identities)
		if person == 0 || purchase.companyRoutingIssue != "" {
			index.incomplete[roster] = true
			continue
		}
		index.addReference(index.enrollmentFrom, member.SourceEnrollmentID, roster)
		for _, payment := range purchase.payments {
			index.addReference(index.paymentFrom, payment.Fact.PaymentID, roster)
		}
		if index.counts[roster] == nil {
			index.counts[roster] = make(map[int64]int64)
		}
		index.counts[roster][person]++
	}
	return index, nil
}

func (index practicePriorIndex) addReference(refs map[string]int64, key string, roster int64) {
	if earlier, exists := refs[key]; exists {
		index.incomplete[earlier] = true
		index.incomplete[roster] = true
	}
	refs[key] = roster
}

func (index practicePriorIndex) personCount(person int64, window map[int64]bool) (int64, bool) {
	var count int64
	for roster := range window {
		if index.incomplete[roster] {
			return 0, false
		}
		count += index.counts[roster][person]
	}
	return count, true
}

func (index practicePriorIndex) repeats(purchase practicePurchase, window map[int64]bool) bool {
	if window[index.enrollmentFrom[purchase.member.SourceEnrollmentID]] {
		return true
	}
	for _, payment := range purchase.payments {
		if window[index.paymentFrom[payment.Fact.PaymentID]] {
			return true
		}
	}
	return false
}
