package main

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
)

type poolScope struct {
	Unit     string `json:"unitId"`
	Item     string `json:"itemKey"`
	Scenario string `json:"scenario"`
}

type poolMember struct {
	ID        int64  `json:"personId"`
	Name      string `json:"name"`
	Available int64  `json:"availableMinor"`
}

type poolDraftItem struct {
	poolScope
	Name        string                `json:"name"`
	Opening     int64                 `json:"openingMinor"`
	Income      int64                 `json:"incomeMinor"`
	Refund      int64                 `json:"refundMinor"`
	Confirmed   int64                 `json:"confirmedMinor"`
	Balance     int64                 `json:"balanceMinor"`
	Revision    int64                 `json:"revision"`
	PlanCurrent bool                  `json:"planCurrent"`
	Shares      []poolAllocationShare `json:"shares"`
	Members     []poolMember          `json:"members"`
}

type poolStage struct {
	poolDraftItem
	poolAccount
	current bool
}

type poolDraftReport struct {
	Version int64           `json:"version"`
	Basis   string          `json:"basis"`
	Closed  bool            `json:"closed"`
	Items   []poolDraftItem `json:"items"`
}

const poolAllocationBasisSQL = `SELECT md5(concat_ws('|',
 coalesce(extract(epoch FROM (SELECT captured_at FROM organization_import_state WHERE singleton))::text,''),
 coalesce(extract(epoch FROM c.captured_at)::text,''),
 (SELECT coalesce(string_agg(st.id::text,',' ORDER BY st.id),'') FROM settlements st
  JOIN pool_period_order prior ON prior.id=st.period_id
  WHERE prior.product_id=target.product_id
   AND (prior.book_date,prior.book_stage,prior.ordinal)<(target.book_date,target.book_stage,target.ordinal)
   AND (closed.id IS NULL OR st.id<closed.id))))
 FROM pool_period_order target LEFT JOIN product_catalog_settings config ON config.product_id=target.product_id
 LEFT JOIN settlements closed ON closed.period_id=target.id
 LEFT JOIN catalog_sales_context c ON c.source=config.source_system WHERE target.id=`

// In addition to organization membership, a catalog-funded share requires the
// component's role at the original payment time. Both draft reads and close
// review use this predicate, including allocations from earlier periods.
const poolCatalogRoleSQL = `EXISTS (
 SELECT 1 FROM periods period JOIN products prod ON prod.id=period.product_id
 JOIN product_catalog_settings cfg ON cfg.product_id=prod.id
 JOIN product_rule_versions policy ON policy.source_id=l.catalog_rule_id
 CROSS JOIN LATERAL jsonb_array_elements(policy.definition->'benefit_actions') action
 JOIN catalog_current_sales_appointments r ON r.person_id=m.person_id AND r.source=cfg.source_system
  AND r.role=action->>'role' AND r.valid_from<=v.occurred_at AND (r.valid_to IS NULL OR r.valid_to>v.occurred_at)
 WHERE period.id=l.period_id AND action->>'item_key'=l.component_type AND action->>'beneficiary_mode'='TEAM_POOL' AND (
  (r.role='SALES' AND ((r.scope='PRODUCT' AND r.scope_key=prod.code) OR (r.scope='PROJECT' AND r.scope_key=cfg.project_code)))
  OR (r.role<>'SALES' AND r.scope='ORGANIZATION_UNIT' AND r.scope_key=l.beneficiary_unit_id::text)
 )
)`

const poolAllocationCurrentSQL = `(a.draft_version=d.version AND
 (EXISTS(SELECT 1 FROM settlements final WHERE final.period_id=a.period_id) OR (
  a.basis_token=(` + poolAllocationBasisSQL + `a.period_id) AND NOT EXISTS(
   SELECT 1 FROM monthly_pool_allocation_shares s
   JOIN monthly_pool_allocation_funding f ON f.share_id=s.id
   JOIN calculation_draft_lines l ON l.id=f.source_line_id
   JOIN source_order_versions v ON v.id=l.source_version_id
   WHERE s.allocation_id=a.id AND s.person_id IS NOT NULL AND NOT EXISTS(
    SELECT 1 FROM organization_assignments m WHERE m.person_id=s.person_id AND m.unit_id=l.beneficiary_unit_id
     AND m.valid_from<=v.occurred_at AND (m.valid_to IS NULL OR v.occurred_at<m.valid_to)
     AND (l.practice_pool_id IS NOT NULL OR ` + poolCatalogRoleSQL + `)
   )
  )
 ))
 AND NOT EXISTS (
 SELECT 1 FROM monthly_pool_allocation_shares s WHERE s.allocation_id=a.id AND s.amount_minor<>(
  SELECT coalesce(sum(f.amount_minor),0) FROM monthly_pool_allocation_funding f
  JOIN calculation_draft_lines l ON l.id=f.source_line_id
  WHERE f.share_id=s.id AND (l.period_id=a.period_id OR EXISTS(SELECT 1 FROM settlements st WHERE st.period_id=l.period_id))
 )
))`

