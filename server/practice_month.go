package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

var errPracticeRefundReplay = errors.New("退款影响原班次的联动分润，需要按已确认班次复算")

const practiceRefundMonthScope = `product.period_mode='class' AND period.calendar_month IS NOT NULL`

// Booked refund facts include pre-confirmation amounts already taken into the
// class settlement. Late revisions and new facts still go through the month.
const practiceRefundMonthEventsSQL = `SELECT v.id,coalesce(origin.id,0),v.fact,coalesce(history.changed,false),
 EXISTS(SELECT 1 FROM source_order_versions v2
  WHERE v2.source='DCZD' AND v2.kind='REFUND_SUCCEEDED' AND v2.fact->>'domain'='PRACTICE'
   AND v2.fact->>'refundId'=v.fact->>'refundId' AND v2.record_key<>v.record_key
   AND NOT EXISTS(SELECT 1 FROM source_order_versions newer
    WHERE newer.source='DCZD' AND newer.kind='REFUND_SUCCEEDED' AND newer.record_key=v2.record_key
     AND (newer.updated_at,newer.id)>(v2.updated_at,v2.id)))` + practiceRefundMonthFromSQL + `
 WHERE target.id=$1 AND target.calendar_month IS NOT NULL` + practiceRefundMonthRoutingSQL + `
  AND EXISTS(
   SELECT 1 FROM (` + practiceRefundOrderKeysSQL + `) keys
   JOIN source_order_versions paid ON paid.order_id=keys.id AND paid.source='DCZD' AND paid.kind='ORDER_PAID'
   JOIN calculation_draft_performance a ON a.source_version_id=paid.id AND a.payment_version_id IS NULL
   JOIN settlements final ON final.period_id=a.period_id
   JOIN periods original ON original.id=a.period_id AND original.product_id=target.product_id
   JOIN calculation_drafts d ON d.period_id=original.id
   WHERE paid.fact->>'domain'='PRACTICE' AND d.practice_roster_id IS NOT NULL
  )
 ORDER BY v.occurred_at,v.fact->>'refundId',v.record_key,v.id LIMIT $2`

const practiceRefundOrderKeysSQL = `SELECT v.order_id AS id
 UNION SELECT ref->>'orderId' FROM jsonb_array_elements(v.fact->'refundsPayment') ref`

const practiceRefundMonthFromSQL = ` FROM periods target
 JOIN source_mappings m ON m.product_id=target.product_id AND m.source='DCZD' AND m.domain='PRACTICE'
 JOIN source_order_versions v ON v.source=m.source AND v.fact->>'domain'=m.domain AND v.fact->>'sourceKey'=m.source_key
  AND v.kind='REFUND_SUCCEEDED' AND v.occurred_at<=CURRENT_TIMESTAMP
  AND v.occurred_at<(target.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'
 LEFT JOIN periods origin ON origin.product_id=target.product_id AND origin.calendar_month IS NOT NULL
  AND v.occurred_at>=origin.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'
  AND v.occurred_at<(origin.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'
` + refundPostingHistorySQL

const practiceRefundMonthRoutingSQL = ` AND (NOT history.booked OR history.changed)
  AND ` + latestPracticeRefund + `
  AND (origin.id=target.id OR (
   (origin.id IS NULL AND v.occurred_at<target.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'
    OR EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=origin.id))
   AND (history.reserved_period=target.id OR (history.reserved_period IS NULL
    AND (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN target.starts_on AND target.ends_on))))`

func computePracticeRefundMonth(ctx context.Context, tx pgx.Tx, id int64, window periodWindow) (paymentDraft, []byte, error) {
	var draft paymentDraft
	facts, err := readRefundEvents(ctx, tx, id, practiceRefundMonthEventsSQL)
	if err != nil {
		return draft, nil, err
	}
	if err := applyRefundEvents(ctx, tx, id, window, nil, nil, &draft, nil, facts, true); err != nil {
		return paymentDraft{}, nil, err
	}
	if err := addPracticeCorrections(ctx, tx, id, window, &draft, facts); err != nil {
		return paymentDraft{}, nil, err
	}
	inputs := make([]paymentInput, 0, len(facts))
	for _, f := range facts {
		inputs = append(inputs, paymentInput{ID: f.ID, Fact: f.Fact})
	}
	digest, err := digestDraft(0, draft, inputs)
	return draft, digest, err
}

func practiceMonthlyRefundPolicy(parts []component, original refundObligation, legs, membership int, company, category string) component {
	replay := component{Mode: "practice_replay"}
	if legs != 1 {
		return replay
	}
	member := classMember(membership)
	for _, part := range parts {
		inMemberScope := part.matchesPracticeMember(member)
		// A refund can change the original class base or the tier chosen by other orders.
		if part.Mode == "tiered" && inMemberScope {
			return replay
		}
		if b := part.PeriodBenefit; b != nil && (b.TierOnWholeClass ||
			inMemberScope && (company == "" || len(b.SalesCompanies) == 0 || slices.Contains(b.SalesCompanies, company))) {
			return replay
		}
	}
	if original.ComponentType == "sales" || original.ComponentType == "referral" {
		for _, part := range parts {
			if part.Type != original.ComponentType || !part.matchesPracticeMember(member) {
				continue
			}
			if len(part.salesCompanies()) != 0 && !slices.Contains(part.salesCompanies(), company) {
				return replay
			}
			if len(part.retrainSplits()) != 0 {
				return replay
			}
			switch part.Mode {
			case "rate":
				if calculatePaymentAmount(original.Base, part) == original.Accrued {
					part.RefundPolicy = "proportional"
					return part
				}
			case "fixed":
				if part.usesFixedPersonalPaymentShares() {
					return part
				}
				if part.Value == original.Accrued {
					if part.RefundPolicy != "" {
						return part
					}
					return component{Mode: "practice_fixed"}
				}
			}
			return replay
		}
	}
	key := strings.Split(original.ComponentType, ":")
	if len(key) != 3 || key[0] != "period" || (key[1] != "company_sales" && key[1] != "company_referral") {
		return replay
	}
	index, err := strconv.Atoi(key[2])
	if err != nil || index < 0 || index >= len(parts) || parts[index].CompanySales == nil {
		return replay
	}
	offers := practiceCompanyStandards(paymentRule{Components: parts})
	c, matched := offers.match(member, company, category)
	if !matched || c.internal || c.componentIndex != index || c.key == "" {
		return replay
	}
	rate := c.Value
	if key[1] == "company_referral" {
		rate = c.recommendationRate
	} else if c.Mode == "fixed" {
		if c.proratesFixedRefunds(member) {
			return component{RefundPolicy: "proportional"}
		}
		if c.Value == original.Accrued {
			return component{Mode: "practice_company_fixed"}
		}
		return replay
	}
	if practiceCompanyRate(original.Base, rate) != original.Accrued {
		return replay
	}
	return component{RefundPolicy: "proportional"}
}
