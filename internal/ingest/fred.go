// Package ingest moves data from sources into the store.
package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/fred"
	"macro-collector/internal/store"
	"macro-collector/internal/units"
)

const sourceFRED = "fred"

// Revision windows for incremental fetches: how far back from the last
// stored observation to re-read, so that revisions are picked up.
const (
	windowDailyWeekly = 1 // years
	windowMonthlyUp   = 5 // years; catches benchmark revisions (PAYEMS, GDP)
	lastUpdatedFormat = "2006-01-02 15:04:05-07"
)

// FREDClient is the part of fred.Client that ingest uses.
type FREDClient interface {
	FetchMeta(ctx context.Context, id string) (fred.Meta, error)
	FetchSeries(ctx context.Context, id string, since time.Time) ([]model.Observation, error)
}

// Mode selects how a run decides what to fetch.
type Mode int

const (
	// Backfill fetches every series from a start date, ignoring cursors.
	Backfill Mode = iota
	// Incremental skips series whose last_updated has not changed and
	// otherwise re-reads a revision window before the last observation.
	Incremental
)

// Outcome of one series in a run.
type Outcome string

const (
	OutcomeUpdated Outcome = "updated"
	OutcomeSkipped Outcome = "skipped"
	OutcomeFailed  Outcome = "failed"
)

// SeriesResult is the result of one series.
type SeriesResult struct {
	ID      string
	Outcome Outcome
	Since   time.Time // zero when skipped
	store.UpsertResult
	Err error
}

// Report summarises a run.
type Report struct {
	Results []SeriesResult
	// Interrupted is set when ctx was cancelled (shutdown); series after
	// that point are not in Results and are not recorded as failed.
	Interrupted bool
}

// Failed returns the failed series.
func (r Report) Failed() []SeriesResult {
	var out []SeriesResult
	for _, s := range r.Results {
		if s.Outcome == OutcomeFailed {
			out = append(out, s)
		}
	}
	return out
}

// Count returns how many series ended with outcome o.
func (r Report) Count(o Outcome) int {
	n := 0
	for _, s := range r.Results {
		if s.Outcome == o {
			n++
		}
	}
	return n
}

// FRED loads the configured FRED series into the store.
type FRED struct {
	Client  FREDClient
	Store   *store.Store
	Series  []config.Series
	History time.Time // history_start
	Log     *slog.Logger
	Now     func() time.Time // defaults to time.Now
}

// Run processes every series. A failing series is recorded in fetch_state
// and does not stop the others; cancelling ctx stops the run (see
// Report.Interrupted). In Backfill mode since overrides History when
// non-zero.
func (f *FRED) Run(ctx context.Context, mode Mode, since time.Time) Report {
	if since.IsZero() {
		since = f.History
	}
	var rep Report
	for _, s := range f.Series {
		if ctx.Err() != nil {
			rep.Interrupted = true
			break
		}
		res := f.runSeries(ctx, mode, s, since)
		if res.Err != nil && ctx.Err() != nil {
			// Shutdown, not a source failure: leave fetch_state alone.
			rep.Interrupted = true
			break
		}
		if res.Err != nil {
			f.Log.Error("series failed", "series", s.ID, "err", res.Err)
			if err := f.Store.Write(ctx, func(tx *sql.Tx) error {
				return store.SetFetchError(tx, cursorKey(s.ID), res.Err, f.now())
			}); err != nil {
				f.Log.Error("record fetch error", "series", s.ID, "err", err)
			}
		} else {
			f.Log.Info("series done", "series", s.ID, "outcome", res.Outcome,
				"inserted", res.Inserted, "revised", res.Revised, "unchanged", res.Unchanged)
		}
		rep.Results = append(rep.Results, res)
	}
	return rep
}

func (f *FRED) runSeries(ctx context.Context, mode Mode, s config.Series, backfillSince time.Time) SeriesResult {
	res := SeriesResult{ID: s.ID, Outcome: OutcomeFailed}
	key := cursorKey(s.ID)

	meta, err := f.Client.FetchMeta(ctx, s.ID)
	if err != nil {
		res.Err = err
		return res
	}
	if err := units.CheckFRED(s.ID, meta.Units); err != nil {
		res.Err = err
		return res
	}
	cursor := meta.LastUpdated.Format(lastUpdatedFormat)

	since := backfillSince
	if mode == Incremental {
		st, ok, err := f.Store.FetchStateOf(ctx, key)
		if err != nil {
			res.Err = err
			return res
		}
		last, hasObs, err := f.Store.LastObsDate(ctx, sourceFRED, s.ID)
		if err != nil {
			res.Err = err
			return res
		}
		if ok && hasObs && st.Cursor == cursor {
			res.Outcome = OutcomeSkipped
			res.Err = f.Store.Write(ctx, func(tx *sql.Tx) error {
				return store.SetFetchOK(tx, key, cursor, f.now())
			})
			if res.Err != nil {
				res.Outcome = OutcomeFailed
			}
			return res
		}
		since = f.History
		if hasObs {
			since = latest(f.History, last.AddDate(-revisionWindow(meta.Frequency), 0, 0))
		}
	}
	res.Since = since

	obs, err := f.Client.FetchSeries(ctx, s.ID, since)
	if err != nil {
		res.Err = err
		return res
	}
	// Meta, observations and cursor commit together: the cursor only moves
	// when the data it describes is stored.
	err = f.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
			Source: sourceFRED, SeriesID: s.ID, Label: s.Label, Agent: s.Agent,
			Frequency: meta.Frequency, Units: meta.Units,
		}); err != nil {
			return err
		}
		up, err := store.UpsertObservations(tx, sourceFRED, s.ID, obs, f.now())
		if err != nil {
			return err
		}
		res.UpsertResult = up
		return store.SetFetchOK(tx, key, cursor, f.now())
	})
	if err != nil {
		res.Err = fmt.Errorf("fred store %s: %w", s.ID, err)
		return res
	}
	res.Outcome = OutcomeUpdated
	return res
}

func (f *FRED) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func cursorKey(id string) string { return sourceFRED + ":" + id }

// FREDLastUpdated is the last_updated of series id as of its last
// successful fetch; ok is false if it was never fetched.
func FREDLastUpdated(ctx context.Context, st *store.Store, id string) (t time.Time, ok bool, err error) {
	fs, ok, err := st.FetchStateOf(ctx, cursorKey(id))
	if err != nil || !ok || fs.Cursor == "" {
		return time.Time{}, false, err
	}
	t, err = time.Parse(lastUpdatedFormat, fs.Cursor)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("fred cursor %s %q: %w", id, fs.Cursor, err)
	}
	return t, true, nil
}

func revisionWindow(freq string) int {
	switch freq {
	case "d", "w", "bw":
		return windowDailyWeekly
	default:
		return windowMonthlyUp
	}
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
