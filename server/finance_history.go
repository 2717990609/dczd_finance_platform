package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errFinanceHistoryTarget   = errors.New("承接清单与目标数据库、应用范围或产品期次不一致")
	errFinanceHistoryMismatch = errors.New("试算与归档核对金额或笔数不一致")
	errFinanceHistoryPostings = errors.New("试算明细与已核对清单不一致，请重新逐笔核对")
	errFinanceHistoryToken    = errors.New("历史结算承接标识缺失或已变化，请重新试跑")
	errFinanceHistoryTime     = errors.New("归档确认时间不符，请核对原结算并按确认顺序承接")
	errFinanceHistoryEnabled  = errors.New("该产品已开放日常结算，请先核对历史承接范围")
)

const financeHistoryActor = "历史结算承接"

type financeHistoryInput struct {
	Database       string                `json:"database"`
	Schema         string                `json:"schema"`
	ProductCode    string                `json:"productCode"`
	PeriodID       int64                 `json:"periodId"`
	ConfirmedAt    time.Time             `json:"confirmedAt"`
	Expected       *financeHistoryTotals `json:"expected"`
	PostingsSHA256 string                `json:"postingsSha256,omitempty"`
	Token          string                `json:"reviewToken,omitempty"`
}

type financeHistoryTotals struct {
	PerformanceMinor int64 `json:"performanceMinor"`
	AccruedMinor     int64 `json:"accruedMinor"`
	RefundMinor      int64 `json:"refundMinor"`
	CorrectionMinor  int64 `json:"correctionMinor"`
	PerformanceCount int64 `json:"performanceCount"`
	AccrualCount     int64 `json:"accrualCount"`
	RefundCount      int64 `json:"refundCount"`
	CorrectionCount  int64 `json:"correctionCount"`
}

type financeHistoryReview struct {
	PeriodID       int64                `json:"periodId"`
	DraftVersion   int64                `json:"draftVersion"`
	ConfirmedAt    time.Time            `json:"confirmedAt"`
	Totals         financeHistoryTotals `json:"totals"`
	PostingsSHA256 string               `json:"postingsSha256"`
	Token          string               `json:"reviewToken,omitempty"`
	Settlement     *settlement          `json:"settlement,omitempty"`
}

