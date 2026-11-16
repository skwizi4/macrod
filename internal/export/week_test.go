package export

import (
	"testing"
	"time"
)

func TestWeek(t *testing.T) {
	bts, err := time.LoadLocation("Europe/Bratislava")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ at, end, week string }{
		{"2026-10-28T18:00:40Z", "2026-11-01T16:00:00+01:00", "2026-W44"}, // FOMC Wednesday
		{"2026-10-25T14:59:59Z", "2026-10-25T16:00:00+01:00", "2026-W43"}, // Sunday 15:59 CET
		{"2026-10-25T15:00:00Z", "2026-11-01T16:00:00+01:00", "2026-W44"}, // Sunday 16:00 CET starts the next week
		{"2026-09-16T18:00:40Z", "2026-09-20T16:00:00+02:00", "2026-W38"},
		{"2026-12-30T12:00:00Z", "2027-01-03T16:00:00+01:00", "2026-W53"}, // ISO year of the closing Sunday
	}
	for _, tt := range tests {
		at, _ := time.Parse(time.RFC3339, tt.at)
		if got := WeekEnd(at, bts).Format(time.RFC3339); got != tt.end {
			t.Errorf("%s: end %s, want %s", tt.at, got, tt.end)
		}
		if got := ISOWeek(at, bts); got != tt.week {
			t.Errorf("%s: week %s, want %s", tt.at, got, tt.week)
		}
	}
}
