package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errPracticeRefundOriginalMissing = errors.New("有退款找不到原付款，请检查来源订单后继续同步")

func runPracticeRefundSync(ctx context.Context, conn *pgxpool.Conn, client orderFeedClient, from time.Time, pageSize int) (int64, error) {
	key := client.sourceKey()
	position := practiceRefundCursor{SuccessAt: from, ID: "0"}
	var target, completed *time.Time
	err := conn.QueryRow(ctx, `SELECT initial_from,completed_through,target_through,next_success_at,next_id,next_code
	 FROM practice_refund_sync WHERE source_key=$1`, key).
		Scan(&from, &completed, &target, &position.SuccessAt, &position.ID, &position.Code)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var total int64
	for {
		page, err := client.practiceRefunds(ctx, position, target, pageSize)
		if err != nil {
			return total, err
		}
		originals, err := readMissingPracticeRefundPayments(ctx, conn, client, page.Facts)
		if err != nil {
			return total, err
		}
		target = &page.Through
		position = page.Next
		if !page.More {
			target, completed = nil, &page.Through
			if page.Through.After(position.SuccessAt) {
				position = practiceRefundCursor{SuccessAt: page.Through, ID: "0"}
			}
		}
		var inserted int64
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if err := lockOrderProvider(ctx, tx, "DCZD", key); err != nil {
				return err
			}
			if _, err := storeSourceFactsFrom(ctx, tx, originals, key); err != nil {
				return err
			}
			var err error
			inserted, err = storeSourceFactsFrom(ctx, tx, page.Facts, key)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO practice_refund_sync(source_key,initial_from,completed_through,target_through,
			 next_success_at,next_id,next_code,last_success_at) VALUES($1,$2,$3,$4,$5,$6,$7,CASE WHEN $4::timestamptz IS NULL THEN clock_timestamp() END)
			 ON CONFLICT(source_key) DO UPDATE SET completed_through=excluded.completed_through,target_through=excluded.target_through,
			 next_success_at=excluded.next_success_at,next_id=excluded.next_id,next_code=excluded.next_code,
			 last_success_at=coalesce(excluded.last_success_at,practice_refund_sync.last_success_at)`,
				key, from, completed, target, position.SuccessAt, position.ID, position.Code)
			return err
		})
		if err != nil {
			return total, err
		}
		total += inserted
		if !page.More {
			return total, nil
		}
	}
}

func readMissingPracticeRefundPayments(ctx context.Context, conn *pgxpool.Conn, client orderFeedClient, refunds []sourceFact) ([]sourceFact, error) {
	orders := make(map[string]struct{})
	for _, refund := range refunds {
		orders[refund.OrderID] = struct{}{}
		for _, payment := range refund.RefundsPayment {
			orders[payment.OrderID] = struct{}{}
		}
	}
	if len(orders) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(orders))
	for id := range orders {
		ids = append(ids, id)
	}
	rows, err := conn.Query(ctx, `SELECT DISTINCT order_id FROM source_order_versions
	 WHERE source='DCZD' AND order_id=ANY($1::text[]) AND kind='ORDER_PAID' AND fact->>'domain'='PRACTICE'`, ids)
	if err != nil {
		return nil, err
	}
	var id string
	if _, err := pgx.ForEachRow(rows, []any{&id}, func() error {
		delete(orders, id)
		return nil
	}); err != nil {
		return nil, err
	}
	selections := make([]practiceFactSelection, 0, len(orders))
	required := make(map[string]bool, len(orders))
	for id := range orders {
		number := positiveSourceID(id)
		if number == 0 || strconv.FormatInt(number, 10) != id {
			return nil, errPracticeRefundOriginalMissing
		}
		selection := practiceFactSelection{Kind: "ORDER_PAID", ID: number}
		selections = append(selections, selection)
		required[selection.recordKey()] = true
	}
	facts, missing, err := readPracticeScopedFacts(ctx, conn, client, selections, required)
	if err == nil && missing > 0 {
		err = errPracticeRefundOriginalMissing
	}
	return facts, err
}