func adoptFinanceHistoryFromFile(ctx context.Context, cfg config, filename, mode string) error {
	var input financeHistoryInput
	if err := readImportFile(filename, &input, 64<<10); err != nil {
		return errors.New("无法读取历史结算承接清单")
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("无法连接财务数据库")
	}
	defer db.Close()
	var details io.Writer
	if mode == "--rows" {
		details = os.Stdout
	}
	result, err := (workspace{db: db}).runFinanceHistory(ctx, input, mode == "--apply", details)
	if err != nil {
		return initializationError("历史结算承接", err)
	}
	if details != nil {
		return nil
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func (s workspace) runFinanceHistory(ctx context.Context, input financeHistoryInput, apply bool, details io.Writer) (financeHistoryReview, error) {
	if !textOK(input.Database, 128) || !textOK(input.Schema, 128) || !textOK(input.ProductCode, 48) || input.PeriodID < 1 ||
		input.ConfirmedAt.IsZero() || input.Expected == nil || strings.HasPrefix(input.Schema, "pg_") || input.Schema == "information_schema" {
		return financeHistoryReview{}, invalid
	}
	input.ConfirmedAt = input.ConfirmedAt.UTC().Truncate(time.Microsecond)
	if apply {
		token, err := hex.DecodeString(input.Token)
		if err != nil || len(token) != sha256.Size {
			return financeHistoryReview{}, errFinanceHistoryToken
		}
	}
	if input.PostingsSHA256 != "" || apply {
		digest, err := hex.DecodeString(input.PostingsSHA256)
		if err != nil || len(digest) != sha256.Size {
			return financeHistoryReview{}, errFinanceHistoryPostings
		}
	}
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if apply {
		options = pgx.TxOptions{IsoLevel: pgx.Serializable}
	}
	for attempt := 0; attempt < 3; attempt++ {
		var result financeHistoryReview
		err := pgx.BeginTxFunc(ctx, s.db, options, func(tx pgx.Tx) error {
			var err error
			result, err = s.reviewFinanceHistory(ctx, tx, input, apply, details)
			return err
		})
		var pgError *pgconn.PgError
		if !apply || !errors.As(err, &pgError) || pgError.Code != "40001" {
			if err == nil && apply {
				err = verifyAdoptedHistory(ctx, s.db.Config().ConnConfig, result)
			}
			return result, err
		}
	}
	return financeHistoryReview{}, conflict
}

func (s workspace) reviewFinanceHistory(ctx context.Context, tx pgx.Tx, input financeHistoryInput, apply bool, details io.Writer) (financeHistoryReview, error) {
	review := financeHistoryReview{PeriodID: input.PeriodID, ConfirmedAt: input.ConfirmedAt}
	var database string
	var schemas []string
	if err := tx.QueryRow(ctx, `SELECT current_database(),current_schemas(false)`).Scan(&database, &schemas); err != nil {
		return review, err
	}
	if input.Database != database || len(schemas) != 1 || schemas[0] != input.Schema {
		return review, errFinanceHistoryTarget
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('TimeZone','UTC',true),set_config('bytea_output','hex',true)`); err != nil {
		return review, err
	}
	scope, err := readSettlementScope(ctx, tx, input.PeriodID, apply)
	if errors.Is(err, pgx.ErrNoRows) {
		return review, errFinanceHistoryTarget
	}
	if err != nil {
		return review, err
	}
	var code string
	var through, now time.Time
	if err := tx.QueryRow(ctx, `SELECT pro.code,(p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai',CURRENT_TIMESTAMP
	 FROM periods p JOIN products pro ON pro.id=p.product_id WHERE p.id=$1`, input.PeriodID).Scan(&code, &through, &now); err != nil {
		return review, err
	}
	if code != input.ProductCode {
		return review, errFinanceHistoryTarget
	}
	if input.ConfirmedAt.Before(through) || input.ConfirmedAt.After(now) {
		return review, errFinanceHistoryTime
	}
	if scope.Version == 0 {
		return review, errSettlementStale
	}
	review.DraftVersion = scope.Version
	stored, err := readFinanceHistoryTotals(ctx, tx, input.PeriodID)
	if err != nil {
		return review, err
	}
	review.Totals = stored
	if stored != *input.Expected {
		return review, errFinanceHistoryMismatch
	}
	if final, err := readSettlement(ctx, tx, input.PeriodID); err == nil {
		if final.ConfirmedBy != financeHistoryActor || !final.ConfirmedAt.Equal(input.ConfirmedAt) || apply && final.reviewToken != financeHistoryReceipt(input) {
			return review, errSettlementClosed
		}
		review.Settlement = &final
		if apply {
			review.PostingsSHA256 = input.PostingsSHA256
		} else if details != nil {
			review.PostingsSHA256, err = financeHistoryPostings(ctx, tx, input.PeriodID, details)
		}
		return review, err
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return review, err
	}
	if scope.Enabled {
		return review, errFinanceHistoryEnabled
	}
	if apply {
		if err := lockDraftInputs(ctx, tx, input.PeriodID); err != nil {
			return review, err
		}
	}
	if err := verifyFinanceHistoryOrder(ctx, tx, input, scope); err != nil {
		return review, err
	}
	current, err := readUnchangedDraft(ctx, tx, input.PeriodID, scope)
	if err != nil {
		return review, err
	}
	if current.blockingIssues() != 0 || scope.IssueCount != 0 {
		return review, errSettlementPending
	}
	if pending, err := draftRefreshPending(ctx, tx, input.PeriodID); err != nil {
		return review, err
	} else if pending {
		return review, errDraftChanged
	}
	// The historical period remains closed to routine confirmation. Reuse the
	// same pool checks while evaluating the archived allocation.
	scope.Enabled = true
	pool, err := reviewPoolSettlement(ctx, tx, input.PeriodID, scope)
	if err != nil {
		return review, err
	}
	if len(pool.Pools) != 0 {
		return review, errSettlementPools
	}
	if pool.LaterPoolActivity {
		return review, errSettlementPoolHistory
	}
	review.PostingsSHA256, err = financeHistoryPostings(ctx, tx, input.PeriodID, details)
	if err != nil {
		return review, err
	}
	if input.PostingsSHA256 != "" && input.PostingsSHA256 != review.PostingsSHA256 {
		return review, errFinanceHistoryPostings
	}
	dbConfig := s.db.Config().ConnConfig
	token, err := financeHistoryToken(ctx, tx, input, scope, pool.Token, fmt.Sprintf("%s:%d", dbConfig.Host, dbConfig.Port))
	if err != nil {
		return review, err
	}
	if input.PostingsSHA256 == "" {
		return review, nil
	}
	if !apply {
		review.Token = token
		return review, nil
	}
	if input.Token != token {
		return review, errFinanceHistoryToken
	}
	final, err := insertSettlement(ctx, tx, input.PeriodID, scope.Version, financeHistoryActor, financeHistoryReceipt(input), &input.ConfirmedAt)
	if err != nil {
		return review, err
	}
	review.Settlement = &final
	return review, nil
}

// The frozen receipt lets the exact manifest retry after a source metadata
// backfill or database restart, without treating a different manifest as a retry.
func financeHistoryReceipt(input financeHistoryInput) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("adopted/1\n"+input.Token+"\n"+input.PostingsSHA256)))
}

func verifyFinanceHistoryOrder(ctx context.Context, tx pgx.Tx, input financeHistoryInput, scope settlementReview) error {
	var later bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM settlements closed JOIN periods p ON p.id=closed.period_id
	 WHERE p.product_id=$1 AND closed.confirmed_at>=$2)`, scope.product, input.ConfirmedAt).Scan(&later); err != nil {
		return err
	}
	if later {
		return errFinanceHistoryTime
	}
	if scope.practiceRefundMonth {
		dependencies, err := readPracticeMonthSettlementDependencies(ctx, tx, input.PeriodID)
		if err != nil {
			return err
		}
		if len(dependencies) != 0 {
			return errPracticeMonthSettlementDependency
		}
	}
	return nil
}

func financeHistoryToken(ctx context.Context, tx pgx.Tx, input financeHistoryInput, scope settlementReview, base, target string) (string, error) {
	if base == "" {
		return "", errSettlementStale
	}
	if scope.isPracticeClass() {
		roster, err := readPracticeClassRosterReview(ctx, tx, input.PeriodID)
		if err != nil {
			return "", err
		}
		if roster.firstVersion != 1 || roster.Version != roster.LatestVersion {
			return "", errDraftChanged
		}
		if input.ConfirmedAt.Before(roster.ConfirmedAt) {
			return "", errFinanceHistoryTime
		}
		base = roster.reviewToken(base)
	}
	var started time.Time
	if err := tx.QueryRow(ctx, `SELECT pg_postmaster_start_time()`).Scan(&started); err != nil {
		return "", err
	}
	value := fmt.Sprintf("history/1\n%q\n%q\n%q\n%d\n%q\n%d\n%s\n%s\n%s", input.Database, input.Schema, input.ProductCode, input.PeriodID,
		target, started.UnixMicro(), input.ConfirmedAt.Format(time.RFC3339Nano), input.PostingsSHA256, base)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value))), nil
}

