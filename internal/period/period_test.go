package period

import (
	"testing"
	"time"
)

func TestEndOfWeekAndNext(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Bratislava")
	if err != nil {
		t.Fatal(err)
	}
	end, err := EndOfWeek("2026-W46", loc)
	if err != nil || !end.Equal(time.Date(2026, 11, 15, 16, 0, 0, 0, loc)) {
		t.Fatalf("end = %v, %v", end, err)
	}
	if got := ISOWeek(end.Add(-time.Minute), loc); got != "2026-W46" {
		t.Errorf("iso week before the end = %s", got)
	}
	if got := ISOWeek(end, loc); got != "2026-W47" {
		t.Errorf("iso week at the end = %s", got)
	}
	for in, want := range map[string]string{"2026-W46": "2026-W47", "2026-W53": "2027-W01", "2026-W52": "2026-W53", "bad": ""} {
		if got := Next(in, loc); got != want {
			t.Errorf("Next(%s) = %q, want %q", in, got, want)
		}
	}
	if _, err := EndOfWeek("2025-W53", loc); err == nil {
		t.Error("2025-W53 does not exist")
	}
	if got := LastEnd(time.Date(2026, 11, 15, 15, 59, 0, 0, loc), loc); !got.Equal(time.Date(2026, 11, 8, 16, 0, 0, 0, loc)) {
		t.Errorf("last end = %v", got)
	}
}
