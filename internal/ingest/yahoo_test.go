package ingest

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

type fakeYahoo struct {
	series map[string]yahoo.Series
	since  map[string]time.Time
}

func (f *fakeYahoo) Fetch(_ context.Context, symbol string, since, _ time.Time) (yahoo.Series, error) {
	f.since[symbol] = since
	s, ok := f.series[symbol]
	if !ok {
		return yahoo.Series{}, errors.New("yahoo " + symbol + ": not found")
	}
	return s, nil
}

func bars(from string, closes ...float64) []yahoo.Bar {
	var out []yahoo.Bar
	for i, c := range closes {
		out = append(out, yahoo.Bar{Date: day(from).AddDate(0, 0, i), Close: c, Volume: float64(10 * i), HasVol: true})
	}
	return out
}

func TestYahooSymbols(t *testing.T) {
	cfg := &config.Yahoo{
		Symbols: []config.YahooSymbol{
			{Symbol: "^NDX", Agent: "usa_master", Label: "NASDAQ 100"},
			{Symbol: "^GDAXI", Agent: "eu_master", Label: "GER40"},
		},
		FedFundsFutures: config.FedFundsFutures{SymbolFormat: "ZQ{month}{yy}.CBT", MonthsAhead: 12, Agent: "usa_monetary"},
		TagRegion:       map[string]string{"usa_master": "usa", "usa_monetary": "usa", "eu_master": "eu"},
	}
	got := YahooSymbols(cfg, "usa", time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
	var names []string
	for _, s := range got {
		names = append(names, s.Symbol)
	}
	if len(names) != 14 || names[0] != "^NDX" || names[1] != "ZQV26.CBT" || names[13] != "ZQV27.CBT" || slices.Contains(names, "^GDAXI") {
		t.Errorf("usa symbols = %v", names)
	}
	if !got[1].Volume || got[1].Label != "Fed funds future Oct 2026" || got[0].Volume {
		t.Errorf("zq = %+v, ndx = %+v", got[1], got[0])
	}
	if eu := YahooSymbols(cfg, "eu", time.Now()); len(eu) != 1 || eu[0].Symbol != "^GDAXI" {
		t.Errorf("eu = %+v", eu)
	}
}

func TestYahooRun(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := &fakeYahoo{since: map[string]time.Time{}, series: map[string]yahoo.Series{
		"^NDX":      {Symbol: "^NDX", Currency: "USD", Bars: bars("2026-10-19", 100, 101, 102)},
		"ZQX26.CBT": {Symbol: "ZQX26.CBT", Currency: "USD", Bars: bars("2026-10-19", 96.08, 96.1)},
	}}
	ing := &Yahoo{Client: client, Store: st, History: day("2000-01-01"), Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return time.Date(2026, 10, 24, 20, 30, 0, 0, time.UTC) },
		Symbols: []YahooSymbol{{Symbol: "^NDX", Agent: "usa_master"}, {Symbol: "ZQX26.CBT", Agent: "usa_monetary", Volume: true},
			{Symbol: "^MOVE", Agent: "usa_master"}}}
	ctx := context.Background()
	rep := ing.Run(ctx, Incremental)
	if len(rep.Results) != 3 || len(rep.Failed()) != 1 || rep.Failed()[0].Symbol != "^MOVE" || rep.Results[0].Inserted != 3 {
		t.Fatalf("report = %+v", rep)
	}
	if fs, _, _ := st.FetchStateOf(ctx, "yahoo:^MOVE"); fs.LastError == "" {
		t.Error("^MOVE error not recorded")
	}
	vol, err := st.Observations(ctx, "yahoo", "ZQX26.CBT#volume", day("2026-01-01"))
	if err != nil || len(vol) != 2 || vol[1].Value != 10 {
		t.Errorf("volume = %v %v", vol, err)
	}
	// Second run: from 10 days before the last bar.
	ing.Run(ctx, Incremental)
	if got := client.since["^NDX"].Format(time.DateOnly); got != "2026-10-11" {
		t.Errorf("since = %s", got)
	}
	if got := client.since["^MOVE"]; !got.Equal(day("2000-01-01")) {
		t.Errorf("never stored: since = %s", got)
	}
}

// Company tickers start at edgar.history_start, not at the 2000 history of
// the market symbols.
func TestCompanySymbols(t *testing.T) {
	cfg := &config.Config{
		EDGAR:   config.EDGAR{Enabled: true, History: day("2024-01-01"), Companies: []config.Company{{Ticker: "MSFT"}, {Ticker: "AMZN"}}},
		Regions: []config.Region{{ID: "usa", Agents: []config.Agent{{SeriesTags: []string{"usa_micro"}, Companies: []string{"MSFT", "AMZN"}}}}},
	}
	syms := CompanySymbols(cfg, "usa")
	if len(syms) != 2 || syms[0].Symbol != "MSFT" || syms[0].Agent != "usa_micro" || !syms[0].History.Equal(day("2024-01-01")) {
		t.Fatalf("symbols = %+v", syms)
	}
	if got := CompanySymbols(cfg, "eu"); got != nil {
		t.Errorf("symbols of another region = %+v", got)
	}
	disabled := *cfg
	disabled.EDGAR.Enabled = false
	if CompanySymbols(&disabled, "usa") != nil {
		t.Error("symbols for disabled EDGAR")
	}

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := &fakeYahoo{since: map[string]time.Time{}, series: map[string]yahoo.Series{
		"MSFT": {Symbol: "MSFT", Currency: "USD", Bars: bars("2026-10-19", 520, 525)},
	}}
	ing := &Yahoo{Client: client, Store: st, History: day("2000-01-01"), Log: slog.New(slog.DiscardHandler), Symbols: syms[:1],
		Now: func() time.Time { return time.Date(2026, 10, 24, 20, 30, 0, 0, time.UTC) }}
	ing.Run(context.Background(), Backfill)
	if got := client.since["MSFT"]; !got.Equal(day("2024-01-01")) {
		t.Errorf("backfill since = %s", got)
	}
	ing.Run(context.Background(), Incremental)
	if got := client.since["MSFT"].Format(time.DateOnly); got != "2026-10-10" {
		t.Errorf("incremental since = %s", got)
	}
}
