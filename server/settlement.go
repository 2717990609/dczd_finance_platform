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
	errSettlementDisabled    = errors.New("该产品尚未开放结算")
	errSettlementOpen        = errors.New("账期尚未结束")
	errSettlementStale       = errors.New("试算已变化，请重新试算并核对")
	errSettlementPending     = errors.New("仍有待核项，请处理后重新试算")
	errSettlementClosed      = errors.New("本账期已确认结算")
	errSettlementPools       = errors.New("团队池还有正余额未分配，或分配草稿需要重新核对")
	errSettlementPoolHistory = errors.New("后续账期已结算此团队池，请核对跨期处理")
)

type confirmSettlementInput struct {
	PeriodID        int64  `json:"periodId"`
	ExpectedVersion int64  `json:"expectedVersion"`
	ReviewToken     string `json:"reviewToken,omitempty"`
}

type settlement struct {
	ID           int64     `json:"id"`
	PeriodID     int64     `json:"periodId"`
	DraftVersion int64     `json:"draftVersion"`
	ConfirmedAt  time.Time `json:"confirmedAt"`
	ConfirmedBy  string    `json:"confirmedBy"`
	reviewToken  string
}

func readSettlement(ctx context.Context, tx pgx.Tx, periodID int64) (settlement, error) {
	var value settlement
	err := tx.QueryRow(ctx, `SELECT id,period_id,draft_version,confirmed_at,confirmed_by,review_token FROM settlements WHERE period_id=$1`,
		periodID).Scan(&value.ID, &value.PeriodID, &value.DraftVersion, &value.ConfirmedAt, &value.ConfirmedBy, &value.reviewToken)
	return value, err
}

func (s workspace) confirmSettlement(ctx context.Context, req confirmSettlementInput, actor string) (settlement, error) {
	if req.PeriodID < 1 || req.ExpectedVersion < 1 || !textOK(actor, 128) || len(req.ReviewToken) > 64 {
		return settlement{}, invalid
	}
	var result settlement
	var err error
	sourceKey := s.dcaiSourceKey()
	// A competing confirmation may win after this transaction obtains its snapshot.
	for attempt := 0; attempt < 3; attempt++ {
		err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
			var applyErr error
			result, applyErr = confirmPeriod(ctx, tx, req, strings.TrimSpace(actor), sourceKey)
			return applyErr
		})
		var dbErr *pgconn.PgError
		if !errors.As(err, &dbErr) || (dbErr.Code != "40001" && dbErr.Code != "23505") {
			return result, err
		}
	}
	return settlement{}, conflict
}

func confirmPeriod(ctx context.Context, tx pgx.Tx, req confirmSettlementInput, actor, sourceKey string) (settlement, error) {
	scope, err := readSettlementScope(ctx, tx, req.PeriodID, true)
	if err != nil {
		return settlement{}, err
	}
	existing, err := readSettlement(ctx, tx, req.PeriodID)
	if err == nil {
		if existing.DraftVersion != req.ExpectedVersion || existing.reviewToken != req.ReviewToken {
			return settlement{}, errSettlementClosed
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return settlement{}, err
	}
	if !scope.Enabled {
		return settlement{}, errSettlementDisabled
	}
	if !scope.Ended {
		return settlement{}, errSettlementOpen
	}
	if scope.mode == "class" {
		return settlement{}, errPracticeSettlementUnavailable
	}
	if scope.Version != req.ExpectedVersion {
		return settlement{}, errSettlementStale
	}
	if scope.IssueCount != 0 {
		return settlement{}, errSettlementPending
	}
	if !scope.catalog && req.ReviewToken != "" {
		return settlement{}, errSettlementStale
	}
	if err := lockSettlementSources(ctx, tx, req.PeriodID); err != nil {
		return settlement{}, err
	}
	if scope.catalog {
		sources, err := readSettlementSourceCoverage(ctx, tx, req.PeriodID, sourceKey)
		if err != nil {
			return settlement{}, err
		}
		if err := settlementCoverageError(sources); err != nil {
			return settlement{}, err
		}
	}
	if err := verifySettlementDraft(ctx, tx, req.PeriodID, scope); err != nil {
		return settlement{}, err
	}
	if scope.catalog {
		review, err := reviewPoolSettlement(ctx, tx, req.PeriodID, scope)
		if err != nil {
			return settlement{}, err
		}
		if len(review.Pools) != 0 {
			return settlement{}, errSettlementPools
		}
		if review.LaterPoolActivity {
			return settlement{}, errSettlementPoolHistory
		}
		if req.ReviewToken == "" || req.ReviewToken != review.Token {
			return settlement{}, errSettlementStale
		}
	}
	result := settlement{PeriodID: req.PeriodID, DraftVersion: req.ExpectedVersion, ConfirmedBy: actor, reviewToken: req.ReviewToken}
	err = tx.QueryRow(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by,review_token) VALUES($1,$2,$3,$4)
		RETURNING id,confirmed_at`, result.PeriodID, result.DraftVersion, result.ConfirmedBy, result.reviewToken).Scan(&result.ID, &result.ConfirmedAt)
	return result, err
}

func verifySettlementDraft(ctx context.Context, tx pgx.Tx, periodID int64, scope settlementReview) error {
	summary := draftSummary{PeriodID: periodID}
	window, rules, err := readDraftScope(ctx, tx, &summary, false)
	if err != nil {
		return err
	}
	if summary.Catalog != scope.catalog {
		return errSettlementStale
	}
	current, digest, err := computeDraft(ctx, tx, summary, window, rules)
	if err != nil {
		return err
	}
	if !bytes.Equal(digest, scope.digest) {
		return errSettlementStale
	}
	if len(current.Issues) != 0 {
		return errSettlementPending
	}
	return nil
}
