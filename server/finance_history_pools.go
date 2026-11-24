package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

var errFinanceHistoryPoolFunding = errors.New("历史团队池分配与原计提、收款人或可用金额不一致")
var errFinanceHistorySavedPools = errors.New("该期已有保存的团队池分配，请先核对后再按历史分配承接")

type financeHistoryPool struct {
	poolScope
	Funding []financeHistoryFundingCell `json:"funding"`
}

type financeHistoryFundingCell struct {
	LineID   int64 `json:"sourceLineId"`
	PersonID int64 `json:"personId"`
	Amount   int64 `json:"amountMinor"`
}

func (c *financeHistoryFundingCell) UnmarshalJSON(raw []byte) error {
	var row struct {
		LineID   int64  `json:"sourceLineId"`
		PersonID *int64 `json:"personId"`
		Amount   int64  `json:"amountMinor"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&row); err != nil {
		return err
	}
	if row.PersonID == nil {
		return errFinanceHistoryPoolFunding
	}
	*c = financeHistoryFundingCell{LineID: row.LineID, PersonID: *row.PersonID, Amount: row.Amount}
	return nil
}

func normalizeHistoryPoolFunding(pools []financeHistoryPool) ([]financeHistoryPool, error) {
	pools = slices.Clone(pools)
	for i := range pools {
		pool := &pools[i]
		unit, ok := canonicalSourceUUID(pool.Unit)
		if !ok || !textOK(pool.Item, 128) || (pool.Scenario != "" && !textOK(pool.Scenario, 128)) || len(pool.Funding) == 0 {
			return nil, errFinanceHistoryPoolFunding
		}
		pool.Unit = unit
		pool.Funding = slices.Clone(pool.Funding)
		slices.SortFunc(pool.Funding, func(a, b financeHistoryFundingCell) int {
			return cmp.Or(cmp.Compare(a.LineID, b.LineID), cmp.Compare(a.PersonID, b.PersonID))
		})
		for j, cell := range pool.Funding {
			if cell.LineID <= 0 || cell.PersonID < 0 || cell.Amount <= 0 {
				return nil, errFinanceHistoryPoolFunding
			}
			if j > 0 && pool.Funding[j-1].LineID == cell.LineID && pool.Funding[j-1].PersonID == cell.PersonID {
				return nil, errFinanceHistoryPoolFunding
			}
		}
		if _, err := historyPoolShareTotals(pool.Funding); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(pools, func(a, b financeHistoryPool) int {
		return cmp.Or(cmp.Compare(a.Unit, b.Unit), cmp.Compare(a.Item, b.Item), cmp.Compare(a.Scenario, b.Scenario))
	})
	for i := 1; i < len(pools); i++ {
		if pools[i-1].poolScope == pools[i].poolScope {
			return nil, errFinanceHistoryPoolFunding
		}
	}
	return pools, nil
}

func reviewHistoryPoolFunding(ctx context.Context, tx pgx.Tx, periodID int64, scope settlementReview,
	pools []financeHistoryPool) (settlementReview, string, error) {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM monthly_pool_allocations WHERE period_id=$1)", periodID).Scan(&exists); err != nil {
		return scope, "", err
	}
	if exists {
		return scope, "", errFinanceHistorySavedPools
	}
	var basis string
	if err := tx.QueryRow(ctx, poolAllocationBasisSQL+"$1", periodID).Scan(&basis); err != nil {
		return scope, "", err
	}
	states, err := readPoolStages(ctx, tx, periodID)
	if err != nil {
		return scope, "", err
	}
	assignees := map[int64]struct{}{}
	for _, pool := range pools {
		stage := states[pool.poolScope]
		if stage == nil {
			return scope, "", errFinanceHistoryPoolFunding
		}
		for _, cell := range pool.Funding {
			if stage.distribute(cell.LineID, cell.Amount) != nil {
				return scope, "", errFinanceHistoryPoolFunding
			}
			if cell.PersonID > 0 {
				assignees[cell.PersonID] = struct{}{}
			}
		}
	}
	ids := make([]int64, 0, len(assignees))
	for person := range assignees {
		ids = append(ids, person)
	}
	var matched int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM people WHERE id=ANY($1::bigint[])", ids).Scan(&matched); err != nil {
		return scope, "", err
	}
	if matched != len(assignees) {
		return scope, "", errFinanceHistoryPoolFunding
	}
	// Eligibility for these payments came from their original confirmation. The
	// reconciled posting digest binds all supplied line/person pairs.
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%x:%s:%d",
		periodID, scope.Version, scope.digest, basis, scope.lastRemoval))))
	review, err := finishPoolSettlementReview(ctx, tx, periodID, scope, states, nil, token)
	return review, basis, err
}

func historyPoolShareTotals(cells []financeHistoryFundingCell) (map[int64]int64, error) {
	totals := map[int64]int64{}
	for _, cell := range cells {
		total, err := addMinor(totals[cell.PersonID], cell.Amount)
		if err != nil {
			return nil, errFinanceHistoryPoolFunding
		}
		totals[cell.PersonID] = total
	}
	return totals, nil
}

func storeHistoryPoolFunding(ctx context.Context, tx pgx.Tx, period, version int64, basis string, pools []financeHistoryPool) error {
	for _, pool := range pools {
		input := savePoolAllocationInput{PeriodID: period, Version: version, Basis: basis, poolScope: pool.poolScope}
		totals, err := historyPoolShareTotals(pool.Funding)
		if err != nil {
			return err
		}
		for person, amount := range totals {
			input.Shares = append(input.Shares, poolAllocationShare{PersonID: person, Amount: amount})
		}
		cells := make([]poolFundingCell, 0, len(pool.Funding))
		for _, cell := range pool.Funding {
			cells = append(cells, poolFundingCell(cell))
		}
		slices.SortFunc(input.Shares, func(a, b poolAllocationShare) int { return cmp.Compare(a.PersonID, b.PersonID) })
		if _, err := storePoolAllocation(ctx, tx, input, cells); err != nil {
			return err
		}
	}
	return nil
}

func verifyStoredHistoryPoolFunding(ctx context.Context, tx pgx.Tx, period int64, pools []financeHistoryPool) error {
	if len(pools) == 0 {
		return nil
	}
	pairs := make(map[poolScope]map[poolFundingCell]bool, len(pools))
	shares := make(map[poolScope]map[int64]int64, len(pools))
	var remaining int
	for _, pool := range pools {
		pairs[pool.poolScope] = make(map[poolFundingCell]bool, len(pool.Funding))
		totals, err := historyPoolShareTotals(pool.Funding)
		if err != nil {
			return err
		}
		shares[pool.poolScope] = totals
		for _, cell := range pool.Funding {
			pairs[pool.poolScope][poolFundingCell(cell)] = true
			remaining++
		}
	}
	rows, err := tx.Query(ctx, settlementPoolFundingSQL, period)
	if err != nil {
		return err
	}
	var key poolScope
	var cell poolFundingCell
	var allocation, revision, share int64
	_, err = pgx.ForEachRow(rows, []any{&allocation, &revision, &key.Unit, &key.Item, &key.Scenario, &cell.PersonID, &share, &cell.LineID, &cell.Amount}, func() error {
		if !pairs[key][cell] || shares[key][cell.PersonID] != share {
			return errFinanceHistoryPoolFunding
		}
		delete(pairs[key], cell)
		remaining--
		return nil
	})
	if err != nil {
		return err
	}
	if remaining != 0 {
		return errFinanceHistoryPoolFunding
	}
	return nil
}
