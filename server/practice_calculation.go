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

func calculatePracticeDraft(members []practiceEnrollment, rule paymentRule, orders map[string][]paymentInput,
	identities map[identityKey][]identityAssignment, blocked map[int64]string) (paymentDraft, error) {
	var draft paymentDraft
	counts := make(map[string]int)
	for _, member := range members {
		if member.CommissionEligible {
			for _, id := range member.PaidOrderIDs {
				counts[id]++
			}
		}
	}
	for _, member := range members {
		if !member.CommissionEligible {
			continue
		}
		if issue := blocked[member.ID]; issue != "" {
			draft.Issues = append(draft.Issues, practiceIssue(issue, member, rule, ""))
			continue
		}
		payments, base, issue, err := practicePayments(member, orders, counts)
		if err != nil {
			return paymentDraft{}, err
		}
		if issue != "" {
			draft.Issues = append(draft.Issues, practiceIssue(issue, member, rule, ""))
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
			amount := calculatePaymentAmount(base, part)
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
