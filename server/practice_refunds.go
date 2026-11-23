package main

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

const latestPracticeRefund = `NOT EXISTS (
 SELECT 1 FROM source_order_versions newer WHERE newer.source='DCZD' AND newer.kind='REFUND_SUCCEEDED'
 AND newer.record_key=v.record_key
 AND (newer.updated_at,newer.id)>(v.updated_at,v.id)
)`

const practiceRefundsSQL = `WITH wanted AS (SELECT DISTINCT unnest($1::text[]) AS order_id),
 candidates AS (
  SELECT v.id FROM wanted w JOIN source_order_versions v USING(order_id)
  WHERE v.source='DCZD' AND v.kind='REFUND_SUCCEEDED' AND v.fact->>'domain'='PRACTICE'
  UNION
  SELECT v.id FROM wanted w JOIN source_order_versions v
   ON v.fact->'refundsPayment' @> jsonb_build_array(jsonb_build_object('orderId',w.order_id))
  WHERE v.source='DCZD' AND v.kind='REFUND_SUCCEEDED' AND v.fact->>'domain'='PRACTICE'
 ), matched AS (
  SELECT v.id,v.fact,v.fact->>'refundId' AS refund_id,v.occurred_at,v.record_key
  FROM candidates c JOIN source_order_versions v USING(id) WHERE ` + latestPracticeRefund + `
  ORDER BY v.occurred_at,v.fact->>'refundId',v.record_key,v.id LIMIT $2
 ), duplicate AS (
  SELECT w.refund_id FROM (SELECT DISTINCT refund_id FROM matched WHERE refund_id<>'') w
  JOIN source_order_versions v ON v.source='DCZD' AND v.kind='REFUND_SUCCEEDED' AND v.fact->>'domain'='PRACTICE'
   AND v.fact->>'refundId'=w.refund_id
  WHERE ` + latestPracticeRefund + ` GROUP BY w.refund_id HAVING count(*)>1
 )
 SELECT m.id,m.fact,d.refund_id IS NOT NULL,NOT EXISTS(
  SELECT 1 FROM source_mappings s JOIN periods p ON p.product_id=s.product_id
  WHERE p.id=$3 AND s.source='DCZD' AND s.domain='PRACTICE' AND s.source_key=m.fact->>'sourceKey'
 ) FROM matched m LEFT JOIN duplicate d USING(refund_id)
 ORDER BY m.occurred_at,m.refund_id,m.record_key,m.id`

type practiceRefundMatch struct {
	event paymentInput
	issue string
}

type practiceRefund struct {
	paymentInput
	duplicate    bool
	wrongProduct bool
}

func readPracticeRefunds(ctx context.Context, tx pgx.Tx, periodID int64, orders map[string][]paymentInput) ([]practiceRefund, error) {
	if len(orders) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(orders))
	for key := range orders {
		keys = append(keys, key)
	}
	rows, err := tx.Query(ctx, practiceRefundsSQL, keys, maxDraftPayments+1, periodID)
	if err != nil {
		return nil, err
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceRefund, error) {
		var refund practiceRefund
		err := row.Scan(&refund.ID, &refund.Fact, &refund.duplicate, &refund.wrongProduct)
		return refund, err
	})
	if len(events) > maxDraftPayments {
		return nil, errDraftLimit
	}
	return events, err
}

func matchPracticeRefunds(events []practiceRefund, orders map[string][]paymentInput) map[int64][]practiceRefundMatch {
	matches := make(map[int64][]practiceRefundMatch)
	for _, event := range events {
		f := event.Fact
		match := practiceRefundMatch{event: event.paymentInput}
		var targets []paymentInput
		if len(f.RefundsPayment) == 1 && f.RefundsPayment[0].OrderID != "" {
			ref := f.RefundsPayment[0]
			targets = orders[ref.OrderID]
			if f.OrderID != ref.OrderID {
				targets = append(append([]paymentInput(nil), targets...), orders[f.OrderID]...)
				match.issue = "REFUND_PAYMENT_AMBIGUOUS"
			} else if len(targets) != 1 || !refundRefMatches(refundReference{LineID: ref.LineID, RecordID: ref.RecordID}, targets[0].Fact) {
				match.issue = "REFUND_PAYMENT_AMBIGUOUS"
			}
		} else {
			match.issue = "REFUND_MISSING_PAYMENT_REFERENCE"
			targets = append(targets, orders[f.OrderID]...)
			for _, ref := range f.RefundsPayment {
				if ref.OrderID != f.OrderID {
					targets = append(targets, orders[ref.OrderID]...)
				}
			}
		}
		if f.RefundID == "" || f.AmountMinor == nil || *f.AmountMinor <= 0 {
			match.issue = "REFUND_MISSING_AMOUNT_OR_ID"
		} else if event.duplicate {
			match.issue = "REFUND_DUPLICATE_ID"
		} else if event.wrongProduct && match.issue == "" {
			match.issue = "PRACTICE_REFUND_PRODUCT_MISMATCH"
		}
		for _, payment := range targets {
			if payment.Fact.Domain == "PRACTICE" {
				matches[payment.ID] = append(matches[payment.ID], match)
			}
		}
	}
	return matches
}

