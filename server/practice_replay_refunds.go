package main

import (
	"context"
	"errors"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var errPracticeRefundUnanchored = errors.New("阶梯复算新增分润缺少原结算明细")

type practiceReplayRefund struct {
	refundInput
	classID, paymentID int64
}

type practiceReplayLineKey struct {
	payment, enrollment, person, pool, owner int64
	identity                                 int64
	component                                string
	unit                                     pgtype.UUID
}

type practiceReplayOriginal struct {
	practiceReplayLineKey
	id, base, amount, adjusted int64
	proportionalRefund         bool
	fixedSignup                bool
	tierRefund                 bool
}

func applyPracticeReplayRefunds(ctx context.Context, tx pgx.Tx, draft *paymentDraft, events []practiceReplayRefund) error {
	classes := make(map[int64][]practiceReplayRefund)
	var ordered []int64
	for _, event := range events {
		if _, found := classes[event.classID]; !found {
			ordered = append(ordered, event.classID)
		}
		classes[event.classID] = append(classes[event.classID], event)
	}
	for _, id := range ordered {
		classEvents := classes[id]
		lines, err := replayPracticeRefunds(ctx, tx, id, classEvents)
		if errors.Is(err, errPracticeRefundReplay) || errors.Is(err, errPracticeRefundUnanchored) {
			code := "PRACTICE_REFUND_REPLAY_REQUIRED"
			if errors.Is(err, errPracticeRefundUnanchored) {
				code = "PRACTICE_REFUND_REPLAY_NEW_LINE"
			}
			for _, event := range classEvents {
				draft.Issues = append(draft.Issues, draftIssue{FactID: event.ID, Code: code})
			}
			continue
		}
		if err != nil {
			return err
		}
		for _, line := range lines {
			draft.RefundTotal, err = addMinor(draft.RefundTotal, line.Amount)
			if err != nil {
				return err
			}
			draft.Refunds = append(draft.Refunds, line)
		}
	}
	return nil
}

func replayPracticeRefunds(ctx context.Context, tx pgx.Tx, classID int64, events []practiceReplayRefund) ([]refundLine, error) {
	replay, originals, err := readVerifiedPracticeReplay(ctx, tx, classID)
	if err != nil {
		return nil, err
	}
	replay.runtime.legacyFixedRefundPolicy = false
	if err := replay.loadSettledRefunds(ctx, tx, classID); err != nil {
		return nil, err
	}
	frozen := make(map[practiceReplayLineKey]bool, len(originals))
	for _, original := range originals {
		frozen[original.practiceReplayLineKey] = true
	}
	var lines []refundLine
	for len(events) != 0 {
		n := 1
		for n < len(events) && events[n].RefundPeriodID == events[0].RefundPeriodID {
			n++
		}
		group := events[:n]
		events = events[n:]
		anchors := make(map[int64]practiceReplayRefund)
		for _, event := range group {
			if err := replay.addLateRefund(event.paymentID, paymentInput{ID: event.ID, Fact: event.Fact}); err != nil {
				return nil, err
			}
			anchors[event.paymentID] = event
		}
		result, err := replay.calculate()
		if err != nil {
			return nil, err
		}
		target, err := practiceReplayLines(result.Lines)
		if err != nil {
			return nil, err
		}
		for key, line := range target {
			if line.practiceTierRefund && line.Amount > 0 && !frozen[key] {
				return nil, errPracticeRefundUnanchored
			}
		}
		for i := range originals {
			line := &originals[i]
			remaining := target[line.practiceReplayLineKey].Amount
			if line.proportionalRefund || line.fixedSignup {
				remaining, err = practiceRefundRemaining(line.base, line.amount, replay.lateRefunded[line.payment], line.fixedSignup)
				if err != nil {
					return nil, err
				}
			}
			settledAmount, err := addMinor(line.amount, line.adjusted)
			if err != nil {
				return nil, err
			}
			change := remaining - settledAmount
			if change == 0 || change > 0 && !line.tierRefund {
				continue
			}
			anchor, found := anchors[line.payment]
			if !found {
				anchor = group[len(group)-1]
			}
			precise := ""
			if line.proportionalRefund && !line.fixedSignup {
				precise = preciseRefundAmount(component{RefundPolicy: "proportional"},
					refundObligation{Base: line.base, Accrued: line.amount}, replay.lateRefunded[line.payment], -line.adjusted, change)
			} else if !line.fixedSignup {
				if targetYuan, ok := new(big.Rat).SetString(target[line.practiceReplayLineKey].PreciseAmount); ok {
					precise = targetYuan.Sub(targetYuan, big.NewRat(settledAmount, 100)).FloatString(draftPrecision)
				}
			}
			lines = append(lines, refundLine{
				OriginalLineID: line.id, RefundPeriodID: anchor.RefundPeriodID, RefundSourceVersionID: anchor.ID,
				Amount: change, Principal: *anchor.Fact.AmountMinor, RefundID: anchor.Fact.RefundID, OccurredAt: anchor.Fact.OccurredAt.UTC(),
				TierCredit: change > 0, PreciseAmount: precise,
			})
			line.adjusted = remaining - line.amount
		}
	}
	return lines, nil
}

func readVerifiedPracticeReplay(ctx context.Context, tx pgx.Tx, classID int64) (practiceReplay, []practiceReplayOriginal, error) {
	replay, err := readPracticeReplay(ctx, tx, classID)
	if errors.Is(err, pgx.ErrNoRows) {
		return replay, nil, errPracticeRefundReplay
	}
	if err != nil {
		return replay, nil, err
	}
	originals, err := readPracticeReplayOriginals(ctx, tx, classID)
	if err != nil {
		return replay, nil, err
	}
	baseline, err := replay.calculate()
	if err != nil {
		return replay, nil, err
	}
	before, err := practiceReplayLines(baseline.Lines)
	if err != nil || len(baseline.Performance) != len(replay.posted) {
		return replay, nil, errPracticeRefundReplay
	}
	for _, posting := range baseline.Performance {
		expected := practiceReplayPosting{posting.PaymentVersionID, posting.PersonID, posting.Principal}
		if replay.posted[posting.SourceVersionID] != expected {
			return replay, nil, errPracticeRefundReplay
		}
	}
	for i := range originals {
		line := &originals[i]
		value, found := before[line.practiceReplayLineKey]
		if !found || value.Amount != line.amount || value.Base != line.base ||
			line.adjusted < -line.amount || line.adjusted > 0 && !value.practiceTierRefund {
			return replay, nil, errPracticeRefundReplay
		}
		line.proportionalRefund = value.practiceProportionalRefund
		line.fixedSignup = value.practiceFixedSignup
		line.tierRefund = value.practiceTierRefund
		delete(before, line.practiceReplayLineKey)
	}
	for _, line := range before {
		// Older settlements did not retain zero-value tier references.
		if !line.PracticeTierReference {
			return replay, nil, errPracticeRefundReplay
		}
	}
	return replay, originals, nil
}

func practiceReplayLines(lines []draftLine) (map[practiceReplayLineKey]draftLine, error) {
	byKey := make(map[practiceReplayLineKey]draftLine, len(lines))
	for _, line := range lines {
		key := practiceReplayLineKey{payment: line.FactID, enrollment: line.EnrollmentID, person: line.PersonID,
			component: line.Component, identity: line.IdentityID}
		if line.BeneficiaryUnit != nil {
			key.unit = *line.BeneficiaryUnit
		}
		if line.PracticePool != nil {
			key.pool, key.owner = line.PracticePool.PoolID, line.PracticePool.OwnerID
		}
		if _, duplicate := byKey[key]; duplicate || line.Amount < 0 {
			return nil, errPracticeRefundReplay
		}
		byKey[key] = line
	}
	return byKey, nil
}

func readPracticeReplayOriginals(ctx context.Context, tx pgx.Tx, classID int64) ([]practiceReplayOriginal, error) {
	rows, err := tx.Query(ctx, `SELECT l.id,l.source_version_id,coalesce(l.practice_enrollment_id,0),coalesce(l.person_id,0),
	 l.component_type,l.beneficiary_unit_id,coalesce(l.practice_pool_id,0),coalesce(l.allocation_owner_id,0),
	 coalesce(l.identity_id,0),l.base_minor,l.amount_minor,
	 coalesce((SELECT sum(r.amount_minor) FROM calculation_draft_refunds r JOIN settlements closed USING(period_id)
	  WHERE r.original_line_id=l.id),0)::bigint
	 FROM calculation_draft_lines l WHERE l.period_id=$1 ORDER BY l.id`, classID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceReplayOriginal, error) {
		var line practiceReplayOriginal
		err := row.Scan(&line.id, &line.payment, &line.enrollment, &line.person, &line.component, &line.unit, &line.pool, &line.owner,
			&line.identity, &line.base, &line.amount, &line.adjusted)
		return line, err
	})
}

