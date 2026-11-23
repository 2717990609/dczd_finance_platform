package main

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type practiceCorrectionBatch struct {
	revisions []practiceCorrectionRevision
	booked    map[int64]practiceCorrectionRevision
	refunds   []draftPerformance
	earlier   bool
}

func addPracticeCorrections(ctx context.Context, tx pgx.Tx, periodID int64, window periodWindow, draft *paymentDraft, facts []refundInput) error {
	rows, err := tx.Query(ctx, practiceCorrectionCandidatesSQL, periodID, window.Start, window.Through, maxDraftPayments+1)
	if err != nil {
		return err
	}
	revisions, err := readPracticeCorrectionRevisions(rows)
	if err != nil {
		return err
	}
	if len(revisions) > maxDraftPayments {
		return errDraftLimit
	}
	batches := make(map[int64]*practiceCorrectionBatch)
	batch := func(classID int64) *practiceCorrectionBatch {
		if batches[classID] == nil {
			batches[classID] = &practiceCorrectionBatch{booked: make(map[int64]practiceCorrectionRevision)}
		}
		return batches[classID]
	}
	for _, change := range revisions {
		b := batch(change.ClassID)
		b.earlier = b.earlier || change.EarlierPeriod
		if !change.EarlierPeriod {
			b.revisions = append(b.revisions, change)
		}
	}
	refunded := make(map[int64][]draftPerformance)
	payments := make([]int64, 0, len(draft.Performance))
	for _, row := range draft.Performance {
		if row.PaymentVersionID != 0 {
			payments = append(payments, row.PaymentVersionID)
			refunded[row.PaymentVersionID] = append(refunded[row.PaymentVersionID], row)
		}
	}
	if len(payments) != 0 {
		rows, err := tx.Query(ctx, `SELECT a.period_id,a.source_version_id FROM calculation_draft_performance a
		 JOIN settlements s USING(period_id) JOIN periods class ON class.id=a.period_id
		 JOIN periods target ON target.id=$1 AND target.product_id=class.product_id
		 WHERE a.source_version_id=ANY($2::bigint[]) AND a.payment_version_id IS NULL AND class.calendar_month IS NULL
		 ORDER BY a.period_id,a.source_version_id`, periodID, payments)
		if err != nil {
			return err
		}
		var classID, paymentID int64
		_, err = pgx.ForEachRow(rows, []any{&classID, &paymentID}, func() error {
			b := batch(classID)
			b.refunds = append(b.refunds, refunded[paymentID]...)
			return nil
		})
		if err != nil {
			return err
		}
	}
	classIDs := slices.Sorted(maps.Keys(batches))
	if len(classIDs) == 0 {
		return nil
	}
	rows, err = tx.Query(ctx, practiceCorrectionBookedSQL, classIDs)
	if err != nil {
		return err
	}
	booked, err := readPracticeCorrectionRevisions(rows)
	if err != nil {
		return err
	}
	for _, change := range booked {
		batches[change.ClassID].booked[change.OriginalID] = change
	}
	refundFacts := make(map[int64]paymentInput, len(facts))
	for _, refund := range facts {
		refundFacts[refund.ID] = paymentInput{ID: refund.ID, Fact: refund.Fact}
	}
	for _, classID := range classIDs {
		b := batches[classID]
		if len(b.refunds) == 0 && len(b.revisions) == 0 || len(b.booked) == 0 && len(b.revisions) == 0 && !b.earlier {
			continue
		}
		if err := b.apply(ctx, tx, classID, draft, refundFacts); err != nil {
			return err
		}
	}
	if len(draft.Corrections) == 0 {
		return nil
	}
	return attributeCorrectionRecipients(ctx, tx, draft.Corrections)
}

