package main

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type refundInput struct {
	ID, RefundPeriodID int64
	Fact               sourceFact
	BookedChanged      bool
	Duplicate          bool
}

type refundLine struct {
	OriginalLineID, RefundPeriodID, RefundSourceVersionID int64
	Amount, Principal                                     int64
	CurrentPaymentVersion                                 int64
	ComponentType                                         string
	RefundID                                              string
	OccurredAt                                            time.Time
	TierCredit                                            bool `json:",omitempty"`
}

type refundObligation struct {
	ID, PaymentVersion, Base, Accrued int64
	ComponentType                     string
	Policy                            component
	SplitRole                         string
}

type refundLineKey struct {
	originalLineID, currentPaymentVersion int64
	componentType                         string
}

func (o refundObligation) key() refundLineKey {
	if o.ID != 0 {
		return refundLineKey{originalLineID: o.ID}
	}
	return refundLineKey{currentPaymentVersion: o.PaymentVersion, componentType: o.ComponentType}
}

type refundHistory struct {
	deducted  int64
	principal int64
	byID      map[string]int64
}

// A second current record may fall in a different month or point to another order.
// Versions of the same source record are not distinct refunds.
const duplicateRefundSQL = `v.kind='REFUND_SUCCEEDED' AND EXISTS(
 SELECT 1 FROM source_order_versions other
 WHERE other.source=v.source AND other.kind='REFUND_SUCCEEDED' AND other.fact->>'domain'=v.fact->>'domain'
  AND other.fact->>'refundId'=v.fact->>'refundId' AND other.occurred_at<=CURRENT_TIMESTAMP
  AND (other.order_id,other.line_id,other.record_key)<>(v.order_id,v.line_id,v.record_key)
  AND NOT EXISTS (
   SELECT 1 FROM source_order_versions newer
   WHERE (newer.source,newer.order_id,newer.line_id,newer.record_key,newer.kind)
      =(other.source,other.order_id,other.line_id,other.record_key,other.kind)
     AND (newer.updated_at,newer.id)>(other.updated_at,other.id)
  )
)`

const refundInputSelectionSQL = `SELECT v.id,origin.id,v.fact,coalesce(history.changed,false),` +
	duplicateRefundSQL + ` FROM periods target`

const refundInputsSQL = refundInputSelectionSQL + `
 JOIN source_mappings m ON m.product_id=target.product_id
 JOIN source_order_versions v ON v.source=m.source AND v.fact->>'domain'=m.domain AND v.fact->>'sourceKey'=m.source_key
` + refundPeriodSQL + ` AND NOT ` + catalogSourceReservedSQL + refundEligibilitySQL

const catalogRefundInputsSQL = refundInputSelectionSQL + `
 JOIN product_catalog_settings c ON c.product_id=target.product_id
 JOIN source_order_versions v ON v.source=c.source_system AND EXISTS(
  SELECT 1 FROM catalog_business_routes m
  WHERE m.product_id=c.product_id AND m.observed_at=c.captured_at AND ` + catalogRouteFactSQL + `
 )
` + refundPeriodSQL + refundEligibilitySQL

const refundPeriodSQL = `
 JOIN periods origin ON origin.product_id=target.product_id AND origin.aggregate_package_id IS NULL
  AND v.occurred_at >= (origin.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai')
  AND v.occurred_at < ((origin.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')
` + refundPostingHistorySQL + `
 WHERE target.id=$1 AND v.kind='REFUND_SUCCEEDED' AND v.occurred_at<=CURRENT_TIMESTAMP`

