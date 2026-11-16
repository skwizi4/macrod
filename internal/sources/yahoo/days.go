package yahoo

import (
	"strings"
	"time"
)

// AllWeek reports whether symbol trades every day: Yahoo crypto pairs
// ("BTC-USD") have a bar on weekends too, in UTC days.
func AllWeek(symbol string) bool { return strings.HasSuffix(symbol, "-USD") }

// LastBarDay is the day of the last bar of symbol that is closed at t, as a
// UTC midnight date like the bar dates: the day before t's UTC day for
// crypto, otherwise the last trading day (LastTradingDay in exch).
func LastBarDay(t time.Time, symbol string, exch *time.Location) time.Time {
	if AllWeek(symbol) {
		u := t.UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	}
	return LastTradingDay(t, exch)
}

// LastTradingDay is the last weekday before t's day in loc, as a UTC
// midnight date like the bar dates (holidays are not known: a bar that
// cannot exist is asked for once, harmlessly).
func LastTradingDay(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	d := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}