func (b practiceCorrectionBatch) apply(ctx context.Context, tx pgx.Tx, classID int64, draft *paymentDraft, refundFacts map[int64]paymentInput) error {
	var trigger int64
	if len(b.refunds) != 0 {
		trigger = b.refunds[0].SourceVersionID
	}
	if len(b.revisions) != 0 {
		trigger = b.revisions[0].VersionID
	}
	key := strconv.FormatInt(classID, 10)
	for _, change := range b.revisions {
		key += ":" + change.Key
	}
	issue := func(code string) error {
		var line int64
		if err := tx.QueryRow(ctx, "SELECT coalesce(min(id),0) FROM calculation_draft_lines WHERE period_id=$1", classID).Scan(&line); err != nil {
			return err
		}
		draft.Issues = append(draft.Issues, draftIssue{FactID: trigger, Code: code, CorrectionLineID: line, stableKey: key})
		return nil
	}
	if b.earlier {
		return issue("PRACTICE_CORRECTION_WAITING")
	}
	replay, original, err := readVerifiedPracticeReplay(ctx, tx, classID)
	if errors.Is(err, errPracticeRefundReplay) {
		return issue("PRACTICE_CORRECTION_REPLAY_REQUIRED")
	}
	if err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(b.booked)) {
		for user, assignments := range b.booked[id].Identities {
			key := identityKey{"DCZD", user}
			for _, assignment := range assignments {
				if !hasPracticeIdentityAt(replay.identities[key], assignment.ValidFrom) {
					replay.identities[key] = append(replay.identities[key], assignment)
				}
			}
		}
	}
	changes := maps.Clone(b.booked)
	for _, change := range b.revisions {
		changes[change.OriginalID] = change
	}
	if err := replay.applyRecipientRevisions(ctx, tx, changes); err != nil {
		if errors.Is(err, errPracticeRefundReplay) {
			return issue("CORRECTION_SCOPE_CHANGED")
		}
		return err
	}
	if len(changes) != 0 {
		if err := replay.repriceCorrectedCumulativeTiers(); err != nil {
			if errors.Is(err, errPracticeRefundReplay) {
				return issue("PRACTICE_CORRECTION_REPLAY_REQUIRED")
			}
			return err
		}
	}
	for i := range b.revisions {
		change := &b.revisions[i]
		change.Company = &practiceReplayCompany{Sales: change.Fact.SalesCompanyID,
			Referrer: change.Fact.ReferrerCompanyID, Conflict: change.Fact.ReferrerConflict}
		change.Identities = make(map[string][]identityAssignment)
		for _, user := range practiceCorrectionUsers(change.Fact) {
			key := identityKey{change.Fact.Source, user}
			change.Identities[user] = practiceIdentitiesAt(replay.identities[key], change.Fact.OccurredAt)
		}
	}
	revised, err := replay.calculateCorrectionLines(ctx, tx, classID, b.refunds, refundFacts)
	if errors.Is(err, errPracticeRefundReplay) {
		return issue("PRACTICE_CORRECTION_REPLAY_REQUIRED")
	}
	if err != nil {
		return err
	}

	inputs := make([]correctionInput, len(original))
	touched := make(map[int64]int64)
	for _, change := range b.revisions {
		touched[change.OriginalID] = change.VersionID
	}
	for _, refund := range b.refunds {
		if touched[refund.PaymentVersionID] == 0 {
			touched[refund.PaymentVersionID] = refund.SourceVersionID
		}
	}
	for i, line := range original {
		sourceID := trigger
		if direct := touched[line.payment]; direct != 0 {
			sourceID = direct
		}
		inputs[i] = correctionInput{OriginalLineID: line.id, OriginalPersonID: line.person,
			OriginalAmount: line.amount, SourceVersionID: sourceID, OriginalUnit: line.unit}
	}
	balances, err := readCorrectionBalances(ctx, tx, inputs, draft.Refunds)
	if err != nil {
		return err
	}
	target, ok := practiceCorrectedLines(original, revised)
	if !ok {
		return issue("PRACTICE_CORRECTION_LINKED_REVIEW")
	}
	if replay.independent {
		for i, line := range original {
			target[i].Amount = line.amount - balances[line.id].refundDebit
			if target[i].Amount < 0 {
				return issue("CORRECTION_REFUND_CONFLICT")
			}
		}
	}
	if replay.independentRefunds {
		if err := applyIndependentPracticeCorrectionRefunds(replay, original, target, balances); err != nil {
			if errors.Is(err, errRefundAmount) || errors.Is(err, errRefundPolicy) || errors.Is(err, errPracticeRefundReplay) {
				return issue("CORRECTION_REFUND_CONFLICT")
			}
			return err
		}
	}
	for i, line := range original {
		after := target[i]
		var result correctionEvaluation
		if line.person != 0 {
			result, err = evaluateRecipientCorrection(inputs[i], balances[line.id], after.PersonID, after.Amount)
		} else {
			result, err = evaluateOrganizationCorrection(inputs[i], balances[line.id], after.BeneficiaryUnit, after.Amount)
		}
		if err != nil {
			return err
		}
		if err := appendCorrection(draft, inputs[i], line.component, key, result); err != nil {
			return err
		}
	}
	if err := b.flagLaterTiers(ctx, tx, classID, trigger, draft, replay, original, target); err != nil {
		return err
	}
	draft.practiceChanges = append(draft.practiceChanges, b.revisions...)
	return nil
}

