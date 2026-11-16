package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/cboe"
	"macro-collector/internal/store"
)

// CboeClient is the part of cboe.Client that ingest uses.
type CboeClient interface {
	Day(ctx context.Context, day time.Time) (map[string]float64, bool, error)
}

// CboeResult is the result of a run.
type CboeResult struct {
	Since, Last time.Time // first day asked, last day stored
	Days        int       // days with a file
	Err         error
}

// Cboe loads the daily put/call ratios (FIXES_W44 F3), one request per
// weekday from the day after the last stored one.
type Cboe struct {
	Client  CboeClient
	Store   *store.Store
	History time.Time // first day of a backfill
	Agent   string
	Log     *slog.Logger
	Now     func() time.Time
}

// Run fetches the missing days; an error is recorded in fetch_state["cboe"]
// and keeps the days stored before it.
func (c *Cboe) Run(ctx context.Context, mode Mode) CboeResult {
	res := c.run(ctx, mode)
	if res.Err != nil && ctx.Err() == nil {
		c.Log.Error("cboe failed", "err", res.Err)
		if err := c.Store.Write(ctx, func(tx *sql.Tx) error {
			return store.SetFetchError(tx, cboe.Source, res.Err, c.now())
		}); err != nil {
			c.Log.Error("record fetch error", "err", err)
		}
	} else if res.Err == nil {
		c.Log.Info("cboe done", "since", res.Since.Format(time.DateOnly), "days", res.Days, "last", res.Last.Format(time.DateOnly))
	}
	return res
}

func (c *Cboe) run(ctx context.Context, mode Mode) CboeResult {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		return CboeResult{Err: err}
	}
	now := c.now()
	res := CboeResult{Since: c.History}
	if mode == Incremental {
		last, ok, err := c.Store.LastObsDate(ctx, cboe.Source, cboe.SeriesTotal)
		if err != nil {
			res.Err = err
			return res
		}
		if ok {
			res.Since = latest(c.History, last.AddDate(0, 0, 1))
		}
	}
	today := model.DayIn(now, ny)
	err = c.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, s := range cboe.Series {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
				Source: cboe.Source, SeriesID: s.ID, Label: s.Label, Agent: c.Agent, Frequency: "d", Units: "Ratio",
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		res.Err = fmt.Errorf("cboe meta: %w", err)
		return res
	}
	for d := model.Day(res.Since); !d.After(today); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		if ctx.Err() != nil {
			res.Err = ctx.Err()
			return res
		}
		ratios, ok, err := c.Client.Day(ctx, d)
		if err != nil {
			res.Err = err
			return res
		}
		if !ok {
			continue // a holiday, or the day is not published yet
		}
		day := d
		if err := c.Store.Write(ctx, func(tx *sql.Tx) error {
			for id, v := range ratios {
				if _, err := store.UpsertObservations(tx, cboe.Source, id, []model.Observation{{Date: day, Value: v}}, now); err != nil {
					return err
				}
			}
			return store.SetFetchOK(tx, cboe.Source, day.Format(time.DateOnly), now)
		}); err != nil {
			res.Err = fmt.Errorf("cboe store: %w", err)
			return res
		}
		res.Days++
		res.Last = day
	}
	if res.Days == 0 {
		// Nothing new (a weekend): the source still answered.
		if err := c.Store.Write(ctx, func(tx *sql.Tx) error {
			return store.SetFetchOK(tx, cboe.Source, res.Since.Format(time.DateOnly), now)
		}); err != nil {
			res.Err = fmt.Errorf("cboe store: %w", err)
		}
	}
	return res
}

func (c *Cboe) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
