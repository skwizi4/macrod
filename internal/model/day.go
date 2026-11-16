package model

import "time"

// Day is the calendar date of t in t's own location, as UTC midnight: the
// form observation and report dates are stored in.
func Day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// DayIn is the calendar date of t in loc, as UTC midnight. 23:30 in New
// York is already the next day in UTC: DayIn(t, ny) and Day(t) differ then.
func DayIn(t time.Time, loc *time.Location) time.Time { return Day(t.In(loc)) }
