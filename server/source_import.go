package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sourceFactFile struct {
	Source string            `json:"source"`
	Facts  []json.RawMessage `json:"facts"`
}

func importSourceFactFile(ctx context.Context, cfg config, filename string) error {
	if !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return errors.New("order import requires a dedicated development database")
	}
	var input sourceFactFile
	if err := readImportFile(filename, &input, 32<<20); err != nil {
		return err
	}
	if !validSource(input.Source) || len(input.Facts) == 0 {
		return invalid
	}
	facts := make([]sourceFact, 0, len(input.Facts))
	for i, raw := range input.Facts {
		var wire struct {
			Order struct {
				ID string `json:"source_order_id"`
			} `json:"order"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil || !textOK(wire.Order.ID, 128) {
			return fmt.Errorf("invalid order identity at item %d", i+1)
		}
		fact, err := normalizeFact(input.Source, wire.Order.ID, raw)
		if err != nil {
			return fmt.Errorf("invalid order fact at item %d", i+1)
		}
		facts = append(facts, fact)
	}

	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("could not initialize the development database")
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		return err
	}
	var inserted int64
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		count, err := storeSourceFacts(ctx, tx, facts)
		inserted = count
		return err
	})
	if err != nil {
		return err
	}
	fmt.Printf("Checked %d order facts; created %d source versions\n", len(facts), inserted)
	return nil
}
