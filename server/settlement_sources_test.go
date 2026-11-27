package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestSettlementSourceClockTolerance(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	var now time.Time
	if err := s.db.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	month := now.In(shanghai).AddDate(0, -2, 0).Format("2006-01")
	origin := "fixture"
	cycleStartDay := 25
	config := productConfigFile{Environment: "test", CapturedAt: now, Products: []productConfigEntry{{
		SourceID: "70000000-0000-4000-8000-000000000001", Code: "CLOCK_TEST", Name: "健康大使",
		Project: "DCAI", Source: "DCAI", SourceKey: "clock-test", PeriodMode: "calendar", CycleStartDay: &cycleStartDay,
		BusinessRoutes: []catalogBusinessRoute{{
			SourceID: "70000000-0000-4000-8000-000000000002", Source: "DCAI", Domain: "HEALTH_IDENTITY",
			Entity: "HEALTH_IDENTITY_ORDER", RecordTable: "ai_health_ambassador_order", SourceProductCode: "HEALTH_AMBASSADOR", BusinessKey: "dcai:health-ambassador:new",
			Classifier: "dcai.health-identity.v1", SKU: "HEALTH_AMBASSADOR", Label: "健康大使", Version: 1, From: now.AddDate(-1, 0, 0),
		}},
	}}}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, config) }); err != nil {
		t.Fatal(err)
	}
	var productID int64
	if err := s.db.QueryRow(ctx, "SELECT id FROM products WHERE code='CLOCK_TEST'").Scan(&productID); err != nil {
		t.Fatal(err)
	}
	created, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: month, SettlementMonth: month})
	if err != nil {
		t.Fatal(err)
	}
	periodID := created.(map[string]int64)["id"]
	var cutoff time.Time
	if err := s.db.QueryRow(ctx, "SELECT (ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai' FROM periods WHERE id=$1", periodID).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE source_write_state SET provider_key=$1 WHERE source='DCAI'", origin); err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		name      string
		lastSync  time.Time
		clockSkew time.Duration
		status    string
		wantErr   error
	}{
		{"small difference after historical cutoff", now, 134 * time.Millisecond, "READY", nil},
		{"difference at the tolerance", now, time.Second, "READY", nil},
		{"small difference near cutoff", cutoff.Add(500 * time.Millisecond), 134 * time.Millisecond, "CLOCK_GAP", errSettlementSourceClock},
		{"completion just before the cutoff margin", cutoff.Add(time.Hour - time.Microsecond), 134 * time.Millisecond, "CLOCK_GAP", errSettlementSourceClock},
		{"completion at the cutoff margin is still checked for freshness", cutoff.Add(time.Hour), 134 * time.Millisecond, "STALE", errSettlementSourceStale},
		{"watermark crosses cutoff while completion is earlier", cutoff.Add(-100 * time.Millisecond), 134 * time.Millisecond, "CLOCK_GAP", errSettlementSourceClock},
		{"watermark does not reach cutoff", cutoff.Add(-time.Second), 134 * time.Millisecond, "OUTSIDE_RANGE", errSettlementSourceCoverage},
		{"difference above the tolerance", now, time.Second + time.Microsecond, "CLOCK_GAP", errSettlementSourceClock},
		{"stale checkpoint with small difference", now.Add(-2 * time.Hour), 134 * time.Millisecond, "STALE", errSettlementSourceStale},
		{"normal sync", now, -134 * time.Millisecond, "READY", nil},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,completed_through,last_success_at,next_updated_at,next_id)
				 VALUES($1,$2,$3,$4,$5,$4,'0') ON CONFLICT(stream) DO UPDATE
				 SET completed_through=excluded.completed_through,last_success_at=excluded.last_success_at`,
					dcaiHealthStream, origin, cutoff.AddDate(0, -2, 0), tc.lastSync.Add(tc.clockSkew), tc.lastSync); err != nil {
					return err
				}
				sources, err := readSettlementSourceCoverage(ctx, tx, periodID, origin, time.Hour, settlementSourceCoverageSQL)
				if err != nil {
					return err
				}
				if len(sources) != 1 || sources[0].Status != tc.status {
					t.Fatalf("want %s, got %+v", tc.status, sources)
				}
				if wantWarning := tc.status == "READY" && tc.clockSkew > 0; sources[0].ClockSkewWarning != wantWarning {
					t.Fatalf("want clock skew warning %t, got %+v", wantWarning, sources[0])
				}
				if err := settlementCoverageError(sources); !errors.Is(err, tc.wantErr) {
					t.Fatalf("want %v, got %v", tc.wantErr, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
