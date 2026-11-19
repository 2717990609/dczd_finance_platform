package main

import (
	"testing"
	"time"
)

func TestCalendarCycleUsesShanghaiBoundaries(t *testing.T) {
	for _, tc := range []struct {
		month, instant, start, end string
		day                        int
		member                     bool
	}{
		{"2026-08", "2026-07-24T15:59:59Z", "2026-07-25", "2026-08-24", 25, false},
		{"2026-08", "2026-07-24T16:00:00Z", "2026-07-25", "2026-08-24", 25, true},
		{"2026-08", "2026-08-24T16:00:00Z", "2026-07-25", "2026-08-24", 25, false},
		{"2027-01", "2026-12-25T00:00:00+08:00", "2026-12-25", "2027-01-24", 25, true},
		{"2028-02", "2028-02-29T23:59:59+08:00", "2028-02-01", "2028-02-29", 1, true},
	} {
		t.Run(tc.instant, func(t *testing.T) {
			w, err := calendarWindow(tc.month, tc.day)
			if err != nil {
				t.Fatal(err)
			}
			at, err := time.Parse(time.RFC3339, tc.instant)
			if err != nil {
				t.Fatal(err)
			}
			member := !at.Before(w.Start) && at.Before(w.Through)
			if w.Start.Format(time.DateOnly) != tc.start || w.Through.AddDate(0, 0, -1).Format(time.DateOnly) != tc.end || member != tc.member {
				t.Fatalf("window=%+v contains %s=%v", w, tc.instant, member)
			}
		})
	}
}
