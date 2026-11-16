package sources

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParsePeriod turns a statistical period into the date of its observation,
// as UTC midnight: a day is itself, a month and a quarter are their first
// day (the FRED convention), an ISO week "2026-W44" is its Friday (the
// Eurosystem weekly statement is drawn up as at Friday), a year is 1 January.
func ParsePeriod(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	bad := func() (time.Time, error) { return time.Time{}, fmt.Errorf("period %q: unknown format", s) }
	if len(s) < 4 {
		return bad()
	}
	year, err := strconv.Atoi(s[:4])
	if err != nil {
		return bad()
	}
	rest := s[4:]
	switch {
	case rest == "":
		return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC), nil
	case len(rest) == 6: // -MM-DD
		t, err := time.Parse(time.DateOnly, s)
		if err != nil {
			return bad()
		}
		return t, nil
	case strings.HasPrefix(rest, "-W"):
		w, err := strconv.Atoi(rest[2:])
		if err != nil || w < 1 || w > 53 {
			return bad()
		}
		// 4 January is always in ISO week 1.
		jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
		monday := jan4.AddDate(0, 0, -int((jan4.Weekday()+6)%7))
		return monday.AddDate(0, 0, (w-1)*7+4), nil
	case strings.HasPrefix(rest, "-Q"):
		q, err := strconv.Atoi(rest[2:])
		if err != nil || q < 1 || q > 4 {
			return bad()
		}
		return time.Date(year, time.Month(3*q-2), 1, 0, 0, 0, 0, time.UTC), nil
	case len(rest) == 3 && rest[0] == '-':
		m, err := strconv.Atoi(rest[1:])
		if err != nil || m < 1 || m > 12 {
			return bad()
		}
		return time.Date(year, time.Month(m), 1, 0, 0, 0, 0, time.UTC), nil
	}
	return bad()
}
