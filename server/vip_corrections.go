package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Reconstruct the monetary posting from its original rule version and payment
// facts before comparing the corrected recipients. The confirmed transaction
// still supplies the payment timing, recognition basis and award terms.
const vipCorrectionPaymentsSQL = `WITH booked AS (
 SELECT a.source_version_id,a.period_id FROM periods target
 JOIN periods origin ON origin.product_id=target.product_id AND origin.ends_on<target.starts_on AND origin.aggregate_package_id IS NULL
 JOIN settlements s ON s.period_id=origin.id
 JOIN calculation_drafts d ON d.period_id=s.period_id AND d.catalog_refunds_ready
 JOIN calculation_draft_performance a ON a.period_id=d.period_id AND a.payment_version_id IS NULL
 WHERE target.id=$1
)
SELECT b.period_id,anchor.id,original.id,
 CASE WHEN timing.received_later THEN coalesce(confirmed.source_version_id,original.id) ELSE latest.id END,
 original.fact,CASE WHEN timing.received_later THEN coalesce(confirmed.fact,original.fact) ELSE latest.fact END,
 CASE WHEN timing.received_later THEN true
 ELSE (oc.entity_type,oc.business_key,oc.classifier) IS NOT DISTINCT FROM (latest.entity_type,latest.business_key,latest.classifier) END,
 coalesce(timing.handling_starts_on<target.starts_on,false)
FROM booked b JOIN periods target ON target.id=$1
JOIN source_order_versions original ON original.id=b.source_version_id
LEFT JOIN source_order_classifications oc ON oc.source_version_id=original.id
JOIN LATERAL (
 SELECT l.id,v.definition->'benefit_actions' @> '[{"beneficiary_mode":"` + vipOrderReferrerMode + `"}]' AS has_referral
 FROM calculation_draft_lines l JOIN product_rule_versions v ON v.source_id=l.catalog_rule_id
 WHERE l.period_id=b.period_id AND l.source_version_id=original.id ORDER BY l.id LIMIT 1
) anchor ON true
JOIN LATERAL (
 SELECT v.id,v.fact,v.updated_at,lc.sales_claims,lc.entity_type,lc.business_key,lc.classifier,
  CASE WHEN anchor.has_referral THEN jsonb_path_query_array(v.fact,` + vipReferralParticipantsPath + `) END AS referrals
 FROM source_order_versions v LEFT JOIN source_order_classifications lc ON lc.source_version_id=v.id
 WHERE ` + correctionOrderIdentity + ` AND (v.updated_at,v.id)>(original.updated_at,original.id)
 ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
) latest ON true
LEFT JOIN LATERAL (
 SELECT v.id,v.updated_at FROM source_order_versions v
 LEFT JOIN source_order_classifications c ON c.source_version_id=v.id
 WHERE ` + correctionOrderIdentity + ` AND (v.updated_at,v.id)<(latest.updated_at,latest.id)
 AND (c.sales_claims,CASE WHEN anchor.has_referral THEN jsonb_path_query_array(v.fact,` + vipReferralParticipantsPath + `) END)
  IS DISTINCT FROM (latest.sales_claims,latest.referrals)
 ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
) previous ON true
JOIN LATERAL (
 SELECT min(greatest(v.received_at,c.sales_claims_completed_at)) AS received_at FROM source_order_versions v
 LEFT JOIN source_order_classifications c ON c.source_version_id=v.id
 WHERE ` + correctionOrderIdentity + `
 AND (previous.id IS NULL OR (v.updated_at,v.id)>(previous.updated_at,previous.id))
) receipt ON true
LEFT JOIN LATERAL (
 SELECT p.id AS period_id,p.starts_on FROM (
  SELECT c.period_id,c.source_version_id FROM calculation_draft_lines l JOIN calculation_draft_corrections c ON c.original_line_id=l.id
   WHERE l.source_version_id=original.id AND l.period_id=b.period_id
  UNION SELECT i.period_id,i.source_version_id FROM calculation_draft_lines l JOIN calculation_draft_issues i ON i.correction_line_id=l.id
   WHERE l.source_version_id=original.id AND l.period_id=b.period_id
    AND i.code<>'CORRECTION_REFUND_WAITING'
 ) active JOIN periods p ON p.id=active.period_id
 LEFT JOIN source_order_classifications ac ON ac.source_version_id=active.source_version_id
 JOIN source_order_versions av ON av.id=active.source_version_id
 WHERE (ac.sales_claims,CASE WHEN anchor.has_referral THEN jsonb_path_query_array(av.fact,` + vipReferralParticipantsPath + `) END)
  IS NOT DISTINCT FROM (latest.sales_claims,latest.referrals)
 AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=active.period_id)
 ORDER BY p.starts_on,p.id LIMIT 1
) reserved ON true
LEFT JOIN periods observed ON reserved.period_id IS NULL AND observed.product_id=target.product_id AND observed.aggregate_package_id IS NULL
 AND (receipt.received_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN observed.starts_on AND observed.ends_on
 AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=observed.id)
CROSS JOIN LATERAL (
 SELECT coalesce(reserved.starts_on,observed.starts_on) AS handling_starts_on,
  coalesce(coalesce(reserved.starts_on,observed.starts_on)>target.starts_on,false)
   OR (reserved.period_id IS NULL OR reserved.period_id=target.id) AND receipt.received_at >= $3 AS received_later
) timing
LEFT JOIN LATERAL (
 SELECT c.source_version_id,v.fact FROM calculation_draft_lines l
 JOIN calculation_draft_corrections c ON c.original_line_id=l.id
 JOIN settlements s ON s.period_id=c.period_id JOIN source_order_versions v ON v.id=c.source_version_id
 WHERE l.source_version_id=original.id AND l.period_id=b.period_id
 ORDER BY s.id DESC,c.id DESC LIMIT 1
) confirmed ON timing.received_later
WHERE ((oc.sales_claims,CASE WHEN anchor.has_referral THEN jsonb_path_query_array(original.fact,` + vipReferralParticipantsPath + `) END)
 IS DISTINCT FROM (latest.sales_claims,latest.referrals) OR EXISTS(
 SELECT 1 FROM calculation_draft_lines l JOIN calculation_draft_corrections c ON c.original_line_id=l.id
 WHERE l.source_version_id=original.id AND l.period_id=b.period_id
))
AND (coalesce(reserved.period_id,observed.id)=$1
 OR EXISTS(SELECT 1 FROM calculation_draft_lines l WHERE l.id=ANY($4::bigint[])
  AND l.period_id=b.period_id AND l.source_version_id=original.id)
 OR coalesce(reserved.period_id,observed.id) IS NULL
  AND $2 <= CURRENT_TIMESTAMP AND CURRENT_TIMESTAMP < $3 AND receipt.received_at < $2)
ORDER BY anchor.id LIMIT $5`