// Closed views use the postings already confirmed when the target was closed.
const poolEntriesSQL = `WITH scope AS (
 SELECT p.id,p.book_date<>'-infinity'::date AS practice,row_number() OVER (ORDER BY p.book_date,p.book_stage,p.ordinal) AS seq
 FROM pool_period_order target
 JOIN pool_period_order p ON p.product_id=target.product_id
  AND (p.book_date,p.book_stage,p.ordinal)<=(target.book_date,target.book_stage,target.ordinal)
 JOIN calculation_drafts d ON d.period_id=p.id
 LEFT JOIN settlements final ON final.period_id=target.id
 LEFT JOIN settlements posted ON posted.period_id=p.id
 WHERE target.id=$1 AND (p.id=target.id OR posted.id IS NOT NULL
  AND (final.id IS NULL OR posted.id<final.id))
), refunds AS MATERIALIZED (
 SELECT r.original_line_id,r.period_id,r.amount_minor,r.refund_source_version_id,r.id,p.seq,p.practice
 FROM scope p JOIN calculation_draft_refunds r ON r.period_id=p.id AND r.amount_minor<0
 JOIN pool_line_accounts account ON account.line_id=r.original_line_id
 LEFT JOIN scope origin ON origin.id=account.period_id
 WHERE p.practice OR origin.id IS NOT NULL
), required_lines AS (
 SELECT account.line_id FROM scope JOIN pool_line_accounts account ON account.period_id=scope.id
 UNION SELECT original_line_id FROM refunds
), lines AS MATERIALIZED (
 SELECT l.id,account.unit_id::text AS unit,account.item_key AS item,account.scenario_key AS scenario,
  coalesce(pool.name,a.name,l.component_type) AS name,l.period_id,l.amount_minor,scope.seq,v.occurred_at
 FROM required_lines needed JOIN pool_line_accounts account ON account.line_id=needed.line_id
 JOIN calculation_draft_lines l ON l.id=account.line_id
 LEFT JOIN scope ON scope.id=l.period_id
 LEFT JOIN product_rule_versions r ON r.source_id=l.catalog_rule_id
 LEFT JOIN practice_pools pool ON pool.id=l.practice_pool_id
 JOIN source_order_versions v ON v.id=l.source_version_id
 LEFT JOIN LATERAL (
  SELECT b->>'pool_name' AS name FROM jsonb_array_elements(r.definition->'benefit_actions') b
  WHERE b->>'item_key'=l.component_type LIMIT 1
 ) a ON true
), entries AS (
 SELECT l.unit,l.item,l.scenario,l.name,l.period_id,l.seq,l.id AS source_id,l.amount_minor,
  0 AS kind,l.occurred_at,l.id AS sort_id,false AS practice_distribution FROM lines l WHERE l.seq IS NOT NULL
 UNION ALL
 SELECT l.unit,l.item,l.scenario,l.name,r.period_id,r.seq,
  CASE WHEN r.practice AND (l.seq IS NULL OR l.seq>r.seq) THEN 0 ELSE l.id END,r.amount_minor,1,v.occurred_at,r.id,false
 FROM refunds r
 JOIN lines l ON l.id=r.original_line_id JOIN source_order_versions v ON v.id=r.refund_source_version_id
 UNION ALL
 SELECT l.unit,l.item,l.scenario,l.name,a.period_id,p.seq,l.id,-f.amount_minor,2,s.confirmed_at,sh.id,p.practice
 FROM scope p JOIN settlements s ON s.period_id=p.id JOIN monthly_pool_allocations a ON a.period_id=p.id AND a.draft_version=s.draft_version
 JOIN monthly_pool_allocation_shares sh ON sh.allocation_id=a.id
 JOIN monthly_pool_allocation_funding f ON f.share_id=sh.id JOIN lines l ON l.id=f.source_line_id
)
SELECT unit,item,scenario,name,period_id,source_id,amount_minor,kind,practice_distribution FROM entries
ORDER BY seq,kind,occurred_at,sort_id,source_id`

