package main

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
	member   practiceEnrollment
	payments []paymentInput
	base     int64
	issue    string
}

func preparePracticePurchases(members []practiceEnrollment, orders map[string][]paymentInput, blocked map[int64]string) ([]practicePurchase, error) {
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
		purchase := practicePurchase{member: member, issue: blocked[member.ID]}
		if purchase.issue == "" {
			var err error
			purchase.payments, purchase.base, purchase.issue, err = practicePayments(member, orders, counts)
			if err != nil {
				return nil, err
			}
		}
		purchases = append(purchases, purchase)
	}
	return purchases, nil
}

func calculatePracticeDraft(members []practiceEnrollment, rule paymentRule, orders map[string][]paymentInput,
	identities map[identityKey][]identityAssignment, blocked map[int64]string, history practiceTierHistory) (paymentDraft, error) {
	var draft paymentDraft
	purchases, err := preparePracticePurchases(members, orders, blocked)
	if err != nil {
		return draft, err
	}
	awards, tierIssue, err := practiceTierAwards(rule, purchases, orders, identities, blocked, history)
	if err != nil {
		return draft, err
	}
	for _, purchase := range purchases {
		member, payments, base := purchase.member, purchase.payments, purchase.base
		if purchase.issue != "" {
			draft.Issues = append(draft.Issues, practiceIssue(purchase.issue, member, rule, ""))
			continue
		}
		var matched bool
		for _, part := range rule.Components {
			if part.ClassMember != "" && part.ClassMember != classMember(member.MembershipType) {
				continue
			}
			matched = true
			if (part.Type != "sales" && part.Type != "referral") || part.SourceRole == "" {
				draft.Issues = append(draft.Issues, practiceIssue("UNCONFIGURED_COMPONENT", member, rule, part.Type))
				continue
			}
			assignments, issue := practiceBeneficiaries(payments, part.SourceRole, identities)
			if issue != "" {
				draft.Issues = append(draft.Issues, practiceIssue(issue, member, rule, part.Type))
				continue
			}
			isTiered := part.Mode == "tiered"
			if isTiered {
				issue := tierIssue[assignments[0].PersonID]
				if issue == "" {
					issue = tierIssue[0]
				}
				if issue != "" {
					draft.Issues = append(draft.Issues, practiceIssue(issue, member, rule, part.Type))
					continue
				}
				tier, ok := awards[member.ID]
				if !ok {
					draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_TIER_NOT_MATCHED", member, rule, part.Type))
					continue
				}
				part.Mode, part.Value = tier.Mode, tier.Value
			}
			amount := calculatePaymentAmount(base, part)
			if isTiered && amount > base {
				draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_TIER_EXCEEDS_PAYMENT", member, rule, part.Type))
				continue
			}
			draft.Total, err = addMinor(draft.Total, amount)
			if err != nil {
				return paymentDraft{}, err
			}
			var cumulativeBase, previousShare int64
			for index, payment := range payments {
				cumulativeBase += *payment.Fact.AmountMinor
				share := prorateMinor(amount, cumulativeBase, base)
				payee := assignments[index]
				draft.Lines = append(draft.Lines, draftLine{
					FactID: payment.ID, RuleID: rule.ID, EnrollmentID: member.ID, PersonID: payee.PersonID,
					IdentityID: payee.ID, Component: part.Type, Base: *payment.Fact.AmountMinor, Amount: share - previousShare,
				})
				previousShare = share
			}
		}
		if !matched {
			draft.Issues = append(draft.Issues, practiceIssue("PRACTICE_MEMBER_RULE_MISSING", member, rule, ""))
		}
	}
	return draft, nil
}

func practiceTierAwards(rule paymentRule, current []practicePurchase, orders map[string][]paymentInput,
	identities map[identityKey][]identityAssignment, blocked map[int64]string, history practiceTierHistory) (map[int64]practiceTier, map[int64]string, error) {
	part := practiceTierComponent(rule)
	if part == nil {
		return nil, nil, nil
	}
	config := part.Tiers
	issues := make(map[int64]string)
	seenPayments := make(map[string]bool)
	rows := make([]practiceTierPurchase, 0, len(current))
	purchases := make(map[int64]practicePurchase)
	for _, purchase := range current {
		member := purchase.member
		if classMember(member.MembershipType) != "new" {
			continue
		}
		person := practicePurchaseOwner(purchase, part.SourceRole, identities)
		if person == 0 || repeatedPracticePayment(purchase.payments, seenPayments) {
			issues[0] = "PRACTICE_TIER_GROUP_INCOMPLETE"
			return nil, issues, nil
		}
		row := practiceTierPurchase{EnrollmentID: member.ID, PersonID: person, Base: purchase.base,
			SequenceKey: purchase.payments[0].Fact.OrderID, PaidAt: purchase.payments[0].Fact.OccurredAt}
		if len(purchase.payments) == 2 {
			row.SequenceKey = member.SourceEnrollmentID
			if second := purchase.payments[1].Fact.OccurredAt; second.Before(row.PaidAt) {
				row.PaidAt = second
			}
		}
		rows = append(rows, row)
		purchases[member.ID] = purchase
	}
	if len(config.Count) == 0 || len(rows) == 0 {
		awards, err := choosePracticeSalesTiers(*config, rows, nil)
		return awards, issues, err
	}

	index, err := indexPracticePrior(history, part.SourceRole, orders, identities, blocked)
	if err != nil {
		return nil, nil, err
	}
	startsByPerson := make(map[int64]practiceCountStart)
	for _, person := range config.PersonCountStarts {
		if person.includes(history.current) {
			startsByPerson[person.PersonID] = person.practiceCountStart
		}
	}
	defaultStart := config.defaultCountStart()
	defaultWindow, defaultReady := history.window(defaultStart)
	windows := make(map[int64]map[int64]bool)
	prior := make(map[int64]int64)
	for _, row := range rows {
		person := row.PersonID
		if _, checked := windows[person]; checked {
			continue
		}
		window, ready := defaultWindow, defaultReady
		if start, personal := startsByPerson[person]; personal {
			window, ready = history.window(start)
		}
		windows[person] = window
		count, valid := index.personCount(person, window)
		if !ready {
			issues[person] = "PRACTICE_TIER_HISTORY_MISSING"
		} else if !valid {
			issues[person] = "PRACTICE_TIER_HISTORY_INCOMPLETE"
		} else {
			prior[person] = count
		}
	}
	for _, row := range rows {
		if issues[row.PersonID] == "" && index.repeats(purchases[row.EnrollmentID], windows[row.PersonID]) {
			issues[row.PersonID] = "PRACTICE_TIER_GROUP_INCOMPLETE"
		}
	}
	ready := rows[:0]
	for _, row := range rows {
		if issues[row.PersonID] == "" {
			ready = append(ready, row)
		}
	}
	awards, err := choosePracticeSalesTiers(*config, ready, prior)
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
