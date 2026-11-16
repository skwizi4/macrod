package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

// fakeTreasury publishes auction results from resultsAt on (virtual clock).
type fakeTreasury struct {
	clock     Clock
	resultsAt time.Time
	tga       []model.Observation
	mu        sync.Mutex
	calls     map[string]int
}

func (f *fakeTreasury) count(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *fakeTreasury) FetchTGA(context.Context, time.Time) ([]model.Observation, error) {
	f.count("dts")
	return f.tga, nil
}

func (f *fakeTreasury) FetchMTS(context.Context, time.Time) (map[string][]model.Observation, error) {
	f.count("mts")
	return nil, nil
}

func (f *fakeTreasury) FetchPublicDebt(context.Context, time.Time) (map[string][]model.Observation, error) {
	f.count("public_debt")
	return nil, nil
}

func (f *fakeTreasury) FetchAuctions(context.Context, []string, time.Time, time.Time) ([]model.Auction, error) {
	f.count("auctions")
	a := model.Auction{AuctionDate: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), SecurityType: "Note", Kind: "Note",
		Term: "2-Year", Tenor: "2-Year", CUSIP: "91282CRU7", ClosingTime: "13:00"}
	if !f.clock.Now().Before(f.resultsAt) {
		a.HighYield, a.BidToCover = model.Some(3.49), model.Some(2.63)
	}
	return []model.Auction{a}, nil
}

func (f *fakeTreasury) FetchBuybacks(context.Context, time.Time) ([]model.Buyback, error) {
	f.count("buybacks")
	return nil, nil
}

func treasuryConfig() *config.Treasury {
	return &config.Treasury{Datasets: []config.TreasuryDataset{
		{Name: config.DatasetDTS, Agent: "usa_fiscal", Enabled: true},
		{Name: config.DatasetMTS, Agent: "usa_fiscal", Enabled: true},
		{Name: config.DatasetAuctions, Agent: "usa_fiscal", Enabled: true, SecurityTypes: []string{"Note"}},
	}}
}

func addTreasuryEvent(t *testing.T, st *store.Store, e model.ReleaseEvent) int64 {
	t.Helper()
	ctx := context.Background()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := store.SyncReleases(tx, e.Source, e.Name, e.ScheduledAt, e.ScheduledAt.Add(time.Second), []model.ReleaseEvent{e})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	evs, err := st.Releases(ctx, e.ScheduledAt, e.ScheduledAt.Add(time.Second))
	if err != nil || len(evs) != 1 {
		t.Fatalf("event: %v %v", evs, err)
	}
	return evs[0].ID
}

// A 13:00 ET auction: results appear at 13:03; polls every minute from the
// close until they are stored, then the event is done.
func TestAuctionEventPolledUntilResults(t *testing.T) {
	st := openStore(t)
	et := nyLoc(t)
	close := time.Date(2026, 10, 26, 13, 0, 0, 0, et)
	clock := &fakeClock{now: close.Add(-2 * time.Minute)}
	fake := &fakeTreasury{clock: clock, resultsAt: close.Add(3 * time.Minute)}
	runner := &TreasuryRunner{Ingest: &ingest.Treasury{Client: fake, Store: st, Config: treasuryConfig(),
		History: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Log: slog.New(slog.DiscardHandler), Now: clock.Now}}
	cfg := testConfig(t)
	cfg.Treasury = *treasuryConfig()
	id := addTreasuryEvent(t, st, model.ReleaseEvent{Source: treasury.SourceAuction, Name: "2-Year Note",
		ScheduledAt: close.UTC(), Region: "usa", Importance: "medium", Related: model.Related{CUSIP: "91282CRU7"}})

	routes := treasuryRoutes(cfg, st, runner, nil, nil)
	ev := &Events{Store: st, Clock: clock, Log: slog.New(slog.DiscardHandler), Routes: routes, spawn: func(f func()) { f() }}
	ev.Dispatch(context.Background())

	e := eventOf(t, st, id)
	if e.Status != model.StatusDone || !e.DoneAt.Equal(close.Add(3*time.Minute)) {
		t.Fatalf("event = %s at %s", e.Status, e.DoneAt.In(et))
	}
	if n := fake.calls["auctions"]; n != 4 { // 13:00, 13:01, 13:02, 13:03
		t.Errorf("auction fetches = %d, want 4", n)
	}
	if fake.calls["dts"]+fake.calls["mts"] != 0 {
		t.Errorf("other datasets fetched: %v", fake.calls)
	}
	as, _ := st.Auctions(context.Background(), close, close)
	if len(as) != 1 || !as[0].HasResults() {
		t.Errorf("stored = %+v", as)
	}
}

