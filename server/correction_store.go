package main

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type correctionInput struct {
	OriginalLineID, OriginalPersonID, OriginalAmount int64
	SourceVersionID                                  int64
	Original, Latest                                 sourceFact
	Components                                       []component
	OriginalUnit                                     pgtype.UUID
}

type correctionLine struct {
	OriginalLineID, SourceVersionID int64
	PersonID, Amount                int64
	Unit                            pgtype.UUID `json:"-"`
	organization                    reportOrganization
}

type pendingCorrectionRecovery struct {
	PersonID, Amount int64
}

type correctionEvaluation struct {
	lines   []correctionLine
	pending []recipientDelta
	issue   string
}

type correctionBalance struct {
	recorded           map[int64]int64
	organizations      map[pgtype.UUID]int64
	refunded           int64
	refundDebit        int64
	currentRefundDebit int64
	recoverability     map[int64]string
}

const correctionOrderIdentity = `(v.source,v.order_id,v.line_id,v.record_key,v.kind)
 =(original.source,original.order_id,original.line_id,original.record_key,original.kind)`

// The current financial revision may have several display-only versions.
// Keep the cycle in which that revision was first observed.
const correctionInputsSQL = `SELECT l.id,l.person_id,l.amount_minor,latest.id,original.fact,latest.fact,rule.components
 FROM periods target JOIN periods origin ON origin.product_id=target.product_id AND origin.ends_on<target.starts_on
 JOIN settlements closed ON closed.period_id=origin.id
 JOIN calculation_draft_lines l ON l.period_id=origin.id AND l.component_type='referral'
 JOIN rule_versions rule ON rule.id=l.rule_id
 JOIN source_order_versions original ON original.id=l.source_version_id
 JOIN LATERAL (
  SELECT v.id,v.fact,v.updated_at FROM source_order_versions v WHERE ` + correctionOrderIdentity + `
  ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
 ) latest ON latest.id<>original.id
 LEFT JOIN LATERAL (
  SELECT v.id,v.updated_at FROM source_order_versions v WHERE ` + correctionOrderIdentity + `
   AND (v.updated_at,v.id)<(latest.updated_at,latest.id)
   AND (v.fact-` + draftDisplayFactFields + `) IS DISTINCT FROM (latest.fact-` + draftDisplayFactFields + `)
  ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
 ) previous_change ON true
 JOIN LATERAL (
  SELECT min(v.received_at) AS received_at FROM source_order_versions v WHERE ` + correctionOrderIdentity + `
   AND (previous_change.id IS NULL OR (v.updated_at,v.id)>(previous_change.updated_at,previous_change.id))
 ) receipt ON true
 LEFT JOIN LATERAL (
  SELECT min(active.period_id) AS period_id FROM (
   SELECT c.period_id FROM calculation_draft_corrections c WHERE c.original_line_id=l.id
   UNION SELECT i.period_id FROM calculation_draft_issues i WHERE i.correction_line_id=l.id
  ) active WHERE NOT EXISTS(SELECT 1 FROM settlements st WHERE st.period_id=active.period_id)
 ) reserved ON true
 WHERE target.id=$1 AND (
  reserved.period_id=target.id OR (reserved.period_id IS NULL AND (
   (receipt.received_at >= $2 AND receipt.received_at < $3)
   OR l.id=ANY($5::bigint[])
   OR ($2 <= CURRENT_TIMESTAMP AND CURRENT_TIMESTAMP < $3 AND receipt.received_at < $2
       AND NOT EXISTS(
        SELECT 1 FROM periods observed WHERE observed.product_id=target.product_id
        AND (receipt.received_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN observed.starts_on AND observed.ends_on
        AND NOT EXISTS(SELECT 1 FROM settlements st WHERE st.period_id=observed.id)
       ))
  ))
 )
 ORDER BY l.id LIMIT $4`

