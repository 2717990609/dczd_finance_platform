package main

import (
	"context"
	"testing"
	"time"
)

func TestMigrateSkipsAppliedSchemaWithConcurrentReader(t *testing.T) {
	db := integrationDB(t)
	ctx := context.Background()
	reader, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback(ctx)
	if _, err := reader.Exec(ctx, "SELECT count(*) FROM source_order_versions"); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	if err := migrate(short, db); err != nil {
		t.Fatalf("an unchanged schema must start while an order read is open: %v", err)
	}
}
