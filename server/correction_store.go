package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type correctionInput struct {
	OriginalLineID, OriginalPersonID, OriginalAmount int64
	SourceVersionID                                  int64
	Original, Latest                                 sourceFact
	Components                                       []component
}

type correctionLine struct {
	OriginalLineID, SourceVersionID int64
	PersonID, Amount                int64
	organization                    reportOrganization
}

type correctionBalance struct {
	recorded           map[int64]int64
	refunded           int64
	currentRefundDebit int64
	recoverability     map[int64]string
}

const correctionInputsSQL = `SELECT l.id,l.person_id,l.amount_minor,latest.id,original.fact,latest.fact,rule.components
 FROM periods target JOIN periods origin ON origin.product_id=target.product_id AND origin.ends_on<target.starts_on
 JOIN settlements closed ON closed.period_id=origin.id
 JOIN calculation_draft_lines l ON l.period_id=origin.id AND l.component_type='referral'
 JOIN rule_versions rule ON rule.id=l.rule_id
 JOIN source_order_versions original ON original.id=l.source_version_id
 JOIN LATERAL (
  SELECT v.id,v.fact,v.received_at FROM source_order_versions v
  WHERE (v.source,v.order_id,v.line_id,v.record_key,v.kind)
    =(original.source,original.order_id,original.line_id,original.record_key,original.kind)
  ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
 ) latest ON latest.id<>original.id
 LEFT JOIN LATERAL (
  SELECT min(c.period_id) AS period_id FROM calculation_draft_corrections c
  WHERE c.original_line_id=l.id AND NOT EXISTS(SELECT 1 FROM settlements st WHERE st.period_id=c.period_id)
 ) reserved ON true
 WHERE target.id=$1 AND (
  reserved.period_id=target.id OR (reserved.period_id IS NULL AND (
   (latest.received_at >= $2 AND latest.received_at < $3)
   OR l.id=ANY($5::bigint[])
   OR ($2 <= CURRENT_TIMESTAMP AND CURRENT_TIMESTAMP < $3 AND latest.received_at < $2
       AND NOT EXISTS(
        SELECT 1 FROM periods observed WHERE observed.product_id=target.product_id
        AND (latest.received_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN observed.starts_on AND observed.ends_on
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
		lines, issue, err := computeRecommendationCorrection(input, balances[input.OriginalLineID], identities)
		if err != nil {
			return err
		}
		if issue != "" {
			key, err := correctionIssueKey(input)
			if err != nil {
				return err
			}
			draft.Issues = append(draft.Issues, draftIssue{FactID: input.SourceVersionID, Component: "referral", Code: issue, stableKey: key})
		}
		for _, line := range lines {
			draft.CorrectionTotal, err = addMinor(draft.CorrectionTotal, line.Amount)
			if err != nil {
				return err
			}
			draft.Corrections = append(draft.Corrections, line)
		}
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
	originalPeople := make(map[int64]int64, len(inputs))
	for _, input := range inputs {
		id := input.OriginalLineID
		ids = append(ids, id)
		originalPeople[id] = input.OriginalPersonID
		balances[id] = &correctionBalance{recorded: map[int64]int64{input.OriginalPersonID: input.OriginalAmount}, recoverability: make(map[int64]string)}
	}
	rows, err := tx.Query(ctx, `SELECT c.original_line_id,c.person_id,c.amount_minor,0::bigint
	 FROM calculation_draft_corrections c JOIN settlements s USING (period_id)
	 WHERE c.original_line_id=ANY($1::bigint[])
	 UNION ALL
	 SELECT a.original_line_id,l.person_id,a.amount_minor,(v.fact->>'amountMinor')::bigint
	 FROM calculation_draft_refunds a JOIN settlements s USING (period_id)
	 JOIN calculation_draft_lines l ON l.id=a.original_line_id JOIN source_order_versions v ON v.id=a.refund_source_version_id
	 WHERE a.original_line_id=ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, person, amount, principal int64
		if err = rows.Scan(&id, &person, &amount, &principal); err != nil {
			rows.Close()
			return nil, err
		}
		if err = balances[id].apply(person, amount, principal); err != nil {
			rows.Close()
			return nil, err
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, item := range pendingRefunds {
		if entry := balances[item.OriginalLineID]; entry != nil {
			if err = entry.apply(originalPeople[item.OriginalLineID], item.Amount, item.Principal); err != nil {
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

func (b *correctionBalance) apply(person, amount, principal int64) error {
	var err error
	b.recorded[person], err = addMinor(b.recorded[person], amount)
	if err == nil {
		b.refunded, err = addMinor(b.refunded, principal)
	}
	return err
}

func computeRecommendationCorrection(input correctionInput, b *correctionBalance,
	identities map[identityKey][]identityAssignment) ([]correctionLine, string, error) {
	old, latest := input.Original, input.Latest
	if old.PaymentID != latest.PaymentID || old.Domain != latest.Domain || old.SourceKey != latest.SourceKey ||
		old.TransactionType != latest.TransactionType || !old.OccurredAt.Equal(latest.OccurredAt) {
		return nil, "CORRECTION_SCOPE_CHANGED", nil
	}
	part := referralComponent(input.Components)
	if part.SourceRole == "" || latest.AmountMinor == nil || *latest.AmountMinor < 0 {
		return nil, "CORRECTION_MISSING_FACT_OR_RULE", nil
	}
	assignment, issue := paymentBeneficiary(latest, part.SourceRole, identities)
	if issue != "" {
		return nil, issue, nil
	}
	base := *latest.AmountMinor
	var expected int64
	if base > 0 {
		expected = calculatePaymentAmount(base, part)
	}
	if b.refunded > 0 {
		negative, err := refundDelta(part, base, expected, b.refunded, 0)
		if err != nil {
			return nil, "CORRECTION_REFUND_CONFLICT", nil
		}
		expected += negative
	}
	desired := map[int64]int64{assignment.PersonID: expected}
	if original := input.OriginalPersonID; assignment.PersonID != original &&
		b.recoverability[original] == "departed_unreachable" && b.currentRefundDebit > 0 {
		// Undo the provisional debit from the frozen payee when their recovery was waived.
		retained, err := addMinor(b.recorded[original], b.currentRefundDebit)
		if err != nil {
			return nil, "", err
		}
		desired[original] = max(0, retained)
	}
	deltas, pending, err := recommendationDeltas(b.recorded, desired, b.recoverability)
	if err != nil {
		return nil, "", err
	}
	if pending {
		issue = "CORRECTION_RECOVERY_PENDING"
	}
	lines := make([]correctionLine, 0, len(deltas))
	for _, delta := range deltas {
		lines = append(lines, correctionLine{OriginalLineID: input.OriginalLineID, SourceVersionID: input.SourceVersionID, PersonID: delta.PersonID, Amount: delta.Amount})
	}
	return lines, issue, nil
}

type reviewCorrectionRecoveryInput struct {
	OriginalLineID, PersonID int64
	Outcome                  string
}

// The caller verifies the settlement operator's permission before recording a debit decision.
func (s workspace) reviewCorrectionRecovery(ctx context.Context, input reviewCorrectionRecoveryInput, actor string) error {
	if input.OriginalLineID < 1 || input.PersonID < 1 || !textOK(actor, 128) ||
		(input.Outcome != "recover" && input.Outcome != "departed_unreachable") {
		return invalid
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO correction_recovery_decisions(original_line_id,person_id,outcome,reviewed_by)
	 SELECT l.id,$2,$3,$4 FROM calculation_draft_lines l JOIN settlements s USING (period_id)
	 WHERE l.id=$1 AND l.component_type='referral' AND (l.person_id=$2 OR EXISTS(
	  SELECT 1 FROM calculation_draft_corrections c JOIN settlements st USING (period_id)
	  WHERE c.original_line_id=l.id AND c.person_id=$2 AND c.amount_minor>0))
	 ON CONFLICT(original_line_id,person_id) DO UPDATE SET
	 outcome=excluded.outcome,reviewed_by=excluded.reviewed_by,reviewed_at=now()`,
		input.OriginalLineID, input.PersonID, input.Outcome, strings.TrimSpace(actor))
	if err == nil && tag.RowsAffected() == 0 {
		return invalid
	}
	return err
}