func (r *practiceReplay) repriceCorrectedCumulativeTiers() error {
	if !slices.ContainsFunc(r.rule.Components, func(part component) bool {
		return part.Tiers != nil && part.Tiers.CumulativeMetric != ""
	}) {
		return nil
	}
	// Select the corrected tiers before applying refunds booked after the class.
	// Their effect on the cumulative basis starts in the refund posting period.
	r.history.repriceAmounts = true
	priced, err := r.calculate()
	r.history.repriceAmounts = false
	if err != nil {
		return err
	}
	r.history.frozenAmounts = priced.practiceTierAmounts
	for member, amounts := range priced.practiceFrozenTiers {
		history := r.history.arrangements[member]
		history.frozenAmounts = amounts.Amounts
		r.history.arrangements[member] = history
	}
	return nil
}

func (r practiceReplay) calculateCorrectionLines(ctx context.Context, tx pgx.Tx, classID int64,
	refunds []draftPerformance, facts map[int64]paymentInput) ([]draftLine, error) {
	classRefunds := r.refunds
	if !r.independent && !r.independentRefunds {
		r.refunds = maps.Clone(classRefunds)
		if err := r.loadSettledRefunds(ctx, tx, classID); err != nil {
			return nil, err
		}
		for _, refund := range refunds {
			if err := r.addLateRefund(refund.PaymentVersionID, facts[refund.SourceVersionID]); err != nil {
				return nil, err
			}
		}
	}
	revised, err := r.calculate()
	if err != nil || len(r.lateRefunded) == 0 {
		return revised.Lines, err
	}

	// Price the revised recipients before post-settlement refunds for the
	// percentage lines. Linked tiers still use the replayed class balance.
	r.refunds = classRefunds
	baseline, err := r.calculate()
	if err != nil {
		return nil, err
	}
	lines := slices.DeleteFunc(revised.Lines, func(line draftLine) bool { return line.practiceProportionalRefund })
	for _, line := range baseline.Lines {
		if !line.practiceProportionalRefund {
			continue
		}
		line.Amount, err = practiceRefundRemaining(line.Base, line.Amount, r.lateRefunded[line.FactID])
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func applyIndependentPracticeCorrectionRefunds(replay practiceReplay, original []practiceReplayOriginal, target []draftLine,
	balances map[int64]*correctionBalance) error {
	members := make(map[int64]int, len(replay.members))
	for _, member := range replay.members {
		members[member.ID] = member.MembershipType
	}
	paymentsByID := make(map[int64]paymentInput)
	for _, payments := range replay.payments {
		for _, payment := range payments {
			paymentsByID[payment.ID] = payment
		}
	}
	for i, line := range original {
		principal := balances[line.id].refunded
		after := &target[i]
		if principal == 0 || after.Amount == 0 {
			continue
		}
		f := paymentsByID[line.payment].Fact
		terms := practiceMonthlyRefundPolicy(replay.rule.Components,
			refundObligation{Base: after.Base, Accrued: after.Amount, ComponentType: after.Component},
			1, members[line.enrollment], f.SalesCompanyID, replay.runtime.companyClassifications[f.PaymentID])
		debit, err := refundDelta(terms, after.Base, after.Amount, principal, 0)
		if err != nil {
			return err
		}
		after.Amount += debit
	}
	return nil
}

func (b practiceCorrectionBatch) flagLaterTiers(ctx context.Context, tx pgx.Tx, classID, trigger int64, draft *paymentDraft,
	replay practiceReplay, original []practiceReplayOriginal, target []draftLine) error {
	if len(b.revisions) == 0 {
		return nil
	}
	membership := make(map[int64]string, len(replay.members))
	for _, member := range replay.members {
		membership[member.ID] = classMember(member.MembershipType)
	}
	changed := make(map[int64]sourceFact)
	for _, revision := range b.revisions {
		changed[revision.OriginalID] = revision.Fact
	}
	affectedPeople := map[string]map[int64]bool{"new": {}, "retrain": {}}
	saleRules := make(map[string]component, len(affectedPeople))
	for kind := range affectedPeople {
		for _, part := range replay.rule.Components {
			if part.Type == "sales" && part.matchesPracticeMember(kind) {
				saleRules[kind] = part
				break
			}
		}
	}
	for i, line := range original {
		next := target[i].PersonID
		revised, hasChange := changed[line.payment]
		kind := membership[line.enrollment]
		people := affectedPeople[kind]
		if line.component != "sales" || !hasChange || people == nil {
			continue
		}
		sales := saleRules[kind]
		companies := sales.salesCompanies()
		leftTierScope := (kind == "retrain" || sales.Tiers != nil) && len(companies) != 0 &&
			revised.SalesCompanyID != "" && !slices.Contains(companies, revised.SalesCompanyID)
		if next == 0 && !leftTierScope {
			continue
		}
		if booked, ok := b.booked[line.payment]; ok && leftTierScope &&
			!slices.Contains(companies, booked.Fact.SalesCompanyID) {
			continue
		}
		prior := line.person
		if sales.SalesPersonID != 0 {
			prior = replay.posted[line.payment].personID
			assigned, issue := paymentBeneficiary(revised, sales.SourceRole, replay.identities)
			if issue != "" {
				continue
			}
			next = assigned.PersonID
		}
		if change, ok := b.booked[line.payment]; ok {
			if assigned, issue := paymentBeneficiary(change.Fact, sales.SourceRole, replay.identities); issue == "" {
				prior = assigned.PersonID
			}
		}
		if leftTierScope || next != prior {
			if prior != 0 {
				people[prior] = true
			}
			if !leftTierScope && next != 0 {
				people[next] = true
			}
		}
	}
	newPeople, retrainPeople := affectedPeople["new"], affectedPeople["retrain"]
	if len(newPeople)+len(retrainPeople) == 0 {
		return nil
	}
	var affected bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
	 SELECT 1 FROM periods original JOIN periods later ON later.product_id=original.product_id
	  AND later.ordinal>original.ordinal AND split_part(later.source_period_key,':',3)=split_part(original.source_period_key,':',3)
	 JOIN settlements s ON s.period_id=later.id
	 JOIN calculation_drafts d ON d.period_id=later.id
	 JOIN rule_versions rule ON rule.id=d.rule_id
	 WHERE original.id=$1
	 AND EXISTS(SELECT 1 FROM calculation_draft_lines l
	  JOIN practice_enrollments enrollment ON enrollment.id=l.practice_enrollment_id
	  LEFT JOIN calculation_draft_performance performance ON performance.period_id=l.period_id
	   AND performance.source_version_id=l.source_version_id
	  CROSS JOIN LATERAL (SELECT coalesce(performance.person_id,l.person_id) AS person_id) owner
	  CROSS JOIN LATERAL jsonb_array_elements(rule.components) component
	  CROSS JOIN LATERAL (SELECT component->'tiers' AS config) tier
	  WHERE l.period_id=later.id AND l.component_type='sales'
	  AND component->>'type'='sales' AND component->>'mode'='tiered'
	  AND (coalesce(component->>'classMember','') IN ('',CASE enrollment.membership_type WHEN 3 THEN 'new' WHEN 2 THEN 'retrain' END)
	   OR enrollment.membership_type=2 AND tier.config->>'includeRetrain'='true')
	  AND (owner.person_id=ANY($2::bigint[]) AND (
	    tier.config->>'cumulativeMetric' IN ('cumulative_new','cumulative_combined')
	    OR jsonb_path_exists(tier.config,'$.count[*]')
	    OR jsonb_path_exists(tier.config,'$.companySchedules[*].count[*]')
	    OR jsonb_path_exists(tier.config,'$.categorySchedules[*].count[*]')
	    OR jsonb_path_exists(tier.config,'$.personSchedules[*] ? (@.personId == $person).count[*]',
	     jsonb_build_object('person',owner.person_id)))
	   OR owner.person_id=ANY($3::bigint[]) AND tier.config->>'cumulativeMetric' IN ('cumulative_retrain','cumulative_combined')))
	)`, classID, slices.Sorted(maps.Keys(newPeople)), slices.Sorted(maps.Keys(retrainPeople))).Scan(&affected)
	if err == nil && affected {
		draft.Issues = append(draft.Issues, draftIssue{FactID: trigger, Code: "PRACTICE_CORRECTION_LATER_TIERS", Advisory: true,
			stableKey: strconv.FormatInt(classID, 10)})
	}
	return err
}

func (r practiceReplay) applyRecipientRevisions(ctx context.Context, tx pgx.Tx, changes map[int64]practiceCorrectionRevision) error {
	keys := make(map[identityKey]bool)
	needed := make(map[identityKey]map[time.Time]bool)
	var window periodWindow
	seen := 0
	for _, payments := range r.payments {
		for i, payment := range payments {
			change, found := changes[payment.ID]
			if !found {
				continue
			}
			seen++
			old, revised := payment.Fact, change.Fact
			if change.Booked {
				company := change.Company
				if company == nil {
					company = &practiceReplayCompany{old.SalesCompanyID, old.ReferrerCompanyID, old.ReferrerConflict}
				}
				revised.SalesCompanyID, revised.ReferrerCompanyID, revised.ReferrerConflict = company.Sales, company.Referrer, company.Conflict
			}
			if old.PaymentID != revised.PaymentID || old.SourceKey != revised.SourceKey || old.Domain != revised.Domain ||
				old.TransactionType != revised.TransactionType || !old.OccurredAt.Equal(revised.OccurredAt) ||
				old.AmountMinor == nil || revised.AmountMinor == nil || *old.AmountMinor != *revised.AmountMinor {
				return errPracticeRefundReplay
			}
			if old.SalesCompanyID != revised.SalesCompanyID {
				// The captured category belongs to the original company. An explicitly
				// configured replacement can still be priced without a category lookup.
				delete(r.runtime.companyClassifications, old.PaymentID)
				delete(r.runtime.internalSalesCategories, old.PaymentID)
			}
			for _, user := range practiceCorrectionUsers(revised) {
				key := identityKey{old.Source, user}
				at := old.OccurredAt.UTC()
				if !change.Booked && !hasPracticeIdentityAt(r.identities[key], at) {
					keys[key] = true
					if needed[key] == nil {
						needed[key] = make(map[time.Time]bool)
					}
					needed[key][at] = true
				}
			}
			if window.Start.IsZero() || old.OccurredAt.Before(window.Start) {
				window.Start = old.OccurredAt
			}
			if !old.OccurredAt.Before(window.Through) {
				window.Through = old.OccurredAt.Add(time.Microsecond)
			}
			payments[i].Fact = revised
		}
	}
	if seen != len(changes) {
		return errPracticeRefundReplay
	}
	if len(keys) == 0 {
		return nil
	}
	identities, err := readIdentityAssignments(ctx, tx, window, keys)
	if err != nil {
		return err
	}
	for key, times := range needed {
		for at := range times {
			r.identities[key] = append(r.identities[key], practiceIdentitiesAt(identities[key], at)...)
		}
	}
	return nil
}

func hasPracticeIdentityAt(assignments []identityAssignment, at time.Time) bool {
	return slices.ContainsFunc(assignments, func(assignment identityAssignment) bool {
		return !at.Before(assignment.ValidFrom) && (assignment.ValidTo == nil || at.Before(*assignment.ValidTo))
	})
}

func practiceIdentitiesAt(assignments []identityAssignment, at time.Time) []identityAssignment {
	var result []identityAssignment
	at = at.UTC()
	end := at.Add(time.Microsecond)
	for _, assignment := range assignments {
		if !at.Before(assignment.ValidFrom) && (assignment.ValidTo == nil || at.Before(*assignment.ValidTo)) {
			assignment.ValidFrom, assignment.ValidTo = at, &end
			result = append(result, assignment)
		}
	}
	return result
}

func practiceCorrectionUsers(f sourceFact) []string {
	out := make([]string, 0, len(f.Participants)+1)
	if f.ReferrerID != "" {
		out = append(out, f.ReferrerID)
	}
	for _, participant := range f.Participants {
		if participant.UserID != "" {
			out = append(out, participant.UserID)
		}
	}
	return out
}

// The frozen payment, enrollment and component identify a posting even when
// the recipient or amount changes. A pool adjustment stays in the original
// account, including when its original component becomes inapplicable.
func practiceCorrectedLines(original []practiceReplayOriginal, revised []draftLine) ([]draftLine, bool) {
	type slot struct {
		payment, enrollment int64
		component           string
	}
	current := make(map[slot]draftLine, len(revised))
	for _, line := range revised {
		key := slot{line.FactID, line.EnrollmentID, line.Component}
		if _, duplicate := current[key]; duplicate {
			return nil, false
		}
		current[key] = line
	}
	target := make([]draftLine, len(original))
	for i, line := range original {
		key := slot{line.payment, line.enrollment, line.component}
		after, found := current[key]
		delete(current, key)
		if line.person != 0 {
			if found && after.PersonID == 0 {
				return nil, false
			}
		} else if found && (after.PersonID != 0 || after.BeneficiaryUnit == nil ||
			(line.pool != 0) != (after.PracticePool != nil) || after.PracticePool != nil &&
			(*after.BeneficiaryUnit != line.unit || after.PracticePool.PoolID != line.pool || after.PracticePool.OwnerID != line.owner)) {
			return nil, false
		}
		target[i] = after
	}
	for _, line := range current {
		if !line.PracticeTierReference {
			return nil, false
		}
	}
	return target, true
}
