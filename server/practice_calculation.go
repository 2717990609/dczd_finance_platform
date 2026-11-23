package main

import (
	"slices"

	"github.com/jackc/pgx/v5/pgtype"
)

func classMember(kind int) string {
	if kind == 3 {
		return "new"
	}
	if kind == 2 {
		return "retrain"
	}
	return ""
}

func practiceIssue(code string, member practiceEnrollment, rule paymentRule, part string) draftIssue {
	return draftIssue{RuleID: rule.ID, Code: code, Component: part, EnrollmentID: member.ID}
}

type practicePurchase struct {
	member              practiceEnrollment
	payments            []paymentInput
	originalPayments    []paymentInput
	gross, base         int64
	issue               string
	issueFactID         int64
	netPayments         map[int64]int64
	refunds             map[int64][]paymentInput
	lateRefunds         []paymentInput
	companyAward        *practiceCompanyAward
	companyRoutingIssue string
	noReferrer          bool
	replaying           bool
}

func (p practicePurchase) salesIssue(code string, rule paymentRule, part string) draftIssue {
	issue := practiceIssue(code, p.member, rule, part)
	if (code == "PRACTICE_TIER_COMPANY_MISSING" || code == "MISSING_PARTICIPANT") && len(p.payments) == 1 {
		payment := p.payments[0]
		if payment.Fact.PracticeInactiveCompany.matches(p.member) {
			issue.Code, issue.FactID = "PRACTICE_SALES_COMPANY_INACTIVE", payment.ID
		}
	}
	return issue
}

type practiceDraftRuntime struct {
	enrollmentQuotes            *practiceDraftQuoteRun
	appointments                map[practiceAppointmentKey][]practiceAppointment
	orderAppointments           practiceOrderAppointments
	registrations               map[int64]practiceRegistrationInput
	salesOrganizations          practiceSalesOrganizations
	companyOffers               *practiceCompanyOffers
	companyClassifications      map[string]string
	internalSalesCategories     map[string]string
	companyRecipients           map[string][]companySourceAssignment
	recommendations             practiceCompanyRecommendations
	poolUnits                   map[int64]pgtype.UUID
	legacyGrossRateShares       bool
	legacyBenefitSalesAgreement bool
	legacyBenefitAttribution    bool
	legacyFixedTierRefund       bool
}

func preparePracticePurchases(members []practiceEnrollment, orders map[string][]paymentInput, blocked map[int64]string,
	refunds map[int64][]practiceRefundMatch, replaying bool) ([]practicePurchase, error) {
	counts := make(map[string]int)
	for _, member := range members {
		if member.CommissionEligible {
			for _, id := range member.PaidOrderIDs {
				counts[id]++
			}
		}
	}
	purchases := make([]practicePurchase, 0, len(members))
	for _, member := range members {
		if !member.CommissionEligible {
			continue
		}
		purchase := practicePurchase{member: member, issue: blocked[member.ID], replaying: replaying}
		if purchase.issue == "" {
			var err error
			purchase.payments, purchase.base, purchase.issue, err = practicePayments(member, orders, counts)
			if err != nil {
				return nil, err
			}
		}
		if purchase.issue == "" {
			purchase.noReferrer = len(purchase.payments) == 1 && purchase.payments[0].Fact.PracticeNoReferrer.matches(member)
			purchase.applyRefunds(refunds)
		}
		purchases = append(purchases, purchase)
	}
	return purchases, nil
}