func verifyAdoptedHistory(ctx context.Context, cfg *pgx.ConnConfig, result financeHistoryReview) error {
	conn, err := pgx.ConnectConfig(ctx, cfg.Copy())
	if err != nil {
		return errors.New("历史结算已提交，暂时无法连接复查，请使用相同清单重试")
	}
	defer conn.Close(ctx)
	err = pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		final, err := readSettlement(ctx, tx, result.PeriodID)
		if err != nil {
			return err
		}
		if final.ID != result.Settlement.ID || final.DraftVersion != result.DraftVersion || !final.ConfirmedAt.Equal(result.ConfirmedAt) ||
			final.ConfirmedBy != result.Settlement.ConfirmedBy || final.reviewToken != result.Settlement.reviewToken {
			return errSettlementStale
		}
		totals, err := readFinanceHistoryTotals(ctx, tx, result.PeriodID)
		if err != nil {
			return err
		}
		if totals != result.Totals {
			return errFinanceHistoryMismatch
		}
		return nil
	})
	if err != nil {
		return errors.New("历史结算已提交，回读结果与提交不一致，请使用相同清单核对")
	}
	return nil
}

func readFinanceHistoryTotals(ctx context.Context, tx pgx.Tx, periodID int64) (financeHistoryTotals, error) {
	var totals financeHistoryTotals
	err := tx.QueryRow(ctx, `WITH performance AS (
	 SELECT coalesce(sum(`+performancePostingMinorSQL+`),0) AS amount,count(*) AS rows
	 FROM calculation_draft_performance a JOIN source_order_versions v ON v.id=a.source_version_id
	 LEFT JOIN source_order_versions prior ON prior.id=a.prior_refund_version_id WHERE a.period_id=$1
	), accrued AS (SELECT coalesce(sum(amount_minor),0) AS amount,count(*) AS rows FROM calculation_draft_lines WHERE period_id=$1),
	 refunds AS (SELECT coalesce(sum(amount_minor),0) AS amount,count(*) AS rows FROM calculation_draft_refunds WHERE period_id=$1),
	 corrections AS (SELECT coalesce(sum(amount_minor),0) AS amount,count(*) AS rows FROM calculation_draft_corrections WHERE period_id=$1)
	 SELECT performance.amount,accrued.amount,refunds.amount,corrections.amount,performance.rows,accrued.rows,refunds.rows,corrections.rows
	 FROM performance,accrued,refunds,corrections`, periodID).Scan(&totals.PerformanceMinor, &totals.AccruedMinor, &totals.RefundMinor,
		&totals.CorrectionMinor, &totals.PerformanceCount, &totals.AccrualCount, &totals.RefundCount, &totals.CorrectionCount)
	return totals, err
}