func addRecommendationCorrections(ctx context.Context, tx pgx.Tx, periodID int64, window periodWindow, draft *paymentDraft) error {
	refundedLines := make([]int64, 0, len(draft.Refunds))
	for _, refund := range draft.Refunds {
		if refund.OriginalLineID != 0 {
			refundedLines = append(refundedLines, refund.OriginalLineID)
		}
	}
	rows, err := tx.Query(ctx, correctionInputsSQL, periodID, window.Start, window.Through, maxDraftPayments+1, refundedLines)
	if err != nil {
		return err
	}
	inputs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (correctionInput, error) {
		var input correctionInput
		err := row.Scan(&input.OriginalLineID, &input.OriginalPersonID, &input.OriginalAmount, &input.SourceVersionID,
			&input.Original, &input.Latest, &input.Components)
		return input, err
	})
	if err != nil || len(inputs) == 0 {
		return err
	}
	if len(inputs) > maxDraftPayments {
		return errDraftLimit
	}
	balances, err := readCorrectionBalances(ctx, tx, inputs, draft.Refunds)
	if err != nil {
		return err
	}
	keys := make(map[identityKey]bool)
	oldWindow := periodWindow{Start: inputs[0].Original.OccurredAt, Through: inputs[0].Original.OccurredAt.Add(time.Microsecond)}
	for _, entry := range inputs {
		if at := entry.Original.OccurredAt; at.Before(oldWindow.Start) {
			oldWindow.Start = at
		} else if !at.Before(oldWindow.Through) {
			oldWindow.Through = at.Add(time.Microsecond)
		}
		role := referralComponent(entry.Components).SourceRole
		for _, p := range entry.Latest.Participants {
			if p.Role == role {
				keys[identityKey{entry.Latest.Source, p.UserID}] = true
			}
		}
		if role == "DIRECT_REFERRER" && entry.Latest.ReferrerID != "" {
			keys[identityKey{entry.Latest.Source, entry.Latest.ReferrerID}] = true
		}
	}
	identities, err := readIdentityAssignments(ctx, tx, oldWindow, keys)
	if err != nil {
		return err
	}
	for _, input := range inputs {
		result, err := computeRecommendationCorrection(input, balances[input.OriginalLineID], identities)
		if err != nil {
			return err
		}
		key := ""
		if result.issue != "" || len(result.pending) != 0 {
			key, err = correctionIssueKey(input)
			if err != nil {
				return err
			}
		}
		if err := appendCorrection(draft, input, "referral", key, result); err != nil {
			return err
		}
	}
	return nil
}

func appendCorrection(draft *paymentDraft, input correctionInput, component, key string, result correctionEvaluation) error {
	if result.issue != "" {
		draft.Issues = append(draft.Issues, draftIssue{FactID: input.SourceVersionID, Component: component, Code: result.issue,
			CorrectionLineID: input.OriginalLineID, stableKey: key})
	}
	for _, pending := range result.pending {
		draft.Issues = append(draft.Issues, draftIssue{FactID: input.SourceVersionID, Component: component, Code: "CORRECTION_RECOVERY_PENDING",
			CorrectionLineID: input.OriginalLineID, stableKey: key,
			Recovery: &pendingCorrectionRecovery{PersonID: pending.PersonID, Amount: pending.Amount}})
	}
	for _, line := range result.lines {
		total, err := addMinor(draft.CorrectionTotal, line.Amount)
		if err != nil {
			return err
		}
		draft.CorrectionTotal = total
		draft.Corrections = append(draft.Corrections, line)
	}
	return nil
}

func correctionIssueKey(input correctionInput) (string, error) {
	latest := input.Latest
	role := referralComponent(input.Components).SourceRole
	var recipients []string
	for _, participant := range latest.Participants {
		if participant.Role == role && participant.UserID != "" {
			recipients = append(recipients, participant.UserID)
		}
	}
	if len(recipients) == 0 && role == "DIRECT_REFERRER" && latest.ReferrerID != "" {
		recipients = append(recipients, latest.ReferrerID)
	}
	slices.Sort(recipients)
	value, err := json.Marshal(struct {
		OriginalLineID                             int64
		PaymentID, Domain, ProductKey, Transaction string
		OccurredAt                                 time.Time
		Amount                                     *int64
		Recipients                                 []string
	}{input.OriginalLineID, latest.PaymentID, latest.Domain, latest.SourceKey,
		latest.TransactionType, latest.OccurredAt.UTC(), latest.AmountMinor, slices.Compact(recipients)})
	return string(value), err
}

func referralComponent(components []component) component {
	for _, c := range components {
		if c.Type == "referral" {
			return c
		}
	}
	return component{}
}