func calculatePracticeDraft(members []practiceEnrollment, rule paymentRule, orders map[string][]paymentInput,
	identities map[identityKey][]identityAssignment, blocked map[int64]string, history practiceTierHistory,
	refunds map[int64][]practiceRefundMatch, runtime practiceDraftRuntime) (paymentDraft, error) {
	var draft paymentDraft
	purchases, err := preparePracticePurchases(members, orders, blocked, refunds, history.replaying)
	if err != nil {
		return draft, err
	}
	var fullyRefunded []practicePurchase
	if history.replaying {
		keepForClassFixed := slices.ContainsFunc(rule.Components, func(part component) bool {
			return part.PeriodBenefit != nil && part.PeriodBenefit.FixedPerClass
		})
		purchases = slices.DeleteFunc(purchases, func(p practicePurchase) bool {
			if p.issue != "" || len(p.payments) != 0 {
				return false
			}
			// A fully refunded signup still contributes its original weight to a fixed class award.
			if keepForClassFixed {
				fullyRefunded = append(fullyRefunded, p)
			}
			return true
		})
	}
	for i := range purchases {
		if !purchases[i].noReferrer {
			purchases[i].classifyCompany(runtime.companyOffers, runtime.companyClassifications)
		}
	}
	performancePayments := make(map[paymentKey]int)
	for _, purchase := range purchases {
		for _, payment := range purchase.payments {
			performancePayments[paymentKey{payment.Fact.Source, payment.Fact.PaymentID}]++
		}
	}
	for i := range purchases {
		p := &purchases[i]
		for _, payment := range p.payments {
			if performancePayments[paymentKey{payment.Fact.Source, payment.Fact.PaymentID}] > 1 {
				p.issue, p.issueFactID = "PRACTICE_DUPLICATE_PERFORMANCE_PAYMENT", payment.ID
				break
			}
		}
	}
	var tierResults map[string]practiceTierResult
	tierCount := 0
	for _, part := range rule.Components {
		if part.Tiers != nil {
			tierCount++
		}
	}
	if tierCount != 0 {
		tierResults = make(map[string]practiceTierResult, tierCount)
	}
	if tierCount > 1 {
		draft.practiceFrozenTiers = make(map[string]practiceFrozenTier, tierCount)
	}
	for _, part := range rule.Components {
		if part.Tiers == nil {
			continue
		}
		state := history
		if configured, ok := history.arrangements[part.ClassMember]; ok {
			state = configured
			state.replaying, state.bookedRefunds, state.liveCategories = history.replaying, history.bookedRefunds, history.liveCategories
			state.legacyMeasures = history.legacyMeasures
			state.repriceAmounts = history.repriceAmounts
		}
		awards, issues, err := practiceTierAwards(part, purchases, orders, identities, blocked, state, refunds, runtime)
		if err != nil {
			return draft, err
		}
		tierResults[part.ClassMember] = practiceTierResult{awards, issues}
		if tierCount == 1 {
			draft.practicePriorCounts, draft.practiceTierAmounts = issues.priorCounts, issues.amounts
		} else {
			draft.practiceFrozenTiers[part.ClassMember] = practiceFrozenTier{PriorCounts: issues.priorCounts, Amounts: issues.amounts}
		}
	}
	for _, purchase := range purchases {
		member, payments := purchase.member, purchase.payments
		if purchase.issue != "" {
			issue := practiceIssue(purchase.issue, member, rule, "")
			issue.FactID = purchase.issueFactID
			draft.Issues = append(draft.Issues, issue)
			continue
		}
		for _, refund := range purchase.lateRefunds {
			issue := practiceIssue("PRACTICE_REFUND_AFTER_ROSTER", member, rule, "")
			issue.FactID = refund.ID
			issue.Advisory = true
			draft.Issues = append(draft.Issues, issue)
		}
		for _, payment := range payments {
			fact := payment.Fact
			var owner int64
			if !purchase.noReferrer {
				owner = performanceOwner(fact, &rule, classMember(member.MembershipType), identities)
			}
			if purchase.companyAward != nil {
				seller, _ := paymentBeneficiary(fact, purchase.companyAward.role, identities)
				owner = seller.PersonID
			}
			draft.Performance = append(draft.Performance, draftPerformance{SourceVersionID: payment.ID, Principal: *fact.AmountMinor, PersonID: owner})
			for _, refund := range purchase.refunds[payment.ID] {
				f := refund.Fact
				draft.Performance = append(draft.Performance, draftPerformance{
					SourceVersionID: refund.ID, PaymentVersionID: payment.ID, PersonID: owner, Principal: *f.AmountMinor,
					Refund: &performanceRefundKey{f.Source, f.Domain, f.RefundID, f.OccurredAt.UTC()},
				})
			}
		}
		if runtime.enrollmentQuotes != nil {
			err = runtime.enrollmentQuotes.append(&draft, rule, purchase, identities, runtime)
		} else {
			err = appendPracticeEnrollmentAwards(&draft, rule, purchase, identities, runtime, tierResults)
		}
		if err != nil {
			return paymentDraft{}, err
		}
	}
	if err := appendPracticePeriodBenefits(&draft, rule, purchases, fullyRefunded, runtime, identities, performancePayments); err != nil {
		return paymentDraft{}, err
	}
	if run := runtime.enrollmentQuotes; run != nil {
		run.cache.remember(run.current)
	}
	return draft, nil
}

