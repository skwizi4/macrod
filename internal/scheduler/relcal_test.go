package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

func euConfig(t *testing.T) *config.Config {
	t.Helper()
	root := filepath.Join("..", "..")
	cfg, err := config.LoadAt(filepath.Join(root, "presets", "eu-equities.yaml"), root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// fakeZEW serves one monthly value per series; calls counts the requests.
type fakeZEW struct {
	month time.Time
	calls int
}

func (f *fakeZEW) Name() string { return config.SourceZEW }

func (f *fakeZEW) FetchSeries(_ context.Context, _ string, _ time.Time) ([]model.Observation, error) {
	f.calls++
	return []model.Observation{{Date: f.month, Value: 30}}, nil
}

func TestSeriesReleaseHandler(t *testing.T) {
	cfg := euConfig(t)
	st := openStore(t)
	ctx := context.Background()
	src := &fakeZEW{month: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	now := time.Date(2026, 10, 20, 9, 10, 0, 0, time.UTC)
	runner := collect.NumericRunner{Numeric: &ingest.Numeric{Source: src, Store: st, Series: cfg.ZEW.Series, History: cfg.History,
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}}
	if rep := runner.Run(ctx, ingest.Incremental); len(rep.Failed()) > 0 { // October, loaded on its release day
		t.Fatalf("load: %+v", rep)
	}
	h := &SeriesRelease{Config: cfg, Store: st, Numeric: []collect.NumericRunner{runner}, Root: slog.New(slog.DiscardHandler)}
	ev := model.ReleaseEvent{Source: "relcal:zew", Name: "ZEW (Германия)", ScheduledAt: time.Date(2026, 11, 17, 10, 5, 0, 0, time.UTC),
		Related: model.Related{Series: []string{"ZEW_SENTIMENT", "ZEW_SITUATION"}}}

	// November not out yet: the source is asked, nothing changes.
	now = time.Date(2026, 11, 17, 10, 6, 0, 0, time.UTC)
	if done, err := h.Poll(ctx, ev); done || err != nil || src.calls != 4 {
		t.Fatalf("poll 1: %v %v, calls %d", done, err, src.calls)
	}
	if h.Expire(ctx, ev) != model.StatusTimeout {
		t.Error("expire with nothing new should time out")
	}
	// November arrives: done, and the next poll does not ask the source.
	src.month = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if done, err := h.Poll(ctx, ev); !done || err != nil {
		t.Fatalf("poll 2: %v %v", done, err)
	}
	calls := src.calls
	if done, _ := h.Poll(ctx, ev); !done || src.calls != calls {
		t.Errorf("poll 3: %v, calls %d → %d", done, calls, src.calls)
	}

	// An event without series is in the calendar only: done at once.
	if done, err := h.Poll(ctx, model.ReleaseEvent{Source: "relcal:ec_survey", Name: "flash", ScheduledAt: ev.ScheduledAt}); !done || err != nil {
		t.Errorf("no series: %v %v", done, err)
	}
}

func TestBundAuctionHeld(t *testing.T) {
	cfg := euConfig(t)
	st := openStore(t)
	ctx := context.Background()
	h := &BundAuction{Config: cfg, Store: st}
	ev := model.ReleaseEvent{Source: "bund_auction", Name: "Bund 10 years", ScheduledAt: time.Date(2026, 11, 25, 10, 30, 0, 0, time.UTC),
		Related: model.Related{CUSIP: "DE000BU2Z072", Security: "Bund", Tenor: "10Y"}}
	if ok, err := h.held(ctx, ev); ok || err != nil {
		t.Fatalf("held before results: %v %v", ok, err)
	}
	day := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := store.UpsertAuctions(tx, []model.Auction{
			{Source: "finanzagentur", AuctionDate: day, CUSIP: "DE000BU2Z072", SecurityType: "Bund", Kind: "Bund", Term: "10 Y", Tenor: "10Y",
				BidToCover: model.Some(1.4), AvgYield: model.Some(3.3)},
			{Source: "finanzagentur", AuctionDate: day, CUSIP: "DE000BU2D012", SecurityType: "Bund", Kind: "Bund", Term: "30 Y", Tenor: "30Y",
				BidToCover: model.Some(1.2), AvgYield: model.Some(3.6)},
		}, day)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := h.held(ctx, ev); !ok || err != nil {
		t.Errorf("held after results: %v %v", ok, err)
	}
	// A range of terms has no ISIN: any Bund of the day closes it.
	rng := ev
	rng.Name, rng.Related = "Bund 15/20/30 years", model.Related{Security: "Bund"}
	if ok, _ := h.held(ctx, rng); !ok {
		t.Error("range not held")
	}
	other := ev
	other.ScheduledAt = ev.ScheduledAt.AddDate(0, 0, 1)
	if ok, _ := h.held(ctx, other); ok {
		t.Error("held on another day")
	}
}
