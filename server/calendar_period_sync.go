package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var errCalendarPeriodUnavailable = errors.New("该产品没有可同步的周期账期")

func (s workspace) pullSourcePeriods(ctx context.Context, input practicePeriodPullInput) (practicePeriodPullResult, error) {
	if input.ProductID < 1 {
		return practicePeriodPullResult{}, invalid
	}
	var mode string
	if err := s.db.QueryRow(ctx, "SELECT period_mode FROM products WHERE id=$1", input.ProductID).Scan(&mode); err != nil {
		return practicePeriodPullResult{}, err
	}
	if mode == "class" {
		return s.pullPracticePeriods(ctx, input)
	}
	return s.syncCalendarPeriods(ctx, input.ProductID, time.Now())
}

func calendarPeriodMonth(at time.Time, day int) time.Time {
	at = at.In(shanghai)
	month := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, shanghai)
	if day > 1 && at.Day() >= day {
		return month.AddDate(0, 1, 0)
	}
	return month
}

func (s workspace) syncCalendarPeriods(ctx context.Context, productID int64, now time.Time) (practicePeriodPullResult, error) {
	result := practicePeriodPullResult{Conflicts: []string{}}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var day int
		var name string
		err := tx.QueryRow(ctx, `SELECT p.name,p.cycle_start_day FROM products p JOIN product_catalog_settings c ON c.product_id=p.id
 WHERE p.id=$1 AND c.source_system='DCAI' AND p.period_mode='calendar' AND p.cycle_start_day IN (1,25) FOR UPDATE OF p`,
			productID).Scan(&name, &day)
		if errors.Is(err, pgx.ErrNoRows) {
			return errCalendarPeriodUnavailable
		}
		if err != nil {
			return err
		}
		var firstPayment *time.Time
		err = tx.QueryRow(ctx, `SELECT min(v.occurred_at)
 FROM product_catalog_settings c JOIN catalog_business_routes m ON m.product_id=c.product_id AND m.observed_at=c.captured_at
 JOIN source_order_versions v ON `+catalogRouteFactSQL+`
 JOIN source_order_classifications k ON k.source_version_id=v.id
 WHERE c.product_id=$1 AND v.kind='ORDER_PAID' AND v.occurred_at<=$2
  AND (k.entity_type,k.business_key,k.classifier)=(m.entity_type,m.business_key,m.classifier)
  AND m.effective_from<=v.occurred_at AND (m.effective_to IS NULL OR v.occurred_at<m.effective_to)`,
			productID, now).Scan(&firstPayment)
		if err != nil {
			return err
		}
		last := calendarPeriodMonth(now, day)
		first := last
		if firstPayment != nil {
			first = calendarPeriodMonth(*firstPayment, day)
		}
		for month := first; !month.After(last); month = month.AddDate(0, 1, 0) {
			label := month.Format("2006-01")
			window, err := calendarWindow(label, day)
			if err != nil {
				return err
			}
			from, to := window.Start.Format(time.DateOnly), window.Through.AddDate(0, 0, -1).Format(time.DateOnly)
			var exact, overlaps bool
			err = tx.QueryRow(ctx, `SELECT coalesce(bool_or(starts_on=$2::date AND ends_on=$3::date),false),count(*)>0
 FROM periods WHERE product_id=$1 AND starts_on<=$3::date AND ends_on>=$2::date`, productID, from, to).Scan(&exact, &overlaps)
			if err != nil {
				return err
			}
			switch {
			case exact:
				result.Existing++
			case overlaps:
				result.Conflicts = append(result.Conflicts, label)
			default:
				if _, err := tx.Exec(ctx, `INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month)
 VALUES($1,$2,$3,$4::date,$5::date,$6::date)`, productID, name+" · "+label,
					month.Year()*100+int(month.Month()), from, to, month.Format(time.DateOnly)); err != nil {
					return err
				}
				result.Added++
			}
		}
		return nil
	})
	return result, err
}