// The DTS event fetches the public debt transactions of the same statement
// once the TGA has arrived, and only then.
func TestDTSEventFetchesPublicDebt(t *testing.T) {
	st := openStore(t)
	et := nyLoc(t)
	at := time.Date(2026, 10, 29, 16, 0, 0, 0, et)
	clock := &fakeClock{now: at.Add(-time.Minute)}
	fake := &fakeTreasury{clock: clock}
	tc := treasuryConfig()
	tc.Datasets = append(tc.Datasets, config.TreasuryDataset{Name: config.DatasetPublicDebt, Agent: "usa_fiscal", Enabled: true})
	runner := &TreasuryRunner{Ingest: &ingest.Treasury{Client: fake, Store: st, Config: tc,
		History: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Log: slog.New(slog.DiscardHandler), Now: clock.Now}}
	cfg := testConfig(t)
	cfg.Treasury = *tc
	id := addTreasuryEvent(t, st, model.ReleaseEvent{Source: treasury.Source, Name: treasury.NameDTS,
		ScheduledAt: at.UTC(), Region: "usa", Importance: "low", Related: model.Related{Dataset: "dts"}})
	routes := treasuryRoutes(cfg, st, runner, nil, nil)
	ev := &Events{Store: st, Clock: clock, Log: slog.New(slog.DiscardHandler), Routes: routes, spawn: func(f func()) { f() }}

	clock.onSleep = func(now time.Time) {
		if !now.Before(at.Add(2 * time.Minute)) { // the statement is out at 16:02
			fake.tga = []model.Observation{{Date: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), Value: 1015632}}
		}
	}
	ev.Dispatch(context.Background())
	if e := eventOf(t, st, id); e.Status != model.StatusDone {
		t.Fatalf("event = %s", e.Status)
	}
	if fake.calls["dts"] != 3 || fake.calls["public_debt"] != 1 {
		t.Errorf("calls = %v, want dts 3 (16:00–16:02), public_debt 1", fake.calls)
	}
}