type practiceTierResult struct {
	awards map[int64]practiceTier
	issues practiceTierIssues
}

func appendPracticeEnrollmentAwards(draft *paymentDraft, rule paymentRule, purchase practicePurchase,
	identities map[identityKey][]identityAssignment, runtime practiceDraftRuntime, tierResults map[string]practiceTierResult) error {
	member := purchase.member
	var err error
	if purchase.companyRoutingIssue != "" {
		draft.Issues = append(draft.Issues, purchase.salesIssue(purchase.companyRoutingIssue, rule, "company_sales"))
	}
	if purchase.companyAward != nil {
		if err := appendPracticeCompanySale(draft, rule, purchase, runtime, identities); err != nil {
			return err
		}
	}
	for index, part := range rule.Components {
		if !part.matchesPracticeMember(classMember(member.MembershipType)) {
			continue
		}
		if part.CompanySales != nil || part.PeriodBenefit != nil {
			continue
		}
		if purchase.noReferrer && (part.Type == "sales" || part.Type == "referral") {
			continue
		}
		if part.Type == "sales" && (purchase.companyAward != nil || purchase.companyRoutingIssue != "") {
			continue
		}
		inScope, issue := purchase.matchesSalesCompanies(part.salesCompanies())
		if issue != "" {
			draft.Issues = append(draft.Issues, purchase.salesIssue(issue, rule, part.Type))
		}
		if !inScope {
			continue
		}
		if (part.Type != "sales" && part.Type != "referral") || part.SourceRole == "" {
			draft.Issues = append(draft.Issues, practiceIssue("UNCONFIGURED_COMPONENT", member, rule, part.Type))
			continue
		}
		sale, scopeIssue := purchase.scopedSale(part, identities, runtime)
		if scopeIssue != "" {
			draft.Issues = append(draft.Issues, purchase.salesIssue(scopeIssue, rule, part.Type))
			continue
		}
		if len(sale.payments) == 0 {
			continue
		}
		payments, base := sale.payments, sale.base
		assignments, issue := practiceBeneficiaries(payments, part.SourceRole, identities)
		if issue != "" {
			draft.Issues = append(draft.Issues, purchase.salesIssue(issue, rule, part.Type))
			continue
		}
		salesOwner := assignments[0].PersonID
		if part.SalesPersonID != 0 {
			for i := range assignments {
				assignments[i] = identityAssignment{PersonID: part.SalesPersonID}
			}
		}
		split, issue := purchase.retrainSplit(part.retrainSplits())
		if issue != "" {
			draft.Issues = append(draft.Issues, practiceIssue(issue, member, rule, part.Type))
			continue
		}
		if split != nil {
			serviceMatches := [2]bool{true, true}
			hasServiceAmount := split.ServiceMinor > 0 || split.ServiceRate != nil && split.ServiceRate.Value > 0
			if hasServiceAmount && len(split.ServiceSalesPeople)+len(split.ServiceSalesUnits) != 0 {
				scope := practicePeriodBenefit{SalesPeople: split.ServiceSalesPeople, SalesUnits: split.ServiceSalesUnits}
				for i, payment := range payments {
					serviceMatches[i], issue = scope.eligiblePayment(payment, part.SourceRole, nil, identities, runtime)
					if issue != "" {
						break
					}
				}
				if issue != "" {
					key := practicePeriodComponentKey(component{Type: "retrain_service"}, index)
					draft.Issues = append(draft.Issues, purchase.salesIssue(issue, rule, key))
					continue
				}
			}
			commission := part.Value
			if part.Mode == "rate" {
				commission = calculatePaymentAmount(purchase.base, part)
			}
			activeSplit := *split
			if !slices.Contains(serviceMatches[:len(payments)], true) {
				activeSplit.ServiceRate, activeSplit.ServiceMinor = nil, 0
			}
			amounts, valid := activeSplit.amounts(commission, purchase.base)
			if !valid {
				draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_RETRAIN_SPLIT_EXCEEDS_COMMISSION", member, rule, part.Type))
				continue
			}
			unit := runtime.poolUnits[split.Pool.PoolID]
			if !unit.Valid {
				draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_POOL_MISSING", member, rule, part.Type))
				continue
			}
			if err := appendPracticeRetrainSplit(draft, purchase, rule, index, amounts, assignments, *split, unit, part.Mode == "rate", serviceMatches); err != nil {
				return err
			}
			continue
		}
		isTiered := part.Mode == "tiered"
		if isTiered {
			result := tierResults[part.ClassMember]
			if issue := result.issues.forPurchase(salesOwner, member.ID); issue != "" {
				draft.Issues = append(draft.Issues, practiceIssue(issue, member, rule, part.Type))
				continue
			}
			tier, ok := result.awards[member.ID]
			if !ok {
				draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_TIER_NOT_MATCHED", member, rule, part.Type))
				continue
			}
			part.Mode, part.Value = tier.Mode, tier.Value
		}
		var amount int64
		var fixedShares [2]int64
		fixedByPayment := isTiered && part.Mode == "fixed" && !runtime.legacyFixedTierRefund &&
			(part.RefundPolicy == "" || part.RefundPolicy == "proportional")
		if fixedByPayment {
			fixedShares, issue = purchase.fixedTierPaymentShares(sale, part, identities, runtime)
			amount = fixedShares[0] + fixedShares[1]
		} else {
			amount, issue = sale.commissionAmount(part)
		}
		if issue != "" {
			draft.Issues = append(draft.Issues, practiceIssue(issue, member, rule, part.Type))
			continue
		}
		if isTiered && amount > base {
			draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_TIER_EXCEEDS_PAYMENT", member, rule, part.Type))
			continue
		}
		draft.Total, err = addMinor(draft.Total, amount)
		if err != nil {
			return err
		}
		var cumulativeBase, previousShare int64
		for index, payment := range payments {
			net := sale.netAmount(payment)
			paymentAmount := fixedShares[index]
			if !fixedByPayment {
				cumulativeBase += net
				cumulativeShare := prorateMinor(amount, cumulativeBase, base)
				paymentAmount = cumulativeShare - previousShare
				previousShare = cumulativeShare
			}
			payee := assignments[index]
			draft.Lines = append(draft.Lines, draftLine{
				FactID: payment.ID, RuleID: rule.ID, EnrollmentID: member.ID, PersonID: payee.PersonID,
				IdentityID: payee.ID, Component: part.Type, Base: net, Amount: paymentAmount,
				practiceProportionalRefund: !isTiered && (part.Mode == "rate" || part.SaleScope != nil && part.RefundPolicy == "proportional"),
				practiceTierRefund:         isTiered,
				practiceArrangementLabel:   part.arrangementLabel(),
			})
		}
	}
	return nil
}

