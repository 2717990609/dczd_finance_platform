package main

import (
	"cmp"
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Historical percentage groups saved one award for the whole group. Allocate
// that exact posted amount over the group's original net payments in source
// version order. Successive cumulative shares preserve every booked cent.
const catalogAmountRefundBasisSQL = `WITH requested AS MATERIALIZED (
 SELECT DISTINCT membership.period_id,membership.group_key
 FROM unnest($1::bigint[],$2::bigint[]) req(period_id,source_version_id)
 JOIN catalog_aggregate_event_groups membership USING(period_id,source_version_id)
 JOIN catalog_aggregate_draft_groups g USING(period_id,group_key)
 JOIN periods p ON p.id=g.period_id JOIN products product ON product.id=p.product_id
 JOIN product_rule_versions v ON v.source_id=g.rule_id
 WHERE product.code IN ('DCAI_VIP','DCAI_HEALTH_AMBASSADOR')
  AND jsonb_array_length(g.targets)=1 AND NOT g.fixed_only
  AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(v.definition->'benefit_actions') action
   WHERE action->>'item_key'=g.targets->0->>'Item' AND (action->>'per_order')::boolean IS TRUE)
), origins AS (
 SELECT m.period_id,m.group_key,e.payment_version_id,
  coalesce(sum(coalesce(e.recognized_amount_minor,(f.fact->>'amountMinor')::bigint))
   FILTER (WHERE e.source_version_id=e.payment_version_id),0)::bigint AS gross,
  coalesce(sum(coalesce(e.recognized_amount_minor,(f.fact->>'amountMinor')::bigint))
   FILTER (WHERE e.source_version_id<>e.payment_version_id),0)::bigint AS returned
 FROM requested g JOIN catalog_aggregate_event_groups m USING(period_id,group_key)
 JOIN catalog_aggregate_draft_events e USING(period_id,source_version_id)
 JOIN source_order_versions f ON f.id=e.source_version_id
 GROUP BY m.period_id,m.group_key,e.payment_version_id
 HAVING bool_or(e.source_version_id=e.payment_version_id)
), shares AS (
 SELECT o.*,
  sum(gross-returned) OVER (PARTITION BY period_id,group_key)::bigint AS net,
  sum(gross-returned) OVER (PARTITION BY period_id,group_key ORDER BY payment_version_id)::bigint AS running
 FROM origins o
)
SELECT s.period_id,s.payment_version_id,s.group_key,s.gross,s.returned,s.net,s.running,
 coalesce(l.id,0),coalesce(l.amount_minor,0)
FROM shares s JOIN unnest($1::bigint[],$2::bigint[]) req(period_id,source_version_id)
 ON (s.period_id,s.payment_version_id)=(req.period_id,req.source_version_id)
LEFT JOIN calculation_draft_lines l ON (l.period_id,l.aggregate_group_key)=(s.period_id,s.group_key)`

func readCatalogAmountRefunds(ctx context.Context, tx pgx.Tx, events []catalogActivityEvent) error {
	type origin struct{ period, payment int64 }
	index := make(map[origin][]int)
	var periods, payments []int64
	for i, event := range events {
		if !event.Refund || event.frozenPeriod == 0 {
			continue
		}
		key := origin{event.frozenPeriod, event.PaymentID}
		if len(index[key]) == 0 {
			periods, payments = append(periods, key.period), append(payments, key.payment)
		}
		index[key] = append(index[key], i)
	}
	if len(index) == 0 {
		return nil
	}
	for _, selected := range index {
		slices.SortFunc(selected, func(a, b int) int {
			return cmp.Or(events[a].OccurredAt.Compare(events[b].OccurredAt), cmp.Compare(events[a].EventID, events[b].EventID))
		})
	}
	rows, err := tx.Query(ctx, catalogAmountRefundBasisSQL, periods, payments)
	if err != nil {
		return err
	}
	var key origin
	var group string
	var gross, returned, net, running, lineID, posted int64
	_, err = pgx.ForEachRow(rows, []any{&key.period, &key.payment, &group, &gross, &returned, &net, &running, &lineID, &posted}, func() error {
		base := gross - returned
		originalAward := int64(0)
		if net > 0 && base > 0 {
			originalAward = prorateMinor(posted, running, net) - prorateMinor(posted, running-base, net)
		}
		refunded := events[index[key][0]].PriorBookedRefunds - returned
		for _, i := range index[key] {
			event := &events[i]
			delta := int64(0)
			if originalAward > 0 && event.Amount > 0 {
				if refunded < 0 || refunded > base || event.Amount > base-refunded {
					return errRefundAmount
				}
				delta = prorateMinor(originalAward, refunded, base) -
					prorateMinor(originalAward, refunded+event.Amount, base)
			}
			refunded += event.Amount
			if event.amountRefunds == nil {
				event.amountRefunds = make(map[string]refundLine)
			}
			event.amountRefunds[group] = refundLine{OriginalLineID: lineID, RefundSourceVersionID: event.EventID,
				Amount: delta, Principal: event.Amount, OccurredAt: event.OccurredAt}
		}
		return nil
	})
	return err
}
