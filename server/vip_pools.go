package main

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
)

type vipPoolContribution struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	PaymentCount int    `json:"paymentCount"`
	Amount       int64  `json:"amountMinor"`
}

type vipPoolReport struct {
	PaymentCount        int                   `json:"paymentCount"`
	PendingPaymentCount int                   `json:"pendingPaymentCount"`
	Pools               []vipPoolContribution `json:"pools"`
}

func (s workspace) periodPoolContributions(w http.ResponseWriter, r *http.Request) {
	periodID, err := strconv.ParseInt(r.URL.Query().Get("periodId"), 10, 64)
	if err != nil || periodID < 1 {
		respond(w, nil, invalid)
		return
	}
	ctx := r.Context()
	var report vipPoolReport
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		matches, err := readCatalogMatches(ctx, tx, periodID, 0, maxDraftPayments)
		if err != nil {
			return err
		}
		if matches.OrderCount != len(matches.Items) {
			return errDraftLimit
		}
		data, err := loadVIPRuntimeData(ctx, tx, periodID, matches.Items, false)
		if err != nil {
			return err
		}
		applyVIPBenefitQuotes(matches.Items, data)
		report, err = summarizeVIPPools(matches.Items)
		return err
	})
	respond(w, report, err)
}

func summarizeVIPPools(items []catalogPaymentMatch) (vipPoolReport, error) {
	report := vipPoolReport{PaymentCount: len(items), Pools: []vipPoolContribution{}}
	index := make(map[string]int)
	lastPayment := make(map[string]int)
	for i, item := range items {
		if item.Status == "COMPOUND_COMPLETION_PENDING" || item.Status == "COMPOUND_COMPLETED_IN_OTHER_PERIOD" {
			continue
		}
		// A supported sales-only rule has an empty, non-nil benefits slice.
		if item.Status != "MATCHED" || item.BenefitParts == nil {
			report.PendingPaymentCount++
			continue
		}
		pending := false
		for _, part := range item.BenefitParts {
			if part.Kind != "pool" || part.Status == "ZERO" || part.Status == vipCommissionOutsideScope {
				continue
			}
			if part.Status != "CALCULATED" {
				pending = true
				continue
			}
			if part.Amount == 0 {
				continue
			}
			key := part.Unit + ":" + part.Pool
			n, exists := index[key]
			if !exists {
				n = len(report.Pools)
				index[key] = n
				report.Pools = append(report.Pools, vipPoolContribution{Key: key, Name: part.Name})
			}
			pool := &report.Pools[n]
			var err error
			pool.Amount, err = addMinor(pool.Amount, part.Amount)
			if err != nil {
				return vipPoolReport{}, err
			}
			if lastPayment[key] != i+1 {
				pool.PaymentCount++
				lastPayment[key] = i + 1
			}
		}
		if pending {
			report.PendingPaymentCount++
		}
	}
	slices.SortFunc(report.Pools, func(a, b vipPoolContribution) int {
		if byName := cmp.Compare(a.Name, b.Name); byName != 0 {
			return byName
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return report, nil
}
