package main

import (
	"cmp"
	"errors"
	"slices"
)

var (
	errPoolAmount      = errors.New("可分配金额不足，请核对退款及人员可用份额")
	errPoolEligibility = errors.New("有人员在对应付款时点没有可分配资格")
	errPoolStale       = errors.New("可分配余额或草稿版本已变化，请刷新后再保存")
)

type poolFundingLot struct {
	LineID, Remaining int64
	Eligible          []int64
}

type poolAccount struct {
	balance int64
	lots    []poolFundingLot
	indices map[int64]int
}

func newPoolAccount() poolAccount {
	return poolAccount{indices: make(map[int64]int)}
}

func (a *poolAccount) credit(id, amount int64) error {
	if _, exists := a.indices[id]; exists || amount <= 0 {
		return conflict
	}
	next, err := addMinor(a.balance, amount)
	if err != nil {
		return err
	}
	available := amount
	if a.balance < 0 {
		available = max(0, next)
	}
	a.balance = next
	a.indices[id] = len(a.lots)
	a.lots = append(a.lots, poolFundingLot{LineID: id, Remaining: available})
	return nil
}

func (a *poolAccount) refund(original, amount int64) error {
	index, found := a.indices[original]
	if !found || amount <= 0 {
		return conflict
	}
	next, err := addMinor(a.balance, -amount)
	if err != nil {
		return err
	}
	a.balance = next
	used := min(amount, a.lots[index].Remaining)
	a.lots[index].Remaining -= used
	left := amount - used
	for i := range a.lots {
		if left == 0 {
			break
		}
		used := min(left, a.lots[i].Remaining)
		a.lots[i].Remaining -= used
		left -= used
	}
	return nil
}

func (a *poolAccount) distribute(source, amount int64) error {
	i, ok := a.indices[source]
	if !ok || amount <= 0 || amount > a.lots[i].Remaining {
		return conflict
	}
	a.balance -= amount
	a.lots[i].Remaining -= amount
	return nil
}

type poolAllocationShare struct {
	PersonID int64 `json:"personId"`
	Amount   int64 `json:"amountMinor"`
}

type poolFundingCell struct {
	LineID, PersonID, Amount int64
}

// Residual edges may move earlier tentative use of a lot to another qualified
// member. A feasible set of shares therefore does not depend on their input order.
func allocatePoolFunding(lots []poolFundingLot, input []poolAllocationShare) ([]poolFundingCell, error) {
	shares := slices.Clone(input)
	slices.SortFunc(shares, func(a, b poolAllocationShare) int { return cmp.Compare(a.PersonID, b.PersonID) })
	type edge struct {
		to, reverse int
		capacity    int64
	}
	sink := len(lots) + len(shares) + 1
	graph := make([][]edge, sink+1)
	connect := func(from, to int, amount int64) int {
		index := len(graph[from])
		graph[from] = append(graph[from], edge{to, len(graph[to]), amount})
		graph[to] = append(graph[to], edge{from, index, 0})
		return index
	}
	type funded struct{ lot, share, index int }
	var edges []funded
	qualified := make([]bool, len(shares))
	for i, lot := range lots {
		if lot.Remaining <= 0 {
			continue
		}
		connect(0, i+1, lot.Remaining)
		for j, share := range shares {
			if share.PersonID == 0 || slices.Contains(lot.Eligible, share.PersonID) {
				qualified[j] = true
				pos := connect(i+1, len(lots)+j+1, lot.Remaining)
				edges = append(edges, funded{i, j, pos})
			}
		}
	}
	var total int64
	for j, share := range shares {
		if share.PersonID < 0 || share.Amount <= 0 || j > 0 && shares[j-1].PersonID == share.PersonID {
			return nil, invalid
		}
		if !qualified[j] {
			return nil, errPoolEligibility
		}
		var err error
		total, err = addMinor(total, share.Amount)
		if err != nil {
			return nil, err
		}
		connect(len(lots)+j+1, sink, share.Amount)
	}
	parent, path := make([]int, len(graph)), make([]int, len(graph))
	queue := make([]int, 0, len(graph))
	for total > 0 {
		for i := range parent {
			parent[i] = -1
		}
		parent[0] = 0
		queue = append(queue[:0], 0)
		for k := 0; k < len(queue) && parent[sink] < 0; k++ {
			from := queue[k]
			for i, e := range graph[from] {
				if e.capacity > 0 && parent[e.to] < 0 {
					parent[e.to], path[e.to] = from, i
					queue = append(queue, e.to)
				}
			}
		}
		if parent[sink] < 0 {
			return nil, errPoolAmount
		}
		amount := total
		for to := sink; to != 0; to = parent[to] {
			amount = min(amount, graph[parent[to]][path[to]].capacity)
		}
		for to := sink; to != 0; to = parent[to] {
			e := &graph[parent[to]][path[to]]
			e.capacity -= amount
			graph[to][e.reverse].capacity += amount
		}
		total -= amount
	}
	out := make([]poolFundingCell, 0, len(edges))
	for _, f := range edges {
		e := graph[f.lot+1][f.index]
		if amount := graph[e.to][e.reverse].capacity; amount > 0 {
			out = append(out, poolFundingCell{lots[f.lot].LineID, shares[f.share].PersonID, amount})
		}
	}
	return out, nil
}
