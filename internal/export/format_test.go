package export

import (
	"testing"
	"time"

	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// §17.8 and the examples/ files.
func TestFormatting(t *testing.T) {
	for _, tt := range []struct{ got, want string }{
		{num(1912000, 0, false), "1 912 000"},
		{num(7000, 0, true), "+7 000"},
		{num(-903.4, 0, false), "−903"},
		{num(-0.04, 1, true), "0.0"},
		{num(0, 0, true), "0"},
		{num(28957.53, 0, false), "28 958"},
		{num(4007.9, 2, false), "4 007.90"},
		{num(123, 0, false), "123"},
		{num(1234, 0, false), "1 234"},
		{pct1(some(2.94)), "2.9%"},
		{signedPct1(some(-3.46)), "−3.5%"},
		{rate2(some(3.5249)), "3.52%"},
		{bp(some(6.4)), "+6"},
		{bp(some(-4.6)), "−5"},
		{pp(some(-0.1)), "−0.1"},
		{billions(some(23715.2), false), "23 715 млрд $"},
		{billions(some(4.5), false), "4.5 млрд $"},
		{billions(some(0.011), false), "0.011 млрд $"},
		{billions(some(0), false), "0.0 млрд $"},
		{billionsNum(some(-0.4123), true), "−0.412"},
		{billions(some(-903), false), "−903 млрд $"},
		{billions(transform.Value{}, false), "—"},
		{obsDate(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "q"), "2026-Q2"},
		{obsDate(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "m"), "2026-08"},
		{obsDate(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), "d"), "2026-10-23"},
		{contractMonth(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)), "Ноя 2026"},
		{byMonth(time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)), "мая 2027"},
	} {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
}

// A revision that rounds to the same shown level is not shown (FIXES_W44 A4).
func TestRevisionCell(t *testing.T) {
	v := view{Kind: kindPct, Factor: 1}
	if c, shown := v.revision(store.Revision{Old: 131.62, New: 131.58}); shown || c != dash {
		t.Errorf("same after rounding: %q %v", c, shown)
	}
	if c, shown := v.revision(store.Revision{Old: 131.6, New: 131.4}); !shown || c != "131.6% → 131.4%" {
		t.Errorf("revision: %q %v", c, shown)
	}
}
