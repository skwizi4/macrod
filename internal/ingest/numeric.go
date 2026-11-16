package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/sources"
	"macro-collector/internal/store"
)

// Numeric loads the configured series of one numeric source (spec 6.1: ecb,
// bundesbank, eurostat, stoxx, zew) into the store. Frequency, units and
// label come from the config; the source gives the observations.
type Numeric struct {
	Source  sources.NumericSource
	Store   *store.Store
	Series  []config.SourceSeries
	History time.Time // history_start
	Log     *slog.Logger
	Now     func() time.Time // defaults to time.Now
}

// Name is the source name.
func (n *Numeric) Name() string { return n.Source.Name() }

// Run fetches every series. A failing series is recorded in
// fetch_state["<source>:<id>"], stores nothing and does not stop the
// others; cancelling ctx stops the run.
func (n *Numeric) Run(ctx context.Context, mode Mode) Report {
	var rep Report
	for _, s := range n.Series {
		if ctx.Err() != nil {
			rep.Interrupted = true
			break
		}
		res := n.runSeries(ctx, mode, s)
		if res.Err != nil && ctx.Err() != nil {
			rep.Interrupted = true
			break
		}
		if res.Err != nil {
			n.Log.Error("series failed", "series", s.ID, "err", res.Err)
			if err := n.Store.Write(ctx, func(tx *sql.Tx) error {
				return store.SetFetchError(tx, n.key(s.ID), res.Err, n.now())
			}); err != nil {
				n.Log.Error("record fetch error", "series", s.ID, "err", err)
			}
		} else {
			n.Log.Info("series done", "series", s.ID, "since", res.Since.Format(time.DateOnly),
				"inserted", res.Inserted, "revised", res.Revised, "unchanged", res.Unchanged)
		}
		rep.Results = append(rep.Results, res)
	}
	return rep
}

func (n *Numeric) key(id string) string { return n.Source.Name() + ":" + id }

func (n *Numeric) runSeries(ctx context.Context, mode Mode, s config.SourceSeries) SeriesResult {
	res := SeriesResult{ID: s.ID, Outcome: OutcomeFailed}
	source := n.Source.Name()
	since := n.History
	if mode == Incremental {
		last, ok, err := n.Store.LastObsDate(ctx, source, s.ID)
		if err != nil {
			res.Err = err
			return res
		}
		if ok {
			since = latest(n.History, last.AddDate(-revisionWindow(s.Frequency), 0, 0))
		}
	}
	res.Since = since
	obs, err := n.Source.FetchSeries(ctx, s.Key, since)
	if err != nil {
		res.Err = err
		return res
	}
	now := n.now()
	cursor := since.Format(time.DateOnly)
	if len(obs) > 0 {
		cursor = obs[len(obs)-1].Date.Format(time.DateOnly)
	}
	err = n.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
			Source: source, SeriesID: s.ID, Label: s.Label, Agent: s.Agent, Frequency: s.Frequency, Units: s.Units,
		}); err != nil {
			return err
		}
		up, err := store.UpsertObservations(tx, source, s.ID, obs, now)
		if err != nil {
			return err
		}
		res.UpsertResult = up
		return store.SetFetchOK(tx, n.key(s.ID), cursor, now)
	})
	if err != nil {
		res.Err = fmt.Errorf("%s store %s: %w", source, s.ID, err)
		return res
	}
	res.Outcome = OutcomeUpdated
	return res
}

func (n *Numeric) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}
