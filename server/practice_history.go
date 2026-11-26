package main

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type practiceHistoryPeriod struct {
	rosterID            int64
	ordinal             int
	startsAt            *time.Time
	endsOn              time.Time
	notEarlierInTime    bool
	companyOffers       *practiceCompanyOffers
	categories          map[string]string
	settled             bool
	personalRole        string
	retrainRole         string
	scopedTierCompanies []string
	retrainCompanies    []string
	ruleKnown           bool
	refundCutoff        *time.Time
}

type practiceTierHistory struct {
	arrangements   map[string]practiceTierHistory
	current        practiceHistoryPeriod
	periods        []practiceHistoryPeriod
	members        []practiceEnrollment
	opening        *practiceCountOpening
	replaying      bool
	legacyMeasures bool
	frozenPrior    map[int64]int64
	frozenAmounts  map[int64]int64
	repriceAmounts bool
	liveCategories map[string]string
	cumulative     practiceAmountMetric
	bookedRefunds  map[int64]int64
}

type practiceFrozenTier struct {
	PriorCounts map[int64]int64 `json:"priorCounts,omitempty"`
	Amounts     map[int64]int64 `json:"tierAmounts,omitempty"`
}

func practiceTierArrangementKey(parts []component, index int) string {
	part := parts[index]
	for _, prior := range parts[:index] {
		if prior.Tiers != nil && prior.ClassMember == part.ClassMember {
			return practicePeriodComponentKey(part, index)
		}
	}
	return part.ClassMember
}

func historicalPracticeSale(parts []component, current component, member string) component {
	var fallback component
	for _, part := range parts {
		if part.Type != "sales" || part.SourceRole == "" || !part.matchesPracticeMember(member) {
			continue
		}
		if fallback.SourceRole == "" {
			fallback = part
		}
		if part.SourceRole == current.SourceRole && part.arrangementLabel() == current.arrangementLabel() &&
			slices.Equal(part.salesCompanies(), current.salesCompanies()) {
			return part
		}
	}
	return fallback
}

func (h practiceTierHistory) defaultStart(config practiceSalesTiers) practiceCountStart {
	if h.opening != nil {
		return practiceCountStart{FromAt: &h.opening.throughAt, after: true}
	}
	return config.defaultCountStart()
}

func (h practiceTierHistory) includesPrior(start practiceCountStart, period practiceHistoryPeriod) bool {
	if start.FromAt == nil {
		return period.ordinal < h.current.ordinal && start.includes(period)
	}
	return !period.notEarlierInTime && start.includes(period)
}

