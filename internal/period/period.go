// Package period is the export week (ТЗ §17.1): from Sunday 16:00 to
// Sunday 16:00 Europe/Bratislava, named by the ISO week of its closing
// Sunday. The export and the forecast log share it.
package period

import (
	"fmt"
	"time"
)

// Export week boundary (§17.1): Sunday 16:00 Europe/Bratislava.
const (
	weekEndDay  = time.Sunday
	weekEndHour = 16
)

// WeekEnd is the end of the export period containing t: the first Sunday
// 16:00 (in loc) after t.
func WeekEnd(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	days := (int(weekEndDay) - int(l.Weekday()) + 7) % 7
	end := time.Date(l.Year(), l.Month(), l.Day()+days, weekEndHour, 0, 0, 0, loc)
	if !end.After(t) {
		end = end.AddDate(0, 0, 7)
	}
	return end
}

// ISOWeek is iso_week of the export period containing t: the ISO week of
// its closing Sunday, "2026-W44".
func ISOWeek(t time.Time, loc *time.Location) string {
	y, w := WeekEnd(t, loc).ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// EndOfWeek is the end of the export period of an ISO week ("2026-W43"):
// its Sunday 16:00 in loc.
func EndOfWeek(isoWeek string, loc *time.Location) (time.Time, error) {
	var y, w int
	if _, err := fmt.Sscanf(isoWeek, "%d-W%d", &y, &w); err != nil || w < 1 || w > 53 {
		return time.Time{}, fmt.Errorf("week %q: want YYYY-Www", isoWeek)
	}
	// 4 January is always in ISO week 1.
	jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, loc)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday())+6)%7)+7*(w-1))
	end := time.Date(monday.Year(), monday.Month(), monday.Day()+6, weekEndHour, 0, 0, 0, loc)
	if got := ISOWeek(end.Add(-time.Minute), loc); got != fmt.Sprintf("%d-W%02d", y, w) {
		return time.Time{}, fmt.Errorf("week %q does not exist", isoWeek)
	}
	return end, nil
}

// LastEnd is the end of the latest finished export period at now.
func LastEnd(now time.Time, loc *time.Location) time.Time {
	return WeekEnd(now, loc).AddDate(0, 0, -7)
}

// Next is the iso_week after isoWeek ("" for a malformed one).
func Next(isoWeek string, loc *time.Location) string {
	end, err := EndOfWeek(isoWeek, loc)
	if err != nil {
		return ""
	}
	return ISOWeek(end.AddDate(0, 0, 7).Add(-time.Minute), loc)
}
