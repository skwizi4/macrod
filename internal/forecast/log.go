package forecast

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/period"
	"macro-collector/internal/store"
)

// ScorecardFile is the file in the master's folder of exchange/.
const ScorecardFile = "forecast_scorecard.md"

var weekDir = regexp.MustCompile(`^\d{4}-W\d{2}$`)

// Log imports and scores the forecasts of one config.
type Log struct {
	Store  *store.Store
	Config *config.Config
}

// Note is what a week's report gave besides forecasts: no block or a
// block that does not parse.
type Note struct {
	Week string
	Err  error // ErrNoBlock or the parse error
}

// ImportResult is the outcome of an import of a region.
type ImportResult struct {
	Weeks   int    // reports read
	Items   int    // forecasts in them
	Changed int    // rows added, changed or deleted
	Notes   []Note // weeks without a usable block
}

// Import reads the master's reports of region in reports/<region>/<week>/
// (only week when not empty) and stores their forecasts. A report without
// a block or with a broken one is a note; its rows stay as they were.
func (l *Log) Import(ctx context.Context, region, week string, now time.Time) (ImportResult, error) {
	var res ImportResult
	weeks, err := l.reportWeeks(region)
	if err != nil {
		return res, err
	}
	for _, wk := range weeks {
		if week != "" && wk != week {
			continue
		}
		path := l.reportPath(region, wk)
		md, err := os.ReadFile(path)
		if err != nil {
			return res, fmt.Errorf("forecast read %s: %w", path, err)
		}
		res.Weeks++
		items, err := Parse(md, func(name string) bool { _, ok := l.Config.ForecastInstrument(name); return ok })
		if err != nil {
			res.Notes = append(res.Notes, Note{Week: wk, Err: err})
			continue
		}
		res.Items += len(items)
		rel := filepath.ToSlash(filepath.Join("reports", region, wk, l.Config.Forecast.Agent+".md"))
		if err := l.Store.Write(ctx, func(tx *sql.Tx) error {
			n, err := store.ReplaceForecasts(tx, region, wk, rel, items, now)
			res.Changed += n
			return err
		}); err != nil {
			return res, err
		}
	}
	return res, nil
}

// ScoreResult counts the rows of a region after scoring.
type ScoreResult struct {
	Scored  int // rows whose score this run set or changed
	Pending int // still pending
	// Overdue are the pending ones whose forecast week has ended: no closes
	// of the instrument (or it left the config). "<week> <instrument>".
	Overdue []string
}

// recheckDays: a score is recomputed while its forecast week ended less
// than this long ago, so a close loaded late (a failed Friday poll) replaces
// the one the forecast was first scored on.
const recheckDays = 14

// Score scores the pending forecasts of region whose week has ended and
// rechecks the recent scores.
func (l *Log) Score(ctx context.Context, region string, now time.Time) (ScoreResult, error) {
	var res ScoreResult
	rows, err := l.Store.Forecasts(ctx, region)
	if err != nil {
		return res, err
	}
	for _, f := range rows {
		pending := f.Outcome == store.OutcomePending
		if !pending && !l.recent(f.ISOWeek, now) {
			continue
		}
		sc, ok, err := l.scoreOne(ctx, f, now)
		if err != nil {
			return res, err
		}
		if !ok {
			if pending {
				res.Pending++
				if l.overdue(f.ISOWeek, now) {
					res.Overdue = append(res.Overdue, f.ISOWeek+" "+f.Instrument)
				}
			}
			continue // a score is not undone by missing prices
		}
		if !pending && sameScore(f, sc) {
			continue
		}
		if err := l.Store.Write(ctx, func(tx *sql.Tx) error { return store.SetForecastScore(tx, f.ID, sc, now) }); err != nil {
			return res, err
		}
		res.Scored++
	}
	return res, nil
}