func (h practiceTierHistory) window(start practiceCountStart) (map[int64]bool, bool) {
	if !start.includes(h.current) {
		return nil, false
	}
	rosters := make(map[int64]bool)
	for _, period := range h.periods {
		if !h.includesPrior(start, period) {
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
	totals           map[int64]map[int64]int64
	incomplete       map[int64]bool
	incompleteSeller map[practicePriorSeller]bool
	enrollmentFrom   map[string]int64
	paymentFrom      map[string]int64
}

type practicePriorSeller struct{ roster, person int64 }

func indexPracticePrior(history practiceTierHistory, part component, orders map[string][]paymentInput,
	identities map[identityKey][]identityAssignment, blocked map[int64]string, refunds map[int64][]practiceRefundMatch,
	runtime practiceDraftRuntime) (practicePriorIndex, error) {
	index := practicePriorIndex{
		totals: make(map[int64]map[int64]int64), incomplete: make(map[int64]bool),
		incompleteSeller: make(map[practicePriorSeller]bool),
		enrollmentFrom:   make(map[string]int64), paymentFrom: make(map[string]int64),
	}
	historical := make(map[int64]practiceHistoryPeriod, len(history.periods))
	cutoffs := make(map[int64]*time.Time)
	for _, period := range history.periods {
		historical[period.rosterID] = period
		if period.refundCutoff != nil {
			cutoffs[period.rosterID] = period.refundCutoff
		}
	}
	past, err := preparePracticePurchases(history.members, orders, blocked, refunds, false, cutoffs)
	if err != nil {
		return index, err
	}
	arrangementCompanies := part.legacySalesCompanyFilter()
	countAllNewSignups := part.PracticeSignupBasis && history.cumulative == ""
	for _, purchase := range past {
		member := purchase.member
		kind := classMember(member.MembershipType)
		if purchase.noReferrer || purchase.issue == "" && len(purchase.payments) == 0 || purchase.issue == practicePaymentAlreadySettled ||
			history.cumulative == "" && kind != "new" ||
			history.cumulative != "" && !history.cumulative.includes(kind) {
			continue
		}
		roster := member.RosterID
		period := historical[roster]
		categories := period.categories
		if !period.settled {
			categories = history.liveCategories
		}
		purchase.classifyCompanyWithOrganizations(period.companyOffers, categories, identities, runtime)
		if purchase.companyAward != nil && !countAllNewSignups {
			continue
		}
		companies, periodRole := period.scopedTierCompanies, period.personalRole
		if kind == "retrain" {
			companies, periodRole = period.retrainCompanies, period.retrainRole
		}
		if countAllNewSignups {
			if purchase.companyAward != nil {
				periodRole = purchase.companyAward.role
			}
		} else {
			inScope, scopeIssue := purchase.matchesSalesCompanies(companies)
			if inScope {
				inScope, scopeIssue = purchase.matchesSalesCompanies(arrangementCompanies)
			}
			if scopeIssue != "" {
				index.incomplete[roster] = true
			}
			if !inScope {
				continue
			}
		}
		if periodRole == "" {
			if !countAllNewSignups && runtime.companyOffers != nil && (!period.ruleKnown || period.companyOffers != nil) {
				index.incomplete[roster] = true
				continue
			}
			periodRole = part.SourceRole
		}
		if !countAllNewSignups && part.SaleScope != nil && purchase.issue == "" {
			historicalPart := part
			historicalPart.SourceRole = periodRole
			selected, scopeIssue := purchase.scopedSale(historicalPart, identities, runtime)
			if scopeIssue != "" {
				if person := practicePurchaseOwner(purchase, periodRole, identities); person != 0 {
					index.incompleteSeller[practicePriorSeller{roster, person}] = true
				} else {
					index.incomplete[roster] = true
				}
				continue
			}
			if len(selected.payments) == 0 {
				continue
			}
			purchase = selected
		}
		person := practicePurchaseOwner(purchase, periodRole, identities)
		if person == 0 || !countAllNewSignups && purchase.companyRoutingIssue != "" {
			index.incomplete[roster] = true
			continue
		}
		index.addReference(index.enrollmentFrom, member.SourceEnrollmentID, roster)
		for _, payment := range purchase.payments {
			index.addReference(index.paymentFrom, payment.Fact.PaymentID, roster)
		}
		if index.totals[roster] == nil {
			index.totals[roster] = make(map[int64]int64)
		}
		value := int64(1)
		if history.cumulative != "" {
			value = purchase.base
			for _, payment := range purchase.payments {
				refund := history.bookedRefunds[payment.ID]
				if refund > purchase.netAmount(payment) {
					index.incomplete[roster] = true
					break
				}
				value -= refund
			}
		}
		index.totals[roster][person], err = addMinor(index.totals[roster][person], value)
		if err != nil {
			return index, err
		}
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

func (index practicePriorIndex) personTotal(person int64, window map[int64]bool) (int64, bool, error) {
	var total int64
	for roster := range window {
		if index.incomplete[roster] || index.incompleteSeller[practicePriorSeller{roster, person}] {
			return 0, false, nil
		}
		var err error
		total, err = addMinor(total, index.totals[roster][person])
		if err != nil {
			return 0, false, err
		}
	}
	return total, true, nil
}

// The settlement's refund-period performance row supplies the principal and
// actual booking month. Match by the payment's stable source key so an
// attribution-only revision can use the same booked refund.
func readPracticeCumulativeRefunds(ctx context.Context, tx pgx.Tx, periodID int64, paymentIDs []int64) (map[int64]int64, error) {
	if len(paymentIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT input.id,sum((refund.fact->>'amountMinor')::bigint)::bigint
	 FROM periods p JOIN source_order_versions input ON input.id=ANY($2::bigint[])
	 JOIN source_order_versions paid ON (paid.source,paid.order_id,paid.line_id,paid.record_key,paid.kind)=
	  (input.source,input.order_id,input.line_id,input.record_key,input.kind)
	 JOIN calculation_draft_performance booked ON booked.payment_version_id=paid.id
	 JOIN settlements closed ON closed.period_id=booked.period_id
	 JOIN periods booking ON booking.id=closed.period_id AND booking.product_id=p.product_id
	  AND booking.calendar_month IS NOT NULL AND booking.calendar_month<=`+periodReportMonthSQL+`
	 JOIN source_order_versions refund ON refund.id=booked.source_version_id AND refund.kind='REFUND_SUCCEEDED'
	 WHERE p.id=$1 GROUP BY input.id`, periodID, paymentIDs)
	if err != nil {
		return nil, err
	}
	totals := make(map[int64]int64)
	var payment, amount int64
	_, err = pgx.ForEachRow(rows, []any{&payment, &amount}, func() error {
		totals[payment] = amount
		return nil
	})
	return totals, err
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