const refundPostingHistorySQL = ` LEFT JOIN LATERAL (
  SELECT min(a.period_id) FILTER (WHERE s.id IS NULL) AS reserved_period,
    count(*) FILTER (WHERE s.id IS NOT NULL)>0 AS booked,
    bool_or(rv.occurred_at IS DISTINCT FROM v.occurred_at
      OR rv.fact->'amountMinor' IS DISTINCT FROM v.fact->'amountMinor'
      OR rv.fact->'refundsPayment' IS DISTINCT FROM v.fact->'refundsPayment'
      OR v.fact->>'domain'='HEALTH_IDENTITY' AND rv.fact->'sourceKey' IS DISTINCT FROM v.fact->'sourceKey'
      OR rv.order_id IS DISTINCT FROM v.order_id OR rv.line_id IS DISTINCT FROM v.line_id)
      FILTER (WHERE s.id IS NOT NULL) AS changed
  FROM source_order_versions rv
  JOIN LATERAL (
   SELECT period_id FROM calculation_draft_refunds WHERE refund_source_version_id=rv.id
   UNION
   SELECT period_id FROM calculation_draft_performance WHERE source_version_id=rv.id AND payment_version_id IS NOT NULL
   UNION
   SELECT period_id FROM calculation_draft_performance WHERE prior_refund_version_id=rv.id
  ) a ON true
  LEFT JOIN settlements s ON s.period_id=a.period_id
  WHERE rv.kind='REFUND_SUCCEEDED' AND rv.source=v.source
    AND (rv.fact->>'domain')=(v.fact->>'domain') AND (rv.fact->>'refundId')=(v.fact->>'refundId')
 ) history ON true`

const refundEligibilitySQL = `
 AND (NOT history.booked OR history.changed)
 AND (origin.id=target.id OR (
  EXISTS(SELECT 1 FROM settlements WHERE period_id=origin.id)
  AND (history.reserved_period=target.id OR (
   history.reserved_period IS NULL AND (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date BETWEEN target.starts_on AND target.ends_on
  ))
 ))
 AND NOT EXISTS (
   SELECT 1 FROM source_order_versions newer
   WHERE (newer.source,newer.order_id,newer.line_id,newer.record_key,newer.kind)
     =(v.source,v.order_id,v.line_id,v.record_key,v.kind)
   AND (newer.updated_at,newer.id)>(v.updated_at,v.id)
 )
 ORDER BY v.occurred_at,v.source,v.fact->>'domain',v.fact->>'refundId',v.order_id,v.line_id,v.record_key LIMIT $2`

type refundReference struct {
	ID       int64  `json:"id"`
	Source   string `json:"source"`
	OrderID  string `json:"order_id"`
	LineID   string `json:"line_id"`
	RecordID string `json:"record_id"`
}

func loadRefundObligations(ctx context.Context, tx pgx.Tx, periodID int64, requests []refundReference, practiceMonth bool) (map[int64][]refundObligation, error) {
	rows, err := tx.Query(ctx, `SELECT q.id,l.id,p.id,l.base_minor,l.amount_minor,l.component_type,r.components,
  l.catalog_rule_id IS NOT NULL,coalesce(l.applied_rate_bps,-1),coalesce(cv.definition->>'refund_policy',''),
  coalesce(cv.definition->'internal_sales_direct_split'->>'sales_item_key',''),
  coalesce(cardinality(enrollment.paid_order_ids),0),coalesce(enrollment.membership_type,0),coalesce(company.company_id,''),
  coalesce(d.practice_replay_inputs->'companyClassifications'->>(p.fact->>'paymentId'),'')
 FROM jsonb_to_recordset($1::jsonb) AS q(id bigint,source text,order_id text,line_id text,record_id text)
 JOIN source_order_versions p ON p.source=q.source AND p.order_id=q.order_id AND p.kind='ORDER_PAID'
  AND (q.line_id='' OR p.line_id=q.line_id)
  AND (q.record_id='' OR substr(p.record_key,strpos(p.record_key,':')+1)=q.record_id AND strpos(p.record_key,':')>0)
 JOIN calculation_draft_lines l ON l.source_version_id=p.id AND l.aggregate_group_key IS NULL
 JOIN settlements s ON s.period_id=l.period_id
 JOIN calculation_drafts d ON d.period_id=l.period_id
 JOIN periods original ON original.id=l.period_id
 LEFT JOIN rule_versions r ON r.id=l.rule_id
 LEFT JOIN product_rule_versions cv ON cv.source_id=l.catalog_rule_id
 LEFT JOIN practice_enrollments enrollment ON enrollment.id=l.practice_enrollment_id
 LEFT JOIN source_payment_companies company ON company.source_version_id=p.id AND enrollment.id IS NOT NULL
 WHERE original.product_id=(SELECT product_id FROM periods WHERE id=$2)
  AND (NOT $3 OR enrollment.id IS NOT NULL AND NOT `+practiceRefundReplaySQL+`)
 ORDER BY q.id,l.id`, requests, periodID, practiceMonth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64][]refundObligation)
	for rows.Next() {
		var id int64
		var line refundObligation
		var components []component
		var catalog bool
		var rate int64
		var terms vipRefundTerms
		var legs, membership int
		var company, category string
		if err = rows.Scan(&id, &line.ID, &line.PaymentVersion, &line.Base, &line.Accrued, &line.ComponentType,
			&components, &catalog, &rate, &terms.Mode, &terms.SplitItem, &legs, &membership, &company, &category); err != nil {
			return nil, err
		}
		line.Policy = refundComponent(components, line.ComponentType)
		if catalog {
			terms.apply(&line, rate)
		}
		if practiceMonth {
			line.Policy = practiceMonthlyRefundPolicy(components, line, legs, membership, company, category)
		}
		result[id] = append(result[id], line)
	}
	return result, rows.Err()
}