type vipCorrectionPayment struct {
	Period, Anchor, OriginalID, LatestID int64
	Original, Latest                     sourceFact
	SameClassification                   bool
	ReservedElsewhere                    bool
	Frozen                               []vipCorrectionPosting
}

type vipCorrectionPosting struct {
	ID   int64
	Line draftLine
}

func addVIPRecipientCorrections(ctx context.Context, tx pgx.Tx, periodID int64, window periodWindow, draft *paymentDraft) error {
	refundedLines := make([]int64, 0, len(draft.Refunds))
	for _, refund := range draft.Refunds {
		if refund.OriginalLineID != 0 {
			refundedLines = append(refundedLines, refund.OriginalLineID)
		}
	}
	rows, err := tx.Query(ctx, vipCorrectionPaymentsSQL, periodID, window.Start, window.Through, refundedLines, maxDraftPayments+1)
	if err != nil {
		return err
	}
	payments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (vipCorrectionPayment, error) {
		var p vipCorrectionPayment
		err := row.Scan(&p.Period, &p.Anchor, &p.OriginalID, &p.LatestID, &p.Original, &p.Latest, &p.SameClassification, &p.ReservedElsewhere)
		return p, err
	})
	if err != nil || len(payments) == 0 {
		return err
	}
	if len(payments) > maxDraftPayments {
		return errDraftLimit
	}
	// A refund that precedes an open correction uses the last confirmed
	// recipient. Without any confirmed change the normal refund already used it.
	payments = slices.DeleteFunc(payments, func(p vipCorrectionPayment) bool { return p.OriginalID == p.LatestID })
	if len(payments) == 0 {
		return nil
	}
	lines, err := loadVIPCorrectionPostings(ctx, tx, payments)
	if err != nil {
		return err
	}
	balances, err := readCorrectionBalances(ctx, tx, lines, draft.Refunds)
	if err != nil {
		return err
	}
	quotes := make([]catalogPaymentMatch, 0, 2*len(payments))
	for _, payment := range payments {
		quotes = append(quotes, payment.match(payment.OriginalID), payment.match(payment.LatestID))
	}
	data, err := loadVIPRuntimeData(ctx, tx, periodID, quotes, false)
	if err != nil {
		return err
	}
	applyVIPQuotes(quotes, data)
	for i, payment := range payments {
		if err := payment.append(draft, quotes[2*i], quotes[2*i+1], balances, data); err != nil {
			return err
		}
	}
	if len(draft.Corrections) == 0 {
		return nil
	}
	return attributeCorrectionRecipients(ctx, tx, draft.Corrections)
}

