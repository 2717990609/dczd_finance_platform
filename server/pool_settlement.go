package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
)

type settlementPoolGap struct {
	poolScope
	Name        string `json:"name"`
	Unallocated int64  `json:"unallocatedMinor"`
	Stale       bool   `json:"stale"`
}

type settlementReview struct {
	Version           int64               `json:"version"`
	Enabled           bool                `json:"enabled"`
	Ended             bool                `json:"ended"`
	IssueCount        int                 `json:"issueCount"`
	DraftStale        bool                `json:"draftStale"`
	LaterPoolActivity bool                `json:"laterPoolActivity"`
	Pools             []settlementPoolGap `json:"pools"`
	Sources           []settlementSource  `json:"sources,omitempty"`
	Token             string              `json:"reviewToken,omitempty"`
	Settlement        *settlement         `json:"settlement,omitempty"`
	mode              string
	catalog           bool
	digest            []byte
}

func readSettlementScope(ctx context.Context, tx pgx.Tx, periodID int64, lock bool) (settlementReview, error) {
	query := `SELECT pro.settlement_enabled,
	 ((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')<=CURRENT_TIMESTAMP,pro.period_mode,
	 coalesce(d.version,0),coalesce(d.input_digest,''::bytea),coalesce(d.issue_count,0),
	 coalesce(d.rule_id IS NULL AND d.catalog_refunds_ready,false)
	 FROM periods p JOIN products pro ON pro.id=p.product_id LEFT JOIN calculation_drafts d ON d.period_id=p.id
	 WHERE p.id=$1`
	if lock {
		query += ` FOR UPDATE OF p`
	}
	review := settlementReview{Pools: []settlementPoolGap{}}
	err := tx.QueryRow(ctx, query, periodID).Scan(&review.Enabled, &review.Ended, &review.mode,
		&review.Version, &review.digest, &review.IssueCount, &review.catalog)
	return review, err
}

func (s workspace) getSettlementReview(w http.ResponseWriter, r *http.Request) {
	periodID, version, _, err := draftReportPage(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	ctx := r.Context()
	var review settlementReview
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		review, err = readSettlementScope(ctx, tx, periodID, false)
		if err != nil {
			return err
		}
		if review.Version != version || !review.catalog {
			return errSettlementStale
		}
		settled, err := readSettlement(ctx, tx, periodID)
		if err == nil {
			review.Settlement = &settled
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		review.Sources, err = readSettlementSourceCoverage(ctx, tx, periodID, s.dcaiSourceKey())
		if err != nil {
			return err
		}
		review, err = reviewPoolSettlement(ctx, tx, periodID, review)
		if settlementCoverageError(review.Sources) != nil {
			review.Token = ""
		}
		if err != nil || review.Token == "" {
			return err
		}
		err = verifySettlementDraft(ctx, tx, periodID, review)
		if errors.Is(err, errSettlementStale) || errors.Is(err, errSettlementPending) {
			review.DraftStale, review.Token = true, ""
			return nil
		}
		return err
	})
	respond(w, review, err)
}

const settlementPoolFundingSQL = `SELECT a.id,a.revision,a.unit_id::text,a.item_key,a.scenario_key,
 coalesce(s.person_id,0),s.amount_minor,f.source_line_id,f.amount_minor
 FROM monthly_pool_allocations a JOIN monthly_pool_allocation_shares s ON s.allocation_id=a.id
 JOIN monthly_pool_allocation_funding f ON f.share_id=s.id
 WHERE a.period_id=$1 ORDER BY a.id,s.id,f.source_line_id`

// A historical close can only affect later periods on an account whose movements
// it changes. Unrelated products and pools can still settle independently.
const laterSettlementPoolSQL = `SELECT EXISTS (
 SELECT 1 FROM periods target
 JOIN periods later ON later.product_id=target.product_id AND later.ordinal>target.ordinal
 JOIN settlements s ON s.period_id=later.id
 JOIN pool_account_activity a ON a.period_id=later.id
 JOIN jsonb_to_recordset($2::jsonb) q("unitId" uuid,"itemKey" text,scenario text)
  ON (a.unit_id,a.item_key,a.scenario_key)=(q."unitId",q."itemKey",q.scenario)
 WHERE target.id=$1
)`

func reviewPoolSettlement(ctx context.Context, tx pgx.Tx, periodID int64, review settlementReview) (settlementReview, error) {
	var basis string
	if err := tx.QueryRow(ctx, poolAllocationBasisSQL+"$1", periodID).Scan(&basis); err != nil {
		return review, err
	}
	states, err := readPoolStages(ctx, tx, periodID)
	if err != nil {
		return review, err
	}
	if err = readPoolSavedPlans(ctx, tx, periodID, states); err != nil {
		return review, err
	}
	personal := map[poolScope]*poolStage{}
	stale := map[poolScope]bool{}
	for key, stage := range states {
		if len(stage.Shares) == 0 {
			continue
		}
		if !stage.PlanCurrent {
			stale[key] = true
		} else if slices.ContainsFunc(stage.Shares, func(s poolAllocationShare) bool { return s.PersonID != 0 }) {
			personal[key] = stage
		}
	}
	if err = poolStageMembers(ctx, tx, personal); err != nil {
		return review, err
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%d:%d:%x:%s\n", periodID, review.Version, review.digest, basis)
	rows, err := tx.Query(ctx, settlementPoolFundingSQL, periodID)
	if err != nil {
		return review, err
	}
	var key poolScope
	var allocation, revision, person, share, line, amount int64
	_, err = pgx.ForEachRow(rows, []any{&allocation, &revision, &key.Unit, &key.Item, &key.Scenario, &person, &share, &line, &amount}, func() error {
		stage := states[key]
		if stale[key] {
			return nil
		}
		i, found := stage.indices[line]
		if !found || person != 0 && !slices.Contains(stage.lots[i].Eligible, person) || stage.distribute(line, amount) != nil {
			stale[key] = true
			return nil
		}
		fmt.Fprintf(hash, "%d:%d:%q:%q:%q:%d:%d:%d:%d\n", allocation, revision, key.Unit, key.Item, key.Scenario, person, share, line, amount)
		return nil
	})
	if err != nil {
		return review, err
	}
	var affected []poolScope
	for key, stage := range states {
		unallocated := stage.balance
		if stale[key] {
			unallocated = stage.Balance
		}
		if unallocated > 0 || stale[key] {
			review.Pools = append(review.Pools, settlementPoolGap{poolScope: key, Name: stage.Name, Unallocated: max(0, unallocated), Stale: stale[key]})
		}
		if stage.current || stage.Balance != stage.balance {
			affected = append(affected, key)
		}
	}
	slices.SortFunc(review.Pools, func(a, b settlementPoolGap) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Unit, b.Unit), cmp.Compare(a.Scenario, b.Scenario), cmp.Compare(a.Item, b.Item))
	})
	if len(affected) > 0 {
		if err = tx.QueryRow(ctx, laterSettlementPoolSQL, periodID, affected).Scan(&review.LaterPoolActivity); err != nil {
			return review, err
		}
	}
	if review.Enabled && review.Ended && review.IssueCount == 0 && len(review.Pools) == 0 && !review.LaterPoolActivity {
		review.Token = hex.EncodeToString(hash.Sum(nil))
	}
	return review, nil
}
