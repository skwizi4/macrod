package transform

import (
	"time"

	"macro-collector/internal/model"
)

// Flags shown next to a series (ТЗ §10, §17.3, §17.7).
const (
	FlagStale    = "stale"
	FlagRevised  = "rev"
	FlagAboveMax = "gt" // value above the series' flag_if_gt
)

// FlagInput describes the state of a series for Flags.
type FlagInput struct {
	Freq     string
	Last     time.Time // date of the last observation
	Value    float64   // last value
	AsOf     time.Time
	Revised  bool     // the last observations were revised in this fetch
	FlagIfGt *float64 // threshold from config, nil if none
	// ReleaseDay, if set, is the day (midnight, release timezone) of the
	// latest calendar release of the series past its poll window; Seen is
	// when the last observation became known. Then stale means that
	// release brought nothing (StaleByRelease), and §17.7 applies only to
	// series without a calendar.
	ReleaseDay time.Time
	Seen       time.Time
	// NextRelease, used without ReleaseDay, is the day of the next release
	// of the series in the calendar (the stored calendar may start after
	// its previous release). Then the series is stale only if a release was
	// missed: by NextRelease even the period after Last would be overdue
	// by §17.7 (ULCNFB at Q2 before the Q3 release on 11-05 is not).
	NextRelease time.Time
}

// Flags returns the flags that apply, in a fixed order.
func Flags(in FlagInput) []string {
	var out []string
	stale := Stale(in.Freq, in.Last, in.AsOf)
	if !in.ReleaseDay.IsZero() {
		stale = StaleByRelease(in.Freq, in.ReleaseDay, in.Seen)
	} else if !in.NextRelease.IsZero() {
		stale = Stale(in.Freq, nextPeriod(in.Freq, in.Last), in.NextRelease)
	}
	if stale {
		out = append(out, FlagStale)
	}
	if in.Revised {
		out = append(out, FlagRevised)
	}
	if in.FlagIfGt != nil && in.Value > *in.FlagIfGt {
		out = append(out, FlagAboveMax)
	}
	return out
}

// Stale implements §17.7: the last observation is older than expected for
// the series' frequency. Weekends and holidays of daily series are covered
// by the 5-business-day allowance.
func Stale(freq string, last, asOf time.Time) bool {
	last, asOf = model.Day(last), model.Day(asOf)
	switch freq {
	case "d":
		return businessDaysBetween(last, asOf) > 5
	case "w":
		return asOf.Sub(last) > 14*24*time.Hour
	case "bw":
		return asOf.Sub(last) > 28*24*time.Hour
	case "m":
		// FRED dates a month by its first day; the next month's figure is
		// due by the end of the following month, plus 45 days of grace.
		endOfNextMonth := last.AddDate(0, 2, -1)
		return asOf.After(endOfNextMonth.AddDate(0, 0, 45))
	case "q":
		endOfQuarter := last.AddDate(0, 3, -1)
		return asOf.After(endOfQuarter.AddDate(0, 0, 120))
	}
	return false
}

// StaleByRelease: the series' latest release (on day, midnight in the
// release timezone) passed without new data. FRED may post a non-daily
// release the evening before its date, so those count from the day before;
// a daily series must be new on the day itself.
func StaleByRelease(freq string, day, seen time.Time) bool {
	from := day
	if freq != "d" {
		from = day.AddDate(0, 0, -1)
	}
	return seen.Before(from)
}

// nextPeriod is the date of the observation after last.
func nextPeriod(freq string, last time.Time) time.Time {
	switch freq {
	case "w":
		return last.AddDate(0, 0, 7)
	case "bw":
		return last.AddDate(0, 0, 14)
	case "m":
		return last.AddDate(0, 1, 0)
	case "q":
		return last.AddDate(0, 3, 0)
	}
	return last.AddDate(0, 0, 1)
}

// businessDaysBetween counts Mon–Fri days in (from, to].
func businessDaysBetween(from, to time.Time) int {
	n := 0
	for d := from.AddDate(0, 0, 1); !d.After(to); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}
