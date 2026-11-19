package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type financeRuleGrant struct {
	ProductID int64 `json:"productId"`
	Publish   bool  `json:"publish"`
}

type financeAccountConfig struct {
	Login             string             `json:"login"`
	PersonID          int64              `json:"personId"`
	Enabled           bool               `json:"enabled"`
	Workspace         bool               `json:"workspace"`
	OrganizationWrite bool               `json:"organizationWrite"`
	Rules             []financeRuleGrant `json:"rules"`
	Settlements       []int64            `json:"settlements"`
}

var financeLoginPattern = regexp.MustCompile(`^[a-z0-9._@+-]{1,80}$`)

func normalizeFinanceLogin(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (s workspace) saveFinanceAccount(ctx context.Context, input financeAccountConfig, password string) error {
	login := normalizeFinanceLogin(input.Login)
	if input.PersonID < 1 || !financeLoginPattern.MatchString(login) {
		return invalid
	}
	var hash *string
	if password != "" {
		if len(password) < 12 || len(password) > 72 {
			return errors.New("账号密码须为 12 至 72 字节")
		}
		raw, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		hashed := string(raw)
		hash = &hashed
	}
	seen := make(map[int64]bool, len(input.Rules))
	for _, grant := range input.Rules {
		if grant.ProductID < 1 || seen[grant.ProductID] {
			return invalid
		}
		seen[grant.ProductID] = true
	}
	if !input.Workspace && (len(input.Settlements) != 0 || input.OrganizationWrite) {
		return invalid
	}
	clear(seen)
	for _, product := range input.Settlements {
		if product < 1 || seen[product] {
			return invalid
		}
		seen[product] = true
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var accountID string
		if hash != nil {
			err := tx.QueryRow(ctx, `INSERT INTO finance_accounts (login,person_id,password_hash,enabled,workspace_access,organization_write)
			 VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT(person_id) DO UPDATE SET
			  login=excluded.login,password_hash=excluded.password_hash,enabled=excluded.enabled,
			  workspace_access=excluded.workspace_access,organization_write=excluded.organization_write,failures=0,retry_after=NULL RETURNING id::text`,
				login, input.PersonID, hash, input.Enabled, input.Workspace, input.OrganizationWrite).Scan(&accountID)
			if err != nil {
				return err
			}
		} else {
			err := tx.QueryRow(ctx, `UPDATE finance_accounts SET login=$2,enabled=$3,workspace_access=$4,organization_write=$5,failures=0,retry_after=NULL
			 WHERE person_id=$1 RETURNING id::text`, input.PersonID, login, input.Enabled, input.Workspace, input.OrganizationWrite).Scan(&accountID)
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("首次登记须从标准输入提供密码")
			}
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM finance_account_rule_grants WHERE account_id=$1::uuid`, accountID); err != nil {
			return err
		}
		for _, grant := range input.Rules {
			if _, err := tx.Exec(ctx, `INSERT INTO finance_account_rule_grants(account_id,product_id,can_publish)
			 VALUES($1::uuid,$2,$3)`, accountID, grant.ProductID, grant.Publish); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM finance_account_settlement_grants WHERE account_id=$1::uuid`, accountID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO finance_account_settlement_grants(account_id,product_id)
		 SELECT $1::uuid,product_id FROM unnest($2::bigint[]) AS scope(product_id)`, accountID, input.Settlements); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM finance_account_sessions WHERE account_id=$1::uuid`, accountID)
		return err
	})
}

func configureFinanceAccount(ctx context.Context, cfg config, file string) error {
	var input financeAccountConfig
	if err := readImportFile(file, &input, 64<<10); err != nil {
		return err
	}
	info, err := os.Stdin.Stat()
	if err != nil {
		return errors.New("无法读取标准输入")
	}
	var password string
	if info.Mode()&os.ModeCharDevice == 0 {
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 75))
		if err != nil {
			return errors.New("无法读取密码")
		}
		password = strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("无法连接财务数据库")
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		return err
	}
	if err := (workspace{db: db}).saveFinanceAccount(ctx, input, password); err != nil {
		return err
	}
	fmt.Println("账号和权限已保存")
	return nil
}
