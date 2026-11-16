package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// IntradayClient is the part of yahoo.Client that ingest uses.
type IntradayClient interface {
	Intraday(ctx context.Context, symbol string, from, to time.Time) ([]yahoo.MinuteBar, error)
}

// IntradayResult is the result of a run.
type IntradayResult struct {
	Events int // event times loaded
	Bars   int
	Err    error
}

// IntradayFetchKey is the fetch_state row of the one-minute bars (of the
// first region's reaction; others add ":<region>").
const IntradayFetchKey = yahoo.Source + ":intraday"

// barsBefore is how early the bars of an event start: the price at the
// event is the last bar before it.
const barsBefore = 15 * time.Minute

// Intraday stores one-minute bars around high-importance events (FIXES_W44
// F1): from shortly before the event to the close the reaction runs to,
// once that close is fetch_delay behind and while Yahoo still has the bars.
type Intraday struct {
	Client   IntradayClient
	Store    *store.Store
	Reaction config.Reaction
	Log      *slog.Logger
	Now      func() time.Time
	// FetchKey is the fetch_state key of the run; default "intraday" (the
	// first region's reaction), "intraday:<region>" for the others.
	FetchKey string
}

func (in *Intraday) fetchKey() string {
	if in.FetchKey != "" {
		return in.FetchKey
	}
	return IntradayFetchKey
}

// Run loads the bars of events that need them; events at the same moment
// share one window. Events with bars stored already are skipped.
func (in *Intraday) Run(ctx context.Context, events []model.ReleaseEvent) IntradayResult {
	var res IntradayResult
	if len(in.Reaction.BarSymbols()) == 0 {
		return res
	}
	loc, cl := in.Reaction.Location, in.Reaction.CloseAt
	if loc == nil {
		res.Err = fmt.Errorf("intraday: reaction timezone not resolved")
		return res
	}
	now := in.now()
	seen := map[time.Time]bool{}
	var errs []error
	for _, e := range events {
		if _, _, ok := in.Reaction.Match(e); !ok || seen[e.ScheduledAt] {
			continue
		}
		seen[e.ScheduledAt] = true
		closeAt := transform.ReactionClose(e.ScheduledAt, loc, cl.Hour, cl.Minute)
		from, to := e.ScheduledAt.Add(-barsBefore), closeAt.Add(time.Minute)
		if now.Before(closeAt.Add(in.Reaction.FetchDelay)) || now.Sub(from) > yahoo.MinuteHistory {
			continue // not over yet, or Yahoo has dropped the bars
		}
		// Windows of nearby events overlap: the event is loaded when the
		// moments its reaction reads (the event, the end of the first
		// reaction, the close) all have bars.
		first := e.ScheduledAt.Add(in.Reaction.Window)
		have, err := in.loaded(ctx, [][2]time.Time{{from, e.ScheduledAt}, {first.Add(-barsBefore), first}, {closeAt.Add(-closeSlack), to}})
		if err != nil {
			res.Err = err
			return res
		}
		if have {
			continue
		}
		n, err := in.window(ctx, from, to, now)
		res.Bars += n
		if err != nil {
			errs = append(errs, fmt.Errorf("event %s %s: %w", e.Name, e.ScheduledAt.Format(time.RFC3339), err))
			continue
		}
		res.Events++
	}
	res.Err = errors.Join(errs...)
	if res.Events == 0 && res.Err == nil {
		return res // nothing was due
	}
	if werr := in.Store.Write(ctx, func(tx *sql.Tx) error {
		if res.Err != nil {
			return store.SetFetchError(tx, in.fetchKey(), res.Err, now)
		}
		return store.SetFetchOK(tx, in.fetchKey(), now.Format(time.RFC3339), now)
	}); werr != nil && res.Err == nil {
		res.Err = werr
	}
	if res.Err != nil && ctx.Err() == nil {
		in.Log.Error("intraday bars failed", "err", res.Err)
	} else if res.Events > 0 {
		in.Log.Info("intraday bars done", "events", res.Events, "bars", res.Bars)
	}
	return res
}

// closeSlack: a session closing early (a holiday) still counts as loaded
// when it has bars within this time before the regular close.
const closeSlack = 6 * time.Hour

// loaded reports whether the first symbol has bars in every range.
func (in *Intraday) loaded(ctx context.Context, ranges [][2]time.Time) (bool, error) {
	sym := in.Reaction.BarSymbols()[0].Symbol
	for _, r := range ranges {
		bars, err := in.Store.IntradayBars(ctx, sym, r[0], r[1])
		if err != nil || len(bars) == 0 {
			return false, err
		}
	}
	return true, nil
}

func (in *Intraday) window(ctx context.Context, from, to, now time.Time) (int, error) {
	n := 0
	for _, s := range in.Reaction.BarSymbols() {
		bars, err := in.Client.Intraday(ctx, s.Symbol, from, to)
		if err != nil {
			return n, err
		}
		rows := make([]store.IntradayBar, len(bars))
		for i, b := range bars {
			rows[i] = store.IntradayBar{TS: b.TS, Close: b.Close}
		}
		keep := now.AddDate(0, 0, -in.Reaction.KeepDays)
		if err := in.Store.Write(ctx, func(tx *sql.Tx) error {
			return store.UpsertIntradayBars(tx, s.Symbol, rows, keep)
		}); err != nil {
			return n, fmt.Errorf("intraday store %s: %w", s.Symbol, err)
		}
		n += len(rows)
	}
	return n, nil
}

func (in *Intraday) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}
