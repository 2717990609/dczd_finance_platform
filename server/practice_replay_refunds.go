package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

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
	id, base, amount, recovered int64
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
		if errors.Is(err, errPracticeRefundReplay) {
			for _, event := range classEvents {
				draft.Issues = append(draft.Issues, draftIssue{FactID: event.ID, Code: "PRACTICE_REFUND_REPLAY_REQUIRED"})
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
	if err := replay.loadSettledRefunds(ctx, tx, classID); err != nil {
		return nil, err
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
			replay.addRefund(event.paymentID, paymentInput{ID: event.ID, Fact: event.Fact})
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
		for i := range originals {
			line := &originals[i]
			delta := line.amount - line.recovered - target[line.practiceReplayLineKey].Amount
			if delta <= 0 {
				continue
			}
			anchor, found := anchors[line.payment]
			if !found {
				anchor = group[len(group)-1]
			}
			lines = append(lines, refundLine{
				OriginalLineID: line.id, RefundPeriodID: anchor.RefundPeriodID, RefundSourceVersionID: anchor.ID,
				Amount: -delta, Principal: *anchor.Fact.AmountMinor, RefundID: anchor.Fact.RefundID, OccurredAt: anchor.Fact.OccurredAt.UTC(),
			})
			line.recovered += delta
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
	if err != nil || len(before) != len(originals) || len(baseline.Performance) != len(replay.posted) {
		return replay, nil, errPracticeRefundReplay
	}
	for _, posting := range baseline.Performance {
		expected := practiceReplayPosting{posting.PaymentVersionID, posting.PersonID, posting.Principal}
		if replay.posted[posting.SourceVersionID] != expected {
			return replay, nil, errPracticeRefundReplay
		}
	}
	for _, line := range originals {
		if value, found := before[line.practiceReplayLineKey]; !found || value.Amount != line.amount || value.Base != line.base ||
			line.recovered < 0 || line.recovered > line.amount {
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
	 coalesce((SELECT -sum(r.amount_minor) FROM calculation_draft_refunds r JOIN settlements closed USING(period_id)
	  WHERE r.original_line_id=l.id),0)::bigint
	 FROM calculation_draft_lines l WHERE l.period_id=$1 ORDER BY l.id`, classID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceReplayOriginal, error) {
		var line practiceReplayOriginal
		err := row.Scan(&line.id, &line.payment, &line.enrollment, &line.person, &line.component, &line.unit, &line.pool, &line.owner,
			&line.identity, &line.base, &line.amount, &line.recovered)
		return line, err
	})
}

func (r practiceReplay) addRefund(paymentID int64, item paymentInput) {
	r.refunds[paymentID] = append(r.refunds[paymentID], practiceRefundMatch{event: item})
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
		r.addRefund(paymentID, item)
		return nil
	})
	return err
}
