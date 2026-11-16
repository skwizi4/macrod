package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/derived"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// weekdays returns n weekday observations ending on last, the value
// moving a little so that a z-score exists.
func weekdays(last string, n int, base float64) []model.Observation {
	d, err := time.Parse(time.DateOnly, last)
	if err != nil {
		panic(err)
	}
	var out []model.Observation
	for i := 0; len(out) < n; i++ {
		day := d.AddDate(0, 0, -i)
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		out = append(out, model.Observation{Date: day, Value: base + float64(len(out)%5)/100})
	}
	slices.Reverse(out)
	return out
}

// Phase 11, batch 11b: the Europe preset shows series of the new sources,
// spreads built across sources, and the marks of §17.5 and D25.
func TestEuropeRowsAndFlags(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	cfg, err := presetConfigErr(t, "eu-equities", "eu", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fetched := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	put := func(source, id, units string, obs []model.Observation) {
		t.Helper()
		if err := st.Write(ctx, func(tx *sql.Tx) error {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: source, SeriesID: id, Frequency: "d", Units: units}); err != nil {
				return err
			}
			_, err := store.UpsertObservations(tx, source, id, obs, fetched)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	const last = "2026-11-06"
	put("yahoo", "MIT10.AS", config.UnitsRate, weekdays(last, 50, 4.30))
	put("yahoo", "MFR10.AS", config.UnitsRate, weekdays(last, 130, 4.74))
	// No MDE10.AS: the Bund leg of the spreads is the Bundesbank yield.
	put("bundesbank", "BUND_10Y", config.UnitsRate, weekdays(last, 300, 3.28))
	put("bundesbank", "BUND_2Y", config.UnitsRate, weekdays(last, 300, 2.99))
	put("ecb", "ECB_YC_ALL_10Y", config.UnitsRate, weekdays(last, 30, 3.90))
	put("ecb", "ECB_YC_AAA_10Y", config.UnitsRate, weekdays(last, 30, 3.29))
	put("ecb", "ECB_ASSETS", config.UnitsMillionsEUR, []model.Observation{{Date: day("2026-10-30"), Value: 5945410}, {Date: day(last), Value: 5913173}})
	put("yahoo", "EURUSD=X", config.UnitsFX, weekdays(last, 30, 1.1228))
	put("yahoo", "^GDAXI", "EUR", weekdays(last, 30, 25800))
	put("yahoo", "^NDX", "USD", weekdays(last, 30, 27000))
	// Batch 11f: the inputs of the money market spread, net exports and
	// excess liquidity.
	put("ecb", "ESTR", config.UnitsRate, weekdays(last, 30, 1.93))
	put("ecb", "ECB_DFR", config.UnitsRate, weekdays(last, 30, 2.00))
	weekly := []model.Observation{{Date: day("2026-10-30"), Value: 370000}, {Date: day(last), Value: 371000}}
	put("ecb", "ILM_CURRENT_ACC", config.UnitsMillionsEUR, weekly)
	put("ecb", "ILM_DEPOSIT_FAC", config.UnitsMillionsEUR, []model.Observation{{Date: day("2026-10-30"), Value: 1900000}, {Date: day(last), Value: 1897000}})
	// The wage tracker publishes ahead: the row is the value at the period
	// end, the last point is named as the forecast.
	var tracker []model.Observation
	for m := 0; m < 21; m++ { // 2026-01 … 2027-09
		tracker = append(tracker, model.Observation{Date: time.Date(2026, time.Month(1+m), 1, 0, 0, 0, 0, time.UTC), Value: 3.0 - float64(m)*0.04})
	}
	put("ecb", "WAGE_TRACKER", config.UnitsPercent, tracker)
	put("eurostat", "EXPORTS_EA", config.UnitsMillionsEUR, []model.Observation{{Date: day("2026-04-01"), Value: 2086049.3}})
	put("eurostat", "IMPORTS_EA", config.UnitsMillionsEUR, []model.Observation{{Date: day("2026-04-01"), Value: 1938249.1}})

	comp := &derived.Computer{Store: st, Config: cfg, Defs: cfg.Derived, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return fetched }}
	for _, r := range comp.Run(ctx) {
		if r.Name != "UST_BUND" && r.Outcome != derived.OutcomeUpdated { // no DGS10 in this store
			t.Fatalf("derived %s: %+v", r.Name, r)
		}
	}
	for id, want := range map[string]float64{"ESTR_DFR": -7, "EXCESS_LIQ": 2268000, "NETEXP_EA": 147800.2} {
		obs, err := st.Observations(ctx, "derived", id, time.Time{})
		if err != nil || len(obs) == 0 || math.Abs(obs[len(obs)-1].Value-want) > 0.01 {
			t.Errorf("%s = %+v, %v; want %v", id, obs, err, want)
		}
	}

	now := time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC)
	x := &Weekly{Store: st, Config: cfg, Region: &cfg.Regions[0], Now: func() time.Time { return now }}
	end, err := PeriodEndOfWeek("2026-W45", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	var md, monetary string
	var m MarketsReport
	var km KeyMetricsReport
	for _, f := range files {
		switch {
		case strings.HasSuffix(f.Path, "markets_eu.md"):
			md = f.Body
		case strings.HasSuffix(f.Path, "monetary_macro/current_week.md"):
			monetary = f.Body
		case strings.HasSuffix(f.Path, "markets_eu.json"):
			if err := json.Unmarshal([]byte(f.Body), &m); err != nil {
				t.Fatal(err)
			}
		case strings.HasSuffix(f.Path, "key_metrics_eu.json"):
			if err := json.Unmarshal([]byte(f.Body), &km); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 2026-11 is the month of the period end (W45): 3.0 − 10·0.04 = 2.6;
	// the forecast runs to 2027-09: 3.0 − 20·0.04 = 2.2.
	if !strings.Contains(monetary, "| Wage tracker ЕЦБ, г/г | 2026-11-01 | 2.6% |") || !strings.Contains(monetary, "прогноз на 2027-09: 2.2%") {
		t.Errorf("wage tracker row:\n%s", monetary)
	}
	// §12.7: twelve rows from the config, none of the US sentiment rows.
	if len(km.Rows) != 12 || km.Rows[0].Label != "Deposit Rate (ЕЦБ)" || km.Rows[11].Label != "GER40/NDX" {
		t.Errorf("key_metrics_eu rows = %d %+v", len(km.Rows), km.Rows)
	}
	if r := km.Rows[3]; r.Label != "BTP-Bund 10Y" || r.Value.Value == nil {
		t.Errorf("BTP-Bund key row = %+v", r)
	}
	// D25: CFTC Euro FX in the EU master's markets file.
	if len(m.COT) != 1 || m.COT[0].Name != "Euro FX" || !slices.Contains(m.Header.SourcesOK, "cftc") || !strings.Contains(md, "## 2. Позиционирование (CFTC COT)") {
		t.Errorf("markets_eu COT = %+v, sources %v", m.COT, m.Header.SourcesOK)
	}
	rows := map[string]MarketRow{}
	for _, r := range m.Rows {
		rows[r.ID] = r
	}
	hasFlag := func(id, part string) bool {
		return slices.ContainsFunc(rows[id].Flags, func(f string) bool { return strings.Contains(f, part) })
	}

	// §17.5: 50 observations of a marked series — no z, the count is shown;
	// 130 observations — z.
	if r := rows["MIT10.AS"]; r.Z.Value != nil || !hasFlag("MIT10.AS", "z: копим историю (50/120)") {
		t.Errorf("MIT10.AS: z %v, flags %v", r.Z.Value, r.Flags)
	}
	if r := rows["MFR10.AS"]; r.Z.Value == nil || hasFlag("MFR10.AS", "копим историю") {
		t.Errorf("MFR10.AS: z %v, flags %v", r.Z.Value, r.Flags)
	}
	// An unmarked series follows the usual rule of 20 observations.
	if r := rows["BUND_10Y"]; r.Z.Value == nil || len(r.Flags) != 0 {
		t.Errorf("BUND_10Y: z %v, flags %v", r.Z.Value, r.Flags)
	}
	// D25: the spread against the Bundesbank leg is marked; it has the 50
	// days of its shorter leg.
	btp := rows["BTP_BUND"]
	if btp.Level.Value == nil || btp.Level.Unit != unitBP || !hasFlag("BTP_BUND", "запасная нога: Bund 10Y, bundesbank") || !hasFlag("BTP_BUND", "(50/120)") {
		t.Errorf("BTP_BUND = %+v", btp)
	}
	if got := *btp.Level.Value; got < 101.5 || got > 102.5 { // 4.30 − 3.28
		t.Errorf("BTP_BUND level = %v", got)
	}
	if !hasFlag("OAT_BUND", "запасная нога") || hasFlag("OAT_BUND", "копим историю") || rows["OAT_BUND"].Z.Value == nil {
		t.Errorf("OAT_BUND = %+v", rows["OAT_BUND"])
	}
	if hasFlag("BUND_2S10S", "запасная нога") {
		t.Errorf("BUND_2S10S flags = %v", rows["BUND_2S10S"].Flags)
	}
	// The proxy says what it is.
	if r := rows["ECB_ALL_AAA_10Y"]; r.Quality != config.QualityProxy || !hasFlag("ECB_ALL_AAA_10Y", "прокси") || r.Label != "Кривая ЕЦБ: все эмитенты − AAA, 10Y" {
		t.Errorf("ECB_ALL_AAA_10Y = %+v", r)
	}
	// Units: yields change in basis points, the index ratio in percent,
	// euro amounts are in billions, exchange rates keep four decimals.
	if u := rows["MFR10.AS"].Change1W.Unit; u != unitBP {
		t.Errorf("MTS yield change unit = %s", u)
	}
	if u := rows["GER40_NDX"].Change1W.Unit; u != unitPercent {
		t.Errorf("GER40_NDX change unit = %s", u)
	}
	if r := rows["ECB_ASSETS"]; r.Level.Unit != unitEURBn || *r.Level.Value != 5913.173 {
		t.Errorf("ECB_ASSETS = %+v", r.Level)
	}
	for _, want := range []string{"| Баланс Евросистемы | 2026-11-06 | 5 913 млрд € |", "| EUR/USD | 2026-11-06 | 1.1228 |", "| BTP-Bund 10Y | 2026-11-06 | 102 б.п. |"} {
		if !strings.Contains(md, want) {
			t.Errorf("markets_eu.md has no %q", want)
		}
	}
	for _, s := range []string{"ecb", "bundesbank"} {
		if !slices.Contains(m.Header.SourcesOK, s) {
			t.Errorf("sources_ok = %v, no %s", m.Header.SourcesOK, s)
		}
	}
	// UST_BUND could not be computed: the derived series are named as failing.
	if !slices.Contains(m.Header.SourcesFailed, "derived") {
		t.Errorf("sources_failed = %v, no derived", m.Header.SourcesFailed)
	}
}

// Release days of a manual series come from the config (the Ifo site
// cannot be read): the latest one past the poll window is the release the
// staleness rule looks at.
func TestReleaseIndexConfigDays(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	days := map[string][]time.Time{"IFO_CLIMATE": {day("2026-10-26"), day("2026-11-24"), day("2026-12-17")}}
	at := func(s string) time.Time {
		return time.Date(day(s).Year(), day(s).Month(), day(s).Day(), 0, 0, 0, 0, berlin)
	}
	for _, tt := range []struct {
		now        time.Time
		last, next string
	}{
		{time.Date(2026, 11, 10, 12, 0, 0, 0, berlin), "2026-10-26", "2026-11-24"},
		{time.Date(2026, 11, 24, 12, 0, 0, 0, berlin), "2026-10-26", "2026-11-24"}, // the day itself: still being typed in
		{time.Date(2026, 11, 25, 12, 0, 0, 0, berlin), "2026-11-24", "2026-12-17"},
		{time.Date(2026, 12, 30, 12, 0, 0, 0, berlin), "2026-12-17", ""},
	} {
		ix := ReleaseIndex{last: map[string]time.Time{}, next: map[string]time.Time{}}
		ix.AddConfigDays(days, tt.now, berlin)
		last, ok, next := ix.Of("IFO_CLIMATE")
		if !ok || !last.Equal(at(tt.last)) || (tt.next == "") != next.IsZero() || (tt.next != "" && !next.Equal(at(tt.next))) {
			t.Errorf("at %s: last %v (%v), next %v", tt.now.Format(time.DateOnly), last, ok, next)
		}
	}
	// October's value typed on the release day is fresh until the November
	// release passes without an input.
	typed := time.Date(2026, 10, 26, 11, 0, 0, 0, berlin)
	if transform.StaleByRelease("m", at("2026-10-26"), typed) {
		t.Error("stale right after the input")
	}
	if !transform.StaleByRelease("m", at("2026-11-24"), typed) {
		t.Error("not stale after a release without an input")
	}
}
