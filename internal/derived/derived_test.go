package derived

import (
	"context"
	"database/sql"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
	"macro-collector/internal/units"
)

func date(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func obs(pairs ...any) []model.Observation {
	var out []model.Observation
	for i := 0; i < len(pairs); i += 2 {
		v := pairs[i+1]
		f, ok := v.(float64)
		if !ok {
			f = float64(v.(int))
		}
		out = append(out, model.Observation{Date: date(pairs[i].(string)), Value: f})
	}
	return out
}

func assertObs(t *testing.T, got, want []model.Observation) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d observations %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if !got[i].Date.Equal(want[i].Date) || math.Abs(got[i].Value-want[i].Value) > 1e-9 {
			t.Errorf("[%d] = %s %v, want %s %v", i, got[i].Date.Format(time.DateOnly), got[i].Value,
				want[i].Date.Format(time.DateOnly), want[i].Value)
		}
	}
}

func TestNetLiquidity(t *testing.T) {
	// Already in billions.
	walcl := obs("2026-10-07", 6600.0, "2026-10-14", 6590.0, "2026-10-21", 6580.0)
	tga := obs("2026-10-07", 850.0, "2026-10-21", 800.0) // 10-14 missing
	rrp := obs(
		"2026-10-06", 20.0,
		"2026-10-07", 25.0, // same Wednesday
		"2026-10-20", 30.0, // no value on 10-21: take Tuesday's
		"2026-10-22", 99.0, // after the last Wednesday, unused
	)
	assertObs(t, NetLiquidity(walcl, tga, rrp), obs(
		"2026-10-07", 6600-850-25.0,
		"2026-10-21", 6580-800-30.0,
	))

	// No RRP on or before the date: skipped.
	assertObs(t, NetLiquidity(walcl[:1], tga[:1], obs("2026-10-08", 1.0)), nil)
}

func TestNetLiquidityFromMillions(t *testing.T) {
	// WALCL and WDTGAL come in millions, RRPONTSYD in billions (§17.4).
	mil, _ := units.ToBillions(units.MillionsUSD)
	bil, _ := units.ToBillions(units.BillionsUSD)
	got := NetLiquidity(
		Scale(obs("2026-10-21", 6_580_123.0), mil),
		Scale(obs("2026-10-21", 800_456.0), mil),
		Scale(obs("2026-10-21", 30.5), bil),
	)
	assertObs(t, got, obs("2026-10-21", 6580.123-800.456-30.5))
}

func TestRatio(t *testing.T) {
	unemploy := obs("2026-07-01", 7000, "2026-08-01", 7100, "2026-09-01", 7200)
	jolts := obs("2026-07-01", 7000, "2026-08-01", 0)
	assertObs(t, Ratio(unemploy, jolts), obs("2026-07-01", 1.0)) // Aug: b = 0, Sep: no JOLTS yet
}

func TestSpreadBP(t *testing.T) {
	dgs10 := obs("2026-10-20", 4.10, "2026-10-21", 4.15, "2026-10-22", 4.12)
	dgs2 := obs("2026-10-20", 3.60, "2026-10-22", 3.70, "2026-10-23", 3.71)
	assertObs(t, SpreadBP(dgs10, dgs2), obs(
		"2026-10-20", 50.0,
		"2026-10-22", 42.0, // 10-21 has no 2Y: no interpolation
	))
}

// Every implemented formula is in a committed config, with the same text:
// the example (US) or the Europe preset.
func TestFormulasMatchConfig(t *testing.T) {
	t.Setenv(config.EnvDataDir, "")
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	for _, name := range []string{"config.example.yaml", filepath.Join("presets", "eu-equities.yaml")} {
		cfg, err := config.LoadAt(filepath.Join(root, name), root)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range cfg.Derived {
			if f, ok := Formulas[d.Name]; ok {
				found[d.Name] = true
				if d.Formula != f.Formula {
					t.Errorf("%s: config %q, implemented %q", d.Name, d.Formula, f.Formula)
				}
			}
		}
	}
	for name := range Formulas {
		if !found[name] {
			t.Errorf("%s implemented but missing from the committed configs", name)
		}
	}
	for name := range config.DerivedFormulas {
		if _, ok := Formulas[name]; !ok {
			t.Errorf("%s is known to the config but not implemented", name)
		}
	}
}