type practiceTierIssues struct {
	people      map[int64]string
	enrollments map[int64]string
	priorCounts map[int64]int64
	amounts     map[int64]int64
}

func (i practiceTierIssues) forPurchase(personID, enrollmentID int64) string {
	if i.enrollments[enrollmentID] != "" {
		return i.enrollments[enrollmentID]
	}
	if i.people[personID] != "" {
		return i.people[personID]
	}
	return i.people[0]
}

func practicePurchaseCompany(payments []paymentInput) (string, string) {
	var company string
	for _, payment := range payments {
		id := payment.Fact.SalesCompanyID
		if id == "" {
			return "", "PRACTICE_TIER_COMPANY_MISSING"
		}
		if company != "" && company != id {
			return "", "PRACTICE_TIER_COMPANY_CONFLICT"
		}
		company = id
	}
	return company, ""
}

func (p practicePurchase) matchesSalesCompanies(companies []string) (bool, string) {
	if len(companies) == 0 {
		return true, ""
	}
	if len(p.payments) == 0 {
		return false, "PRACTICE_TIER_COMPANY_MISSING"
	}
	company, issue := practicePurchaseCompany(p.payments)
	return issue == "" && slices.Contains(companies, company), issue
}

func practiceTierAwards(part component, current []practicePurchase, orders map[string][]paymentInput,
	identities map[identityKey][]identityAssignment, blocked map[int64]string, history practiceTierHistory,
	refunds map[int64][]practiceRefundMatch, runtime practiceDraftRuntime) (map[int64]practiceTier, practiceTierIssues, error) {
	config := part.Tiers
	cumulative := config.CumulativeMetric != ""
	if cumulative {
		history.cumulative = config.CumulativeMetric
	}
	issues := practiceTierIssues{people: make(map[int64]string), enrollments: make(map[int64]string)}
	schedules := config.schedules()
	needsHistory := make(map[int64]bool)
	seenPayments := make(map[string]bool)
	rows := make([]practiceTierPurchase, 0, len(current))
	purchases := make(map[int64]practicePurchase)
	for _, purchase := range current {
		member := purchase.member
		kind := classMember(member.MembershipType)
		retrain := kind == "retrain"
		pays := part.matchesPracticeMember(kind)
		measures := config.CumulativeMetric.includes(kind) || !pays && config.measuresUnpaidMember(kind)
		if !pays && !measures || purchase.noReferrer || purchase.issue == practicePaymentAlreadySettled ||
			retrain && !measures && (purchase.issue != "" || purchase.companyRoutingIssue != "") {
			continue
		}
		if purchase.companyAward != nil {
			continue
		}
		if purchase.companyRoutingIssue != "" {
			issues.people[practicePurchaseOwner(purchase, part.SourceRole, identities)] = "PRACTICE_TIER_GROUP_INCOMPLETE"
			continue
		}
		if inScope, scopeIssue := purchase.matchesSalesCompanies(part.salesCompanies()); !inScope {
			if scopeIssue != "" && (!retrain || measures) {
				issues.people[practicePurchaseOwner(purchase, part.SourceRole, identities)] = "PRACTICE_TIER_GROUP_INCOMPLETE"
			}
			continue
		}
		if part.SaleScope != nil && purchase.issue == "" {
			selected, scopeIssue := purchase.scopedSale(part, identities, runtime)
			if scopeIssue != "" {
				issues.enrollments[member.ID] = scopeIssue
				if measures || !retrain {
					issues.people[practicePurchaseOwner(purchase, part.SourceRole, identities)] = "PRACTICE_TIER_GROUP_INCOMPLETE"
				}
				continue
			}
			if len(selected.payments) == 0 {
				continue
			}
			purchase = selected
		}
		person := practicePurchaseOwner(purchase, part.SourceRole, identities)
		unpricedMeasure := !pays && !history.legacyMeasures
		_, hasPersonalSchedule := schedules.people[person]
		var company, category, companyIssue string
		if !unpricedMeasure && !hasPersonalSchedule && len(config.CompanySchedules)+len(config.CategorySchedules) != 0 && purchase.issue == "" {
			company, companyIssue = practicePurchaseCompany(purchase.payments)
		}
		if _, exception := schedules.companies[company]; company != "" && !exception && len(schedules.categories) != 0 {
			category, companyIssue = practicePurchaseCompanyClassification(purchase.payments, runtime.companyClassifications, runtime.internalSalesCategories)
			if category == practiceUncategorizedCompany {
				companyIssue = "PRACTICE_TIER_SALES_CATEGORY_MISSING"
			}
		}
		schedule, configured := schedules.forSale(person, company, category)
		if company != "" && companyIssue == "" && !configured {
			issues.enrollments[member.ID] = "PRACTICE_TIER_COMPANY_NOT_CONFIGURED"
			continue
		}
		if person == 0 || repeatedPracticePayment(purchase.payments, seenPayments) {
			if retrain && !measures {
				issues.enrollments[member.ID] = "PRACTICE_TIER_GROUP_INCOMPLETE"
				continue
			}
			issues.people[0] = "PRACTICE_TIER_GROUP_INCOMPLETE"
			return nil, issues, nil
		}
		if companyIssue != "" {
			issues.enrollments[member.ID] = companyIssue
			if !retrain || measures {
				issues.people[person] = "PRACTICE_TIER_GROUP_INCOMPLETE"
			}
			continue
		}
		if pays && (len(schedule.Count) != 0 || cumulative && len(schedule.Performance) != 0) {
			needsHistory[person] = true
		}
		row := practiceTierPurchase{EnrollmentID: member.ID, PersonID: person, CompanyID: company, SalesCategoryID: category, Base: purchase.base,
			SequenceKey: purchase.payments[0].Fact.OrderID, PaidAt: purchase.payments[0].Fact.OccurredAt, Retrain: retrain, MeasureOnly: !pays,
			unpricedMeasure: unpricedMeasure}
		if len(purchase.payments) == 2 {
			row.SequenceKey = member.SourceEnrollmentID
			if second := purchase.payments[1].Fact.OccurredAt; second.Before(row.PaidAt) {
				row.PaidAt = second
			}
		}
		rows = append(rows, row)
		purchases[member.ID] = purchase
	}
	if history.replaying {
		frozen := history.frozenPrior
		if cumulative && !history.repriceAmounts {
			frozen = history.frozenAmounts
		}
		for person := range needsHistory {
			if _, found := frozen[person]; !found {
				return nil, issues, errPracticeRefundReplay
			}
		}
		if cumulative {
			if history.repriceAmounts {
				var err error
				frozen, err = practiceCumulativeAmountTotals(*config, rows, frozen)
				if err != nil {
					return nil, issues, err
				}
				issues.amounts = frozen
			}
			return choosePracticeCumulativeAmountTiers(*config, rows, frozen), issues, nil
		}
		awards, err := choosePracticeSalesTiers(*config, rows, history.frozenPrior)
		return awards, issues, err
	}
	var index practicePriorIndex
	if len(needsHistory) != 0 {
		var err error
		index, err = indexPracticePrior(history, part, orders, identities, blocked, refunds, runtime)
		if err != nil {
			return nil, issues, err
		}
	}
	startsByPerson := make(map[int64]practiceCountStart)
	for _, person := range config.PersonCountStarts {
		if person.includes(history.current) {
			startsByPerson[person.PersonID] = person.practiceCountStart
		}
	}
	defaultStart := history.defaultStart(*config)
	defaultWindow, defaultReady := history.window(defaultStart)
	windows := make(map[int64]map[int64]bool)
	prior := make(map[int64]int64)
	for _, row := range rows {
		person := row.PersonID
		_, checked := windows[person]
		if checked || !needsHistory[person] || issues.people[person] != "" {
			continue
		}
		window, ready := defaultWindow, defaultReady
		start, hasPersonalStart := startsByPerson[person]
		if hasPersonalStart {
			window, ready = history.window(start)
		}
		windows[person] = window
		historicalTotal, valid, err := index.personTotal(person, window)
		if err != nil {
			return nil, issues, err
		}
		if !ready {
			issues.people[person] = "PRACTICE_TIER_HISTORY_MISSING"
		} else if !valid {
			issues.people[person] = "PRACTICE_TIER_HISTORY_INCOMPLETE"
		} else if history.opening != nil && !hasPersonalStart {
			openingCount, found := history.opening.counts[person]
			if !found {
				issues.people[person] = "PRACTICE_TIER_OPENING_MISSING"
				continue
			}
			var err error
			prior[person], err = addMinor(openingCount, historicalTotal)
			if err != nil {
				return nil, issues, err
			}
		} else {
			prior[person] = historicalTotal
		}
	}
	for _, row := range rows {
		if issues.people[row.PersonID] == "" && index.repeats(purchases[row.EnrollmentID], windows[row.PersonID]) {
			issues.people[row.PersonID] = "PRACTICE_TIER_GROUP_INCOMPLETE"
		}
	}
	ready := rows[:0]
	for _, row := range rows {
		if issues.people[row.PersonID] == "" {
			ready = append(ready, row)
		}
	}
	if cumulative {
		totals, err := practiceCumulativeAmountTotals(*config, ready, prior)
		if err != nil {
			return nil, issues, err
		}
		issues.priorCounts, issues.amounts = prior, totals
		return choosePracticeCumulativeAmountTiers(*config, ready, totals), issues, nil
	}
	awards, err := choosePracticeSalesTiers(*config, ready, prior)
	issues.priorCounts = prior
	return awards, issues, err
}