// Late refunds use the net payment and benefit that were valid at settlement.
// Class-internal refunds have already reduced both amounts in that baseline.
func practiceRefundRemaining(base, amount, principal int64, fixedSignup bool) (int64, error) {
	if principal == 0 {
		return amount, nil
	}
	change, err := refundDelta(component{RefundPolicy: "proportional"}, base, amount, principal, 0)
	if err != nil {
		return 0, errors.Join(errPracticeRefundReplay, err)
	}
	if fixedSignup && principal < base {
		return amount, nil
	}
	return amount + change, nil
}

func (r practiceReplay) addLateRefund(paymentID int64, item paymentInput) error {
	if item.Fact.AmountMinor == nil || *item.Fact.AmountMinor <= 0 {
		return errPracticeRefundReplay
	}
	refunded, err := addMinor(r.lateRefunded[paymentID], *item.Fact.AmountMinor)
	if err != nil {
		return err
	}
	r.lateRefunded[paymentID] = refunded
	r.refunds[paymentID] = append(r.refunds[paymentID], practiceRefundMatch{event: item})
	return nil
}

func (r practiceReplay) loadSettledRefunds(ctx context.Context, tx pgx.Tx, classID int64) error {
	rows, err := tx.Query(ctx, `SELECT v.id,v.fact,p.payment_version_id
	 FROM calculation_draft_performance origin
	 JOIN calculation_draft_performance p ON p.payment_version_id=origin.source_version_id
	 JOIN settlements s ON s.period_id=p.period_id JOIN source_order_versions v ON v.id=p.source_version_id
	 WHERE origin.period_id=$1 AND origin.payment_version_id IS NULL AND p.period_id<>$1
	 ORDER BY v.occurred_at,v.id`, classID)
	if err != nil {
		return err
	}
	var paymentID int64
	var item paymentInput
	_, err = pgx.ForEachRow(rows, []any{&item.ID, &item.Fact, &paymentID}, func() error {
		return r.addLateRefund(paymentID, item)
	})
	return err
}
