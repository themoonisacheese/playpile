package web

import (
	"testing"
	"time"
)

func TestTimetableWeekBoundsAfterSkippedMidnight(t *testing.T) {
	loc, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2024, 9, 8, 12, 0, 0, 0, loc)
	bounds, err := scheduleWeekBounds(now, loc)
	if err != nil {
		t.Fatal(err)
	}
	start, end := bounds.Start.In(loc), bounds.End.In(loc)
	if start.Format("2006-01-02 15:04") != "2024-09-08 01:00" || end.Format("2006-01-02 15:04") != "2024-09-15 00:00" || bounds.End.Sub(bounds.Start) != 167*time.Hour {
		t.Fatalf("wrong seven-civil-day bounds %v–%v", start, end)
	}
}
