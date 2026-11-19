package main

import (
	"time"
	_ "time/tzdata"
)

var shanghai, _ = time.LoadLocation("Asia/Shanghai")

type periodWindow struct {
	Start, Through time.Time
}

func calendarWindow(settlementMonth string, day int) (periodWindow, error) {
	month, err := time.ParseInLocation("2006-01", settlementMonth, shanghai)
	if err != nil || month.Year() < 1 || day < 1 || day > 28 {
		return periodWindow{}, invalid
	}
	if day == 1 {
		return periodWindow{Start: month, Through: month.AddDate(0, 1, 0)}, nil
	}
	through := month.AddDate(0, 0, day-1)
	return periodWindow{Start: through.AddDate(0, -1, 0), Through: through}, nil
}
