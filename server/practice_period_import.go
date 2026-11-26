package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errPracticePeriodMappingMissing = errors.New("缺少该来源策略号对应的实战班产品映射")
var errPracticePeriodMappingConflict = errors.New("期数或来源期次与已登记的班次冲突")

type practicePeriodMapping struct {
	PeriodKey string `json:"periodKey"`
	Name      string `json:"name"`
	Ordinal   int    `json:"ordinal"`
	StartsOn  string `json:"startsOn"`
	EndsOn    string `json:"endsOn"`
}

func storePracticePeriodMappings(ctx context.Context, tx pgx.Tx, periods []practicePeriodMapping) (int, error) {
	if len(periods) == 0 {
		return 0, invalid
	}
	inserted := 0
	for _, row := range periods {
		start, startErr := time.Parse(time.DateOnly, row.StartsOn)
		end, endErr := time.Parse(time.DateOnly, row.EndsOn)
		if !validPracticeKey(row.PeriodKey) || !textOK(row.Name, 80) ||
			row.Ordinal < 1 || startErr != nil || endErr != nil || end.Before(start) {
			return 0, invalid
		}
		row.Name = strings.TrimSpace(row.Name)
		added, err := storePracticePeriodMapping(ctx, tx, row)
		if err != nil {
			return 0, fmt.Errorf("来源期次 %s：%w", row.PeriodKey, err)
		}
		if added {
			inserted++
		}
	}
	return inserted, nil
}

func storePracticePeriodMapping(ctx context.Context, tx pgx.Tx, row practicePeriodMapping) (bool, error) {
	var productID int64
	err := tx.QueryRow(ctx, `SELECT p.id FROM source_mappings m JOIN products p ON p.id=m.product_id
	 WHERE m.source='DCZD' AND m.domain='PRACTICE' AND m.source_key=split_part($1,':',3)
	  AND p.period_mode='class' FOR SHARE OF m`, row.PeriodKey).Scan(&productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errPracticePeriodMappingMissing
	}
	if err != nil {
		return false, err
	}
	args := []any{productID, row.Name, row.Ordinal, row.StartsOn, row.EndsOn, row.PeriodKey}
	tag, err := tx.Exec(ctx, `INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,source_period_key)
	 VALUES($1,$2,$3,$4::text::date,$5::text::date,$6) ON CONFLICT DO NOTHING`, args...)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return true, enqueueDraftConfiguration(ctx, tx, draftRefreshRoster, row.PeriodKey, nil)
	}
	var same bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM periods
	 WHERE source_period_key=$6 AND product_id=$1 AND name=$2 AND ordinal=$3
	  AND starts_on=$4::text::date AND ends_on=$5::text::date AND calendar_month IS NULL)`, args...).Scan(&same)
	switch {
	case err != nil || same:
		return false, err
	default:
		return false, errPracticePeriodMappingConflict
	}
}

func importPracticePeriods(ctx context.Context, cfg config, filename string) error {
	if !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return errors.New("期次映射导入需要使用专用开发数据库")
	}
	var input struct {
		Periods []practicePeriodMapping `json:"periods"`
	}
	if err := readImportFile(filename, &input, 8<<20); err != nil {
		return err
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("could not initialize the isolated database")
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		return err
	}
	var added int
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var err error
		added, err = storePracticePeriodMappings(ctx, tx, input.Periods)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Printf("新增 %d 个实战班来源期次，复用 %d 个\n", added, len(input.Periods)-added)
	return nil
}
