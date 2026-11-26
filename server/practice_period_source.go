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

const practicePeriodReadContract = "dczd.finance-practice-period-read/1.0"
const practicePeriodSourceMappings = `m.source='DCZD' AND m.domain='PRACTICE' AND m.source_key IN ('1','4')`
const practiceSourceProductsSQL = `SELECT coalesce(jsonb_agg(jsonb_build_object('id',p.id,'name',p.name) ORDER BY p.name,p.id),'[]')
 FROM products p WHERE p.period_mode='class' AND EXISTS (
  SELECT 1 FROM source_mappings m WHERE m.product_id=p.id AND ` + practicePeriodSourceMappings + `)`

var errPracticePeriodSource = errors.New("期次读取失败，请核对大春之道期次接口和来源凭证")

type practicePeriodPullInput struct {
	ProductID int64 `json:"productId"`
}

type practicePeriodPullResult struct {
	Added     int      `json:"added"`
	Existing  int      `json:"existing"`
	Skipped   int      `json:"skipped"`
	Conflicts []string `json:"conflicts"`
}

type practicePeriodSourcePage struct {
	Contract     string                  `json:"contract_version"`
	StrategyID   int64                   `json:"strategy_id"`
	ActivityType int64                   `json:"activity_type"`
	ThroughID    int64                   `json:"through_id"`
	NextAfterID  int64                   `json:"next_after_id"`
	HasMore      *bool                   `json:"has_more"`
	Skipped      int                     `json:"skipped"`
	Periods      []practicePeriodMapping `json:"periods"`
}

func (c orderFeedClient) practicePeriods(ctx context.Context, strategy int64) ([]practicePeriodMapping, int, error) {
	rows, skipped, legacy, err := c.practicePeriodSeries(ctx, strategy, 2)
	if err != nil || legacy {
		return rows, skipped, err
	}
	online, onlineSkipped, _, err := c.practicePeriodSeries(ctx, strategy, 1)
	if err != nil {
		return nil, 0, err
	}
	return append(rows, online...), skipped + onlineSkipped, nil
}

func (c orderFeedClient) practicePeriodSeries(ctx context.Context, strategy, activityType int64) ([]practicePeriodMapping, int, bool, error) {
	const limit = 100
	var rows []practicePeriodMapping
	var after, through, reportedActivity int64
	var skipped int
	for page := 0; page < 100; page++ {
		query := url.Values{"strategy_id": {strconv.FormatInt(strategy, 10)}, "activity_type": {strconv.FormatInt(activityType, 10)},
			"after_id": {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}}
		if page > 0 {
			query.Set("through_id", strconv.FormatInt(through, 10))
		}
		var wire practicePeriodSourcePage
		if err := c.getWithContract(ctx, "practice-periods", practicePeriodReadContract, query, &wire); err != nil {
			return nil, 0, false, errPracticePeriodSource
		}
		if page == 0 {
			through, reportedActivity = wire.ThroughID, wire.ActivityType
		}
		if wire.Contract != practicePeriodReadContract || wire.StrategyID != strategy || wire.ThroughID != through ||
			wire.ActivityType != reportedActivity || reportedActivity != activityType && (reportedActivity != 0 || activityType != 2) ||
			wire.NextAfterID < after || wire.NextAfterID > through || wire.HasMore == nil || wire.Periods == nil ||
			wire.Skipped < 0 || wire.Skipped > limit-len(wire.Periods) ||
			len(wire.Periods)+wire.Skipped > 0 && wire.NextAfterID == after ||
			*wire.HasMore && (wire.NextAfterID == after || wire.NextAfterID == through) {
			return nil, 0, false, errPracticePeriodSource
		}
		previous := after
		for _, row := range wire.Periods {
			if !validPracticeKey(row.PeriodKey) {
				return nil, 0, false, errPracticePeriodSource
			}
			key := strings.Split(row.PeriodKey, ":")
			activity, sourceStrategy := positiveSourceID(key[3]), positiveSourceID(key[2])
			start, startErr := time.Parse(time.DateOnly, row.StartsOn)
			end, endErr := time.Parse(time.DateOnly, row.EndsOn)
			if positiveSourceID(key[1]) != activityType || sourceStrategy != strategy || activity <= previous || activity > wire.NextAfterID ||
				!textOK(row.Name, 80) || row.Ordinal < 1 || row.Ordinal > 2147483647 || startErr != nil || endErr != nil || end.Before(start) {
				return nil, 0, false, errPracticePeriodSource
			}
			previous = activity
		}
		rows = append(rows, wire.Periods...)
		skipped += wire.Skipped
		if !*wire.HasMore {
			return rows, skipped, reportedActivity == 0, nil
		}
		after = wire.NextAfterID
	}
	return nil, 0, false, errPracticePeriodSource
}

