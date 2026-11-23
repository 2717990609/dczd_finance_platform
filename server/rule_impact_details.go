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
	CurrentRuleAvailable bool                    `json:"currentRuleAvailable"`
	MatchedPaymentCount  *int                    `json:"matchedPaymentCount,omitempty"`
	BeforeMinor          int64                   `json:"beforeMinor"`
	AfterMinor           int64                   `json:"afterMinor"`
	DeltaMinor           int64                   `json:"deltaMinor"`
	BeforeIssueCount     int                     `json:"beforeIssueCount"`
	AfterIssueCount      int                     `json:"afterIssueCount"`
	IssueReasons         []ruleImpactIssueReason `json:"issueReasons,omitempty"`
	Items                []ruleImpactItem        `json:"items"`
}

type ruleImpactIssueReason struct {
	Code        string `json:"code"`
	BeforeCount int    `json:"beforeCount"`
	AfterCount  int    `json:"afterCount"`
}

type ruleImpactItem struct {
	Key             int    `json:"key"`
	OrderID         string `json:"orderId"`
	OrderNo         string `json:"orderNo"`
	Recipient       string `json:"recipient"`
	AllocationOwner string `json:"allocationOwner,omitempty"`
	Arrangement     string `json:"arrangement"`
	ArrangementRole string `json:"arrangementRole,omitempty"`
	EntryKind       string `json:"entryKind"`
	BeforeMinor     int64  `json:"beforeMinor"`
	AfterMinor      int64  `json:"afterMinor"`
	DeltaMinor      int64  `json:"deltaMinor"`
}

type ruleImpactRecipient struct {
	person, practicePool, owner int64
	unit, catalogRule           pgtype.UUID
	poolItem                    string
}

type ruleImpactPosting struct {
	fact      int64
	recipient ruleImpactRecipient
	component impactComponentRef
	kind      string
	label     string
	amount    int64
}

type impactComponentRef struct {
	rule    int64
	catalog pgtype.UUID
	code    string
}

type impactArrangement struct {
	key, name, role string
}