const poolEligibilitySQL = `SELECT DISTINCT l.id,m.person_id,person.name FROM unnest($1::bigint[]) source(id)
 JOIN calculation_draft_lines l ON l.id=source.id
 JOIN source_order_versions v ON v.id=l.source_version_id
 JOIN organization_assignments m ON m.unit_id=l.beneficiary_unit_id
  AND m.valid_from<=v.occurred_at AND (m.valid_to IS NULL OR m.valid_to>v.occurred_at)
 JOIN people person ON person.id=m.person_id
 WHERE l.practice_pool_id IS NOT NULL OR ` + poolCatalogRoleSQL + `
 ORDER BY 1,2`

func readPoolDraftVersion(ctx context.Context, tx pgx.Tx, periodID int64) (int64, bool, error) {
	var version int64
	var closed bool
	err := tx.QueryRow(ctx, `SELECT d.version,EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=d.period_id)
	 FROM calculation_drafts d JOIN periods period ON period.id=d.period_id JOIN products product ON product.id=period.product_id
	 WHERE d.period_id=$1 AND (d.rule_id IS NULL AND d.catalog_refunds_ready OR d.practice_roster_id IS NOT NULL
	  OR (`+practiceRefundMonthScope+`))`, periodID).Scan(&version, &closed)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errPoolStale
	}
	return version, closed, err
}

