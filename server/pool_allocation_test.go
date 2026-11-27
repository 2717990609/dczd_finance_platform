package main

import (
	"errors"
	"testing"
)

func TestPoolRefundPrefersOriginalLotAndCarriesDeficit(t *testing.T) {
	account := newPoolAccount()
	add := func(id, amount int64) {
		t.Helper()
		if err := account.credit(id, amount, ""); err != nil {
			t.Fatal(err)
		}
	}
	add(1, 100)
	add(2, 60)
	if err := account.distribute(1, 90); err != nil {
		t.Fatal(err)
	}
	if err := account.debit(1, 40, ""); err != nil {
		t.Fatal(err)
	}
	if account.balance != 30 || account.lots[0].Remaining != 0 || account.lots[1].Remaining != 30 {
		t.Fatalf("refund consumed the wrong lots: %+v", account)
	}
	if err := account.debit(2, 50, ""); err != nil {
		t.Fatal(err)
	}
	if account.balance != -20 || account.lots[1].Remaining != 0 {
		t.Fatalf("negative balance must stay with the pool: %+v", account)
	}
	add(3, 50)
	if account.balance != 30 || account.lots[2].Remaining != 30 {
		t.Fatalf("new income should clear the prior deficit: %+v", account)
	}
}

func TestPoolFundingRespectsQualificationsWithoutGreedyFailure(t *testing.T) {
	lots := []poolFundingLot{
		{LineID: 10, Remaining: 70, Eligible: []int64{1, 2}},
		{LineID: 11, Remaining: 30, Eligible: []int64{1}},
	}
	got, err := allocatePoolFunding(lots, []poolAllocationShare{{PersonID: 1, Amount: 70}, {PersonID: 2, Amount: 30}})
	if err != nil {
		t.Fatal(err)
	}
	sums := map[int64]int64{}
	for _, edge := range got {
		sums[edge.PersonID] += edge.Amount
		if edge.LineID == 11 && edge.PersonID != 1 {
			t.Fatalf("member qualified at a different time was assigned funds: %+v", edge)
		}
	}
	if sums[1] != 70 || sums[2] != 30 {
		t.Fatalf("funding did not cover the requested amounts: %+v", got)
	}
	if _, err := allocatePoolFunding(lots, []poolAllocationShare{{PersonID: 2, Amount: 71}}); !errors.Is(err, errPoolAmount) {
		t.Fatalf("member's eligible capacity should cap their share: %v", err)
	}
	if _, err := allocatePoolFunding(lots, []poolAllocationShare{{PersonID: 9, Amount: 1}}); !errors.Is(err, errPoolEligibility) {
		t.Fatalf("out-of-scope member should not be payable: %v", err)
	}
}