func impactRecipient(line draftLine) ruleImpactRecipient {
	party := ruleImpactRecipient{person: line.PersonID}
	if line.PracticePool != nil {
		party.practicePool = line.PracticePool.PoolID
		party.owner = line.PracticePool.OwnerID
	} else if line.Catalog != nil && line.Catalog.Unit.Valid {
		party.unit = line.Catalog.Unit
		if line.Catalog.AllocationOwnerID > 0 {
			party.owner = line.Catalog.AllocationOwnerID
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
	rows, err := tx.Query(ctx, `SELECT id,source_version_id,coalesce(person_id,0),coalesce(practice_pool_id,0),coalesce(allocation_owner_id,0),
	 beneficiary_unit_id,catalog_rule_id,coalesce(rule_id,0),component_type
	 FROM calculation_draft_lines WHERE id=ANY($1::bigint[])`, slices.Collect(maps.Keys(ids)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var item ruleImpactPosting
		party := &item.recipient
		if err := rows.Scan(&id, &item.fact, &party.person, &party.practicePool, &party.owner, &party.unit,
			&item.component.catalog, &item.component.rule, &item.component.code); err != nil {
			return nil, err
		}
		if party.owner != 0 && item.component.catalog.Valid {
			party.catalogRule, party.poolItem = item.component.catalog, item.component.code
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
		ref := impactComponentRef{rule: line.RuleID, code: line.Component}
		if line.Catalog != nil {
			ref.catalog = line.Catalog.Rule
		}
		posting := ruleImpactPosting{fact: line.FactID, recipient: impactRecipient(line), component: ref, kind: "accrual", amount: line.Amount}
		posting.label = line.practiceArrangementLabel
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

func impactArrangements(ctx context.Context, tx pgx.Tx, all ...[]ruleImpactPosting) (map[impactComponentRef]impactArrangement, error) {
	refs := make(map[impactComponentRef]bool)
	for _, postings := range all {
		for _, posting := range postings {
			refs[posting.component] = true
		}
	}
	result := make(map[impactComponentRef]impactArrangement, len(refs))
	if len(refs) == 0 {
		return result, nil
	}
	rules := make([]int64, 0, len(refs))
	catalog := make([]pgtype.UUID, 0, len(refs))
	codes := make([]string, 0, len(refs))
	for ref := range refs {
		rules, catalog, codes = append(rules, ref.rule), append(catalog, ref.catalog), append(codes, ref.code)
	}
	rows, err := tx.Query(ctx, `WITH inputs AS (
	  SELECT *,CASE WHEN starts_with(code,'period:') THEN split_part(code,':',2) ELSE code END AS kind,
	   CASE WHEN starts_with(code,'period:') AND split_part(code,':',3) ~ '^[0-9]{1,9}$' THEN split_part(code,':',3)::int END AS idx
	  FROM unnest($1::bigint[],$2::uuid[],$3::text[]) AS refs(rule_id,catalog_id,code)
	 ), parts AS (
	  SELECT i.*,r.components,r.components->i.idx AS part,cv.definition
	  FROM inputs i LEFT JOIN rule_versions r ON r.id=i.rule_id
	  LEFT JOIN product_rule_versions cv ON cv.source_id=i.catalog_id
	 )
	 SELECT p.rule_id,p.catalog_id,p.code,p.kind,coalesce(p.part->>'classMember',''),
	  coalesce(title.name,''),coalesce(duplicate.ord,0)::int,
	  coalesce(CASE WHEN action.item IS NOT NULL AND p.code=action.item||':DCAI_SERVICE'
	    THEN nullif(p.definition->'internal_sales_direct_split'->>'service_label','') END,
	   nullif(action.label,''),opt.label,
	   CASE p.kind WHEN 'company_referral' THEN '公司推荐分润' WHEN 'retrain_service' THEN '服务提成'
	    WHEN 'retrain_pool' THEN '复训销售归集' ELSE p.kind END),
	  CASE WHEN action.item IS NOT NULL AND action.item<>p.code THEN split_part(p.code,':',2) ELSE '' END
	 FROM parts p
	 LEFT JOIN LATERAL (
	  SELECT coalesce(p.part->'companySales'->>'label',p.part->'periodBenefit'->>'label') AS name
	 ) title ON p.idx IS NOT NULL
	 LEFT JOIN LATERAL (
	  SELECT count(*) AS ord FROM jsonb_array_elements(p.components) WITH ORDINALITY AS c(part,pos)
	  WHERE c.pos<=p.idx AND c.part->>'type'=p.part->>'type'
	   AND coalesce(c.part->>'classMember','')=coalesce(p.part->>'classMember','')
	   AND coalesce(c.part->'companySales'->>'label',c.part->'periodBenefit'->>'label')=title.name
	 ) duplicate ON title.name<>''
	 LEFT JOIN configuration_options opt ON opt.group_key='component' AND opt.option_key=p.kind
	 LEFT JOIN LATERAL (
	  SELECT a->>'label' AS label,a->>'item_key' AS item FROM jsonb_array_elements(p.definition->'benefit_actions') a
	  WHERE a->>'item_key'=p.code OR
	   (a->>'item_key'=p.definition->'internal_sales_direct_split'->>'sales_item_key' AND starts_with(p.code,(a->>'item_key')||':'))
	  ORDER BY (a->>'item_key'=p.code) DESC LIMIT 1
	 ) action ON true`, rules, catalog, codes)
	if err != nil {
		return nil, err
	}
	var ref impactComponentRef
	var kind, member, explicit, fallback, role string
	var ordinal int
	_, err = pgx.ForEachRow(rows, []any{&ref.rule, &ref.catalog, &ref.code, &kind, &member, &explicit, &ordinal, &fallback, &role}, func() error {
		arrangement := impactArrangement{key: ref.code, name: fallback, role: role}
		if explicit != "" {
			// A practice benefit's position may change when other benefits are removed.
			arrangement.key = kind + "\x00" + member + "\x00" + explicit + "\x00" + strconv.Itoa(ordinal)
			arrangement.name = explicit
			if kind == "company_referral" {
				arrangement.name = fallback + " · " + explicit
			}
		}
		result[ref] = arrangement
		return nil
	})
	return result, err
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
			if party.owner != 0 {
				people[party.owner] = true
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

func readRuleImpactReport(ctx context.Context, tx pgx.Tx, before, after paymentDraft, matchedOrders bool) (ruleImpactReport, error) {
	report := ruleImpactReport{BeforeIssueCount: before.blockingIssues(), AfterIssueCount: after.blockingIssues(),
		IssueReasons: impactIssueReasons(before.Issues, after.Issues), Items: []ruleImpactItem{}}
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
	arrangements, err := impactArrangements(ctx, tx, previous, proposed)
	if err != nil {
		return report, err
	}
	type group struct {
		source, order, recipient, kind, arrangement string
		owner                                       int64
	}
	items := make(map[group]ruleImpactItem)
	for step, postings := range [][]ruleImpactPosting{previous, proposed} {
		for _, posting := range postings {
			source := sources[posting.fact]
			if source.id == "" {
				return report, conflict
			}
			arrangement := arrangements[posting.component]
			key := group{source.source, source.id, posting.recipient.identity(), posting.kind, arrangement.key, posting.recipient.owner}
			item := items[key]
			item.OrderID, item.OrderNo, item.EntryKind = source.id, source.name, posting.kind
			if item.Recipient == "" || step == 1 {
				item.Recipient = posting.recipient.name(names)
				item.Arrangement, item.ArrangementRole = arrangement.name, arrangement.role
				if posting.label != "" {
					item.Arrangement = posting.label
				}
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
		if matchedOrders && key.kind == "accrual" && item.AfterMinor > 0 ||
			!matchedOrders && item.AfterMinor != item.BeforeMinor {
			changed = append(changed, key)
		}
	}
	slices.SortFunc(changed, func(a, b group) int {
		return cmp.Or(cmp.Compare(a.source, b.source), cmp.Compare(a.order, b.order),
			cmp.Compare(a.kind, b.kind), cmp.Compare(a.recipient, b.recipient), cmp.Compare(a.owner, b.owner),
			cmp.Compare(a.arrangement, b.arrangement))
	})
	for index, key := range changed {
		item := items[key]
		item.Key = index + 1
		if key.owner > 0 {
			item.AllocationOwner = names["person:"+strconv.FormatInt(key.owner, 10)]
			if item.AllocationOwner == "" {
				item.AllocationOwner = "人员待核"
			}
		}
		item.DeltaMinor, err = impactDelta(item.BeforeMinor, item.AfterMinor)
		if err != nil {
			return report, err
		}
		report.Items = append(report.Items, item)
	}
	return report, nil
}

func impactIssueReasons(before, after []draftIssue) []ruleImpactIssueReason {
	reasons := make(map[string]ruleImpactIssueReason)
	for _, issue := range before {
		if !issue.Advisory {
			reason := reasons[issue.Code]
			reason.Code = issue.Code
			reason.BeforeCount++
			reasons[issue.Code] = reason
		}
	}
	for _, issue := range after {
		if !issue.Advisory {
			reason := reasons[issue.Code]
			reason.Code = issue.Code
			reason.AfterCount++
			reasons[issue.Code] = reason
		}
	}
	result := slices.Collect(maps.Values(reasons))
	slices.SortFunc(result, func(a, b ruleImpactIssueReason) int {
		return cmp.Or(cmp.Compare(b.AfterCount-b.BeforeCount, a.AfterCount-a.BeforeCount), cmp.Compare(a.Code, b.Code))
	})
	return result
}
