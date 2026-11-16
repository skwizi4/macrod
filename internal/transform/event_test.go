package transform

import (
	"math"
	"testing"
	"time"
)

func ny(t *testing.T) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestReactionClose(t *testing.T) {
	loc := ny(t)
	for _, c := range []struct{ at, want string }{
		{"2026-10-29T08:30:00-04:00", "2026-10-29T16:00:00-04:00"}, // GDP before the open
		{"2026-10-28T14:00:00-04:00", "2026-10-28T16:00:00-04:00"}, // FOMC
		{"2026-10-28T16:00:00-04:00", "2026-10-29T16:00:00-04:00"}, // earnings after the close
		{"2026-10-30T16:05:00-04:00", "2026-11-02T16:00:00-05:00"}, // Friday evening → Monday
	} {
		at, _ := time.Parse(time.RFC3339, c.at)
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := ReactionClose(at, loc, 16, 0); !got.Equal(want) {
			t.Errorf("%s: close %s, want %s", c.at, got, want)
		}
	}
}

func TestPriceAtAndMove(t *testing.T) {
	base := time.Date(2026, 10, 29, 12, 28, 0, 0, time.UTC)
	ticks := []Tick{{base, 100}, {base.Add(time.Minute), 101}, {base.Add(2 * time.Minute), 103}, {base.Add(32 * time.Minute), 99}}
	t0 := base.Add(2 * time.Minute) // 12:30: the 12:29 bar has ended
	if p := PriceAt(ticks, t0); !p.OK || p.V != 101 {
		t.Errorf("price at t0 = %+v", p)
	}
	if m := MoveBetween(ticks, t0, t0.Add(31*time.Minute)); !m.OK || math.Abs(m.V+1.98) > 0.01 {
		t.Errorf("move = %+v", m)
	}
	if p := PriceAt(ticks, base.Add(3*time.Hour)); p.OK {
		t.Errorf("stale price %+v", p)
	}
	if y := YieldFromPrice(some(-0.19), 1.9); !y.OK || math.Abs(y.V-10) > 1e-9 {
		t.Errorf("yield = %+v", y)
	}
}