type env struct {
	t     *testing.T
	store *store.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &env{t: t, store: st}
}

func (e *env) seed(id, unitsStr string, o []model.Observation) {
	e.t.Helper()
	err := e.store.Write(context.Background(), func(tx *sql.Tx) error {
		if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: "fred", SeriesID: id, Label: id, Agent: "usa_fiscal", Units: unitsStr}); err != nil {
			return err
		}
		_, err := store.UpsertObservations(tx, "fred", id, o, time.Now())
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func defs(names ...string) []config.Derived {
	var out []config.Derived
	for _, n := range names {
		d := config.Derived{Name: n, Agent: "usa_fiscal"}
		if f, ok := Formulas[n]; ok {
			d.Formula = f.Formula
		}
		out = append(out, d)
	}
	return out
}

func TestComputerStoresDerivedSeries(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.seed("WALCL", units.MillionsUSD, obs("2026-10-14", 6_590_000.0, "2026-10-21", 6_580_000.0))
	e.seed("WDTGAL", units.MillionsUSD, obs("2026-10-14", 810_000.0, "2026-10-21", 800_000.0))
	e.seed("RRPONTSYD", units.BillionsUSD, obs("2026-10-14", 25.0, "2026-10-20", 30.0))
	e.seed("DGS10", units.Percent, obs("2026-10-21", 4.15, "2026-10-22", 4.12))
	e.seed("DGS2", units.Percent, obs("2026-10-21", 3.60, "2026-10-22", 3.70))

	c := &Computer{Store: e.store, Defs: defs("NET_LIQUIDITY", "UST_2S10S", "FED_PATH"), Log: slog.New(slog.DiscardHandler)}
	res := c.Run(ctx)
	if len(res) != 3 || res[0].Outcome != OutcomeUpdated || res[1].Outcome != OutcomeUpdated || res[2].Outcome != OutcomeAtExport {
		t.Fatalf("results = %+v", res)
	}

	nl, err := e.store.Observations(ctx, Source, "NET_LIQUIDITY", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	assertObs(t, nl, obs("2026-10-14", 6590-810-25.0, "2026-10-21", 6580-800-30.0))
	s, _ := e.store.Observations(ctx, Source, "UST_2S10S", time.Time{})
	assertObs(t, s, obs("2026-10-21", 55.0, "2026-10-22", 42.0))

	if u, ok, _ := e.store.SeriesUnits(ctx, Source, "NET_LIQUIDITY"); !ok || u != UnitsBillionsUSD {
		t.Errorf("NET_LIQUIDITY units = %q", u)
	}
	st, ok, _ := e.store.FetchStateOf(ctx, "derived:UST_2S10S")
	if !ok || st.Cursor != "2026-10-22" {
		t.Errorf("state = %+v", st)
	}

	// Recomputing without new data changes nothing.
	res = c.Run(ctx)
	if res[0].Inserted != 0 || res[0].Revised != 0 || res[0].Unchanged != 2 {
		t.Errorf("second run = %+v", res[0])
	}

	// A revised input revises the derived value.
	e.seed("WDTGAL", units.MillionsUSD, obs("2026-10-21", 790_000.0))
	res = c.Run(ctx)
	if res[0].Revised != 1 {
		t.Errorf("after revision = %+v", res[0])
	}
}

func TestComputerStopsOnCancel(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Computer{Store: e.store, Defs: defs("UST_2S10S"), Log: slog.New(slog.DiscardHandler)}
	if res := c.Run(ctx); len(res) != 0 {
		t.Errorf("results = %+v, want none", res)
	}
	if _, ok, _ := e.store.FetchStateOf(context.Background(), "derived:UST_2S10S"); ok {
		t.Error("cancelled run recorded in fetch_state")
	}
}

func TestComputerFailures(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.seed("DGS10", units.Percent, obs("2026-10-21", 4.15))
	// WDTGAL/RRPONTSYD missing, DGS2 missing.
	e.seed("WALCL", units.MillionsUSD, obs("2026-10-21", 1.0))

	bad := defs("NET_LIQUIDITY", "UST_2S10S")
	wrongFormula := config.Derived{Name: "UST_3M10Y", Agent: "usa_fiscal", Formula: "DGS10 - DGS2"}
	c := &Computer{Store: e.store, Defs: append(bad, wrongFormula), Log: slog.New(slog.DiscardHandler)}
	res := c.Run(ctx)
	for i, wantErr := range []string{"WDTGAL has no observations", "DGS2 has no observations", "differs from implemented"} {
		if res[i].Outcome != OutcomeFailed || res[i].Err == nil || !strings.Contains(res[i].Err.Error(), wantErr) {
			t.Errorf("%s = %+v, want error %q", res[i].Name, res[i], wantErr)
		}
	}
	st, ok, _ := e.store.FetchStateOf(ctx, "derived:UST_2S10S")
	if !ok || !strings.Contains(st.LastError, "DGS2") {
		t.Errorf("state = %+v", st)
	}

	// Units check: all inputs present, WALCL not in dollars.
	e.seed("WALCL", units.Percent, obs("2026-10-21", 1.0))
	e.seed("WDTGAL", units.MillionsUSD, obs("2026-10-21", 1.0))
	e.seed("RRPONTSYD", units.BillionsUSD, obs("2026-10-21", 1.0))
	res = c.Run(ctx)
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "not a dollar amount") {
		t.Errorf("NET_LIQUIDITY = %+v", res[0])
	}
}

func (e *env) seedFrom(source, id, unitsStr string, o []model.Observation) {
	e.t.Helper()
	err := e.store.Write(context.Background(), func(tx *sql.Tx) error {
		if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: source, SeriesID: id, Label: id, Agent: "eu_fiscal", Units: unitsStr}); err != nil {
			return err
		}
		_, err := store.UpsertObservations(tx, source, id, o, time.Now())
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// europe is a config with the inputs of the European formulas, each in its
// own source.
func europe(names ...string) *config.Config {
	return &config.Config{
		FRED: config.FRED{Series: []config.Series{{ID: "DGS10"}}},
		Yahoo: config.Yahoo{Symbols: []config.YahooSymbol{
			{Symbol: "MIT10.AS"}, {Symbol: "MFR10.AS"}, {Symbol: "MDE10.AS"}, {Symbol: "^GDAXI"}, {Symbol: "^NDX"},
		}},
		Bundesbank: config.NumericSource{Enabled: true, Series: []config.SourceSeries{{ID: "BUND_10Y"}, {ID: "BUND_2Y"}}},
		Derived:    defs(names...),
	}
}

func TestComputerReadsInputsOfAnySource(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.seedFrom("yahoo", "MIT10.AS", "Percent per Annum", obs("2026-11-06", 4.31, "2026-11-09", 4.30))
	e.seedFrom("yahoo", "MDE10.AS", "Percent per Annum", obs("2026-11-06", 3.26, "2026-11-09", 3.28))
	e.seedFrom("fred", "DGS10", units.Percent, obs("2026-11-06", 4.10, "2026-11-09", 4.12, "2026-11-11", 4.2))
	e.seedFrom("bundesbank", "BUND_10Y", "Percent per Annum", obs("2026-11-06", 3.27, "2026-11-09", 3.30, "2026-11-10", 3.28))
	e.seedFrom("bundesbank", "BUND_2Y", "Percent per Annum", obs("2026-11-06", 2.95, "2026-11-09", 2.99))
	e.seedFrom("yahoo", "^GDAXI", "EUR", obs("2026-11-05", 26000.0, "2026-11-06", 26260.0, "2026-11-09", 26000.0))
	e.seedFrom("yahoo", "^NDX", "USD", obs("2026-11-06", 20000.0, "2026-11-09", 20200.0))
	// An unrelated FRED series with the id of a Bundesbank input must not be read.
	e.seedFrom("fred", "BUND_10Y", units.Percent, obs("2026-11-09", 9.99))

	cfg := europe("BTP_BUND", "UST_BUND", "BUND_2S10S", "GER40_NDX")
	c := &Computer{Store: e.store, Config: cfg, Defs: cfg.Derived, Log: slog.New(slog.DiscardHandler)}
	for _, r := range c.Run(ctx) {
		if r.Outcome != OutcomeUpdated || r.Fallback != "" {
			t.Fatalf("%s = %+v", r.Name, r)
		}
	}
	get := func(name string) []model.Observation {
		o, err := e.store.Observations(ctx, Source, name, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	assertObs(t, get("BTP_BUND"), obs("2026-11-06", 105.0, "2026-11-09", 102.0))
	assertObs(t, get("UST_BUND"), obs("2026-11-06", 83.0, "2026-11-09", 82.0)) // only dates of both
	assertObs(t, get("BUND_2S10S"), obs("2026-11-06", 32.0, "2026-11-09", 31.0))
	// 26260/20000 = 1.313 is the base; 26000/20200 = 1.28713 → 98.03.
	assertObs(t, get("GER40_NDX"), obs("2026-11-06", 100.0, "2026-11-09", 100*(26000.0/20200)/(26260.0/20000)))
	if u, _, _ := e.store.SeriesUnits(ctx, Source, "GER40_NDX"); u != UnitsIndex {
		t.Errorf("GER40_NDX units = %q", u)
	}
	if !Uses(cfg, "yahoo") || !Uses(cfg, "bundesbank") || Uses(cfg, "stoxx") {
		t.Error("Uses does not follow the inputs")
	}
}

// D25: without the MTS Bund yield the spread is computed against the
// Bundesbank one and says so.
func TestComputerFallbackLeg(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.seedFrom("yahoo", "MIT10.AS", "Percent per Annum", obs("2026-10-30", 4.36, "2026-11-06", 4.31, "2026-11-09", 4.30))
	e.seedFrom("bundesbank", "BUND_10Y", "Percent per Annum", obs("2026-11-06", 3.27, "2026-11-09", 3.30))
	cfg := europe("BTP_BUND")
	c := &Computer{Store: e.store, Config: cfg, Defs: cfg.Derived, Log: slog.New(slog.DiscardHandler)}
	state := func() string {
		st, _, err := e.store.FetchStateOf(ctx, "derived:BTP_BUND")
		if err != nil {
			t.Fatal(err)
		}
		return st.Cursor
	}

	// No MTS Bund yield at all.
	if r := c.Run(ctx)[0]; r.Outcome != OutcomeUpdated || r.Fallback != "BUND_10Y" || r.Rows != 2 {
		t.Fatalf("no MTS leg: %+v", r)
	}
	if got := state(); got != "2026-11-09"+FallbackMark+"BUND_10Y" {
		t.Errorf("cursor = %q", got)
	}

	// The MTS leg is there but stopped 10 days before the other leg.
	e.seedFrom("yahoo", "MDE10.AS", "Percent per Annum", obs("2026-10-30", 3.40))
	if r := c.Run(ctx)[0]; r.Fallback != "BUND_10Y" {
		t.Errorf("stale MTS leg: %+v", r)
	}

	// The MTS leg is back: both legs of one source, no mark.
	e.seedFrom("yahoo", "MDE10.AS", "Percent per Annum", obs("2026-10-30", 3.40, "2026-11-06", 3.26, "2026-11-09", 3.28))
	if r := c.Run(ctx)[0]; r.Outcome != OutcomeUpdated || r.Fallback != "" {
		t.Errorf("MTS leg back: %+v", r)
	}
	if got := state(); got != "2026-11-09" {
		t.Errorf("cursor = %q", got)
	}
	o, _ := e.store.Observations(ctx, Source, "BTP_BUND", time.Time{})
	assertObs(t, o, obs("2026-10-30", 96.0, "2026-11-06", 105.0, "2026-11-09", 102.0))

	// Neither leg: the input is reported, nothing is computed.
	cfg2 := europe("OAT_BUND")
	cfg2.Bundesbank = config.NumericSource{}
	e2 := newEnv(t)
	e2.seedFrom("yahoo", "MFR10.AS", "Percent per Annum", obs("2026-11-09", 4.74))
	c2 := &Computer{Store: e2.store, Config: cfg2, Defs: cfg2.Derived, Log: slog.New(slog.DiscardHandler)}
	if r := c2.Run(ctx)[0]; r.Outcome != OutcomeFailed || r.Err == nil || !strings.Contains(r.Err.Error(), "input MDE10.AS has no observations") {
		t.Errorf("no legs: %+v", r)
	}
}

func TestNormalize(t *testing.T) {
	assertObs(t, Normalize(obs("2026-11-06", 1.25, "2026-11-09", 1.3)), obs("2026-11-06", 100.0, "2026-11-09", 104.0))
	assertObs(t, Normalize(nil), nil)
}
