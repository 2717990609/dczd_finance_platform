package main

import (
	"cmp"
	"context"
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ruleImpactReport struct {
	CurrentRuleAvailable bool             `json:"currentRuleAvailable"`
	MatchedPaymentCount  *int             `json:"matchedPaymentCount,omitempty"`
	BeforeMinor          int64            `json:"beforeMinor"`
	AfterMinor           int64            `json:"afterMinor"`
	DeltaMinor           int64            `json:"deltaMinor"`
	BeforeIssueCount     int              `json:"beforeIssueCount"`
	AfterIssueCount      int              `json:"afterIssueCount"`
	Items                []ruleImpactItem `json:"items"`
}

type ruleImpactItem struct {
	Key         int    `json:"key"`
	OrderID     string `json:"orderId"`
	OrderNo     string `json:"orderNo"`
	Recipient   string `json:"recipient"`
	EntryKind   string `json:"entryKind"`
	BeforeMinor int64  `json:"beforeMinor"`
	AfterMinor  int64  `json:"afterMinor"`
	DeltaMinor  int64  `json:"deltaMinor"`
}

type ruleImpactRecipient struct {
	person, practicePool int64
	unit, catalogRule    pgtype.UUID
	poolItem             string
}

type ruleImpactPosting struct {
	fact      int64
	recipient ruleImpactRecipient
	kind      string
	amount    int64
}

func impactRecipient(line draftLine) ruleImpactRecipient {
	party := ruleImpactRecipient{person: line.PersonID}
	if line.PracticePool != nil {
		party.practicePool = line.PracticePool.PoolID
	} else if line.Catalog != nil && line.Catalog.Unit.Valid {
		party.unit = line.Catalog.Unit
		if line.Catalog.AllocationOwnerID > 0 {
			party.catalogRule = line.Catalog.Rule
			party.poolItem = line.Component
		}
	} else if line.BeneficiaryUnit != nil {
		party.unit = *line.BeneficiaryUnit
	}
	return party
}

func readImpactOriginals(ctx context.Context, tx pgx.Tx, before, after paymentDraft) (map[int64]ruleImpactPosting, error) {
	ids := make(map[int64]bool)
	for _, draft := range []paymentDraft{before, after} {
		for _, refund := range draft.Refunds {
			if refund.OriginalLineID > 0 {
				ids[refund.OriginalLineID] = true
			}
		}
		for _, correction := range draft.Corrections {
			ids[correction.OriginalLineID] = true
		}
	}
	result := make(map[int64]ruleImpactPosting, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT id,source_version_id,coalesce(person_id,0),coalesce(practice_pool_id,0),
	 beneficiary_unit_id,catalog_rule_id,CASE WHEN allocation_owner_id IS NOT NULL AND catalog_rule_id IS NOT NULL THEN component_type ELSE '' END
	 FROM calculation_draft_lines WHERE id=ANY($1::bigint[])`, slices.Collect(maps.Keys(ids)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var item ruleImpactPosting
		party := &item.recipient
		if err := rows.Scan(&id, &item.fact, &party.person, &party.practicePool, &party.unit, &party.catalogRule, &party.poolItem); err != nil {
			return nil, err
		}
		result[id] = item
	}
	return result, rows.Err()
}

func impactPostings(draft paymentDraft, originals map[int64]ruleImpactPosting) ([]ruleImpactPosting, error) {
	type paymentKey struct {
		id        int64
		component string
	}
	current := make(map[paymentKey]ruleImpactPosting, len(draft.Lines))
	postings := make([]ruleImpactPosting, 0, len(draft.Lines)+len(draft.Refunds)+len(draft.Corrections))
	for _, line := range draft.Lines {
		posting := ruleImpactPosting{fact: line.FactID, recipient: impactRecipient(line), kind: "accrual", amount: line.Amount}
		current[paymentKey{line.FactID, line.Component}] = posting
		if !line.PracticeTierReference {
			postings = append(postings, posting)
		}
	}
	for _, refund := range draft.Refunds {
		posting := originals[refund.OriginalLineID]
		if refund.CurrentPaymentVersion > 0 {
			posting = current[paymentKey{refund.CurrentPaymentVersion, refund.ComponentType}]
		}
		if posting.fact == 0 {
			return nil, errRefundAmount
		}
		posting.kind, posting.amount = "refund", refund.Amount
		postings = append(postings, posting)
	}
	for _, correction := range draft.Corrections {
		posting := originals[correction.OriginalLineID]
		if posting.fact == 0 {
			return nil, conflict
		}
		if correction.PersonID != 0 || correction.Unit != posting.recipient.unit {
			posting.recipient = ruleImpactRecipient{person: correction.PersonID, unit: correction.Unit}
		}
		posting.kind, posting.amount = "correction", correction.Amount
		postings = append(postings, posting)
	}
	return postings, nil
}

func (r ruleImpactRecipient) identity() string {
	switch {
	case r.person != 0:
		return "person:" + strconv.FormatInt(r.person, 10)
	case r.practicePool != 0:
		return "practice:" + strconv.FormatInt(r.practicePool, 10)
	case r.poolItem != "":
		return "pool:" + r.unit.String() + ":" + r.poolItem
	default:
		return "unit:" + r.unit.String()
	}
}

func impactPoolLabelKey(rule pgtype.UUID, item string) string {
	return "catalog:" + rule.String() + ":" + item
}

type impactSourceOrder struct{ source, id, name string }

func impactLookups(ctx context.Context, tx pgx.Tx, all ...[]ruleImpactPosting) (map[int64]impactSourceOrder, map[string]string, error) {
	facts, people, pools := map[int64]bool{}, map[int64]bool{}, map[int64]bool{}
	units, rules := map[pgtype.UUID]bool{}, map[pgtype.UUID]bool{}
	for _, postings := range all {
		for _, posting := range postings {
			facts[posting.fact] = true
			party := posting.recipient
			if party.person != 0 {
				people[party.person] = true
			}
			if party.practicePool != 0 {
				pools[party.practicePool] = true
			}
			if party.unit.Valid {
				units[party.unit] = true
			}
			if party.poolItem != "" {
				rules[party.catalogRule] = true
			}
		}
	}
	sources := make(map[int64]impactSourceOrder, len(facts))
	names := make(map[string]string, len(people)+len(pools)+len(units))
	rows, err := tx.Query(ctx, `SELECT id,source,order_id,coalesce(fact->>'orderNo','')
	 FROM source_order_versions WHERE id=ANY($1::bigint[])`, slices.Collect(maps.Keys(facts)))
	if err != nil {
		return nil, nil, err
	}
	var fact int64
	var source impactSourceOrder
	if _, err := pgx.ForEachRow(rows, []any{&fact, &source.source, &source.id, &source.name}, func() error {
		sources[fact] = source
		return nil
	}); err != nil {
		return nil, nil, err
	}
	rows, err = tx.Query(ctx, `SELECT 'person:'||id,name FROM people WHERE id=ANY($1::bigint[])
	 UNION ALL SELECT 'practice:'||id,name FROM practice_pools WHERE id=ANY($2::bigint[])
	 UNION ALL SELECT 'unit:'||id,name FROM organization_units WHERE id=ANY($3::uuid[])
	 UNION ALL SELECT 'catalog:'||v.source_id||':'||(action->>'item_key'),action->>'pool_name'
	 FROM product_rule_versions v CROSS JOIN LATERAL jsonb_array_elements(v.definition->'benefit_actions') action
	 WHERE v.source_id=ANY($4::uuid[]) AND coalesce(action->>'pool_name','')<>''`,
		slices.Collect(maps.Keys(people)), slices.Collect(maps.Keys(pools)),
		slices.Collect(maps.Keys(units)), slices.Collect(maps.Keys(rules)))
	if err != nil {
		return nil, nil, err
	}
	var key, name string
	if _, err := pgx.ForEachRow(rows, []any{&key, &name}, func() error {
		names[key] = name
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return sources, names, nil
}

func (r ruleImpactRecipient) name(names map[string]string) string {
	if r.poolItem != "" {
		if label := names[impactPoolLabelKey(r.catalogRule, r.poolItem)]; label != "" {
			return label
		}
		return names["unit:"+r.unit.String()] + " · 团队池"
	}
	if name := names[r.identity()]; name != "" {
		return name
	}
	return "待核归属"
}

func impactDelta(before, after int64) (int64, error) {
	if before > 0 && after < math.MinInt64+before || before < 0 && after > math.MaxInt64+before {
		return 0, errCalculationOverflow
	}
	return after - before, nil
}

func impactTotal(draft paymentDraft) (int64, error) {
	paid, err := addMinor(draft.Total, draft.RefundTotal)
	if err != nil {
		return 0, err
	}
	return addMinor(paid, draft.CorrectionTotal)
}

func readRuleImpactReport(ctx context.Context, tx pgx.Tx, before, after paymentDraft) (ruleImpactReport, error) {
	report := ruleImpactReport{BeforeIssueCount: before.blockingIssues(), AfterIssueCount: after.blockingIssues(), Items: []ruleImpactItem{}}
	var err error
	if report.BeforeMinor, err = impactTotal(before); err != nil {
		return report, err
	}
	if report.AfterMinor, err = impactTotal(after); err != nil {
		return report, err
	}
	if report.DeltaMinor, err = impactDelta(report.BeforeMinor, report.AfterMinor); err != nil {
		return report, err
	}
	originals, err := readImpactOriginals(ctx, tx, before, after)
	if err != nil {
		return report, err
	}
	previous, err := impactPostings(before, originals)
	if err != nil {
		return report, err
	}
	proposed, err := impactPostings(after, originals)
	if err != nil {
		return report, err
	}
	sources, names, err := impactLookups(ctx, tx, previous, proposed)
	if err != nil {
		return report, err
	}
	type group struct{ source, order, recipient, kind string }
	items := make(map[group]ruleImpactItem)
	for step, postings := range [][]ruleImpactPosting{previous, proposed} {
		for _, posting := range postings {
			source := sources[posting.fact]
			if source.id == "" {
				return report, conflict
			}
			key := group{source.source, source.id, posting.recipient.identity(), posting.kind}
			item := items[key]
			item.OrderID, item.OrderNo, item.EntryKind = source.id, source.name, posting.kind
			if item.Recipient == "" || step == 1 {
				item.Recipient = posting.recipient.name(names)
			}
			if step == 0 {
				item.BeforeMinor, err = addMinor(item.BeforeMinor, posting.amount)
			} else {
				item.AfterMinor, err = addMinor(item.AfterMinor, posting.amount)
			}
			if err != nil {
				return report, err
			}
			items[key] = item
		}
	}
	changed := make([]group, 0)
	for key, item := range items {
		if item.AfterMinor != item.BeforeMinor {
			changed = append(changed, key)
		}
	}
	slices.SortFunc(changed, func(a, b group) int {
		return cmp.Or(cmp.Compare(a.source, b.source), cmp.Compare(a.order, b.order),
			cmp.Compare(a.kind, b.kind), cmp.Compare(a.recipient, b.recipient))
	})
	for index, key := range changed {
		item := items[key]
		item.Key = index + 1
		item.DeltaMinor, err = impactDelta(item.BeforeMinor, item.AfterMinor)
		if err != nil {
			return report, err
		}
		report.Items = append(report.Items, item)
	}
	return report, nil
}
