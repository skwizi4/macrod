package sources

import (
	"testing"
	"time"
)

func TestParsePeriod(t *testing.T) {
	for in, want := range map[string]string{
		"2026-11-09": "2026-11-09",
		"2026-W44":   "2026-10-30", // Friday of the ISO week 26.10–01.11
		"2026-W01":   "2026-01-02",
		"2020-W53":   "2021-01-01",
		"2026-10":    "2026-10-01",
		"2026-Q3":    "2026-07-01",
		"2026":       "2026-01-01",
	} {
		got, err := ParsePeriod(in)
		if err != nil || got.Format(time.DateOnly) != want {
			t.Errorf("ParsePeriod(%q) = %s, %v; want %s", in, got.Format(time.DateOnly), err, want)
		}
	}
	for _, in := range []string{"", "2026-13", "2026-Q5", "2026-W54", "2026-02-30", "26-10", "2026/10"} {
		if _, err := ParsePeriod(in); err == nil {
			t.Errorf("ParsePeriod(%q): no error", in)
		}
	}
}