func refundComponent(components []component, key string) component {
	for _, c := range components {
		if c.Type == key {
			return c
		}
	}
	return component{}
}

func loadRefundHistory(ctx context.Context, tx pgx.Tx, obligations map[int64][]refundObligation) (map[refundLineKey]*refundHistory, error) {
	values := make(map[refundLineKey]*refundHistory)
	ids := make([]int64, 0)
	for _, lines := range obligations {
		for _, line := range lines {
			key := line.key()
			if values[key] == nil {
				values[key] = &refundHistory{byID: make(map[string]int64)}
				if line.ID != 0 {
					ids = append(ids, line.ID)
				}
			}
		}
	}
	rows, err := tx.Query(ctx, `SELECT a.original_line_id,v.fact->>'refundId',(v.fact->>'amountMinor')::bigint,a.amount_minor
 FROM calculation_draft_refunds a JOIN settlements s USING(period_id)
 JOIN source_order_versions v ON v.id=a.refund_source_version_id
 WHERE a.original_line_id=ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, principal, amount int64
		var refundID string
		if err = rows.Scan(&id, &refundID, &principal, &amount); err != nil {
			return nil, err
		}
		h := values[refundLineKey{originalLineID: id}]
		if _, exists := h.byID[refundID]; exists || principal <= 0 || principal > math.MaxInt64-h.principal ||
			amount > 0 || amount < h.deducted-math.MaxInt64 {
			return nil, errRefundAmount
		}
		h.deducted -= amount
		h.principal += principal
		h.byID[refundID] = principal
	}
	return values, rows.Err()
}

func includeCurrentObligations(window periodWindow, refs []refundReference, obligations map[int64][]refundObligation,
	rules []paymentRule, facts []paymentInput, lines []draftLine, catalogTerms map[pgtype.UUID]vipRefundTerms) {
	type orderKey struct{ source, id string }
	byVersion := make(map[int64][]draftLine)
	for _, line := range lines {
		byVersion[line.FactID] = append(byVersion[line.FactID], line)
	}
	payments := make(map[orderKey][]paymentInput)
	for _, item := range facts {
		if isPeriodPayment(window, item.Fact) || catalogTerms != nil && len(byVersion[item.ID]) != 0 {
			key := orderKey{item.Fact.Source, item.Fact.OrderID}
			payments[key] = append(payments[key], item)
		}
	}
	byRule := make(map[int64][]component)
	for _, rule := range rules {
		byRule[rule.ID] = rule.Components
	}
	for _, ref := range refs {
		for _, payment := range payments[orderKey{ref.Source, ref.OrderID}] {
			if !refundRefMatches(ref, payment.Fact) {
				continue
			}
			for _, line := range byVersion[payment.ID] {
				obligation := refundObligation{
					PaymentVersion: payment.ID, Base: line.Base, Accrued: line.Amount,
					ComponentType: line.Component, Policy: refundComponent(byRule[line.RuleID], line.Component),
				}
				if line.Catalog != nil {
					catalogTerms[line.Catalog.Rule].apply(&obligation, line.Catalog.Rate)
				}
				obligations[ref.ID] = append(obligations[ref.ID], obligation)
			}
		}
	}
}

func addRefunds(ctx context.Context, tx pgx.Tx, periodID int64, window periodWindow, rules []paymentRule,
	payments []paymentInput, draft *paymentDraft, catalogTerms map[pgtype.UUID]vipRefundTerms) error {
	query := refundInputsSQL
	if catalogTerms != nil {
		query = catalogRefundInputsSQL
	}
	facts, err := readRefundEvents(ctx, tx, periodID, query)
	if err != nil {
		return err
	}
	return applyRefundEvents(ctx, tx, periodID, window, rules, payments, draft, catalogTerms, facts, false)
}

func readRefundEvents(ctx context.Context, tx pgx.Tx, periodID int64, query string) ([]refundInput, error) {
	rows, err := tx.Query(ctx, query, periodID, maxDraftPayments+1)
	if err != nil {
		return nil, err
	}
	facts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (refundInput, error) {
		var item refundInput
		err := row.Scan(&item.ID, &item.RefundPeriodID, &item.Fact, &item.BookedChanged, &item.Duplicate)
		return item, err
	})
	if err != nil {
		return nil, err
	}
	if len(facts) > maxDraftPayments {
		return nil, errDraftLimit
	}
	return facts, nil
}

func applyRefundEvents(ctx context.Context, tx pgx.Tx, periodID int64, window periodWindow, rules []paymentRule,
	payments []paymentInput, draft *paymentDraft, catalogTerms map[pgtype.UUID]vipRefundTerms, facts []refundInput, practiceMonth bool) error {
	if len(facts) == 0 {
		return nil
	}
	refs := make([]refundReference, 0, len(facts))
	var healthRefs []refundReference
	type refundKey struct{ source, domain, refund string }
	counts := make(map[refundKey]int)
	for _, event := range facts {
		f := event.Fact
		counts[refundKey{f.Source, f.Domain, f.RefundID}]++
		if !event.BookedChanged && event.RefundPeriodID != 0 && len(f.RefundsPayment) == 1 &&
			f.RefundsPayment[0].OrderID != "" && (!practiceMonth || f.OrderID == f.RefundsPayment[0].OrderID) {
			link := f.RefundsPayment[0]
			ref := refundReference{event.ID, f.Source, link.OrderID, link.LineID, link.RecordID}
			refs = append(refs, ref)
			if catalogTerms != nil && f.Source == "DCAI" && f.Domain == "HEALTH_IDENTITY" {
				healthRefs = append(healthRefs, ref)
			}
		}
	}
	performanceOrigins, err := loadPerformanceRefundOrigins(ctx, tx, periodID, refs, payments, draft.Performance)
	if err != nil {
		return err
	}
	pending, err := healthRefundsAwaitingRecognition(ctx, tx, periodID, healthRefs)
	if err != nil {
		return err
	}
	performanceCredited := make(map[int64]int64)
	obligations, err := loadRefundObligations(ctx, tx, periodID, refs, practiceMonth)
	if err != nil {
		return err
	}
	includeCurrentObligations(window, refs, obligations, rules, payments, draft.Lines, catalogTerms)
	history, err := loadRefundHistory(ctx, tx, obligations)
	if err != nil {
		return err
	}
	netted := make(map[int64]bool)
	for _, performance := range draft.Performance {
		if performance.PriorRefundVersionID != 0 {
			netted[performance.PriorRefundVersionID] = true
		}
	}
	var replayEvents []practiceReplayRefund
	for _, event := range facts {
		f := event.Fact
		code := ""
		switch {
		case event.RefundPeriodID == 0:
			code = "PRACTICE_REFUND_MONTH_MISSING"
		case event.BookedChanged:
			code = "REFUND_BOOKED_CHANGED"
		case f.RefundID == "" || f.AmountMinor == nil || *f.AmountMinor <= 0:
			code = "REFUND_MISSING_AMOUNT_OR_ID"
		case event.Duplicate || counts[refundKey{f.Source, f.Domain, f.RefundID}] > 1:
			code = "REFUND_DUPLICATE_ID"
		case len(f.RefundsPayment) != 1 || f.RefundsPayment[0].OrderID == "":
			code = "REFUND_MISSING_PAYMENT_REFERENCE"
		case practiceMonth && f.OrderID != f.RefundsPayment[0].OrderID:
			code = "REFUND_PAYMENT_AMBIGUOUS"
		case netted[event.ID]:
			// This refund already reduced the recognized principal and every benefit base.
			continue
		default:
			origins := performanceOrigins[event.ID]
			if len(origins) == 0 && pending[event.ID] {
				continue
			}
			performanceIssue := recordPerformanceRefund(draft, event, origins, performanceCredited)
			originals := obligations[event.ID]
			if catalogTerms != nil && performanceIssue == "" && len(originals) == 0 {
				// A recognized payment can legitimately have no commission to recover.
				continue
			}
			if practiceMonth && performanceIssue == "" && origins[0].PracticeReplay {
				replayEvents = append(replayEvents, practiceReplayRefund{event, origins[0].PeriodID, origins[0].SourceVersionID})
			} else if (catalogTerms == nil && !practiceMonth) || performanceIssue == "" {
				code, err = applyHistoricalRefund(draft, event, originals, history)
				if err != nil {
					return err
				}
			}
			if code == "" {
				code = performanceIssue
			}
		}
		if code != "" {
			draft.Issues = append(draft.Issues, draftIssue{FactID: event.ID, Code: code})
		}
	}
	return applyPracticeReplayRefunds(ctx, tx, draft, replayEvents)
}

func applyHistoricalRefund(draft *paymentDraft, event refundInput, originals []refundObligation, history map[refundLineKey]*refundHistory) (string, error) {
	if len(originals) == 0 {
		return "REFUND_ORIGINAL_NOT_SETTLED", nil
	}
	// Validate every component before publishing any debit for this refund.
	f := event.Fact
	principal := *f.AmountMinor
	active := make([]refundObligation, 0, len(originals))
	for _, original := range originals {
		if originals[0].PaymentVersion != original.PaymentVersion {
			return "REFUND_PAYMENT_AMBIGUOUS", nil
		}
		h := history[original.key()]
		if booked, exists := h.byID[f.RefundID]; exists {
			if booked != principal {
				return "REFUND_BOOKED_CHANGED", nil
			}
			continue
		}
		if h.principal > original.Base || principal > original.Base-h.principal {
			return "REFUND_AMOUNT_EXCEEDS_PAYMENT", nil
		}
		active = append(active, original)
	}
	var order string
	if len(f.RefundsPayment) == 1 {
		order = f.Source + ":" + f.RefundsPayment[0].OrderID
	}
	group, err := vipRefundGroupDeltas(order, principal, active, history)
	if err != nil {
		return refundProblem(err)
	}
	var pending []refundLine
	for _, original := range active {
		h := history[original.key()]
		delta, split := group[original.key()]
		if !split {
			delta, err = refundDelta(original.Policy, original.Base, original.Accrued, h.principal+principal, h.deducted)
			if err != nil {
				return refundProblem(err)
			}
		}
		line := refundLine{
			OriginalLineID: original.ID, RefundPeriodID: event.RefundPeriodID, RefundSourceVersionID: event.ID,
			Amount: delta, Principal: principal, RefundID: f.RefundID, OccurredAt: f.OccurredAt.UTC(),
		}
		if original.ID == 0 {
			line.CurrentPaymentVersion = original.PaymentVersion
			line.ComponentType = original.ComponentType
		}
		pending = append(pending, line)
	}
	for _, line := range pending {
		if line.Amount < math.MinInt64-draft.RefundTotal {
			return "", errCalculationOverflow
		}
		h := history[refundLineKey{line.OriginalLineID, line.CurrentPaymentVersion, line.ComponentType}]
		h.principal += line.Principal
		h.deducted -= line.Amount
		h.byID[line.RefundID] = line.Principal
		draft.Refunds = append(draft.Refunds, line)
		draft.RefundTotal += line.Amount
	}
	return "", nil
}

func refundProblem(err error) (string, error) {
	switch {
	case errors.Is(err, errRefundPolicy):
		return "REFUND_POLICY_MISSING", nil
	case errors.Is(err, errRefundAmount):
		return "REFUND_AMOUNT_EXCEEDS_PAYMENT", nil
	case errors.Is(err, errPracticeRefundReplay):
		return "PRACTICE_REFUND_REPLAY_REQUIRED", nil
	default:
		return "", err
	}
}
