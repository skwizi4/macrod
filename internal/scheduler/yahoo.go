package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"macro-collector/internal/ingest"
	"macro-collector/internal/sources/yahoo"
)

// Yahoo market data is polled once a day (yahoo.poll_us). Two safety nets
// keep the weekly export from reading a stale close (W44: Friday's futures
// bars came after the Sunday export, the daemon being down on Saturday):
// a failed poll is retried for the failed symbols every
// YahooRetryEvery up to YahooRetryFor, and shortly before the weekly
// export every symbol without the last trading day's bar is polled again.
const (
	YahooRetryEvery = 30 * time.Minute
	YahooRetryFor   = 6 * time.Hour
	// yahooCatchUpBefore: the top-up runs when the weekly export is at
	// most this far ahead (a tick is every YahooRetryEvery), so its rows
	// are fetched before the period end and the export sees them.
	yahooCatchUpBefore = 45 * time.Minute
	yahooCatchUpMin    = 5 * time.Minute
)

// YahooKeeper runs the daily Yahoo poll, its retries and the pre-export
// top-up.
type YahooKeeper struct {
	// Poll polls the symbols in only, or all of them when only is nil.
	Poll func(ctx context.Context, only []string) ingest.YahooReport
	// Symbols are all the symbols polled at now.
	Symbols func(now time.Time) []string
	// LastDate is the date of a symbol's latest bar; ok is false if none.
	LastDate func(ctx context.Context, symbol string) (date time.Time, ok bool, err error)
	Weekly   cron.Schedule  // the weekly export
	Location *time.Location // config timezone, of Weekly
	Exchange *time.Location // America/New_York: bar dates
	// ExchangeOf, if set, is the exchange time zone of a symbol (its
	// exchange_tz in the config); nil or a nil result: Exchange.
	ExchangeOf func(symbol string) *time.Location
	Clock      Clock // nil: real time
	Log        *slog.Logger

	poll       sync.Mutex // one poll at a time
	mu         sync.Mutex
	failed     []string
	retryUntil time.Time
	toppedUp   time.Time // the weekly export the top-up ran for
}

// Daily is the daily poll: all symbols; failures are retried.
func (k *YahooKeeper) Daily(ctx context.Context) error {
	rep := k.run(ctx, nil)
	k.mu.Lock()
	defer k.mu.Unlock()
	k.failed, k.retryUntil = failedSymbols(rep), time.Time{}
	if len(k.failed) > 0 {
		k.retryUntil = k.now().Add(YahooRetryFor)
	}
	return reportErr(rep)
}

// Tick runs every YahooRetryEvery: a pending retry, then the top-up if the
// weekly export is near.
func (k *YahooKeeper) Tick(ctx context.Context) {
	now := k.now()
	k.mu.Lock()
	retry := slices.Clone(k.failed)
	if len(retry) > 0 && now.After(k.retryUntil) {
		k.Log.Warn("yahoo retries given up", "symbols", len(retry), "first", retry[0])
		retry, k.failed = nil, nil
	}
	k.mu.Unlock()
	if len(retry) > 0 {
		rep := k.run(ctx, retry)
		still := failedSymbols(rep)
		k.mu.Lock()
		k.failed = still
		k.mu.Unlock()
		logResult(k.Log, "yahoo retry", reportErr(rep), "symbols", len(retry), "still_failed", len(still))
	}
	if k.Weekly == nil {
		return
	}
	export := k.Weekly.Next(now.In(k.Location))
	if ahead := export.Sub(now); ahead > yahooCatchUpBefore || ahead < yahooCatchUpMin {
		return
	}
	k.mu.Lock()
	done := k.toppedUp.Equal(export)
	k.toppedUp = export
	k.mu.Unlock()
	if done {
		return
	}
	if err := k.topUp(ctx, export); err != nil {
		k.Log.Error("yahoo top-up before the weekly export failed", "err", err)
	}
}

// topUp polls the symbols whose latest bar is older than the last bar
// closed at export: the last trading day, the last calendar day for crypto
// (yahoo.LastBarDay).
func (k *YahooKeeper) topUp(ctx context.Context, export time.Time) error {
	day := yahoo.LastTradingDay(export, k.Exchange)
	var stale []string
	for _, s := range k.Symbols(k.now()) {
		d, ok, err := k.LastDate(ctx, s)
		if err != nil {
			return fmt.Errorf("yahoo last bar %s: %w", s, err)
		}
		if !ok || d.Before(yahoo.LastBarDay(export, s, k.exchange(s))) {
			stale = append(stale, s)
		}
	}
	if len(stale) == 0 {
		k.Log.Info("yahoo top-up: market data complete", "trading_day", day.Format(time.DateOnly))
		return nil
	}
	rep := k.run(ctx, stale)
	inserted := 0
	for _, r := range rep.Results {
		inserted += r.Inserted
	}
	logResult(k.Log, "yahoo top-up", reportErr(rep), "trading_day", day.Format(time.DateOnly),
		"stale_symbols", len(stale), "first", stale[0], "inserted", inserted)
	return nil
}

func (k *YahooKeeper) exchange(symbol string) *time.Location {
	if k.ExchangeOf != nil {
		if loc := k.ExchangeOf(symbol); loc != nil {
			return loc
		}
	}
	return k.Exchange
}

func (k *YahooKeeper) run(ctx context.Context, only []string) ingest.YahooReport {
	k.poll.Lock()
	defer k.poll.Unlock()
	return k.Poll(ctx, only)
}

func (k *YahooKeeper) now() time.Time {
	if k.Clock != nil {
		return k.Clock.Now()
	}
	return time.Now()
}

func failedSymbols(rep ingest.YahooReport) []string {
	var out []string
	for _, f := range rep.Failed() {
		out = append(out, f.Symbol)
	}
	return out
}

func reportErr(rep ingest.YahooReport) error {
	f := rep.Failed()
	if len(f) == 0 {
		return nil
	}
	return fmt.Errorf("%d symbols failed, first %s: %w", len(f), f[0].Symbol, f[0].Err)
}