func sameScore(f store.Forecast, sc store.ForecastScore) bool {
	return f.Outcome == sc.Outcome && f.PriceSeries == sc.PriceSeries &&
		f.RefDate.Equal(sc.RefDate) && f.RefPrice == sc.RefPrice &&
		f.ResultDate.Equal(sc.ResultDate) && f.ResultPrice == sc.ResultPrice
}

func (l *Log) scoreOne(ctx context.Context, f store.Forecast, now time.Time) (store.ForecastScore, bool, error) {
	in, ok := l.Config.ForecastInstrument(f.Instrument)
	if !ok {
		return store.ForecastScore{}, false, nil // dropped from the config: left pending
	}
	from, err := period.EndOfWeek(f.ISOWeek, l.Config.Location)
	if err != nil {
		return store.ForecastScore{}, false, err
	}
	w := Window{From: from, To: from.AddDate(0, 0, 7)}
	primary, err := l.prices(ctx, in.Price, from)
	if err != nil {
		return store.ForecastScore{}, false, err
	}
	var fallback Prices
	if in.Fallback != "" {
		if fallback, err = l.prices(ctx, in.Fallback, from); err != nil {
			return store.ForecastScore{}, false, err
		}
	}
	sc, ok := Score(f.ForecastItem, w, primary, fallback, l.Config.Forecast.RangePct, now)
	return sc, ok, nil
}

func (l *Log) prices(ctx context.Context, name string, from time.Time) (Prices, error) {
	s, ok := l.Config.ResolveSeries(name)
	if !ok {
		return Prices{}, fmt.Errorf("forecast price %q: not a configured series", name)
	}
	obs, err := l.Store.Observations(ctx, s.Source, s.ID, from.AddDate(0, 0, -14))
	if err != nil {
		return Prices{}, err
	}
	return Prices{Series: s.Ref(), Closes: obs}, nil
}

// SyncResult is the outcome of Sync.
type SyncResult struct {
	Import    ImportResult
	Score     ScoreResult
	Scorecard string // the rendered file
}

// Sync imports all reports of region, scores what is due and renders the
// scorecard; the caller writes it.
func (l *Log) Sync(ctx context.Context, region string, now time.Time) (SyncResult, error) {
	var res SyncResult
	var err error
	if res.Import, err = l.Import(ctx, region, "", now); err != nil {
		return res, err
	}
	if res.Score, err = l.Score(ctx, region, now); err != nil {
		return res, err
	}
	rows, err := l.Store.Forecasts(ctx, region)
	if err != nil {
		return res, err
	}
	primary := map[string]string{}
	for _, in := range l.Config.Forecast.Instruments {
		if s, ok := l.Config.ResolveSeries(in.Price); ok {
			primary[in.Name] = s.Ref()
		}
	}
	res.Scorecard = Render(Card{
		Title: l.Config.RegionTitle(region), Rows: rows, Notes: res.Import.Notes, Primary: primary,
		RangePct: l.Config.Forecast.RangePct, Now: now, Loc: l.Config.Location,
	})
	return res, nil
}

// reportWeeks lists the weeks of region that have the master's report.
func (l *Log) reportWeeks(region string) ([]string, error) {
	dir := filepath.Join(l.Config.Export.ReportsDir, region)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("forecast reports %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !weekDir.MatchString(e.Name()) {
			continue
		}
		if _, err := os.Stat(l.reportPath(region, e.Name())); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func (l *Log) reportPath(region, week string) string {
	return filepath.Join(l.Config.Export.ReportsDir, region, week, l.Config.Forecast.Agent+".md")
}

// overdue: the forecast week of a report week has ended at now.
func (l *Log) overdue(week string, now time.Time) bool {
	end, err := period.EndOfWeek(week, l.Config.Location)
	return err == nil && !now.Before(end.AddDate(0, 0, 7))
}

// recent: the forecast week of a report week ended less than recheckDays ago.
func (l *Log) recent(week string, now time.Time) bool {
	end, err := period.EndOfWeek(week, l.Config.Location)
	return err == nil && now.Before(end.AddDate(0, 0, 7+recheckDays))
}
