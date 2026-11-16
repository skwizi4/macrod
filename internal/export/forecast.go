package export

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/forecast"
	"macro-collector/internal/store"
)

// ForecastResult is the forecast log of a region after a sync (phase 13).
type ForecastResult struct {
	Path string // the scorecard, relative to the exchange directory; "" — no forecast agent in the region
	forecast.SyncResult
}

// SyncForecasts imports the forecasts of region's master reports, scores
// what is due at now and renders forecast_scorecard.md. A region without
// the forecast agent (or a disabled log) gives an empty result.
func SyncForecasts(ctx context.Context, st *store.Store, cfg *config.Config, r *config.Region, now time.Time) (ForecastResult, error) {
	if !cfg.Forecast.Enabled {
		return ForecastResult{}, nil
	}
	a := r.AgentByID(cfg.Forecast.Agent)
	if a == nil {
		return ForecastResult{}, nil
	}
	log := &forecast.Log{Store: st, Config: cfg}
	res, err := log.Sync(ctx, r.ID, now)
	if err != nil {
		return ForecastResult{}, err
	}
	return ForecastResult{Path: r.Path(a, forecast.ScorecardFile), SyncResult: res}, nil
}

// WriteScorecard writes the scorecard to the exchange directory and, with
// isoWeek, to archive/<isoWeek>/ (the weekly export; a manual score leaves
// the archive of a past export as it was); it returns the paths written.
func WriteScorecard(cfg *config.Config, isoWeek string, fr ForecastResult) ([]string, error) {
	if fr.Path == "" {
		return nil, errors.New("forecast scorecard: no forecast agent in the region")
	}
	exch := cfg.Export.ExchangeDir
	paths := []string{filepath.Join(exch, fr.Path)}
	if isoWeek != "" {
		paths = append(paths, filepath.Join(exch, "archive", isoWeek, fr.Path))
	}
	var out []string
	for _, p := range paths {
		if err := writeAtomic(p, fr.Scorecard); err != nil {
			return out, err
		}
		out = append(out, p)
	}
	return out, nil
}

// writeForecasts runs the forecast log at the weekly export: last week's
// master report is imported and scored on the week that has just ended. A
// failure is a warning: the export goes on without a fresh scorecard.
func (x *Weekly) writeForecasts(ctx context.Context, w *week, res *WeeklyResult) {
	fr, err := SyncForecasts(ctx, x.Store, x.Config, w.region, w.now)
	if err == nil && fr.Path != "" {
		var written []string
		written, err = WriteScorecard(x.Config, w.iso, fr)
		res.Written = append(res.Written, written...)
	}
	if err != nil {
		x.log().Warn("forecast log failed, scorecard not updated", "region", w.region.ID, "err", err)
		res.ForecastErr = err.Error()
		return
	}
	if fr.Path == "" {
		return
	}
	// Every export rereads all reports: warn about last week's only, the
	// older ones were warned about before and stay listed in the scorecard.
	last := ISOWeek(w.from.Add(-time.Minute), w.loc)
	for _, n := range fr.Import.Notes {
		lvl := slog.LevelDebug
		if n.Week == last {
			lvl = slog.LevelWarn
		}
		x.log().Log(ctx, lvl, "master report without a usable forecast block", "region", w.region.ID, "iso_week", n.Week, "err", n.Err)
	}
	if len(fr.Score.Overdue) > 0 {
		x.log().Warn("forecasts without closes for their week", "region", w.region.ID, "forecasts", fr.Score.Overdue)
	}
	res.Forecast = &fr
}
