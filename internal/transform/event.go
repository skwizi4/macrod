package transform

import "time"

// Tick is one intraday bar: its start and close.
type Tick struct {
	TS    time.Time
	Close float64
}

// maxTickAge is how old the last bar before a moment may be: a thin market
// skips minutes, a closed one has no price.
const maxTickAge = 15 * time.Minute

// ReactionClose is the close an event's reaction runs to (FIXES_W44 F1):
// hour:minute in loc (16:00 New York, 17:30 Xetra) the same day for an
// event before it, else the next weekday (holidays are not known).
func ReactionClose(at time.Time, loc *time.Location, hour, minute int) time.Time {
	t := at.In(loc)
	c := time.Date(t.Year(), t.Month(), t.Day(), hour, minute, 0, 0, loc)
	if t.Before(c) {
		return c
	}
	for {
		c = c.AddDate(0, 0, 1)
		if wd := c.Weekday(); wd != time.Saturday && wd != time.Sunday {
			return c
		}
	}
}

// PriceAt is the price at moment t: the close of the last bar that ended by
// t, if it is at most 15 minutes old.
func PriceAt(ticks []Tick, t time.Time) Value {
	for i := len(ticks) - 1; i >= 0; i-- {
		end := ticks[i].TS.Add(time.Minute)
		if end.After(t) {
			continue
		}
		if t.Sub(end) > maxTickAge {
			return Value{}
		}
		return some(ticks[i].Close)
	}
	return Value{}
}

// MoveBetween is the % change of the price from t0 to t1.
func MoveBetween(ticks []Tick, t0, t1 time.Time) Value {
	a, b := PriceAt(ticks, t0), PriceAt(ticks, t1)
	if !a.OK || !b.OK || a.V == 0 {
		return Value{}
	}
	return some((b.V/a.V - 1) * 100)
}

// YieldFromPrice approximates a yield change in b.p. from the % price change
// of a note future: Δy ≈ −Δp% / duration × 100.
func YieldFromPrice(pct Value, duration float64) Value {
	if !pct.OK || duration <= 0 {
		return Value{}
	}
	return some(-pct.V / duration * 100)
}