func (s workspace) pullPracticePeriods(ctx context.Context, input practicePeriodPullInput) (practicePeriodPullResult, error) {
	result := practicePeriodPullResult{Conflicts: []string{}}
	if input.ProductID < 1 {
		return result, invalid
	}
	var strategies []string
	err := s.db.QueryRow(ctx, `SELECT coalesce(array_agg(m.source_key ORDER BY m.source_key),'{}'::text[])
	 FROM source_mappings m JOIN products p ON p.id=m.product_id
	 WHERE p.id=$1 AND p.period_mode='class' AND `+practicePeriodSourceMappings, input.ProductID).Scan(&strategies)
	if err != nil {
		return result, err
	}
	if len(strategies) == 0 {
		return result, errPracticePeriodMappingMissing
	}
	client, err := newOrderFeedClient("DCZD", s.sources["DCZD"])
	if err != nil {
		return result, err
	}
	pullCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var rows []practicePeriodMapping
	for _, sourceKey := range strategies {
		strategy := positiveSourceID(sourceKey)
		if strategy == 0 {
			return result, errPracticePeriodMappingMissing
		}
		fetched, skipped, err := client.practicePeriods(pullCtx, strategy)
		if err != nil {
			return result, err
		}
		rows = append(rows, fetched...)
		result.Skipped += skipped
	}
	ordinals := make(map[int]int, len(rows))
	for _, row := range rows {
		ordinals[row.Ordinal]++
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		mappings, err := tx.Query(ctx, `SELECT m.source_key FROM source_mappings m
		 WHERE m.product_id=$1 AND `+practicePeriodSourceMappings+` ORDER BY m.source_key FOR SHARE`, input.ProductID)
		if err != nil {
			return err
		}
		current, err := pgx.CollectRows(mappings, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if !slices.Equal(current, strategies) {
			return errPracticePeriodMappingMissing
		}
		for _, row := range rows {
			if ordinals[row.Ordinal] > 1 {
				result.Conflicts = append(result.Conflicts, row.PeriodKey)
				continue
			}
			row.Name = strings.TrimSpace(row.Name)
			added, err := storePracticePeriodMapping(ctx, tx, row)
			switch {
			case added && err == nil:
				result.Added++
			case err == nil:
				result.Existing++
			case errors.Is(err, errPracticePeriodMappingConflict):
				var samePeriod bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM periods WHERE product_id=$1 AND source_period_key=$2
				 AND ordinal=$3 AND starts_on=$4::text::date AND ends_on=$5::text::date AND calendar_month IS NULL)`,
					input.ProductID, row.PeriodKey, row.Ordinal, row.StartsOn, row.EndsOn).Scan(&samePeriod); err != nil {
					return err
				}
				if samePeriod {
					result.Existing++
				} else {
					result.Conflicts = append(result.Conflicts, row.PeriodKey)
				}
			case errors.Is(err, errPracticeStart):
				result.Conflicts = append(result.Conflicts, row.PeriodKey)
			default:
				return err
			}
		}
		return nil
	})
	if err != nil {
		return practicePeriodPullResult{}, err
	}
	return result, nil
}