func readPoolStages(ctx context.Context, tx pgx.Tx, periodID int64) (map[poolScope]*poolStage, error) {
	states := map[poolScope]*poolStage{}
	rows, err := tx.Query(ctx, poolEntriesSQL, periodID)
	if err != nil {
		return nil, err
	}
	var scope poolScope
	var name string
	var postingPeriod, source, amount int64
	var kind int
	var practiceDistribution bool
	_, err = pgx.ForEachRow(rows, []any{&scope.Unit, &scope.Item, &scope.Scenario, &name, &postingPeriod, &source, &amount, &kind, &practiceDistribution}, func() error {
		a := states[scope]
		if a == nil {
			a = &poolStage{poolDraftItem: poolDraftItem{poolScope: scope, Shares: []poolAllocationShare{}, Members: []poolMember{}}, poolAccount: newPoolAccount()}
			states[scope] = a
		}
		a.Name = name
		if postingPeriod == periodID && !a.current {
			a.Opening, a.current = a.balance, true
		}
		switch kind {
		case 0:
			if err := a.credit(source, amount); err != nil {
				return err
			}
			if postingPeriod == periodID {
				a.Income, err = addMinor(a.Income, amount)
			}
		case 1:
			if err := a.debit(source, -amount); err != nil {
				return err
			}
			if postingPeriod == periodID {
				a.Refund, err = addMinor(a.Refund, amount)
			}
		case 2:
			// A backdated refund may reduce the lot that funded an already
			// confirmed distribution. Retain the payment and carry the deficit.
			if practiceDistribution {
				err = a.debit(source, -amount)
			} else {
				err = a.distribute(source, -amount)
			}
			if err != nil {
				return err
			}
			if postingPeriod == periodID {
				a.Confirmed, err = addMinor(a.Confirmed, -amount)
			}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, state := range states {
		state.Balance = state.balance
		if !state.current {
			state.Opening = state.balance
		}
	}
	return states, nil
}

func poolStageMembers(ctx context.Context, tx pgx.Tx, states map[poolScope]*poolStage) error {
	type origin struct {
		Account *poolStage
		Index   int
	}
	byID := map[int64]origin{}
	var ids []int64
	for _, stage := range states {
		for i, lot := range stage.lots {
			if lot.Remaining > 0 {
				byID[lot.LineID] = origin{stage, i}
				ids = append(ids, lot.LineID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, poolEligibilitySQL, ids)
	if err != nil {
		return err
	}
	people := map[*poolStage]map[int64]poolMember{}
	var line, id int64
	var name string
	_, err = pgx.ForEachRow(rows, []any{&line, &id, &name}, func() error {
		source := byID[line]
		lot := &source.Account.lots[source.Index]
		lot.Eligible = append(lot.Eligible, id)
		group := people[source.Account]
		if group == nil {
			group = map[int64]poolMember{}
			people[source.Account] = group
		}
		member := group[id]
		member.ID, member.Name = id, name
		member.Available += lot.Remaining
		group[id] = member
		return nil
	})
	if err != nil {
		return err
	}
	for stage, group := range people {
		for _, member := range group {
			stage.Members = append(stage.Members, member)
		}
		slices.SortFunc(stage.Members, func(a, b poolMember) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	}
	return nil
}

func readPoolSavedPlans(ctx context.Context, tx pgx.Tx, periodID int64, states map[poolScope]*poolStage) error {
	rows, err := tx.Query(ctx, `SELECT a.unit_id::text,a.item_key,a.scenario_key,a.revision,`+poolAllocationCurrentSQL+`,
	 coalesce(s.person_id,0),coalesce(s.amount_minor,0),coalesce(p.name,''),coalesce(pool.name,u.name || ' · ' || a.item_key)
	 FROM monthly_pool_allocations a JOIN calculation_drafts d USING(period_id)
	 JOIN organization_units u ON u.id=a.unit_id
	 JOIN periods target ON target.id=a.period_id
	 LEFT JOIN practice_pools pool ON a.scenario_key='class' AND pool.id::text=a.item_key AND pool.product_id=target.product_id
	  AND pool.unit_id=a.unit_id
	 LEFT JOIN monthly_pool_allocation_shares s ON s.allocation_id=a.id LEFT JOIN people p ON p.id=s.person_id
	 WHERE a.period_id=$1 ORDER BY a.id,s.person_id`, periodID)
	if err != nil {
		return err
	}
	var scope poolScope
	var rev int64
	var current bool
	var name, poolName string
	var share poolAllocationShare
	_, err = pgx.ForEachRow(rows, []any{&scope.Unit, &scope.Item, &scope.Scenario, &rev, &current, &share.PersonID, &share.Amount, &name, &poolName}, func() error {
		stage := states[scope]
		if stage == nil {
			stage = &poolStage{poolDraftItem: poolDraftItem{poolScope: scope, Name: poolName,
				Shares: []poolAllocationShare{}, Members: []poolMember{}}}
			states[scope] = stage
		}
		stage.Revision, stage.PlanCurrent = rev, current
		if share.Amount > 0 {
			stage.Shares = append(stage.Shares, share)
			if share.PersonID != 0 && !slices.ContainsFunc(stage.Members, func(m poolMember) bool { return m.ID == share.PersonID }) {
				stage.Members = append(stage.Members, poolMember{ID: share.PersonID, Name: name})
			}
		}
		return nil
	})
	return err
}

func (s workspace) draftPools(w http.ResponseWriter, r *http.Request) {
	period, version, _, err := draftReportPage(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	report := poolDraftReport{Version: version, Items: []poolDraftItem{}}
	ctx := r.Context()
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		current, closed, err := readPoolDraftVersion(ctx, tx, period)
		if err != nil {
			return err
		}
		if current != version {
			return errPoolStale
		}
		report.Closed = closed
		if err := tx.QueryRow(ctx, poolAllocationBasisSQL+"$1", period).Scan(&report.Basis); err != nil {
			return err
		}
		states, err := readPoolStages(ctx, tx, period)
		if err != nil {
			return err
		}
		if err = poolStageMembers(ctx, tx, states); err != nil {
			return err
		}
		if err = readPoolSavedPlans(ctx, tx, period, states); err != nil {
			return err
		}
		for _, item := range states {
			if item.current || item.Balance != 0 || len(item.Shares) != 0 {
				report.Items = append(report.Items, item.poolDraftItem)
			}
		}
		slices.SortFunc(report.Items, func(a, b poolDraftItem) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Unit, b.Unit), cmp.Compare(a.Scenario, b.Scenario), cmp.Compare(a.Item, b.Item))
		})
		return nil
	})
	respond(w, report, err)
}

type savePoolAllocationInput struct {
	PeriodID int64  `json:"periodId"`
	Version  int64  `json:"version"`
	Revision int64  `json:"revision"`
	Basis    string `json:"basis"`
	poolScope
	Shares   []poolAllocationShare `json:"shares"`
	Retained int64                 `json:"retainedMinor"`
}

func (s workspace) savePoolAllocation(ctx context.Context, input savePoolAllocationInput) (any, error) {
	unit, ok := canonicalSourceUUID(input.Unit)
	if !ok || input.PeriodID <= 0 || input.Version <= 0 || input.Revision < 0 || input.Retained < 0 || len(input.Basis) != 32 ||
		!textOK(input.Item, 128) || !textOK(input.Scenario, 128) {
		return nil, invalid
	}
	for _, share := range input.Shares {
		if share.PersonID <= 0 {
			return nil, invalid
		}
	}
	if input.Retained > 0 {
		input.Shares = append(slices.Clone(input.Shares), poolAllocationShare{Amount: input.Retained})
	}
	input.Unit = unit
	var revision int64
	err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var lock int64
		if err := tx.QueryRow(ctx, `SELECT id FROM periods WHERE id=$1 FOR UPDATE`, input.PeriodID).Scan(&lock); err != nil {
			return err
		}
		current, closed, err := readPoolDraftVersion(ctx, tx, input.PeriodID)
		if err != nil {
			return err
		}
		if closed {
			return errSettlementClosed
		}
		if current != input.Version {
			return errPoolStale
		}
		var basis string
		if err := tx.QueryRow(ctx, poolAllocationBasisSQL+"$1", input.PeriodID).Scan(&basis); err != nil {
			return err
		}
		if basis != input.Basis {
			return errPoolStale
		}
		stages, err := readPoolStages(ctx, tx, input.PeriodID)
		if err != nil {
			return err
		}
		stage := stages[input.poolScope]
		if stage == nil {
			if len(input.Shares) != 0 || input.Revision == 0 {
				return invalid
			}
			revision, err = storePoolAllocation(ctx, tx, input, nil)
			return err
		}
		if err = poolStageMembers(ctx, tx, map[poolScope]*poolStage{input.poolScope: stage}); err != nil {
			return err
		}
		funding, err := allocatePoolFunding(stage.lots, input.Shares)
		if err != nil {
			return err
		}
		revision, err = storePoolAllocation(ctx, tx, input, funding)
		return err
	})
	return map[string]int64{"revision": revision}, err
}

func storePoolAllocation(ctx context.Context, tx pgx.Tx, input savePoolAllocationInput, cells []poolFundingCell) (int64, error) {
	var id, revision int64
	err := tx.QueryRow(ctx, `SELECT id,revision FROM monthly_pool_allocations
	 WHERE period_id=$1 AND unit_id=$2 AND item_key=$3 AND scenario_key=$4`, input.PeriodID, input.Unit, input.Item, input.Scenario).Scan(&id, &revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if revision != input.Revision {
		return 0, errPoolStale
	}
	if id == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO monthly_pool_allocations(period_id,unit_id,item_key,scenario_key,draft_version,revision,basis_token)
		 VALUES($1,$2,$3,$4,$5,1,$6) RETURNING id,revision`, input.PeriodID, input.Unit, input.Item, input.Scenario, input.Version, input.Basis).Scan(&id, &revision)
	} else {
		err = tx.QueryRow(ctx, `UPDATE monthly_pool_allocations SET draft_version=$2,revision=revision+1,
		 basis_token=$3 WHERE id=$1 RETURNING revision`, id, input.Version, input.Basis).Scan(&revision)
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM monthly_pool_allocation_shares WHERE allocation_id=$1`, id); err != nil {
		return 0, err
	}
	shareIDs := map[int64]int64{}
	for _, s := range input.Shares {
		var share int64
		if err = tx.QueryRow(ctx, `INSERT INTO monthly_pool_allocation_shares(allocation_id,person_id,amount_minor) VALUES($1,$2,$3) RETURNING id`,
			id, optionalDraftID(s.PersonID), s.Amount).Scan(&share); err != nil {
			return 0, err
		}
		shareIDs[s.PersonID] = share
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"monthly_pool_allocation_funding"}, []string{"share_id", "source_line_id", "amount_minor"},
		pgx.CopyFromSlice(len(cells), func(i int) ([]any, error) {
			c := cells[i]
			return []any{shareIDs[c.PersonID], c.LineID, c.Amount}, nil
		}))
	return revision, err
}
