package main

import (
	"context"
	"errors"
	"maps"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	if !replay.independent && !replay.independentRefunds {
		if err := replay.loadSettledRefunds(ctx, tx, classID); err != nil {
			return err
		}
		for _, refund := range b.refunds {
			replay.addRefund(refund.PaymentVersionID, refundFacts[refund.SourceVersionID])
		}
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
	revised, err := replay.calculate()
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
	target, ok := practiceCorrectedLines(original, revised.Lines)
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

func applyIndependentPracticeCorrectionRefunds(replay practiceReplay, original []practiceReplayOriginal, target []draftLine,
	balances map[int64]*correctionBalance) error {
	members := make(map[int64]int, len(replay.members))
	for _, member := range replay.members {
		members[member.ID] = member.MembershipType
	}
	companies := make(map[int64]string)
	for _, payments := range replay.payments {
		for _, payment := range payments {
			companies[payment.ID] = payment.Fact.SalesCompanyID
		}
	}
	for i, line := range original {
		principal := balances[line.id].refunded
		after := &target[i]
		if principal == 0 || after.Amount == 0 {
			continue
		}
		terms := practiceMonthlyRefundPolicy(replay.rule.Components,
			refundObligation{Base: after.Base, Accrued: after.Amount, ComponentType: after.Component}, 1, members[line.enrollment], companies[line.payment])
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
	newMembers := make(map[int64]bool)
	for _, member := range replay.members {
		newMembers[member.ID] = classMember(member.MembershipType) == "new"
	}
	changed := make(map[int64]bool)
	for _, revision := range b.revisions {
		changed[revision.OriginalID] = true
	}
	var salesRole string
	for _, part := range replay.rule.Components {
		if part.Type == "sales" && (part.ClassMember == "" || part.ClassMember == "new") {
			salesRole = part.SourceRole
			break
		}
	}
	people := make(map[int64]bool)
	for i, line := range original {
		next := target[i].PersonID
		if line.component != "sales" || !changed[line.payment] || !newMembers[line.enrollment] || next == 0 {
			continue
		}
		prior := line.person
		if change, ok := b.booked[line.payment]; ok {
			if assigned, issue := paymentBeneficiary(change.Fact, salesRole, replay.identities); issue == "" {
				prior = assigned.PersonID
			}
		}
		if next != prior {
			people[prior] = true
			people[next] = true
		}
	}
	if len(people) == 0 {
		return nil
	}
	var affected bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
	 SELECT 1 FROM periods original JOIN periods later ON later.product_id=original.product_id
	  AND later.ordinal>original.ordinal AND split_part(later.source_period_key,':',3)=split_part(original.source_period_key,':',3)
	 JOIN settlements s ON s.period_id=later.id
	 JOIN calculation_drafts d ON d.period_id=later.id
	 JOIN rule_versions rule ON rule.id=d.rule_id
	 WHERE original.id=$1 AND (jsonb_path_exists(rule.components,'$[*].tiers.count[*]')
	  OR jsonb_path_exists(rule.components,'$[*].tiers.companySchedules[*].count[*]'))
	 AND EXISTS(SELECT 1 FROM calculation_draft_lines l WHERE l.period_id=later.id AND l.component_type='sales'
	  AND l.person_id=ANY($2::bigint[]))
	)`, classID, slices.Sorted(maps.Keys(people))).Scan(&affected)
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
	return target, len(current) == 0
}

func evaluateOrganizationCorrection(input correctionInput, balance *correctionBalance, unit *pgtype.UUID, amount int64) (correctionEvaluation, error) {
	var result correctionEvaluation
	keys := slices.SortedFunc(maps.Keys(balance.organizations), func(a, b pgtype.UUID) int {
		return slices.Compare(a.Bytes[:], b.Bytes[:])
	})
	if unit != nil {
		if _, exists := balance.organizations[*unit]; !exists {
			keys = append(keys, *unit)
		}
	}
	for _, recipient := range keys {
		var expected int64
		if unit != nil && *unit == recipient {
			expected = amount
		}
		current := balance.organizations[recipient]
		if current == math.MinInt64 {
			return correctionEvaluation{}, errCalculationOverflow
		}
		delta, err := addMinor(expected, -current)
		if err != nil {
			return correctionEvaluation{}, err
		}
		if delta != 0 {
			result.lines = append(result.lines, correctionLine{
				OriginalLineID: input.OriginalLineID, SourceVersionID: input.SourceVersionID, Unit: recipient, Amount: delta,
			})
		}
	}
	return result, nil
}
