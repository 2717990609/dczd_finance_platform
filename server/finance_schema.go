package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var financeSchemaRevision = sha256.Sum256([]byte(schema))

func financeSchemaApplied(ctx context.Context, q syncQueryer) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT to_regclass('finance_schema_revisions') IS NOT NULL").Scan(&exists); err != nil || !exists {
		return false, err
	}
	var current bool
	err := q.QueryRow(ctx, "SELECT revision=$1 FROM finance_schema_revisions WHERE singleton", financeSchemaRevision[:]).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return current, err
}

func applyFinanceSchema(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, schema); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS finance_schema_revisions(
	 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
	 revision bytea NOT NULL CHECK(octet_length(revision)=32),
	 applied_at timestamptz NOT NULL DEFAULT now())`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO finance_schema_revisions(singleton,revision) VALUES(true,$1)
	 ON CONFLICT(singleton) DO UPDATE SET revision=excluded.revision,applied_at=now()`, financeSchemaRevision[:])
	return err
}

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	applied, err := financeSchemaApplied(ctx, db)
	if err != nil || applied {
		return err
	}
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, schemaLockSQL); err != nil {
			return err
		}
		applied, err := financeSchemaApplied(ctx, tx)
		if err != nil || applied {
			return err
		}
		return applyFinanceSchema(ctx, tx)
	})
}

func prepareFinanceSchema(ctx context.Context, db *pgxpool.Pool) error {
	switch os.Getenv("FINANCE_MIGRATION_MODE") {
	case "", "apply":
		return migrate(ctx, db)
	case "verify":
		applied, err := financeSchemaApplied(ctx, db)
		if err != nil {
			return err
		}
		if !applied {
			return errors.New("finance schema revision differs from this build; run migrate-finance")
		}
		return nil
	default:
		return errors.New("FINANCE_MIGRATION_MODE must be apply or verify")
	}
}

func migrateFinanceCommand(ctx context.Context, cfg config) error {
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("could not initialize the database pool")
	}
	defer db.Close()
	return migrate(ctx, db)
}