func readCorrectionBalances(ctx context.Context, tx pgx.Tx, inputs []correctionInput, pendingRefunds []refundLine) (map[int64]*correctionBalance, error) {
	ids := make([]int64, 0, len(inputs))
	balances := make(map[int64]*correctionBalance, len(inputs))
	originals := make(map[int64]correctionInput, len(inputs))
	for _, input := range inputs {
		id := input.OriginalLineID
		ids = append(ids, id)
		originals[id] = input
		entry := &correctionBalance{recorded: make(map[int64]int64), recoverability: make(map[int64]string)}
		if err := entry.apply(input.OriginalPersonID, input.OriginalUnit, input.OriginalAmount, 0, false); err != nil {
			return nil, err
		}
		balances[id] = entry
	}
	rows, err := tx.Query(ctx, `SELECT c.original_line_id,coalesce(c.person_id,0),c.beneficiary_unit_id,c.amount_minor,0::bigint,false
	 FROM calculation_draft_corrections c JOIN settlements s USING (period_id)
	 WHERE c.original_line_id=ANY($1::bigint[])
	 UNION ALL
	 SELECT a.original_line_id,coalesce(l.person_id,0),l.beneficiary_unit_id,a.amount_minor,(v.fact->>'amountMinor')::bigint,true
	 FROM calculation_draft_refunds a JOIN settlements s USING (period_id)
	 JOIN calculation_draft_lines l ON l.id=a.original_line_id JOIN source_order_versions v ON v.id=a.refund_source_version_id
	 WHERE a.original_line_id=ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, person, amount, principal int64
		var unit pgtype.UUID
		var refund bool
		if err = rows.Scan(&id, &person, &unit, &amount, &principal, &refund); err != nil {
			rows.Close()
			return nil, err
		}
		if err = balances[id].apply(person, unit, amount, principal, refund); err != nil {
			rows.Close()
			return nil, err
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, item := range pendingRefunds {
		if entry := balances[item.OriginalLineID]; entry != nil {
			origin := originals[item.OriginalLineID]
			if err = entry.apply(origin.OriginalPersonID, origin.OriginalUnit, item.Amount, item.Principal, true); err != nil {
				return nil, err
			}
			entry.currentRefundDebit, err = addMinor(entry.currentRefundDebit, -item.Amount)
			if err != nil {
				return nil, err
			}
		}
	}
	rows, err = tx.Query(ctx, `SELECT original_line_id,person_id,outcome FROM correction_recovery_decisions
	 WHERE original_line_id=ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, person int64
		var outcome string
		if err = rows.Scan(&id, &person, &outcome); err != nil {
			return nil, err
		}
		balances[id].recoverability[person] = outcome
	}
	return balances, rows.Err()
}

func (b *correctionBalance) apply(person int64, unit pgtype.UUID, amount, principal int64, refund bool) error {
	var err error
	if unit.Valid {
		if b.organizations == nil {
			b.organizations = make(map[pgtype.UUID]int64)
		}
		b.organizations[unit], err = addMinor(b.organizations[unit], amount)
	} else {
		b.recorded[person], err = addMinor(b.recorded[person], amount)
	}
	if err == nil && refund {
		b.refundDebit, err = addMinor(b.refundDebit, -amount)
	}
	if err == nil && refund {
		b.refunded, err = addMinor(b.refunded, principal)
	}
	return err
}

func computeRecommendationCorrection(input correctionInput, b *correctionBalance,
	identities map[identityKey][]identityAssignment) (correctionEvaluation, error) {
	old, latest := input.Original, input.Latest
	if old.PaymentID != latest.PaymentID || old.Domain != latest.Domain || old.SourceKey != latest.SourceKey ||
		old.TransactionType != latest.TransactionType || !old.OccurredAt.Equal(latest.OccurredAt) {
		return correctionEvaluation{issue: "CORRECTION_SCOPE_CHANGED"}, nil
	}
	part := referralComponent(input.Components)
	if part.SourceRole == "" || latest.AmountMinor == nil || *latest.AmountMinor < 0 {
		return correctionEvaluation{issue: "CORRECTION_MISSING_FACT_OR_RULE"}, nil
	}
	assignment, issue := paymentBeneficiary(latest, part.SourceRole, identities)
	if issue != "" {
		return correctionEvaluation{issue: issue}, nil
	}
	base := *latest.AmountMinor
	var expected int64
	if base > 0 {
		expected = calculatePaymentAmount(base, part)
	}
	if b.refunded > 0 {
		negative, err := refundDelta(part, base, expected, b.refunded, 0)
		if err != nil {
			return correctionEvaluation{issue: "CORRECTION_REFUND_CONFLICT"}, nil
		}
		expected += negative
	}
	return evaluateRecipientCorrection(input, b, assignment.PersonID, expected)
}

func evaluateRecipientCorrection(input correctionInput, b *correctionBalance, recipientID, expected int64) (correctionEvaluation, error) {
	desired := map[int64]int64{recipientID: expected}
	if original := input.OriginalPersonID; recipientID != original &&
		b.recoverability[original] == "departed_unreachable" && b.currentRefundDebit > 0 {
		// Undo the provisional debit from the frozen payee when their recovery was waived.
		retained, err := addMinor(b.recorded[original], b.currentRefundDebit)
		if err != nil {
			return correctionEvaluation{}, err
		}
		desired[original] = max(0, retained)
	}
	deltas, pending, err := recommendationDeltas(b.recorded, desired, b.recoverability)
	if err != nil {
		return correctionEvaluation{}, err
	}
	lines := make([]correctionLine, 0, len(deltas))
	for _, delta := range deltas {
		lines = append(lines, correctionLine{OriginalLineID: input.OriginalLineID, SourceVersionID: input.SourceVersionID, PersonID: delta.PersonID, Amount: delta.Amount})
	}
	return correctionEvaluation{lines: lines, pending: pending}, nil
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