func repeatedPracticePayment(payments []paymentInput, seen map[string]bool) bool {
	for _, payment := range payments {
		id := payment.Fact.PaymentID
		if seen[id] {
			return true
		}
		seen[id] = true
	}
	return false
}

func practicePurchaseOwner(purchase practicePurchase, role string, identities map[identityKey][]identityAssignment) int64 {
	if purchase.issue != "" {
		return 0
	}
	owners, issue := practiceBeneficiaries(purchase.payments, role, identities)
	if issue != "" {
		return 0
	}
	return owners[0].PersonID
}

func practicePayments(member practiceEnrollment, orders map[string][]paymentInput, counts map[string]int) ([]paymentInput, int64, string, error) {
	payments := make([]paymentInput, 0, len(member.PaidOrderIDs))
	seenPayments := make(map[string]bool)
	var base int64
	for _, id := range member.PaidOrderIDs {
		found := orders[id]
		switch {
		case counts[id] != 1 || len(found) > 1:
			return nil, 0, "PRACTICE_DUPLICATE_PAYMENT", nil
		case len(found) == 0:
			return nil, 0, "PRACTICE_MISSING_PAYMENT", nil
		}
		f := found[0].Fact
		if f.PaymentID == "" || f.AmountMinor == nil || *f.AmountMinor <= 0 || f.OccurredAt.After(member.SignedAt) && !member.SignedAt.IsZero() {
			return nil, 0, "PRACTICE_INVALID_PAYMENT", nil
		}
		if seenPayments[f.PaymentID] {
			return nil, 0, "PRACTICE_DUPLICATE_PAYMENT", nil
		}
		seenPayments[f.PaymentID] = true
		var err error
		base, err = addMinor(base, *f.AmountMinor)
		if err != nil {
			return nil, 0, "", err
		}
		payments = append(payments, found[0])
	}
	if base == 0 {
		return nil, 0, "PRACTICE_MISSING_PAYMENT", nil
	}
	return payments, base, "", nil
}

func practiceBeneficiaries(payments []paymentInput, role string, identities map[identityKey][]identityAssignment) ([]identityAssignment, string) {
	assignments := make([]identityAssignment, 0, len(payments))
	for _, payment := range payments {
		assignment, issue := paymentBeneficiary(payment.Fact, role, identities)
		if issue != "" {
			return nil, issue
		}
		if len(assignments) > 0 && assignments[0].PersonID != assignment.PersonID {
			return nil, "PRACTICE_ATTRIBUTION_CONFLICT"
		}
		assignments = append(assignments, assignment)
	}
	return assignments, ""
}
