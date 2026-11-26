package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPracticePeriodImportUsesSourceStrategyAndKeepsExistingMappings(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	if _, err := db.Exec(ctx, `INSERT INTO products(id,code,name,period_mode) OVERRIDING SYSTEM VALUE
	 VALUES(11,'HEALTH','Health','class'),(12,'FAMILY','Family','class');
	 INSERT INTO source_mappings(product_id,source,domain,source_key)
	 VALUES(11,'DCZD','PRACTICE','1'),(12,'DCZD','PRACTICE','4');
	 INSERT INTO practice_rosters(period_key,source_version,source_hash,starts_at,confirmed_at,attendance_count)
	 VALUES('PRACTICE:2:1:101',1,'fixture','2026-11-08 14:30+08','2026-11-12 10:00+08',0);`); err != nil {
		t.Fatal(err)
	}
	run := func(periods ...practicePeriodMapping) (int, error) {
		t.Helper()
		var inserted int
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			var err error
			inserted, err = storePracticePeriodMappings(ctx, tx, periods)
			return err
		})
		return inserted, err
	}
	health := practicePeriodMapping{PeriodKey: "PRACTICE:2:1:101", Name: "Health 157", Ordinal: 157, StartsOn: "2026-11-09", EndsOn: "2026-11-12"}
	family := practicePeriodMapping{PeriodKey: "PRACTICE:2:4:95", Name: "Family 20", Ordinal: 20, StartsOn: "2026-11-08", EndsOn: "2026-11-09"}
	unknown := family
	unknown.PeriodKey = "PRACTICE:2:99:95"
	if _, err := run(health, unknown); !errors.Is(err, errPracticePeriodMappingMissing) {
		t.Fatalf("unknown strategy must fail the batch: %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM periods`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("a rejected batch inserted periods: %d %v", count, err)
	}
	if count, err := run(health, family); err != nil || count != 2 {
		t.Fatalf("first import: %d %v", count, err)
	}
	if count, err := run(health, family); err != nil || count != 0 {
		t.Fatalf("duplicate import: %d %v", count, err)
	}
	var ok bool
	if err := db.QueryRow(ctx, `SELECT
	 EXISTS(SELECT 1 FROM periods WHERE product_id=11 AND ordinal=157 AND source_period_key=$1)
	 AND EXISTS(SELECT 1 FROM periods WHERE product_id=12 AND ordinal=20 AND source_period_key=$2)
	 AND (SELECT count(*) FROM draft_refresh_queue)=2`, health.PeriodKey, family.PeriodKey).Scan(&ok); err != nil || !ok {
		t.Fatalf("both imported periods must be visible under their own product and queued: %v", err)
	}
	rebind := health
	rebind.PeriodKey = "PRACTICE:2:1:102"
	if _, err := run(rebind); !errors.Is(err, errPracticePeriodMappingConflict) {
		t.Fatalf("same product ordinal must retain its original source: %v", err)
	}
	changed := health
	changed.EndsOn = "2026-11-15"
	if _, err := run(changed); !errors.Is(err, errPracticePeriodMappingConflict) {
		t.Fatalf("an import must preserve the original mapping and dates: %v", err)
	}
}
