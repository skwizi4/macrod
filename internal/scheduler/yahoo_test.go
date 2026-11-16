package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"macro-collector/internal/ingest"
)

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fixedClock) Sleep(context.Context, time.Duration) error {
	return nil
}
func (c *fixedClock) set(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	c.t = t
}

// A failed daily poll is retried for the failed symbols every tick until
// they load or YahooRetryFor passes.
func TestYahooKeeperRetries(t *testing.T) {
	clock := &fixedClock{}
	clock.set("2026-10-31T21:30:00Z")
	var polls [][]string
	failing := map[string]bool{"NQ=F": true, "GC=F": true}
	k := &YahooKeeper{
		Poll: func(_ context.Context, only []string) ingest.YahooReport {
			polls = append(polls, only)
			var rep ingest.YahooReport
			for _, s := range append([]string{"^NDX"}, "NQ=F", "GC=F") {
				if only != nil && !slices.Contains(only, s) {
					continue
				}
				r := ingest.SymbolResult{Symbol: s}
				if failing[s] {
					r.Err = errors.New("timeout")
				}
				rep.Results = append(rep.Results, r)
			}
			return rep
		},
		Clock: clock, Log: slog.New(slog.DiscardHandler),
	}
	ctx := context.Background()
	if err := k.Daily(ctx); err == nil {
		t.Fatal("no error for failed symbols")
	}
	clock.set("2026-10-31T22:00:00Z")
	k.Tick(ctx) // still failing
	delete(failing, "GC=F")
	clock.set("2026-10-31T22:30:00Z")
	k.Tick(ctx)
	if want := [][]string{nil, {"NQ=F", "GC=F"}, {"NQ=F", "GC=F"}}; !slices.EqualFunc(polls, want, slices.Equal) {
		t.Fatalf("polls = %v", polls)
	}
	// After YahooRetryFor from the daily poll: given up.
	clock.set("2026-11-01T03:31:00Z")
	k.Tick(ctx)
	k.Tick(ctx)
	if len(polls) != 3 {
		t.Errorf("polls after the retry window = %v", polls[3:])
	}
	// A clean daily poll leaves nothing to retry.
	failing = nil
	k.Daily(ctx)
	k.Tick(ctx)
	if len(polls) != 4 {
		t.Errorf("retried after a clean poll: %v", polls[4:])
	}
}

// Before the weekly export (Sunday 16:00) the symbols without Friday's bar
// are polled again, once.
func TestYahooKeeperTopUp(t *testing.T) {
	bts, _ := time.LoadLocation("Europe/Bratislava")
	ny, _ := time.LoadLocation("America/New_York")
	weekly, err := cron.ParseStandard("0 16 * * 0")
	if err != nil {
		t.Fatal(err)
	}
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	// BTC trades on weekends: Saturday's bar is the last closed one.
	last := map[string]time.Time{"^NDX": day("2026-10-30"), "NQ=F": day("2026-10-29"), "BTC-USD": day("2026-10-31"), "ETH-USD": day("2026-10-30")}
	clock := &fixedClock{}
	var polls [][]string
	k := &YahooKeeper{
		Poll: func(_ context.Context, only []string) ingest.YahooReport {
			polls = append(polls, only)
			return ingest.YahooReport{}
		},
		Symbols: func(time.Time) []string { return []string{"^NDX", "NQ=F", "BTC-USD", "ETH-USD", "ZQX27.CBT"} },
		LastDate: func(_ context.Context, s string) (time.Time, bool, error) {
			d, ok := last[s]
			return d, ok, nil
		},
		Weekly: weekly, Location: bts, Exchange: ny, Clock: clock, Log: slog.New(slog.DiscardHandler),
	}
	ctx := context.Background()
	clock.set("2026-11-01T13:50:00Z") // 14:50: the export is 70 min ahead
	k.Tick(ctx)
	if len(polls) != 0 {
		t.Fatalf("topped up too early: %v", polls)
	}
	clock.set("2026-11-01T14:20:00Z") // 15:20
	k.Tick(ctx)
	clock.set("2026-11-01T14:50:00Z") // 15:50: done already
	k.Tick(ctx)
	if want := [][]string{{"NQ=F", "ETH-USD", "ZQX27.CBT"}}; !slices.EqualFunc(polls, want, slices.Equal) {
		t.Errorf("polls = %v", polls)
	}
}