func loadVIPCorrectionPostings(ctx context.Context, tx pgx.Tx, payments []vipCorrectionPayment) ([]correctionInput, error) {
	periods, facts := make([]int64, len(payments)), make([]int64, len(payments))
	for i, p := range payments {
		periods[i], facts[i] = p.Period, p.OriginalID
	}
	rows, err := tx.Query(ctx, `SELECT req.position,l.id,l.component_type,coalesce(l.person_id,0),l.base_minor,l.amount_minor,
	 l.catalog_rule_id,l.beneficiary_unit_id,coalesce(l.allocation_owner_id,0),coalesce(l.applied_rate_bps,0),coalesce(l.catalog_pool_id,0)
	 FROM unnest($1::bigint[],$2::bigint[]) WITH ORDINALITY AS req(period_id,fact_id,position)
	 JOIN calculation_draft_lines l ON l.period_id=req.period_id AND l.source_version_id=req.fact_id
	 WHERE l.catalog_rule_id IS NOT NULL ORDER BY req.position,l.id`, periods, facts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inputs []correctionInput
	for rows.Next() {
		var pos int
		var saved vipCorrectionPosting
		var benefit catalogDraftBenefit
		l := &saved.Line
		if err := rows.Scan(&pos, &saved.ID, &l.Component, &l.PersonID, &l.Base, &l.Amount,
			&benefit.Rule, &benefit.Unit, &benefit.AllocationOwnerID, &benefit.Rate, &benefit.PoolID); err != nil {
			return nil, err
		}
		l.Catalog = &benefit
		p := &payments[pos-1]
		p.Frozen = append(p.Frozen, saved)
		inputs = append(inputs, p.input(saved))
	}
	return inputs, rows.Err()
}

func (p vipCorrectionPayment) match(id int64) catalogPaymentMatch {
	line := p.Frozen[0].Line
	return catalogPaymentMatch{SourceVersionID: id, OrderID: p.Original.OrderID, PaidAt: p.Original.OccurredAt,
		PaidMinor: p.Original.AmountMinor, RecognizedMinor: &line.Base, PaidInWindow: true, RuleID: line.Catalog.Rule.String(), Status: "MATCHED",
		MembershipOrder:       p.Original.Domain == "VIP" && strings.HasPrefix(p.Original.RecordKey, "ai_vip_order:"),
		SourceTransactionType: p.Original.TransactionType}
}

func (p vipCorrectionPayment) input(posted vipCorrectionPosting) correctionInput {
	return correctionInput{OriginalLineID: posted.ID, OriginalPersonID: posted.Line.PersonID,
		OriginalAmount: posted.Line.Amount, SourceVersionID: p.LatestID, OriginalUnit: posted.Line.Catalog.Unit}
}

func (p vipCorrectionPayment) append(draft *paymentDraft, old, latest catalogPaymentMatch, balances map[int64]*correctionBalance, data vipRuntimeData) error {
	// Match only the fields which change the computation or its identity. The
	// display version is intentionally absent so a rename retains the draft.
	signature, err := json.Marshal(struct {
		Anchor                            int64
		Payment, Domain, Key, Transaction string
		Paid                              *int64
		At                                int64
		Claims                            []vipClaim
	}{p.Anchor, p.Latest.PaymentID, p.Latest.Domain, p.Latest.SourceKey, p.transactionTypeForCorrection(),
		p.Latest.AmountMinor, p.Latest.OccurredAt.UnixMicro(), data.Claims[p.LatestID]})
	if err != nil {
		return err
	}
	anchor := p.input(p.Frozen[0])
	issue := func(code string) error {
		return appendCorrection(draft, anchor, p.Frozen[0].Line.Component, string(signature), correctionEvaluation{issue: code})
	}
	if p.ReservedElsewhere {
		return issue("CORRECTION_REFUND_WAITING")
	}
	if !p.samePayment() {
		return issue("CORRECTION_SCOPE_CHANGED")
	}
	if p.noOutstandingBalance(balances) {
		return nil
	}
	before, err := vipCorrectionQuote(old, data)
	if err != nil {
		return err
	}
	after, err := vipCorrectionQuote(latest, data)
	if err != nil {
		return err
	}
	if len(before) != len(p.Frozen) || len(after) != len(p.Frozen) {
		return issue("CORRECTION_LINKED_REVIEW")
	}
	for _, posting := range p.Frozen {
		frozen := posting.Line
		original, replacement := before[frozen.Component], after[frozen.Component]
		if !sameVIPCorrectionPosting(frozen, original) || original.PersonID != frozen.PersonID ||
			!sameVIPReplacementTerms(frozen, replacement) {
			return issue("CORRECTION_LINKED_REVIEW")
		}
		if balance := balances[posting.ID]; balance != nil && (balance.refundDebit < 0 || balance.refundDebit > frozen.Amount) {
			return issue("CORRECTION_REFUND_CONFLICT")
		}
	}
	for _, posting := range p.Frozen {
		balance := balances[posting.ID]
		if posting.Line.PersonID == 0 && !isVIPCompanyPosting(posting.Line) {
			continue
		}
		input := p.input(posting)
		line := after[posting.Line.Component]
		expected := line.Amount - balance.refundDebit
		var result correctionEvaluation
		var err error
		if posting.Line.PersonID != 0 {
			result, err = evaluateRecipientCorrection(input, balance, line.PersonID, expected)
		} else {
			result, err = evaluateOrganizationCorrection(input, balance, &line.Catalog.Unit, expected)
		}
		if err != nil {
			return err
		}
		if err := appendCorrection(draft, input, line.Component, string(signature), result); err != nil {
			return err
		}
	}
	return nil
}

func (p vipCorrectionPayment) noOutstandingBalance(balances map[int64]*correctionBalance) bool {
	for _, posting := range p.Frozen {
		balance := balances[posting.ID]
		if balance.refundDebit != posting.Line.Amount {
			return false
		}
		for _, amount := range balance.recorded {
			if amount != 0 {
				return false
			}
		}
		for _, amount := range balance.organizations {
			if amount != 0 {
				return false
			}
		}
	}
	return true
}

func (p vipCorrectionPayment) samePayment() bool {
	old, latest := p.Original, p.Latest
	return p.SameClassification && old.PaymentID != "" && old.PaymentID == latest.PaymentID &&
		old.Domain == latest.Domain && old.SourceKey == latest.SourceKey && old.TransactionType == p.transactionTypeForCorrection() &&
		old.OccurredAt.Equal(latest.OccurredAt) && old.AmountMinor != nil && latest.AmountMinor != nil && *old.AmountMinor == *latest.AmountMinor
}

func (p vipCorrectionPayment) transactionTypeForCorrection() string {
	// Member purchase and renewal share the same scenario. A source revision
	// can name the subtype after an older payment was settled without it.
	latest := p.Latest.TransactionType
	if p.Original.Domain == "VIP" && p.Original.TransactionType == "" && (latest == "NEW" || latest == "RENEWAL") {
		return ""
	}
	return latest
}

func vipCorrectionQuote(row catalogPaymentMatch, data vipRuntimeData) (map[string]draftLine, error) {
	var trial paymentDraft
	if err := addVIPDraftPayment(&trial, row, data, reportAttribution{}); err != nil {
		return nil, err
	}
	if trial.blockingIssues() != 0 {
		return nil, nil
	}
	postings := make(map[string]draftLine, len(trial.Lines))
	for _, line := range trial.Lines {
		postings[line.Component] = line
	}
	return postings, nil
}

func sameVIPCorrectionPosting(frozen, computed draftLine) bool {
	return computed.Catalog != nil && *frozen.Catalog == *computed.Catalog && frozen.Base == computed.Base && frozen.Amount == computed.Amount
}

func isVIPCompanyPosting(line draftLine) bool {
	return line.PersonID == 0 && line.Catalog != nil && line.Catalog.Unit.Valid &&
		line.Catalog.AllocationOwnerID == 0 && line.Catalog.PoolID == 0
}

func sameVIPReplacementTerms(frozen, replacement draftLine) bool {
	if (frozen.PersonID != 0) != (replacement.PersonID != 0) {
		return false
	}
	if isVIPCompanyPosting(frozen) && isVIPCompanyPosting(replacement) {
		benefit := *replacement.Catalog
		benefit.Unit = frozen.Catalog.Unit
		replacement.Catalog = &benefit
	}
	return sameVIPCorrectionPosting(frozen, replacement)
}
