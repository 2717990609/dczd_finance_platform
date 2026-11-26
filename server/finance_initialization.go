package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	errFinanceInitializationTarget   = errors.New("初始化清单的目标库和应用 schema 须与实际连接一致")
	errFinanceInitializationExisting = errors.New("目标 schema 已有数据库对象或已使用其他清单初始化")
	errFinanceInitializationInput    = errors.New("请核对初始化清单字段及数据")
)

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type financeInitialization struct {
	Database         string               `json:"database"`
	Schema           string               `json:"schema,omitempty"`
	ProductConfig    productConfigFile    `json:"productConfig"`
	Organization     json.RawMessage      `json:"organization"`
	SalesContext     json.RawMessage      `json:"salesContext,omitempty"`
	PracticeOpenings []financeOpeningSeed `json:"practiceOpenings,omitempty"`
}

type financeOpeningSeed struct {
	ProductCode        string                       `json:"productCode"`
	SourceSettlementID string                       `json:"sourceSettlementId"`
	ThroughAt          time.Time                    `json:"throughAt"`
	People             []practiceCountOpeningPerson `json:"people"`
}

func initializeFinanceFromFile(ctx context.Context, cfg config, filename string, apply bool) error {
	file, err := os.Open(filename)
	if err != nil {
		return errors.New("无法打开初始化清单")
	}
	defer file.Close()
	const limit = 64 << 20
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) > limit {
		return errors.New("无法读取初始化清单或文件超过大小限制")
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("无法连接财务数据库")
	}
	defer db.Close()

	same, err := initializeFinance(ctx, db, raw, apply)
	if err != nil {
		return err
	}
	switch {
	case same:
		fmt.Println("已使用此清单初始化")
	case apply:
		fmt.Println("财务数据库初始化完成")
	default:
		fmt.Println("初始化试跑通过，已回滚")
	}
	return nil
}

// The return value reports an existing initialization with the same input.
func initializeFinance(ctx context.Context, db *pgxpool.Pool, raw []byte, apply bool) (bool, error) {
	var input financeInitialization
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !errors.Is(decoder.Decode(new(json.RawMessage)), io.EOF) {
		return false, errFinanceInitializationInput
	}
	digest := sha256.Sum256(raw)
	// Waiting callers must read the state committed by the initializer holding the lock.
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, errors.New("无法连接财务数据库")
	}
	defer tx.Rollback(ctx)

	var database string
	var schemas []string
	if err := tx.QueryRow(ctx, "SELECT current_database(),current_schemas(false)").Scan(&database, &schemas); err != nil {
		return false, initializationError("读取目标库", err)
	}
	expectedSchema := input.Schema
	if expectedSchema == "" {
		expectedSchema = "public"
	}
	if input.Database != database || len(schemas) != 1 || schemas[0] != expectedSchema ||
		expectedSchema == "information_schema" || strings.HasPrefix(expectedSchema, "pg_") {
		return false, errFinanceInitializationTarget
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('search_path',quote_ident($1),true)", expectedSchema); err != nil {
		return false, initializationError("锁定应用 schema", err)
	}
	if _, err := tx.Exec(ctx, schemaLockSQL); err != nil {
		return false, initializationError("锁定初始化范围", err)
	}
	var initialized, occupied bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('finance_initialization') IS NOT NULL,
	 EXISTS(SELECT 1 FROM pg_depend d JOIN pg_namespace n ON n.oid=d.refobjid
	  WHERE d.refclassid='pg_namespace'::regclass AND n.nspname=current_schema())`).Scan(&initialized, &occupied); err != nil {
		return false, initializationError("校验应用 schema", err)
	}
	if initialized {
		var previous []byte
		err := tx.QueryRow(ctx, "SELECT file_digest FROM finance_initialization WHERE singleton").Scan(&previous)
		if err == nil && bytes.Equal(digest[:], previous) {
			return true, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, initializationError("读取初始化状态", err)
		}
	}
	if occupied {
		return false, errFinanceInitializationExisting
	}
	if err := applyFinanceSchema(ctx, tx); err != nil {
		return false, initializationError("创建财务数据结构", err)
	}
	if err := storeOrganizationSnapshot(ctx, tx, input.Organization); err != nil {
		return false, initializationError("初始化组织与任职", err)
	}
	if err := storeProductConfig(ctx, tx, input.ProductConfig); err != nil {
		return false, initializationError("初始化产品与规则", err)
	}
	if len(input.SalesContext) > 0 {
		if err := storeVIPSalesContext(ctx, tx, input.SalesContext); err != nil {
			return false, initializationError("初始化销售归属", err)
		}
	}
	for _, opening := range input.PracticeOpenings {
		var productID int64
		err := tx.QueryRow(ctx, "SELECT id FROM products WHERE code=$1", opening.ProductCode).Scan(&productID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, errPracticeOpeningScope
		}
		if err != nil {
			return false, initializationError("读取期初所属产品", err)
		}
		if _, _, err := storePracticeCountOpening(ctx, tx, practiceCountOpeningFile{
			ProductID: productID, SourceSettlementID: opening.SourceSettlementID, ThroughAt: opening.ThroughAt, People: opening.People,
		}); err != nil {
			return false, initializationError("初始化实战班累计期初", err)
		}
	}
	if _, err := tx.Exec(ctx, "INSERT INTO finance_initialization(file_digest) VALUES($1)", digest[:]); err != nil {
		return false, initializationError("登记初始化状态", err)
	}
	if apply {
		return false, initializationError("提交初始化", tx.Commit(ctx))
	}
	return false, initializationError("回滚初始化试跑", tx.Rollback(ctx))
}

func initializationError(step string, err error) error {
	if err == nil {
		return nil
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		return fmt.Errorf("%s失败（SQLSTATE %s）", step, pgError.Code)
	}
	var parseError *time.ParseError
	if errors.As(err, &parseError) {
		return fmt.Errorf("%s失败，时间格式无效", step)
	}
	return fmt.Errorf("%s失败：%w", step, err)
}
