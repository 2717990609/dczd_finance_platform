package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
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
	errSettlementUnsupported = errors.New("该产品的来源核验尚未接通结算")
)

type confirmSettlementInput struct {
	PeriodID        int64  `json:"periodId"`
	ExpectedVersion int64  `json:"expectedVersion"`
	ReviewToken     string `json:"reviewToken,omitempty"`
}

func (p *financePrincipal) permitsSettlement(product int64) bool {
	return p != nil && p.Workspace && slices.Contains(p.SettlementProducts, product)
}

func (s workspace) confirmAuthorizedSettlement(ctx context.Context, req confirmSettlementInput) (settlement, error) {
	operator := financeUser(ctx)
	if operator == nil {
		return settlement{}, errFinanceLogin
	}
	return s.confirmSettlement(ctx, req, operator.ID)
}

func lockSettlementOperator(ctx context.Context, tx pgx.Tx, operator *financePrincipal, product int64) error {
	if !operator.permitsSettlement(product) {
		return errFinanceForbidden
	}
	var authorized int
	err := tx.QueryRow(ctx, `SELECT 1 FROM finance_accounts a WHERE a.id=$1::uuid AND a.enabled AND a.workspace_access
	 AND EXISTS(SELECT 1 FROM finance_account_settlement_grants g WHERE g.account_id=a.id AND g.product_id=$2)
	 FOR SHARE OF a`, operator.ID, product).Scan(&authorized)
	if errors.Is(err, pgx.ErrNoRows) {
		return errFinanceForbidden
	}
	if err != nil {
		return err
	}
	return lockFinanceOperatorSession(ctx, tx, operator)
}

type settlement struct {
	ID           int64     `json:"id"`
	PeriodID     int64     `json:"periodId"`
	DraftVersion int64     `json:"draftVersion"`
	ConfirmedAt  time.Time `json:"confirmedAt"`
	ConfirmedBy  string    `json:"confirmedBy"`
	reviewToken  string
}

func readSettlement(ctx context.Context, tx catalogPeriodReader, periodID int64) (settlement, error) {
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
	// A competing confirmation may win after this transaction obtains its snapshot.
	for attempt := 0; attempt < 3; attempt++ {
		err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
			var applyErr error
			result, applyErr = s.confirmPeriod(ctx, tx, req, strings.TrimSpace(actor))
			return applyErr
		})
		var dbErr *pgconn.PgError
		if !errors.As(err, &dbErr) || (dbErr.Code != "40001" && dbErr.Code != "23505") {
			return result, err
		}
	}
	return settlement{}, conflict
}

func (s workspace) confirmPeriod(ctx context.Context, tx pgx.Tx, req confirmSettlementInput, actor string) (settlement, error) {
	scope, err := readSettlementScope(ctx, tx, req.PeriodID, true)
	if err != nil {
		return settlement{}, err
	}
	if operator := financeUser(ctx); operator != nil {
		if err := lockSettlementOperator(ctx, tx, operator, scope.product); err != nil {
			return settlement{}, err
		}
		// The workbench can only confirm scopes with a supported provider coverage check.
		if !scope.requiresReview() {
			return settlement{}, errSettlementUnsupported
		}
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
	if scope.isPracticeClass() && s.orderSourceKey("DCZD") == "" {
		return settlement{}, errPracticeSettlementUnavailable
	}
	if scope.Version != req.ExpectedVersion {
		return settlement{}, errSettlementStale
	}
	if scope.IssueCount != 0 {
		return settlement{}, errSettlementPending
	}
	if !scope.requiresReview() && req.ReviewToken != "" {
		return settlement{}, errSettlementStale
	}
	if err := lockDraftInputs(ctx, tx, req.PeriodID); err != nil {
		return settlement{}, err
	}
	var sources []settlementSource
	if scope.requiresReview() {
		sources, err = s.settlementSources(ctx, tx, req.PeriodID, scope)
		if err != nil {
			return settlement{}, err
		}
		if err := settlementCoverageError(sources); err != nil {
			return settlement{}, err
		}
	}
	if scope.practiceRefundMonth {
		dependencies, err := readPracticeMonthSettlementDependencies(ctx, tx, req.PeriodID)
		if err != nil {
			return settlement{}, err
		}
		if len(dependencies) != 0 {
			return settlement{}, errPracticeMonthSettlementDependency
		}
	}
	if err := verifySettlementDraft(ctx, tx, req.PeriodID, scope); err != nil {
		return settlement{}, err
	}
	if pending, err := draftRefreshPending(ctx, tx, req.PeriodID); err != nil {
		return settlement{}, err
	} else if pending {
		return settlement{}, errDraftChanged
	}
	if scope.requiresReview() {
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
		if review.Token == "" {
			return settlement{}, errSettlementStale
		}
		token := review.Token
		if scope.isPracticeClass() {
			roster, err := readPracticeClassRosterReview(ctx, tx, req.PeriodID)
			if err != nil {
				return settlement{}, err
			}
			token = roster.reviewToken(token)
			if req.ReviewToken != token {
				return settlement{}, errSettlementStale
			}
			if err := s.checkPracticeClassRoster(ctx, &roster); err != nil {
				return settlement{}, err
			}
		}
		if req.ReviewToken != token {
			return settlement{}, errSettlementStale
		}
	}
	if err := verifySettlementSourceDeadline(ctx, tx, sources, s.settlementSyncMaxAge); err != nil {
		return settlement{}, err
	}
	return insertSettlement(ctx, tx, req.PeriodID, req.ExpectedVersion, actor, req.ReviewToken, nil)
}

func insertSettlement(ctx context.Context, tx pgx.Tx, periodID, version int64, actor, token string, confirmedAt *time.Time) (settlement, error) {
	result := settlement{PeriodID: periodID, DraftVersion: version, ConfirmedBy: actor, reviewToken: token}
	err := tx.QueryRow(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by,review_token,confirmed_at)
		VALUES($1,$2,$3,$4,coalesce($5::timestamptz,now())) RETURNING id,confirmed_at`, periodID, version, actor, token, confirmedAt).
		Scan(&result.ID, &result.ConfirmedAt)
	if err != nil {
		return settlement{}, err
	}
	return result, enqueueDraftPeriods(ctx, tx, draftRefreshAfterClose, periodID)
}

func verifySettlementDraft(ctx context.Context, tx pgx.Tx, periodID int64, scope settlementReview) error {
	current, err := readUnchangedDraft(ctx, tx, periodID, scope)
	if err != nil {
		return err
	}
	if current.blockingIssues() != 0 {
		return errSettlementPending
	}
	return nil
}

func readUnchangedDraft(ctx context.Context, tx pgx.Tx, periodID int64, scope settlementReview) (paymentDraft, error) {
	summary := draftSummary{PeriodID: periodID}
	window, rules, err := readDraftScope(ctx, tx, &summary)
	if err != nil {
		return paymentDraft{}, err
	}
	if summary.Catalog != scope.catalog || summary.PracticeRefundMonth != scope.practiceRefundMonth {
		return paymentDraft{}, errSettlementStale
	}
	current, digest, err := computeDraft(ctx, tx, summary, window, rules, nil)
	if err != nil {
		return paymentDraft{}, err
	}
	if !bytes.Equal(digest, scope.digest) {
		return paymentDraft{}, errSettlementStale
	}
	return current, nil
}
