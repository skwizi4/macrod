// Package export writes the .md files the Claude Projects read (ТЗ §12).
// Phase 2 has the event export of FOMC statements (fomc_diff.md); the
// weekly files come with phase 3.
package export

import (
	"time"

	"macro-collector/internal/period"
)

// WeekEnd is period.WeekEnd: the end of the export period containing t.
func WeekEnd(t time.Time, loc *time.Location) time.Time { return period.WeekEnd(t, loc) }

// ISOWeek is period.ISOWeek: iso_week of the export period containing t.
func ISOWeek(t time.Time, loc *time.Location) string { return period.ISOWeek(t, loc) }

// PeriodEndOfWeek is period.EndOfWeek: the end of the export period of an
// ISO week ("2026-W43"), its Sunday 16:00 in loc.
func PeriodEndOfWeek(isoWeek string, loc *time.Location) (time.Time, error) {
	return period.EndOfWeek(isoWeek, loc)
}

// LastPeriodEnd is period.LastEnd: the end of the latest finished export
// period at now.
func LastPeriodEnd(now time.Time, loc *time.Location) time.Time { return period.LastEnd(now, loc) }