// DTS published Monday covers Friday; a holiday Friday means it never
// arrives, and the event closes as done because the data is fresh.
func TestDTSArrivalAndHolidayExpiry(t *testing.T) {
	st := openStore(t)
	et := nyLoc(t)
	ctx := context.Background()
	put := func(date string) {
		d, _ := time.Parse(time.DateOnly, date)
		if err := st.Write(ctx, func(tx *sql.Tx) error {
			_, err := store.UpsertObservations(tx, treasury.Source, treasury.SeriesTGA, []model.Observation{{Date: d, Value: 900}}, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	monday := model.ReleaseEvent{Source: treasury.Source, Name: treasury.NameDTS,
		ScheduledAt: time.Date(2026, 10, 26, 16, 0, 0, 0, et).UTC()}
	arrived, fresh := dtsArrived(st, et), dtsFresh(st, et)

	put("2026-10-22") // Thursday
	if ok, err := arrived(ctx, monday); err != nil || ok {
		t.Errorf("Thursday data counted for Monday's DTS: %v %v", ok, err)
	}
	h := &TreasuryDataset{Arrived: arrived, Fresh: fresh}
	if got := h.Expire(ctx, monday); got != model.StatusDone {
		t.Errorf("expire with Thursday data = %s, want done (holiday Friday)", got)
	}
	put("2026-10-23") // Friday
	if ok, _ := arrived(ctx, monday); !ok {
		t.Error("Friday data not counted for Monday's DTS")
	}
	// Arrived without a poll (missed event, safety job): done, also without Fresh.
	if got := (&TreasuryDataset{Arrived: arrived}).Expire(ctx, monday); got != model.StatusDone {
		t.Errorf("expire with arrived data = %s, want done", got)
	}
	stale := model.ReleaseEvent{Source: treasury.Source, Name: treasury.NameDTS,
		ScheduledAt: time.Date(2026, 11, 9, 16, 0, 0, 0, et).UTC()}
	if got := h.Expire(ctx, stale); got != model.StatusTimeout {
		t.Errorf("expire with two-week-old data = %s, want timeout", got)
	}
}

func TestMTSArrival(t *testing.T) {
	st := openStore(t)
	et := nyLoc(t)
	ctx := context.Background()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := store.UpsertObservations(tx, treasury.Source, treasury.SeriesMTSDeficit,
			[]model.Observation{{Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Value: 2.69e10}}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ev := func(day int, m time.Month) model.ReleaseEvent {
		return model.ReleaseEvent{Source: treasury.Source, Name: treasury.NameMTS, ScheduledAt: time.Date(2026, m, day, 14, 0, 0, 0, et).UTC()}
	}
	arrived := mtsArrived(st, et)
	if ok, _ := arrived(ctx, ev(9, time.October)); !ok {
		t.Error("September in store, October MTS not arrived")
	}
	if ok, _ := arrived(ctx, ev(12, time.November)); ok {
		t.Error("November MTS (October data) counted as arrived")
	}
}

type fakeYahoo struct{}

func (fakeYahoo) Fetch(context.Context, string, time.Time, time.Time) (yahoo.Series, error) {
	return yahoo.Series{}, nil
}

type fakeCFTC struct{}

func (fakeCFTC) Fetch(context.Context, []string, time.Time) (map[string][]model.Observation, error) {
	return nil, nil
}

// The committed config with every source: Treasury routes and the phase 3
// jobs with their cron specs.
func TestNewWithPhase3Sources(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	cfg, err := config.LoadAt(filepath.Join("..", "..", "presets", "us-equities.yaml"), filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Deps{Config: cfg, Store: openStore(t), FRED: fakeFull{&fakeFRED{}}, Fed: fedtexts.New(),
		Treasury: treasury.New(cfg.Treasury, httpx.New()), Yahoo: fakeYahoo{}, CFTC: fakeCFTC{},
		Root: slog.New(slog.DiscardHandler), FetchCron: "0 6 * * *"})
	names := map[string]bool{}
	for _, r := range s.Events.Routes {
		names[r.Source+"/"+r.Name] = true
	}
	for _, k := range []string{"treasury/Daily Treasury Statement", "treasury/Monthly Treasury Statement", "treasury_auction/",
		"treasury/" + treasury.NameFinancingEstimates, "treasury/" + treasury.NameRefundingStatement} {
		if !names[k] {
			t.Errorf("no route for %s", k)
		}
	}
	specs := map[string]string{}
	for _, j := range s.Jobs {
		specs[j.Name] = j.Spec
	}
	for name, want := range map[string]string{
		"treasury safety fetch": "15 6 * * *", "yahoo usa": "30 22 * * *", "cftc": "0 22 * * 1-5",
		"weekly export": "0 16 * * 0", "timeout recheck": "@hourly", "yahoo retry and top-up": "@every 30m0s",
	} {
		if specs[name] != want {
			t.Errorf("job %q spec = %q, want %q", name, specs[name], want)
		}
	}
	if len(s.Jobs) != 10 {
		var all []string
		for n := range specs {
			all = append(all, n)
		}
		t.Errorf("jobs = %s", strings.Join(all, ", "))
	}
}
