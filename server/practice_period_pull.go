package main

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const practiceSourcePeriodContract = "dczd.finance-practice-period-read/1.0"
const practicePeriodMappingsScope = `m.source='DCZD' AND m.domain='PRACTICE' AND m.source_key IN ('1','4') AND p.period_mode='class'`
const practiceSourceProductsSQL = `SELECT coalesce(jsonb_agg(jsonb_build_object('id',p.id,'name',p.name) ORDER BY p.name,p.id),'[]')
 FROM products p WHERE EXISTS (SELECT 1 FROM source_mappings m WHERE m.product_id=p.id AND ` + practicePeriodMappingsScope + `)`

type practicePeriodSourceInput struct {
	ProductID int64 `json:"productId"`
}

type practicePeriodPullSummary struct {
	Added    int `json:"added"`
	Existing int `json:"existing"`
	Skipped  int `json:"skipped"`
}

type practiceSourcePeriodsPage struct {
	Contract     string                  `json:"contract_version"`
	StrategyID   int                     `json:"strategy_id"`
	ActivityType int                     `json:"activity_type"`
	ThroughID    int64                   `json:"through_id"`
	NextID       int64                   `json:"next_after_id"`
	HasMore      *bool                   `json:"has_more"`
	Skipped      int                     `json:"skipped"`
	Periods      []practicePeriodMapping `json:"periods"`
}

func (c orderFeedClient) readPracticeSourcePeriods(ctx context.Context, strategy, activity string) ([]practicePeriodMapping, int, error) {
	const limit = 100
	var items []practicePeriodMapping
	var through, after int64
	var skipped int
	for page := 0; page < 100; page++ {
		query := url.Values{"strategy_id": {strategy}, "activity_type": {activity}, "after_id": {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}}
		if page != 0 {
			query.Set("through_id", strconv.FormatInt(through, 10))
		}
		var result practiceSourcePeriodsPage
		if err := c.getWithContract(ctx, "practice-periods", practiceSourcePeriodContract, query, &result); err != nil {
			return nil, 0, err
		}
		if page == 0 {
			through = result.ThroughID
		}
		if result.HasMore == nil || result.Periods == nil || result.Contract != practiceSourcePeriodContract ||
			strconv.Itoa(result.StrategyID) != strategy || strconv.Itoa(result.ActivityType) != activity || result.ThroughID != through ||
			result.NextID < after || result.NextID > through || result.Skipped < 0 || result.Skipped+len(result.Periods) > limit ||
			*result.HasMore && (result.NextID <= after || result.NextID >= through) {
			return nil, 0, errSourceRead
		}
		prefix := "PRACTICE:" + activity + ":" + strategy + ":"
		previous := after
		for _, row := range result.Periods {
			id := positiveSourceID(strings.TrimPrefix(row.PeriodKey, prefix))
			start, startErr := time.Parse(time.DateOnly, row.StartsOn)
			end, endErr := time.Parse(time.DateOnly, row.EndsOn)
			if !strings.HasPrefix(row.PeriodKey, prefix) || id <= previous || id > result.NextID ||
				row.Ordinal < 1 || row.Ordinal > 2147483647 || !textOK(row.Name, 80) || startErr != nil || endErr != nil || end.Before(start) {
				return nil, 0, errSourceRead
			}
			previous = id
		}
		items = append(items, result.Periods...)
		skipped += result.Skipped
		if !*result.HasMore {
			return items, skipped, nil
		}
		after = result.NextID
	}
	return nil, 0, errSourceRead
}

func (s workspace) pullPracticeSourcePeriods(ctx context.Context, input practicePeriodSourceInput) (practicePeriodPullSummary, error) {
	summary := practicePeriodPullSummary{}
	const mappings = `SELECT m.source_key FROM source_mappings m JOIN products p ON p.id=m.product_id
	 WHERE p.id=$1 AND ` + practicePeriodMappingsScope + ` ORDER BY m.source_key`
	rows, err := s.db.Query(ctx, mappings, input.ProductID)
	if err != nil {
		return summary, err
	}
	strategies, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return summary, err
	}
	if len(strategies) == 0 {
		return summary, errPracticePeriodMappingMissing
	}
	client, err := newOrderFeedClient("DCZD", s.sources["DCZD"])
	if err != nil {
		return summary, err
	}
	var items []practicePeriodMapping
	for _, strategy := range strategies {
		for _, activity := range []string{"2", "1"} {
			periods, skipped, err := client.readPracticeSourcePeriods(ctx, strategy, activity)
			if err != nil {
				return summary, err
			}
			items = append(items, periods...)
			summary.Skipped += skipped
		}
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, mappings+" FOR SHARE OF m,p", input.ProductID)
		if err != nil {
			return err
		}
		current, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if !slices.Equal(strategies, current) {
			return errPracticePeriodMappingMissing
		}
		for _, row := range items {
			row.Name = strings.TrimSpace(row.Name)
			added, err := storePracticePeriodMapping(ctx, tx, row)
			switch {
			case err == nil && added:
				summary.Added++
			case err == nil:
				summary.Existing++
			case errors.Is(err, errPracticePeriodMappingConflict), errors.Is(err, errPracticeStart):
				summary.Skipped++
			default:
				return err
			}
		}
		return nil
	})
	if err != nil {
		return practicePeriodPullSummary{}, err
	}
	return summary, nil
}

func (s workspace) postPracticeSourcePeriods(ctx context.Context, input practicePeriodSourceInput) (any, error) {
	return s.pullPracticeSourcePeriods(ctx, input)
}
