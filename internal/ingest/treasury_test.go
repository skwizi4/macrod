package ingest

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/store"
)

type fakeTreasury struct {
	tga      []model.Observation
	mts      map[string][]model.Observation
	debt     map[string][]model.Observation
	auctions []model.Auction
	buybacks []model.Buyback
	failMTS  error
	since    map[string]time.Time // dataset → requested since/from
	to       time.Time
}

func (f *fakeTreasury) FetchTGA(_ context.Context, since time.Time) ([]model.Observation, error) {
	f.since["dts_operating_cash"] = since
	return f.tga, nil
}

func (f *fakeTreasury) FetchMTS(_ context.Context, since time.Time) (map[string][]model.Observation, error) {
	f.since["mts"] = since
	return f.mts, f.failMTS
}

func (f *fakeTreasury) FetchPublicDebt(_ context.Context, since time.Time) (map[string][]model.Observation, error) {
	f.since["dts_public_debt"] = since
	return f.debt, nil
}

func (f *fakeTreasury) FetchAuctions(_ context.Context, types []string, from, to time.Time) ([]model.Auction, error) {
	f.since["auctions"], f.to = from, to
	return f.auctions, nil
}

func (f *fakeTreasury) FetchBuybacks(_ context.Context, since time.Time) ([]model.Buyback, error) {
	f.since["buybacks"] = since
	return f.buybacks, nil
}

func newTreasury(t *testing.T, client *fakeTreasury, now time.Time) (*Treasury, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Treasury{Datasets: []config.TreasuryDataset{
		{Name: config.DatasetDTS, Agent: "usa_fiscal", Enabled: true},
		{Name: config.DatasetMTS, Agent: "usa_fiscal", Enabled: true},
		{Name: config.DatasetAuctions, Agent: "usa_fiscal", Enabled: true, SecurityTypes: []string{"Note"}},
		{Name: config.DatasetUpcomingAuctions, Agent: "usa_master", Enabled: true},
		{Name: config.DatasetBuybacks, Agent: "usa_fiscal", Enabled: false},
	}}
	return &Treasury{Client: client, Store: st, Config: cfg, History: day("2000-01-01"),
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}, st
}

func TestTreasuryRun(t *testing.T) {
	now := time.Date(2026, 10, 24, 18, 0, 0, 0, time.UTC)
	client := &fakeTreasury{
		tga: daily("2026-10-01", 22, 900),
		mts: map[string][]model.Observation{
			treasury.SeriesMTSReceipts: monthly("2025-10-01", 12, 5e11),
			treasury.SeriesMTSOutlays:  monthly("2025-10-01", 12, 6e11),
			treasury.SeriesMTSDeficit:  monthly("2025-10-01", 12, 1e11),
		},
		auctions: []model.Auction{{AuctionDate: day("2026-10-07"), SecurityType: "Note", Kind: "Note", Term: "10-Year",
			Tenor: "10-Year", CUSIP: "X", BidToCover: model.Some(2.4)}},
		since: map[string]time.Time{},
	}
	ing, st := newTreasury(t, client, now)
	ctx := context.Background()

	rep := ing.Run(ctx, Incremental)
	if len(rep.Results) != 3 || len(rep.Failed()) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if _, called := client.since["buybacks"]; called {
		t.Error("disabled buybacks fetched")
	}
	// Empty store: everything from history_start.
	for ds, s := range client.since {
		if !s.Equal(day("2000-01-01")) {
			t.Errorf("%s since = %s", ds, s.Format(time.DateOnly))
		}
	}
	if !client.to.Equal(day("2026-10-24")) {
		t.Errorf("auctions to = %s", client.to)
	}
	if rep.Results[0].Inserted != 22 || rep.Results[1].Inserted != 36 || rep.Results[2].Auctions.Resulted != 1 {
		t.Errorf("results = %+v", rep.Results)
	}
	units, ok, err := st.SeriesUnits(ctx, treasury.Source, treasury.SeriesTGA)
	if err != nil || !ok || units != treasury.UnitsMillionsUSD {
		t.Errorf("TGA units = %q %v %v", units, ok, err)
	}

	// Second run: look-back windows from the stored data.
	rep = ing.Run(ctx, Incremental)
	if rep.Results[0].Unchanged != 22 || rep.Results[2].Auctions.Unchanged != 1 {
		t.Errorf("second run = %+v", rep.Results)
	}
	for ds, want := range map[string]string{
		"dts_operating_cash": "2026-10-08", // last day 10-22 − 14
		"mts":                "2026-08-15", // today − 70
		"auctions":           "2026-09-23", // last auction 10-07 − 14
	} {
		if got := client.since[ds].Format(time.DateOnly); got != want {
			t.Errorf("%s since = %s, want %s", ds, got, want)
		}
	}
}

func TestTreasuryRunRecordsDatasetError(t *testing.T) {
	now := time.Date(2026, 10, 24, 18, 0, 0, 0, time.UTC)
	client := &fakeTreasury{tga: daily("2026-10-01", 3, 900), failMTS: errors.New("503"), since: map[string]time.Time{}}
	ing, st := newTreasury(t, client, now)
	rep := ing.Run(context.Background(), Incremental)
	if len(rep.Failed()) != 1 || rep.Failed()[0].Dataset != "mts" || len(rep.Results) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	fs, ok, err := st.FetchStateOf(context.Background(), "treasury:mts")
	if err != nil || !ok || fs.LastError != "503" {
		t.Errorf("mts state = %+v %v %v", fs, ok, err)
	}
	if fs, _, _ := st.FetchStateOf(context.Background(), "treasury:dts_operating_cash"); fs.Cursor != "2026-10-24" {
		t.Errorf("dts cursor = %q", fs.Cursor)
	}
}

// Public debt transactions: a backfill loads two years only; an incremental
// run looks back from the last stored day.
func TestTreasuryPublicDebt(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	client := &fakeTreasury{since: map[string]time.Time{}, debt: map[string][]model.Observation{
		treasury.SeriesBillsIssued:   {{Date: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), Value: 354996}},
		treasury.SeriesBillsRedeemed: {{Date: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), Value: 315295}},
	}}
	tr, st := newTreasury(t, client, now)
	tr.Config.Datasets = append(tr.Config.Datasets, config.TreasuryDataset{Name: config.DatasetPublicDebt, Agent: "usa_fiscal", Enabled: true})
	ctx := context.Background()

	res, ok := tr.RunDataset(ctx, Backfill, config.DatasetPublicDebt)
	if !ok || res.Err != nil || res.Inserted != 2 {
		t.Fatalf("backfill: %+v %v", res, ok)
	}
	if got := client.since["dts_public_debt"]; !got.Equal(time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("backfill since %v", got)
	}
	if _, _ = tr.RunDataset(ctx, Incremental, config.DatasetPublicDebt); !client.since["dts_public_debt"].Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("incremental since %v", client.since["dts_public_debt"])
	}
	m, ok, err := st.Meta(ctx, treasury.Source, treasury.SeriesBillsRedeemed)
	if err != nil || !ok || m.Units != treasury.UnitsMillionsUSD || m.Frequency != "d" {
		t.Errorf("meta = %+v %v %v", m, ok, err)
	}
}
