// Package calendar keeps the releases table: when data and texts are due
// (ТЗ §8.1). Sources list dates; Refresh syncs the next horizon_days.
package calendar

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/fred"
	"macro-collector/internal/store"
)

// SourceFRED is releases.source of FRED release events.
const SourceFRED = "fred_release"

const (
	seriesSource = "fred"          // series_meta.source of FRED series
	fredStateKey = "calendar:fred" // fetch_state row of the FRED calendar
)

// FREDClient is the part of fred.Client the calendar uses.
type FREDClient interface {
	FetchSeriesRelease(ctx context.Context, id string) (fred.Release, error)
	FetchReleasesDates(ctx context.Context, from, to time.Time) ([]fred.ReleaseDate, error)
}

// FRED builds release events of the configured FRED series.
type FRED struct {
	Client   FREDClient
	Store    *store.Store
	Series   []config.Series
	Calendar *config.Calendar
	RegionOf func(tag string) string // region of a series tag
	Log      *slog.Logger
	Now      func() time.Time // defaults to time.Now
}

// releaseRegion is the region of a release: that of its first series.
func (f *FRED) releaseRegion(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	first := slices.Min(ids)
	for _, s := range f.Series {
		if s.ID == first {
			return f.RegionOf(s.Agent)
		}
	}
	return ""
}

// FREDReport summarises a refresh.
type FREDReport struct {
	From, To      time.Time // calendar days in release_timezone, inclusive
	Lookups       int       // series whose release was looked up
	LookupsFailed []string  // series whose lookup failed; their releases may be missing
	Releases      int       // releases with at least one event in the window
	Events        int
	store.SyncResult
}

// Refresh looks up the release of series that do not have one yet, reads
// all FRED release dates from today to today+horizon_days and syncs the
// events of our releases. A failed series lookup is logged and skipped; a
// failed dates request fails the refresh and is recorded in fetch_state.
func (f *FRED) Refresh(ctx context.Context) (FREDReport, error) {
	loc := f.Calendar.ReleaseLocation
	w := windowAt(f.now(), f.Calendar)
	rep := FREDReport{From: w.FromDay, To: w.ToDay}
	winFrom, winTo := w.From, w.To

	releaseOf, err := f.releases(ctx, &rep)
	if err != nil {
		return rep, err
	}

	dates, err := f.Client.FetchReleasesDates(ctx, rep.From, rep.To)
	if err != nil {
		if ctx.Err() == nil {
			f.recordError(ctx, err)
		}
		return rep, fmt.Errorf("calendar fred: %w", err)
	}

	// Series of each tracked release, and the name to use for its events:
	// from the response, else from events already stored in the window.
	series := make(map[int][]string)
	for id, rel := range releaseOf {
		series[rel] = append(series[rel], id)
	}
	names := make(map[int]string)
	stored, err := f.Store.Releases(ctx, winFrom, winTo)
	if err != nil {
		return rep, fmt.Errorf("calendar fred: %w", err)
	}
	for _, e := range stored {
		if e.Source == SourceFRED {
			names[e.Related.ReleaseID] = e.Name
		}
	}
	events := make(map[int][]model.ReleaseEvent)
	for _, rd := range dates {
		ids, ok := series[rd.ReleaseID]
		if !ok {
			continue
		}
		names[rd.ReleaseID] = rd.ReleaseName
		events[rd.ReleaseID] = append(events[rd.ReleaseID], model.ReleaseEvent{
			Source:      SourceFRED,
			Name:        rd.ReleaseName,
			ScheduledAt: f.Calendar.ReleaseTime(rd.ReleaseID).On(rd.Date, loc).UTC(),
			Region:      f.releaseRegion(ids),
			Importance:  f.Calendar.Importance(rd.ReleaseID),
			Related:     model.Related{ReleaseID: rd.ReleaseID, Series: slices.Sorted(slices.Values(ids))},
		})
	}

	err = f.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, rel := range slices.Sorted(maps.Keys(series)) {
			name, ok := names[rel]
			if !ok {
				continue // no events now or before: nothing to sync
			}
			res, err := store.SyncReleases(tx, SourceFRED, name, winFrom, winTo, events[rel])
			if err != nil {
				return err
			}
			if len(events[rel]) > 0 {
				rep.Releases++
				rep.Events += len(events[rel])
			}
			rep.Inserted += res.Inserted
			rep.Updated += res.Updated
			rep.Rescheduled += res.Rescheduled
		}
		return store.SetFetchOK(tx, fredStateKey, rep.To.Format(time.DateOnly), f.now())
	})
	if err != nil {
		return rep, fmt.Errorf("calendar fred: %w", err)
	}
	f.Log.Info("fred calendar refreshed", "from", rep.From.Format(time.DateOnly), "to", rep.To.Format(time.DateOnly),
		"releases", rep.Releases, "events", rep.Events, "inserted", rep.Inserted,
		"rescheduled", rep.Rescheduled, "lookup_failed", len(rep.LookupsFailed))
	return rep, nil
}

// releases maps each configured series to its release id, looking up and
// storing the ones not yet in series_meta.
func (f *FRED) releases(ctx context.Context, rep *FREDReport) (map[string]int, error) {
	known, err := f.Store.SeriesReleases(ctx, seriesSource)
	if err != nil {
		return nil, fmt.Errorf("calendar fred: %w", err)
	}
	out := make(map[string]int, len(f.Series))
	for _, s := range f.Series {
		if rel, ok := known[s.ID]; ok {
			out[s.ID] = rel
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("calendar fred: %w", err)
		}
		rep.Lookups++
		r, err := f.Client.FetchSeriesRelease(ctx, s.ID)
		if err == nil {
			err = f.Store.Write(ctx, func(tx *sql.Tx) error {
				return store.SetSeriesRelease(tx, seriesSource, s.ID, s.Label, s.Agent, r.ID)
			})
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("calendar fred: %w", ctx.Err())
			}
			f.Log.Error("series release lookup failed", "series", s.ID, "err", err)
			rep.LookupsFailed = append(rep.LookupsFailed, s.ID)
			continue
		}
		out[s.ID] = r.ID
	}
	return out, nil
}

func (f *FRED) recordError(ctx context.Context, err error) {
	if werr := f.Store.Write(ctx, func(tx *sql.Tx) error {
		return store.SetFetchError(tx, fredStateKey, err, f.now())
	}); werr != nil {
		f.Log.Error("record calendar error", "err", werr)
	}
}

// window is the span a refresh covers: calendar days FromDay..ToDay
// (inclusive, UTC midnight) in release_timezone, i.e. the instants [From, To).
type window struct {
	FromDay, ToDay time.Time
	From, To       time.Time
}

func windowAt(now time.Time, cal *config.Calendar) window {
	loc := cal.ReleaseLocation
	y, m, d := now.In(loc).Date()
	from := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return window{
		FromDay: from,
		ToDay:   from.AddDate(0, 0, cal.HorizonDays),
		From:    time.Date(y, m, d, 0, 0, 0, 0, loc),
		To:      time.Date(y, m, d+cal.HorizonDays+1, 0, 0, 0, 0, loc),
	}
}

func (f *FRED) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}
