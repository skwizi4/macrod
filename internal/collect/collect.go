// Package collect runs a full collection pass: load FRED, then recompute
// derived series. Shared by macroctl and the daemon's daily job.
package collect

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/derived"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/store"
)

// Summary is the result of one pass.
type Summary struct {
	FRED     ingest.Report
	Derived  []derived.Result
	Duration time.Duration
	// Interrupted is set when ctx was cancelled before the pass finished.
	Interrupted bool
}

// Failures is the number of failed FRED and derived series.
func (s Summary) Failures() int {
	n := len(s.FRED.Failed())
	for _, r := range s.Derived {
		if r.Outcome == derived.OutcomeFailed {
			n++
		}
	}
	return n
}

// Rows returns inserted and revised FRED rows.
func (s Summary) Rows() (inserted, revised int) {
	for _, r := range s.FRED.Results {
		inserted += r.Inserted
		revised += r.Revised
	}
	return inserted, revised
}

// RunFRED loads the configured FRED series and then recomputes derived
// series from whatever is stored, even if some inputs failed this time.
func RunFRED(ctx context.Context, cfg *config.Config, st *store.Store, client ingest.FREDClient,
	root *slog.Logger, mode ingest.Mode, since time.Time) Summary {
	start := time.Now()
	ing := &ingest.FRED{
		Client:  client,
		Store:   st,
		Series:  cfg.FRED.Series,
		History: cfg.History,
		Log:     logging.For(root, "fred"),
	}
	rep := ing.Run(ctx, mode, since)
	comp := &derived.Computer{Store: st, Config: cfg, Defs: cfg.Derived, Log: logging.For(root, "derived")}
	d := comp.Run(ctx)
	return Summary{
		FRED: rep, Derived: d, Duration: time.Since(start),
		Interrupted: rep.Interrupted || ctx.Err() != nil,
	}
}

// RunFREDSeries fetches only the given configured series (incremental) and
// recomputes derived series if any row changed. Used by release pollers.
func RunFREDSeries(ctx context.Context, cfg *config.Config, st *store.Store, client ingest.FREDClient,
	root *slog.Logger, ids []string) Summary {
	start := time.Now()
	var series []config.Series
	for _, s := range cfg.FRED.Series {
		if slices.Contains(ids, s.ID) {
			series = append(series, s)
		}
	}
	ing := &ingest.FRED{Client: client, Store: st, Series: series, History: cfg.History, Log: logging.For(root, "fred")}
	sum := Summary{FRED: ing.Run(ctx, ingest.Incremental, time.Time{})}
	if inserted, revised := sum.Rows(); inserted+revised > 0 && ctx.Err() == nil {
		comp := &derived.Computer{Store: st, Config: cfg, Defs: cfg.Derived, Log: logging.For(root, "derived")}
		sum.Derived = comp.Run(ctx)
	}
	sum.Duration = time.Since(start)
	sum.Interrupted = sum.FRED.Interrupted || ctx.Err() != nil
	return sum
}
