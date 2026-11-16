package model

import (
	"testing"
	"time"
)

func TestDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 16:05 ET after-close report on 28 October is 20:05 UTC; 23:30 ET is
	// already 29 October in UTC.
	late := time.Date(2026, 10, 28, 23, 30, 0, 0, ny)
	oct28 := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	oct29 := oct28.AddDate(0, 0, 1)
	for _, tc := range []struct {
		name string
		got  time.Time
		want time.Time
	}{
		{"Day keeps t's location", Day(late), oct28},
		{"Day of the UTC instant", Day(late.UTC()), oct29},
		{"DayIn converts first", DayIn(late.UTC(), ny), oct28},
		{"DayIn UTC", DayIn(late, time.UTC), oct29},
	} {
		if !tc.got.Equal(tc.want) || tc.got.Location() != time.UTC {
			t.Errorf("%s = %s, want %s", tc.name, tc.got, tc.want)
		}
	}
}
