package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errPracticeScopedFactsMissing    = errors.New("有期次的关联付款或签到事实尚未到齐，请稍后继续同步")
	errPracticePeriodSyncUnavailable = errors.New("请先完成实战班订单的初始同步，再按期次补拉")
)

func selectPracticeFacts(snapshot practiceRegistrationSnapshot) ([]practiceFactSelection, map[string]bool) {
	selected := make(map[string]practiceFactSelection)
	required := make(map[string]bool)
	add := func(kind, rawID string, mustExist bool) {
		id := positiveSourceID(rawID)
		if id == 0 {
			return
		}
		fact := practiceFactSelection{Kind: kind, ID: id}
		key := fact.recordKey()
		selected[key] = fact
		if mustExist {
			required[key] = true
		}
	}
	for _, row := range snapshot.Registrations {
		if row.OrderID != nil {
			add("ORDER_PAID", *row.OrderID, false)
		}
	}
	for _, enrollment := range snapshot.Enrollments {
		if !enrollment.CommissionEligible {
			continue
		}
		add("DELIVERY_COMPLETED", enrollment.SourceEnrollmentID, true)
		if enrollment.PaidOrderIDs != nil {
			for _, id := range *enrollment.PaidOrderIDs {
				add("ORDER_PAID", id, true)
			}
		} else if enrollment.SourceOrderID != nil {
			add("ORDER_PAID", *enrollment.SourceOrderID, true)
		}
	}
	facts := make([]practiceFactSelection, 0, len(selected))
	for _, item := range selected {
		facts = append(facts, item)
	}
	slices.SortFunc(facts, func(a, b practiceFactSelection) int {
		if byKind := strings.Compare(a.Kind, b.Kind); byKind != 0 {
			return byKind
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return facts, required
}

func readPracticeScopedFacts(ctx context.Context, conn *pgxpool.Conn, client orderFeedClient, selections []practiceFactSelection, required map[string]bool) ([]sourceFact, int, error) {
	var facts []sourceFact
	var missingRequired int
	for batch := range slices.Chunk(selections, orderPageSize) {
		keys := make([]string, 0, len(batch))
		orderIDs := make([]string, 0, len(batch))
		for _, selection := range batch {
			keys = append(keys, selection.recordKey())
			if selection.Kind == "ORDER_PAID" {
				orderIDs = append(orderIDs, strconv.FormatInt(selection.ID, 10))
			}
		}
		rows, err := conn.Query(ctx, `
		 SELECT DISTINCT ON (record_key) record_key,version FROM (
		  SELECT record_key,version,updated_at,id FROM source_order_versions
		  WHERE source='DCZD' AND kind='ORDER_PAID' AND order_id=ANY($1::text[]) AND record_key=ANY($2::text[])
		  UNION ALL
		  SELECT record_key,version,updated_at,id FROM source_order_versions
		  WHERE source='DCZD' AND kind='DELIVERY_COMPLETED' AND record_key=ANY($2::text[])
		 ) versions ORDER BY record_key,updated_at DESC,id DESC`, orderIDs, keys)
		if err != nil {
			return nil, 0, err
		}
		known := make(map[string]string, len(batch))
		var key, version string
		if _, err := pgx.ForEachRow(rows, []any{&key, &version}, func() error {
			known[key] = version
			return nil
		}); err != nil {
			return nil, 0, err
		}
		for i := range batch {
			batch[i].KnownVersion = known[batch[i].recordKey()]
		}
		changes, missing, err := client.practiceScopedFacts(ctx, batch)
		if err != nil {
			return nil, 0, err
		}
		facts = append(facts, changes...)
		for _, item := range missing {
			if required[item.recordKey()] {
				missingRequired++
			}
		}
	}
	return facts, missingRequired, nil
}

func runPracticeScopedOrderSync(ctx context.Context, conn *pgxpool.Conn, client orderFeedClient, jobID, periodID int64, coveredThrough time.Time) (int64, error) {
	periods, err := readPracticeSyncPeriods(ctx, conn, jobID, periodID)
	if err != nil {
		return 0, err
	}
	var total int64
	var incomplete bool
	for _, period := range periods {
		snapshot, err := client.practiceRegistrations(ctx, period.key)
		if err != nil {
			return total, fmt.Errorf("%w: %w", errPracticeRegistrationSync, err)
		}
		items, required := selectPracticeFacts(snapshot)
		facts, missing, err := readPracticeScopedFacts(ctx, conn, client, items, required)
		if err != nil {
			return total, err
		}
		incomplete = incomplete || missing > 0
		var inserted int64
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if err := lockOrderProvider(ctx, tx, "DCZD", client.sourceKey()); err != nil {
				return err
			}
			eligible, err := lockSynchronizedPracticePeriod(ctx, tx, period)
			if err != nil || !eligible {
				return err
			}
			inserted, err = storeSourceFactsFrom(ctx, tx, facts, client.sourceKey())
			if err != nil {
				return err
			}
			if err := storePracticeRegistrationSnapshot(ctx, tx, snapshot); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO practice_period_sync(period_id,source_key,checked_at,covered_through,missing_facts)
			 VALUES($1,$2,clock_timestamp(),$3,$4) ON CONFLICT(period_id) DO UPDATE SET source_key=excluded.source_key,
			 checked_at=excluded.checked_at,covered_through=excluded.covered_through,missing_facts=excluded.missing_facts`, period.id, client.sourceKey(), coveredThrough, missing)
			return err
		})
		if err != nil {
			return total, err
		}
		total += inserted
	}
	if incomplete {
		return total, errPracticeScopedFactsMissing
	}
	return total, nil
}

func finishScopedPracticeSync(ctx context.Context, conn *pgxpool.Conn, client orderFeedClient, state *orderSyncState, jobID, periodID int64, capabilities orderFeedCapabilities) error {
	refunds, refundErr := runPracticeRefundSync(ctx, conn, client, *state.Completed, capabilities.PageSize)
	orders, ordersErr := runPracticeScopedOrderSync(ctx, conn, client, jobID, periodID, *capabilities.PracticeOrdersThrough)
	if err := errors.Join(refundErr, ordersErr); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if err := updateOrderSync(ctx, tx, client, *state, `UPDATE dcai_order_sync SET scanned=$2,versions=$2,failed=false,
		 last_success_at=clock_timestamp() WHERE stream=$1`, refunds+orders); err != nil {
			return err
		}
		if jobID != 0 {
			return finishOrderSyncJob(ctx, tx, jobID, "SUCCEEDED", "")
		}
		return nil
	})
}