func (p *practicePurchase) applyRefunds(matches map[int64][]practiceRefundMatch) {
	p.gross = p.base
	active := p.payments[:0]
	for _, payment := range p.payments {
		net := *payment.Fact.AmountMinor
		remaining := net
		for _, match := range matches[payment.ID] {
			f := match.event.Fact
			late := !p.replaying && f.OccurredAt.After(p.member.ConfirmedAt)
			if match.issue != "" {
				p.issue, p.issueFactID = match.issue, match.event.ID
				return
			}
			switch {
			case f.OccurredAt.Before(payment.Fact.OccurredAt):
				p.issue = "PRACTICE_REFUND_PAYMENT_TIME"
			case *f.AmountMinor > remaining:
				p.issue = "REFUND_AMOUNT_EXCEEDS_PAYMENT"
			case *f.AmountMinor == net && !p.replaying && !late:
				p.issue = "PRACTICE_REFUND_ROSTER_CHANGED"
			}
			if p.issue != "" {
				p.issueFactID = match.event.ID
				return
			}
			remaining -= *f.AmountMinor
			if late {
				p.lateRefunds = append(p.lateRefunds, match.event)
				continue
			}
			net -= *f.AmountMinor
			p.base -= *f.AmountMinor
			if p.refunds == nil {
				p.refunds = make(map[int64][]paymentInput)
				p.netPayments = make(map[int64]int64)
			}
			p.refunds[payment.ID] = append(p.refunds[payment.ID], match.event)
			p.netPayments[payment.ID] = net
		}
		if p.replaying && net == 0 {
			if p.originalPayments == nil {
				p.originalPayments = slices.Clone(p.payments)
			}
			p.gross -= *payment.Fact.AmountMinor
		} else {
			active = append(active, payment)
		}
	}
	p.payments = active
}

func (p practicePurchase) netAmount(payment paymentInput) int64 {
	if net, ok := p.netPayments[payment.ID]; ok {
		return net
	}
	return *payment.Fact.AmountMinor
}

func (part component) usesFixedPersonalPaymentShares() bool {
	return (part.Type == "sales" || part.Type == "referral") && part.Mode == "fixed" &&
		(part.RefundPolicy == "" || part.RefundPolicy == "proportional") && len(part.retrainSplits()) == 0
}

func (p practicePurchase) fixedPaymentShares(sale practicePurchase, part component,
	identities map[identityKey][]identityAssignment, runtime practiceDraftRuntime) ([2]int64, string) {
	original := sale.payments
	if len(p.originalPayments) != 0 {
		p.payments = p.originalPayments
		scoped, issue := p.scopedSale(part, identities, runtime)
		if issue != "" {
			return [2]int64{}, issue
		}
		original = scoped.payments
	}
	amounts := practiceFixedSignupShares(original, part.Value)
	var active [2]int64
	for i, payment := range original {
		for index, current := range sale.payments {
			if current.ID == payment.ID {
				active[index] = p.remainingFixedShare(payment, amounts[i])
				break
			}
		}
	}
	return active, ""
}

func practiceFixedSignupShares(payments []paymentInput, award int64) [2]int64 {
	var gross int64
	for _, payment := range payments {
		gross += *payment.Fact.AmountMinor
	}
	return practiceGrossShares(gross, payments, award)
}

func (p practicePurchase) remainingFixedShare(payment paymentInput, award int64) int64 {
	paid := *payment.Fact.AmountMinor
	refunded := paid - p.netAmount(payment)
	if refunded == 0 {
		return award
	}
	return award - prorateMinor(award, refunded, paid)
}

func (p practicePurchase) commissionAmount(part component) (int64, string) {
	amount := calculatePaymentAmount(p.base, part)
	if part.Mode == "rate" || p.gross == p.base {
		return amount, ""
	}
	refunded := p.gross - p.base
	if part.RefundPolicy != "" {
		grossAward := calculatePaymentAmount(p.gross, part)
		delta, err := refundDelta(part, p.gross, grossAward, refunded, 0)
		if err != nil {
			return 0, "REFUND_POLICY_MISSING"
		}
		return grossAward + delta, ""
	}
	if part.Mode == "fixed" && part.Type == "sales" && classMember(p.member.MembershipType) == "new" {
		return 0, "REFUND_POLICY_MISSING"
	}
	return amount, ""
}
