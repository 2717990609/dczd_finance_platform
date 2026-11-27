package main

import (
	"context"
	"errors"
	"slices"
)

const (
	retiredHealthCreatorProduct = "DCAI_HEALTH_CREATOR"
	retiredHealthCreatorSource  = "HEALTH_CREATOR"
	activeFinanceProductCodeSQL = " <> '" + retiredHealthCreatorProduct + "'"
)

var errHealthCreatorRetired = errors.New("AI健康创客已下架，停止同步")

func (s workspace) activePeriodPull(next func(context.Context, practicePeriodPullInput) (practicePeriodPullResult, error)) func(context.Context, practicePeriodPullInput) (practicePeriodPullResult, error) {
	return func(ctx context.Context, input practicePeriodPullInput) (practicePeriodPullResult, error) {
		if input.ProductID < 1 {
			return practicePeriodPullResult{}, invalid
		}
		var code string
		if err := s.db.QueryRow(ctx, "SELECT code FROM products WHERE id=$1", input.ProductID).Scan(&code); err != nil {
			return practicePeriodPullResult{}, err
		}
		if code == retiredHealthCreatorProduct {
			return practicePeriodPullResult{}, errHealthCreatorRetired
		}
		return next(ctx, input)
	}
}

func retiredHealthOrder(row orderPageRow) bool {
	return len(row.Facts) != 0 && row.Facts[0].SourceKey == retiredHealthCreatorSource
}

// Empty outbox rows can revoke a previously seen contract. Only retain those
// observations when their paid order was already stored as an active product.
func activeHealthRows(ctx context.Context, q syncQueryer, rows []orderPageRow) ([]orderPageRow, error) {
	emptyKeys := make([]string, 0)
	for _, row := range rows {
		if len(row.Facts) == 0 {
			emptyKeys = append(emptyKeys, "ai_health_ambassador_order:"+row.Cursor.ID)
		}
	}
	var liveKeys []string
	if len(emptyKeys) != 0 {
		if err := q.QueryRow(ctx, `SELECT ARRAY(SELECT record_key FROM (
		 SELECT DISTINCT ON (record_key) record_key,fact->>'sourceKey' AS product
		 FROM source_order_versions WHERE source='DCAI' AND kind='ORDER_PAID'
		  AND fact->>'domain'='HEALTH_IDENTITY' AND record_key=ANY($1::text[])
		 ORDER BY record_key,updated_at DESC,id DESC) latest WHERE product<>$2)`,
			emptyKeys, retiredHealthCreatorSource).Scan(&liveKeys); err != nil {
			return nil, err
		}
	}
	active := make([]orderPageRow, 0, len(rows))
	for _, row := range rows {
		if !retiredHealthOrder(row) && (len(row.Facts) != 0 || slices.Contains(liveKeys, "ai_health_ambassador_order:"+row.Cursor.ID)) {
			active = append(active, row)
		}
	}
	return active, nil
}
