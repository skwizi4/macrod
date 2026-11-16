package collect

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/nasdaq"
	"macro-collector/internal/store"
)

type fakeEarnings map[string]nasdaq.EarningsDate

func (f fakeEarnings) EarningsDate(_ context.Context, ticker string) (nasdaq.EarningsDate, error) {
	d, ok := f[ticker]
	if !ok {
		return nasdaq.EarningsDate{}, errors.New("nasdaq earnings " + ticker + ": status 503")
	}
	d.Ticker = ticker
	return d, nil
}

func earningsConfig(t *testing.T, tickers ...string) *config.Config {
	t.Helper()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Calendar: config.Calendar{HorizonDays: 14, ReleaseLocation: ny},
		Earnings: config.EarningsCalendar{Enabled: true, AfterClose: config.ClockTime{Hour: 16}, PreMarket: config.ClockTime{Hour: 7}, Unknown: config.ClockTime{Hour: 16}},
		EDGAR:    config.EDGAR{Enabled: true},
	}
	for _, tk := range tickers {
		cfg.EDGAR.Companies = append(cfg.EDGAR.Companies, config.Company{Ticker: tk, Importance: "high"})
	}
	return cfg
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func names(evs []model.ReleaseEvent, status string) []string {
	var out []string
	for _, e := range evs {
		if e.Source == nasdaq.Source && e.Status == status {
			out = append(out, e.Name+" "+e.ScheduledAt.Format(time.DateOnly))
		}
	}
	slices.Sort(out)
	return out
}

// A company Nasdaq has no date for (reported already) or that fails keeps
// its stored event; a moved date marks the old event rescheduled.
func TestRefreshEarnings(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	cfg := earningsConfig(t, "MSFT", "AMZN", "META")
	log := slog.New(slog.DiscardHandler)
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 3)
	later := day.AddDate(0, 0, 2)

	first := fakeEarnings{
		"MSFT": {Date: day, Time: nasdaq.TimeAfterClose, OK: true},
		"AMZN": {Date: day, Time: nasdaq.TimeAfterClose, OK: true},
		"META": {Date: later, Estimated: true, OK: true},
	}
	rep, _, err := refreshEarnings(ctx, cfg, st, first, log)
	if err != nil || rep.Inserted != 3 {
		t.Fatalf("first refresh: %+v, %v", rep, err)
	}

	second := fakeEarnings{
		"MSFT": {Date: later, Time: nasdaq.TimeAfterClose, OK: true}, // moved
		"META": {},                                                   // no date now: keep
		// AMZN fails: keep
	}
	rep, noDate, err := refreshEarnings(ctx, cfg, st, second, log)
	if err == nil || rep.Rescheduled != 1 || !slices.Equal(noDate, []string{"META"}) {
		t.Errorf("second refresh: %+v, no date %v, err %v", rep, noDate, err)
	}
	evs, err := st.Releases(ctx, day.AddDate(0, 0, -1), day.AddDate(0, 0, 10))
	if err != nil {
		t.Fatal(err)
	}
	d1, d2 := day.Format(time.DateOnly), later.Format(time.DateOnly)
	if got, want := names(evs, model.StatusScheduled), []string{"AMZN earnings " + d1, "META earnings " + d2, "MSFT earnings " + d2}; !slices.Equal(got, want) {
		t.Errorf("scheduled = %v, want %v", got, want)
	}
	if got := names(evs, model.StatusRescheduled); !slices.Equal(got, []string{"MSFT earnings " + d1}) {
		t.Errorf("rescheduled = %v", got)
	}

	if _, _, err := refreshEarnings(ctx, cfg, st, fakeEarnings{}, log); err == nil {
		t.Error("every company failing should be an error")
	}
}

func TestEarningsClientDisabled(t *testing.T) {
	cfg := earningsConfig(t, "MSFT")
	if NewEarningsClient(cfg, slog.New(slog.DiscardHandler)) == nil {
		t.Error("no client for an enabled calendar")
	}
	cfg.Earnings.Enabled = false
	if NewEarningsClient(cfg, slog.New(slog.DiscardHandler)) != nil {
		t.Error("client for a disabled calendar")
	}
}
