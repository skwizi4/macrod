package yahoo

import (
	"testing"
	"time"
)

func TestLastBarDay(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	for _, c := range []struct{ at, symbol, want string }{
		{"2026-11-01T15:00:00Z", "^NDX", "2026-10-30"},    // Sunday → Friday
		{"2026-11-03T15:00:00Z", "^NDX", "2026-11-02"},    // Tuesday → Monday
		{"2026-11-02T15:00:00Z", "GC=F", "2026-10-30"},    // Monday → Friday
		{"2026-11-01T15:00:00Z", "BTC-USD", "2026-10-31"}, // Sunday → Saturday
		{"2026-11-02T15:00:00Z", "BTC-USD", "2026-11-01"}, // Monday → Sunday
	} {
		ts, _ := time.Parse(time.RFC3339, c.at)
		if got := LastBarDay(ts, c.symbol, ny).Format(time.DateOnly); got != c.want {
			t.Errorf("LastBarDay(%s, %s) = %s, want %s", c.at, c.symbol, got, c.want)
		}
	}
}
