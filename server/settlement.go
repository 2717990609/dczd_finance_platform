package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	errSettlementDisabled = errors.New("该产品尚未开放结算")
	errSettlementOpen     = errors.New("账期尚未结束")
	errSettlementStale    = errors.New("试算已变化，请重新试算并核对")
	errSettlementPending  = errors.New("仍有待核项，请处理后重新试算")
	errSettlementClosed   = errors.New("本账期已确认结算")
)

type confirmSettlementInput struct {
	PeriodID        int64 `json:"periodId"`
	ExpectedVersion int64 `json:"expectedVersion"`
}

type settlement struct {
	ID           int64     `json:"id"`
	PeriodID     int64     `json:"periodId"`
	DraftVersion int64     `json:"draftVersion"`
	ConfirmedAt  time.Time `json:"confirmedAt"`
	ConfirmedBy  string    `json:"confirmedBy"`
}

func readSettlement(ctx context.Context, tx pgx.Tx, periodID int64) (settlement, error) {
	var value settlement
	err := tx.QueryRow(ctx, `SELECT id,period_id,draft_version,confirmed_at,confirmed_by FROM settlements WHERE period_id=$1`,
		periodID).Scan(&value.ID, &value.PeriodID, &value.DraftVersion, &value.ConfirmedAt, &value.ConfirmedBy)
	return value, err
}

func (s workspace) confirmSettlement(ctx context.Context, req confirmSettlementInput, actor string) (settlement, error) {
	if req.PeriodID < 1 || req.ExpectedVersion < 1 || !textOK(actor, 128) {
		return settlement{}, invalid
	}
	var result settlement
	var err error
	// A competing confirmation may win after this transaction obtains its snapshot.
	for attempt := 0; attempt < 3; attempt++ {
		err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
			var applyErr error
			result, applyErr = confirmPeriod(ctx, tx, req, strings.TrimSpace(actor))
			return applyErr
		})
		var dbErr *pgconn.PgError
		if !errors.As(err, &dbErr) || (dbErr.Code != "40001" && dbErr.Code != "23505") {
			return result, err
		}
	}
	return settlement{}, conflict
}

func confirmPeriod(ctx context.Context, tx pgx.Tx, req confirmSettlementInput, actor string) (settlement, error) {
	var enabled, ended bool
	var mode string
	err := tx.QueryRow(ctx, `SELECT pro.settlement_enabled,
		((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')<=CURRENT_TIMESTAMP,pro.period_mode
		FROM periods p JOIN products pro ON pro.id=p.product_id WHERE p.id=$1 FOR UPDATE OF p`, req.PeriodID).Scan(&enabled, &ended, &mode)
	if err != nil {
		return settlement{}, err
	}
	existing, err := readSettlement(ctx, tx, req.PeriodID)
	if err == nil {
		if existing.DraftVersion != req.ExpectedVersion {
			return settlement{}, errSettlementClosed
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return settlement{}, err
	}
	if !enabled {
		return settlement{}, errSettlementDisabled
	}
	if !ended {
		return settlement{}, errSettlementOpen
	}
	if mode == "class" {
		return settlement{}, errPracticeSettlementUnavailable
	}
	var draftVersion int64
	var storedDigest []byte
	var issueCount int
	err = tx.QueryRow(ctx, `SELECT version,input_digest,issue_count FROM calculation_drafts WHERE period_id=$1`, req.PeriodID).
		Scan(&draftVersion, &storedDigest, &issueCount)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && draftVersion != req.ExpectedVersion {
		return settlement{}, errSettlementStale
	}
	if err != nil {
		return settlement{}, err
	}
	if issueCount != 0 {
		return settlement{}, errSettlementPending
	}
	summary := draftSummary{PeriodID: req.PeriodID}
	window, rules, err := readDraftScope(ctx, tx, &summary)
	if err != nil {
		return settlement{}, err
	}
	if summary.Catalog {
		return settlement{}, errSettlementDisabled
	}
	current, digest, err := computeDraft(ctx, tx, summary, window, rules)
	if err != nil {
		return settlement{}, err
	}
	if !bytes.Equal(digest, storedDigest) {
		return settlement{}, errSettlementStale
	}
	if len(current.Issues) != 0 {
		return settlement{}, errSettlementPending
	}
	result := settlement{PeriodID: req.PeriodID, DraftVersion: req.ExpectedVersion, ConfirmedBy: actor}
	err = tx.QueryRow(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,$3)
		RETURNING id,confirmed_at`, result.PeriodID, result.DraftVersion, result.ConfirmedBy).Scan(&result.ID, &result.ConfirmedAt)
	return result, err
}
