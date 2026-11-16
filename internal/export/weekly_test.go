package export

import (
	"context"
	"database/sql"
	"flag"
	"hash/fnv"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/derived"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/cboe"
	"macro-collector/internal/sources/cftc"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/nasdaq"
	"macro-collector/internal/sources/predict"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

var update = flag.Bool("update", false, "rewrite testdata/export/weekly_*.md")

// fredMeta is frequency and units of the configured FRED series (from
// fred/series, 2026-10-24).
var fredMeta = map[string][2]string{
	"A091RC1Q027SBEA": {"q", "Billions of Dollars"}, "BAMLC0A0CM": {"d", "Percent"}, "BAMLH0A0HYM2": {"d", "Percent"},
	"CCSA": {"w", "Number"}, "CES0500000003": {"m", "Dollars per Hour"}, "CPIAUCSL": {"m", "Index 1982-1984=100"},
	"CPILFESL": {"m", "Index 1982-1984=100"}, "CUSR0000SAH1": {"m", "Index 1982-1984=100"}, "CUSR0000SASL2RS": {"m", "Index 1982-1984=100"},
	"DCOILWTICO": {"d", "Dollars per Barrel"}, "DFF": {"d", "Percent"}, "DFII10": {"d", "Percent"}, "DGS10": {"d", "Percent"},
	"DGS2": {"d", "Percent"}, "DGS3": {"d", "Percent"}, "DGS5": {"d", "Percent"}, "DGS7": {"d", "Percent"}, "DGS20": {"d", "Percent"},
	"DGS30": {"d", "Percent"}, "DGS3MO": {"d", "Percent"}, "DHHNGSP": {"d", "Dollars per Million BTU"},
	"DTWEXBGS": {"d", "Index Jan 2006=100"}, "ECIALLCIV": {"q", "Index Dec 2005=100"}, "GCE": {"q", "Billions of Dollars"},
	"GDP": {"q", "Billions of Dollars"}, "GDPC1": {"q", "Billions of Chained 2017 Dollars"}, "GDPDEF": {"q", "Index 2017=100"},
	"GDPNOW": {"q", "Percent Change at Annual Rate"}, "GFDEGDQ188S": {"q", "Percent of GDP"}, "ICSA": {"w", "Number"},
	"IORB": {"d", "Percent"}, "JTSHIL": {"m", "Level in Thousands"}, "JTSJOL": {"m", "Level in Thousands"}, "JTSJOR": {"m", "Rate"},
	"JTSQUR": {"m", "Rate"}, "M2SL": {"m", "Billions of Dollars"}, "MICH": {"m", "Percent"}, "MTSDS133FMS": {"m", "Millions of Dollars"},
	"NASDAQCOM": {"d", "Index Feb 5, 1971=100"}, "NETEXP": {"q", "Billions of Dollars"}, "OPHNFB": {"q", "Index 2017=100"},
	"PAYEMS": {"m", "Thousands of Persons"}, "PCE": {"m", "Billions of Dollars"}, "PCEPI": {"m", "Index 2017=100"},
	"PCEPILFE": {"m", "Index 2017=100"}, "PPIFES": {"m", "Index Apr 2010=100"}, "PPIFIS": {"m", "Index Nov 2009=100"},
	"WRMFNS":  {"w", "Billions of Dollars"},
	"PSAVERT": {"m", "Percent"}, "RPONTSYD": {"d", "Billions of US Dollars"}, "RRPONTSYD": {"d", "Billions of US Dollars"},
	"SOFR": {"d", "Percent"}, "STICKCPIM159SFRBATL": {"m", "Percent Change from Year Ago"}, "T10YIE": {"d", "Percent"},
	"T5YIE": {"d", "Percent"}, "T5YIFR": {"d", "Percent"}, "THREEFYTP10": {"d", "Percent"}, "ULCNFB": {"q", "Index 2017=100"},
	"UMCSENT": {"m", "Index 1966:Q1=100"}, "UNEMPLOY": {"m", "Thousands of Persons"}, "UNRATE": {"m", "Percent"},
	"WALCL": {"w", "Millions of U.S. Dollars"}, "WDTGAL": {"w", "Millions of U.S. Dollars"},
	"WPSFD49116": {"m", "Index Aug 2013=100"}, "WRESBAL": {"w", "Millions of U.S. Dollars"},
}

// bases are rough real levels of the series (October 2026), so the
// synthetic files read plausibly.
var bases = map[string]float64{
	"GDPC1": 24000, "GDP": 31000, "GDPDEF": 128, "GDPNOW": 2.5, "PCE": 21000, "NETEXP": -900, "PSAVERT": 4.5,
	"UMCSENT": 60, "MICH": 3.2, "UNRATE": 4.2, "JTSJOL": 7300, "JTSJOR": 4.4, "UNEMPLOY": 7200, "CES0500000003": 36,
	"ECIALLCIV": 170, "JTSHIL": 5300, "JTSQUR": 2.0, "ULCNFB": 120, "OPHNFB": 115, "PAYEMS": 158000, "ICSA": 225000,
	"CCSA": 1900000, "CPIAUCSL": 320, "CPILFESL": 325, "CUSR0000SAH1": 410, "CUSR0000SASL2RS": 450, "STICKCPIM159SFRBATL": 3.2, "PPIFIS": 150,
	"PPIFES": 150, "WPSFD49116": 140, "PCEPI": 125, "PCEPILFE": 125, "T5YIE": 2.4, "T5YIFR": 2.3, "T10YIE": 2.35,
	"DFF": 3.86, "DGS2": 3.5, "M2SL": 22000, "DCOILWTICO": 65, "DHHNGSP": 3.0, "WALCL": 6600000, "WDTGAL": 900000,
	"WRMFNS": 1950, "RRPONTSYD": 25, "WRESBAL": 3000000, "SOFR": 3.87, "IORB": 3.90, "RPONTSYD": 0, "MTSDS133FMS": -150000,
	"GFDEGDQ188S": 121, "A091RC1Q027SBEA": 1150, "GCE": 5500, "DGS3MO": 3.9, "DGS3": 3.6, "DGS5": 3.7, "DGS7": 3.9,
	"DGS10": 4.15, "DGS20": 4.7, "DGS30": 4.75, "DFII10": 1.8, "THREEFYTP10": 0.8, "BAMLH0A0HYM2": 3.1,
	"BAMLC0A0CM": 0.8, "DTWEXBGS": 120, "NASDAQCOM": 23000,
	// Yahoo symbols without a recorded chart.
	"NQ=F": 29000, "^GSPC": 7600, "^MOVE": 100, "DX-Y.NYB": 100, "GC=F": 4000, "HG=F": 6.5, "NG=F": 3.0,
	"^VIX3M": 21, "^SKEW": 150, "ZT=F": 104, "SPY": 680, "RSP": 190, "QQQ": 620, "QQEW": 150, "IWM": 250, "SMH": 380, "XLU": 85, "XLP": 80, "XLE": 90,
}

// trends are the growth per observation by frequency.
var trends = map[string]float64{"d": 0.00005, "w": 0.0003, "m": 0.002, "q": 0.006}

func hash(s string) float64 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return float64(h.Sum32()%1000) / 1000
}

// synth is a deterministic wiggly trend: growth per step by frequency plus
// a 1 % sine.
func synth(id, freq string, b float64, i int) float64 {
	ph := hash(id) * 6
	return b*(1+trends[freq]*float64(i)) + math.Abs(b)*0.01*math.Sin(float64(i)/3+ph)
}

func dates(freq string, from, to time.Time, weekday time.Weekday) []time.Time {
	var out []time.Time
	switch freq {
	case "d":
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
				out = append(out, d)
			}
		}
	case "w":
		d := from
		for d.Weekday() != weekday {
			d = d.AddDate(0, 0, 1)
		}
		for ; !d.After(to); d = d.AddDate(0, 0, 7) {
			out = append(out, d)
		}
	case "m":
		for d := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); !d.After(to); d = d.AddDate(0, 1, 0) {
			out = append(out, d)
		}
	case "q":
		for d := time.Date(from.Year(), 1, 1, 0, 0, 0, 0, time.UTC); !d.After(to); d = d.AddDate(0, 3, 0) {
			out = append(out, d)
		}
	}
	return out
}

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

type seeder struct {
	t  *testing.T
	st *store.Store
}

func (s seeder) write(fn func(tx *sql.Tx) error) {
	s.t.Helper()
	if err := s.st.Write(context.Background(), fn); err != nil {
		s.t.Fatal(err)
	}
}

func (s seeder) series(source, id, label, agent, freq, units string, obs []model.Observation, at time.Time) {
	s.t.Helper()
	s.write(func(tx *sql.Tx) error {
		if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: source, SeriesID: id, Label: label, Agent: agent, Frequency: freq, Units: units}); err != nil {
			return err
		}
		_, err := store.UpsertObservations(tx, source, id, obs, at)
		return err
	})
}

func (s seeder) events(evs ...model.ReleaseEvent) {
	s.t.Helper()
	for _, e := range evs {
		s.write(func(tx *sql.Tx) error {
			_, err := store.SyncReleases(tx, e.Source, e.Name, e.ScheduledAt, e.ScheduledAt.Add(time.Second), []model.ReleaseEvent{e})
			return err
		})
		if e.Status != "" && e.Status != model.StatusScheduled {
			evs, err := s.st.Releases(context.Background(), e.ScheduledAt, e.ScheduledAt.Add(time.Second))
			if err != nil {
				s.t.Fatal(err)
			}
			for _, x := range evs {
				if x.Name == e.Name {
					s.write(func(tx *sql.Tx) error {
						return store.SetReleaseStatus(tx, x.ID, e.Status, e.ScheduledAt.Add(5*time.Minute))
					})
				}
			}
		}
	}
}

// Last observation dates per series (as published by 2026-10-25).
var lastObs = map[string]string{
	"q": "2026-04-01", "m": "2026-09-01", "PCE": "2026-08-01", "PCEPI": "2026-08-01", "PCEPILFE": "2026-08-01", "PSAVERT": "2026-08-01",
	"JTSJOL": "2026-08-01", "JTSJOR": "2026-08-01", "JTSHIL": "2026-08-01", "JTSQUR": "2026-08-01", "UNEMPLOY": "2026-08-01",
	"GFDEGDQ188S": "2026-01-01", "GDPNOW": "2026-07-01", "UMCSENT": "2026-10-01", "MICH": "2026-10-01",
	"ICSA": "2026-10-17", "CCSA": "2026-10-10", "w": "2026-10-21", "d": "2026-10-23", "MTS": "2026-09-01",
}

// Released during the export week (fetched_at inside the period).
var releasedAt = map[string]string{
	"ICSA": "2026-10-22T12:31:00Z", "CCSA": "2026-10-22T12:31:00Z", "UMCSENT": "2026-10-23T14:05:00Z", "MICH": "2026-10-23T14:05:00Z",
	"WALCL": "2026-10-22T20:35:00Z", "WDTGAL": "2026-10-22T20:35:00Z", "WRESBAL": "2026-10-22T20:35:00Z",
}

var initialLoadAt = ts("2026-10-01T06:00:00Z")

// seed fills a store with a week of data for W43 (2026-10-18 … 10-25).
func seed(t *testing.T, cfg *config.Config, st *store.Store) {
	s := seeder{t, st}
	ctx := context.Background()
	for _, sc := range cfg.FRED.Series {
		m := fredMeta[sc.ID]
		freq, units := m[0], m[1]
		if _, ok := bases[sc.ID]; !ok {
			t.Fatalf("no base for %s", sc.ID)
		}
		last := lastObs[freq]
		if v, ok := lastObs[sc.ID]; ok {
			last = v
		}
		from := day("2015-01-01")
		if freq == "d" {
			from = day("2024-06-03")
		}
		wd := time.Wednesday
		if sc.ID == "ICSA" || sc.ID == "CCSA" {
			wd = time.Saturday
		}
		var obs []model.Observation
		for i, d := range dates(freq, from, day(last), wd) {
			obs = append(obs, model.Observation{Date: d, Value: math.Round(synth(sc.ID, freq, bases[sc.ID], i)*1000) / 1000})
		}
		if at, ok := releasedAt[sc.ID]; ok {
			// The newest observation came this week; the rest earlier.
			s.series("fred", sc.ID, sc.Label, sc.Agent, freq, units, obs[:len(obs)-1], initialLoadAt)
			s.series("fred", sc.ID, sc.Label, sc.Agent, freq, units, obs, ts(at))
			continue
		}
		s.series("fred", sc.ID, sc.Label, sc.Agent, freq, units, obs, initialLoadAt)
	}
	// Revisions this week: last week's claims, and the GDPNow nowcast.
	icsa, _ := st.Observations(ctx, "fred", "ICSA", day("2026-10-10"))
	s.write(func(tx *sql.Tx) error {
		_, err := store.UpsertObservations(tx, "fred", "ICSA", []model.Observation{{Date: icsa[0].Date, Value: icsa[0].Value + 3000}}, ts("2026-10-22T12:31:00Z"))
		return err
	})
	s.write(func(tx *sql.Tx) error {
		_, err := store.UpsertObservations(tx, "fred", "GDPNOW", []model.Observation{{Date: day("2026-07-01"), Value: 2.4}}, ts("2026-10-24T15:00:00Z"))
		return err
	})
	// Derived series: the history at the initial load, this week's after
	// the H.4.1.
	comp := &derived.Computer{Store: st, Defs: cfg.Derived, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return initialLoadAt }}
	comp.Run(ctx)

	// Yahoo: recorded charts where there are fixtures, synthetic otherwise.
	recorded := map[string]bool{}
	for _, f := range yahoo.Fixtures {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "yahoo", f.File()))
		if err != nil {
			t.Fatal(err)
		}
		ser, err := yahoo.Parse(b, ts("2026-10-24T12:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		s.series("yahoo", f.Symbol, f.Symbol, "usa_master", "d", "USD", ser.Observations(), initialLoadAt)
		s.series("yahoo", f.Symbol+yahoo.VolumeSuffix, f.Symbol, "usa_monetary", "d", "Contracts", ser.Volumes(), initialLoadAt)
		recorded[f.Symbol] = true
	}
	for _, y := range cfg.Yahoo.SymbolsFor("usa") {
		if recorded[y.Symbol] {
			continue
		}
		var obs []model.Observation
		for i, d := range dates("d", day("2025-06-02"), day("2026-10-23"), 0) {
			obs = append(obs, model.Observation{Date: d, Value: math.Round(synth(y.Symbol, "d", bases[y.Symbol], i)*100) / 100})
		}
		s.series("yahoo", y.Symbol, y.Label, y.Agent, "d", "USD", obs, initialLoadAt)
	}
	// Cboe put/call ratios.
	for _, c := range cboe.Series {
		var obs []model.Observation
		for i, d := range dates("d", day("2025-06-02"), day("2026-10-23"), 0) {
			obs = append(obs, model.Observation{Date: d, Value: math.Round(synth(c.ID, "d", 0.8, i)*100) / 100})
		}
		s.series(cboe.Source, c.ID, c.Label, "usa_news", "d", "Ratio", obs, initialLoadAt)
	}
	// Prediction markets: a recession market on both platforms (stored
	// before 0009: no event), the next FOMC meeting on both with its
	// outcomes, a market resolved before the week (not shown).
	kq, pq := "Fed decision in Oct 2026? (On Oct 28, 2026)", "Fed Decision in October?"
	for _, m := range []struct {
		src, id, label string
		base           float64
		last           string
		event, q       string
	}{
		{predict.SourceKalshi, "recession|KXRECSSNBER-27", "Will there be a recession in 2027?", 22, "2026-10-23", "", ""},
		{predict.SourcePolymarket, "recession|us-recession-by-end-of-2026", "US recession by end of 2026?", 7, "2026-10-23", "", ""},
		{predict.SourceKalshi, "fed_next|KXFEDDECISION-26SEP-H0", "Fed decision in Sep 2026? — Fed maintains rate", 60, "2026-09-16", "", ""},
		{predict.SourceKalshi, "fed_next|KXFEDDECISION-26OCT-H0", kq + " — Fed maintains rate", 62, "2026-10-23", "KXFEDDECISION-26OCT", kq},
		{predict.SourceKalshi, "fed_next|KXFEDDECISION-26OCT-H25", kq + " — Hike 25bps", 30, "2026-10-23", "KXFEDDECISION-26OCT", kq},
		{predict.SourceKalshi, "fed_next|KXFEDDECISION-26OCT-H26", kq + " — Hike >25bps", 2, "2026-10-23", "KXFEDDECISION-26OCT", kq},
		{predict.SourceKalshi, "fed_next|KXFEDDECISION-26OCT-C25", kq + " — Cut 25bps", 5, "2026-10-23", "KXFEDDECISION-26OCT", kq},
		{predict.SourcePolymarket, "fed_next|no-change-oct-2026", pq + " — No change", 64, "2026-10-23", "fed-decision-in-october", pq},
		{predict.SourcePolymarket, "fed_next|increase-25-oct-2026", pq + " — 25 bps increase", 31, "2026-10-23", "fed-decision-in-october", pq},
		{predict.SourcePolymarket, "fed_next|decrease-25-oct-2026", pq + " — 25 bps decrease", 4, "2026-10-23", "fed-decision-in-october", pq},
	} {
		var obs []model.Observation
		for i, d := range dates("d", day("2026-09-01"), day(m.last), 0) {
			obs = append(obs, model.Observation{Date: d, Value: math.Round(synth(m.id, "d", m.base, i)*10) / 10})
		}
		s.series(m.src, m.id, m.label, "usa_news", "d", "Percent", obs, initialLoadAt)
		if m.event != "" {
			s.write(func(tx *sql.Tx) error {
				return store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: m.src, SeriesID: m.id, Label: m.label, Agent: "usa_news",
					Frequency: "d", Units: "Percent", Event: m.event, Question: m.q})
			})
		}
	}
	// Treasury: TGA, MTS, auctions, a buyback.
	var tga []model.Observation
	for i, d := range dates("d", day("2025-06-02"), day("2026-10-22"), 0) {
		tga = append(tga, model.Observation{Date: d, Value: math.Round(synth("TGA", "d", 900000, i))})
	}
	s.series(treasury.Source, treasury.SeriesTGA, "TGA daily (DTS)", "usa_fiscal", "d", treasury.UnitsMillionsUSD, tga, initialLoadAt)
	// DTS public debt transactions (millions): bills every business day,
	// coupons settle mid-month and at the month end.
	pdt := map[string][]model.Observation{}
	for i, d := range dates("d", day("2026-06-01"), day("2026-10-22"), 0) {
		coupon := d.Day() == 15 || d.AddDate(0, 0, 1).Day() == 1
		for id, v := range map[string]float64{
			treasury.SeriesBillsIssued: float64(100000 + i%5*10000), treasury.SeriesBillsRedeemed: 95000,
			treasury.SeriesCouponsIssued: 0, treasury.SeriesCouponsRedeemed: 0,
		} {
			if coupon && id == treasury.SeriesCouponsIssued {
				v = 70000
			}
			if coupon && id == treasury.SeriesCouponsRedeemed {
				v = 50000
			}
			pdt[id] = append(pdt[id], model.Observation{Date: d, Value: v})
		}
	}
	for id, obs := range pdt {
		s.series(treasury.Source, id, id, "usa_fiscal", "d", treasury.UnitsMillionsUSD, obs, initialLoadAt)
	}
	for _, id := range []string{treasury.SeriesMTSReceipts, treasury.SeriesMTSOutlays} {
		var obs []model.Observation
		for i, d := range dates("m", day("2015-01-01"), day(lastObs["MTS"]), 0) {
			obs = append(obs, model.Observation{Date: d, Value: math.Round(synth(id, "m", 4e11, i))})
		}
		s.series(treasury.Source, id, id, "usa_fiscal", "m", treasury.UnitsUSD, obs, initialLoadAt)
	}
	s.write(func(tx *sql.Tx) error {
		_, err := store.UpsertAuctions(tx, testAuctions(), initialLoadAt)
		return err
	})
	s.write(func(tx *sql.Tx) error {
		_, err := store.UpsertBuybacks(tx, []model.Buyback{{OperationDate: day("2026-10-21"), StartTime: "13:40",
			OperationType: "Liquidity Support", SecurityType: "TIPS", MaturityBucket: "1Y to 10Y",
			ParOffered: model.Some(4.751e9), ParAccepted: model.Some(7.5e8), MaxRedeemed: model.Some(7.5e8)}}, initialLoadAt)
		return err
	})
	// CFTC: a year and a bit of weekly positions.
	for _, k := range cfg.CFTC.ContractsFor("usa") {
		for j, f := range cftc.Fields {
			var obs []model.Observation
			for i, d := range dates("w", day("2025-08-01"), day("2026-10-20"), time.Tuesday) {
				obs = append(obs, model.Observation{Date: d, Value: math.Round(synth(k.Code+f, "w", 50000*float64(j+1), i))})
			}
			s.series(cftc.Source, cftc.SeriesID(k.Code, f), k.Name, k.Agent, "w", "Contracts", obs, initialLoadAt)
		}
	}
	// Fed texts of the week.
	speech := strings.Repeat("Inflation has moved down but remains above our 2 percent goal. ", 60)
	for _, d := range []model.Document{
		{Source: fedtexts.Source, ExternalID: "waller20261021a", DocType: "speech", Region: "usa", PublishedAt: ts("2026-10-21T16:00:00Z"),
			Author: "Governor Christopher J. Waller", Title: "Economic Outlook", Body: speech, URL: "https://www.federalreserve.gov/newsevents/speech/waller20261021a.htm"},
		{Source: fedtexts.Source, ExternalID: "beigebook202611", DocType: "beige_book", Region: "usa", PublishedAt: ts("2026-10-22T18:00:00Z"),
			Title: "Beige Book", Body: "Overall Economic Activity\n\nEconomic activity was little changed in most Districts.",
			URL: "https://www.federalreserve.gov/monetarypolicy/beigebook202611-summary.htm"},
		// Outside the period: not exported.
		{Source: fedtexts.Source, ExternalID: "old", DocType: "speech", Region: "usa", PublishedAt: ts("2026-10-15T16:00:00Z"),
			Author: "x", Title: "Old", Body: "old"},
	} {
		s.write(func(tx *sql.Tx) error { _, _, err := store.UpsertDocument(tx, d, initialLoadAt); return err })
	}
	// Calendar: this week's events (done, one timed out) and next week's.
	ev := func(source, name, at, importance, status string, rel model.Related) model.ReleaseEvent {
		return model.ReleaseEvent{Source: source, Name: name, ScheduledAt: ts(at), Region: "usa", Importance: importance, Status: status, Related: rel}
	}
	s.events(
		ev("fred_release", "Unemployment Insurance Weekly Claims Report", "2026-10-22T12:30:00Z", "high", model.StatusDone, model.Related{ReleaseID: 180, Series: []string{"CCSA", "ICSA"}}),
		ev("fred_release", "Surveys of Consumers", "2026-10-23T14:00:00Z", "high", model.StatusDone, model.Related{ReleaseID: 91, Series: []string{"MICH", "UMCSENT"}}),
		ev("fred_release", "H.4.1 Factors Affecting Reserve Balances", "2026-10-22T20:30:00Z", "medium", model.StatusDone, model.Related{ReleaseID: 20, Series: []string{"WALCL", "WDTGAL", "WRESBAL"}}),
		ev("fred_release", "Treasury Term Premia", "2026-10-21T12:30:00Z", "medium", model.StatusTimeout, model.Related{ReleaseID: 354, Series: []string{"THREEFYTP10"}}),
		ev(treasury.SourceAuction, "20-Year Bond", "2026-10-21T17:00:00Z", "high", model.StatusDone, model.Related{CUSIP: "912810UX4"}),
		// Next week.
		ev("fred_release", "Secured Overnight Financing Rate Data", "2026-10-26T12:00:00Z", "low", "", model.Related{ReleaseID: 445, Series: []string{"SOFR"}}),
		ev(treasury.Source, treasury.NameDTS, "2026-10-26T20:00:00Z", "low", "", model.Related{Dataset: "dts"}),
		ev(treasury.SourceAuction, "13-Week Bill", "2026-10-26T15:30:00Z", "low", "", model.Related{CUSIP: "912797VU8"}),
		ev(treasury.SourceAuction, "2-Year Note", "2026-10-26T17:00:00Z", "medium", "", model.Related{CUSIP: "91282CRU7"}),
		ev(fedtexts.SourceFOMC, fedtexts.NameDecision, "2026-10-28T18:00:00Z", "high", "", model.Related{Meeting: "2026-10-27/28"}),
		ev(nasdaq.Source, nasdaq.EventName("MSFT"), "2026-10-28T20:00:00Z", "high", "", model.Related{Ticker: "MSFT", ReportTime: nasdaq.TimeAfterClose}),
		ev(nasdaq.Source, nasdaq.EventName("AMZN"), "2026-10-29T20:00:00Z", "high", "", model.Related{Ticker: "AMZN", ReportTime: nasdaq.TimeAfterClose}),
		ev(nasdaq.Source, nasdaq.EventName("AVGO"), "2026-10-30T20:00:00Z", "medium", "", model.Related{Ticker: "AVGO", Estimated: true}),
		ev("fred_release", "Gross Domestic Product", "2026-10-29T12:30:00Z", "high", "", model.Related{ReleaseID: 53, Series: []string{"GDP", "GDPC1", "GDPDEF"}}),
		ev("fred_release", "Unemployment Insurance Weekly Claims Report", "2026-10-29T12:30:00Z", "high", "", model.Related{ReleaseID: 180, Series: []string{"CCSA", "ICSA"}}),
		ev("fred_release", "Debt to Gross Domestic Product Ratios", "2026-10-29T12:30:00Z", "low", "", model.Related{ReleaseID: 263, Series: []string{"GFDEGDQ188S"}}),
		ev("fred_release", "Employment Cost Index", "2026-10-30T12:30:00Z", "medium", model.StatusRescheduled, model.Related{ReleaseID: 11, Series: []string{"ECIALLCIV"}}),
		ev(treasury.Source, treasury.NameFinancingEstimates, "2026-11-02T20:00:00Z", "high", "", model.Related{}),
		ev(treasury.Source, treasury.NameRefundingStatement, "2026-11-04T13:30:00Z", "high", "", model.Related{}),
	)
	for _, m := range []string{"2026-12-09", "2027-01-27", "2027-03-17", "2027-04-28", "2027-06-09", "2027-07-28", "2027-09-15", "2027-10-27"} {
		s.events(ev(fedtexts.SourceFOMC, fedtexts.NameDecision, m+"T19:00:00Z", "high", "", model.Related{}))
	}
	// Fetch states: all fine but one Yahoo symbol, which falls back to FRED.
	s.write(func(tx *sql.Tx) error {
		for _, k := range []string{"fred:DGS10", "fed:press_monetary", "treasury:auctions", "cftc", "yahoo:^NDX", "calendar:fred", "calendar:fed", "calendar:treasury", "calendar:earnings"} {
			if err := store.SetFetchOK(tx, k, "x", ts("2026-10-25T05:00:00Z")); err != nil {
				return err
			}
		}
		return store.SetFetchError(tx, "yahoo:DX-Y.NYB", errString("yahoo DX-Y.NYB: GET …: status 429"), ts("2026-10-24T20:30:00Z"))
	})
}

type errString string

func (e errString) Error() string { return string(e) }

func auction(date, kind, sec, term, tenor, cusip string, reopen bool, high, btc, ind, dir, dlr, amt float64) model.Auction {
	a := model.Auction{AuctionDate: day(date), SecurityType: sec, Kind: kind, Term: term, Tenor: tenor, CUSIP: cusip,
		Reopening: reopen, ClosingTime: "13:00", OfferingAmt: model.Some(amt), TotalAccepted: model.Some(amt * 1.04),
		BidToCover: model.Some(btc), IndirectPct: model.Some(ind), DirectPct: model.Some(dir), DealerPct: model.Some(dlr)}
	if sec == model.KindBill {
		a.HighRate, a.ClosingTime = model.Some(high), "11:30"
	} else {
		a.HighYield = model.Some(high)
	}
	return a
}

// testAuctions: a 20-year reopening and a 5-year TIPS in the week with
// their histories, and the week's 13-week bill and a CMB.
func testAuctions() []model.Auction {
	var out []model.Auction
	for i, d := range []string{"2026-04-22", "2026-05-20", "2026-06-17", "2026-07-22", "2026-08-19", "2026-09-16", "2026-03-18"} {
		out = append(out, auction(d, model.KindBond, model.KindBond, "19-Year 10-Month", "20-Year", "C20"+d, true,
			5.0+0.05*float64(i), 2.55+0.02*float64(i), 66+float64(i), 19, 15-float64(i), 13e9))
	}
	out = append(out, auction("2026-10-21", model.KindBond, model.KindBond, "19-Year 10-Month", "20-Year", "912810UX4", true, 5.469, 2.73, 58.2, 31.6, 10.2, 13e9))
	for i, d := range []string{"2026-04-23", "2026-06-25", "2026-08-20"} {
		out = append(out, auction(d, model.KindTIPS, model.KindNote, "5-Year", "5-Year", "T5"+d, i > 0, 2.1+0.1*float64(i), 2.5, 64, 25, 11, 25e9))
	}
	out = append(out, auction("2026-10-22", model.KindTIPS, model.KindNote, "5-Year", "5-Year", "91282CRR4", false, 2.367, 2.41, 62.0, 32.4, 5.6, 26e9))
	for i := range 7 {
		d := day("2026-10-19").AddDate(0, 0, -7*(6-i)).Format(time.DateOnly)
		out = append(out, auction(d, model.KindBill, model.KindBill, "13-Week", "13-Week", "B13"+d, true, 4.1+0.01*float64(i), 2.6+0.02*float64(i), 60, 8, 32, 95e9))
	}
	cmb := auction("2026-10-22", model.KindCMB, model.KindBill, "10-Day", "10-Day", "CMB1022", false, 3.984, 3.87, 18.3, 1, 80.7, 25e9)
	return append(out, cmb)
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := presetConfigErr(t, "us-equities", "usa", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestWeeklyGolden(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, cfg, st)
	seedPosts(t, st, weekPosts)
	seedMicro(t, cfg, st)

	// Last week's report of USA Monetary was saved; a stale copy sits in
	// fiscal_liquidity and must go.
	prev := filepath.Join(cfg.Export.ReportsDir, "usa", "2026-W42", "monetary.md")
	if err := os.MkdirAll(filepath.Dir(prev), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(prev, []byte("# USA Monetary — отчёт 2026-W42\n"), 0o644)
	stale := filepath.Join(cfg.Export.ExchangeDir, "usa", "fiscal_liquidity", "previous_report.md")
	os.MkdirAll(filepath.Dir(stale), 0o755)
	os.WriteFile(stale, []byte("old"), 0o644)

	// Prompts (§21.5): USA Monetary has one; micro has none and keeps a stale
	// instructions.md that must go.
	cfg.Export.PromptsDir = t.TempDir()
	os.MkdirAll(filepath.Join(cfg.Export.PromptsDir, "usa"), 0o755)
	os.WriteFile(filepath.Join(cfg.Export.PromptsDir, "usa", "monetary.md"), []byte("---\nversion: 2\n---\n# USA Monetary\n"), 0o644)
	staleInstr := filepath.Join(cfg.Export.ExchangeDir, "usa", "micro", InstructionsFile)
	os.MkdirAll(filepath.Dir(staleInstr), 0o755)
	os.WriteFile(staleInstr, []byte("old"), 0o644)

	var sunk []Exported
	x := &Weekly{Store: st, Config: cfg, Sink: sinkFunc(func(e Exported) { sunk = append(sunk, e) }),
		Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
	end, err := PeriodEndOfWeek("2026-W43", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	res, err := x.Run(context.Background(), end, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ISOWeek != "2026-W43" || len(mdFiles(res.Files)) != 10 {
		t.Fatalf("result = %s, %d files", res.ISOWeek, len(res.Files))
	}
	checkReportJSON(t, res.Files)
	for _, f := range res.Files {
		if f.Report == "" {
			continue
		}
		golden := filepath.Join("..", "..", "testdata", "export", "json", f.Report+".json")
		if *updateJSON {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, []byte(f.Body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if want, err := os.ReadFile(golden); err != nil || f.Body != string(want) {
			t.Errorf("%s differs from %s (%v; -update-json)", f.Path, golden, err)
		}
		paths := []string{filepath.Join(cfg.Export.ExchangeDir, f.Path), filepath.Join(cfg.Export.ExchangeDir, "archive", "2026-W43", f.Path)}
		if f.ArchiveOnly {
			if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
				t.Errorf("%s written outside the archive", f.Path)
			}
			paths = paths[1:]
		}
		for _, p := range paths {
			if b, err := os.ReadFile(p); err != nil || string(b) != f.Body {
				t.Errorf("%s: written copy differs (%v)", p, err)
			}
		}
	}
	for _, f := range mdFiles(res.Files) {
		name := "weekly_" + strings.ReplaceAll(strings.TrimSuffix(f.Path, ".md"), string(filepath.Separator), "_") + ".md"
		golden := filepath.Join("..", "..", "testdata", "export", name)
		if *update {
			if err := os.WriteFile(golden, []byte(f.Body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if f.Body != string(want) {
			t.Errorf("%s differs from %s (go test ./internal/export -run WeeklyGolden -update)", f.Path, golden)
		}
		paths := []string{filepath.Join(cfg.Export.ExchangeDir, f.Path), filepath.Join(cfg.Export.ExchangeDir, "archive", "2026-W43", f.Path)}
		if f.ArchiveOnly {
			if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
				t.Errorf("%s written outside the archive", f.Path)
			}
			paths = paths[1:]
		}
		for _, p := range paths {
			if b, err := os.ReadFile(p); err != nil || string(b) != f.Body {
				t.Errorf("%s: written copy differs (%v)", p, err)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(cfg.Export.ExchangeDir, "usa", "monetary", "previous_report.md")); err != nil || !strings.Contains(string(b), "2026-W42") {
		t.Errorf("previous_report.md = %q, %v", b, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale previous_report.md kept: %v", err)
	}
	for _, p := range []string{filepath.Join(cfg.Export.ExchangeDir, "usa", "monetary", InstructionsFile),
		filepath.Join(cfg.Export.ExchangeDir, "archive", "2026-W43", "usa", "monetary", InstructionsFile)} {
		if b, err := os.ReadFile(p); err != nil || !strings.HasPrefix(string(b), "---\nprompt: usa/monetary\nprompt_version: \"2 (") ||
			!strings.HasSuffix(string(b), "---\n\n# USA Monetary\n") {
			t.Errorf("%s = %q, %v", p, b, err)
		}
	}
	if _, err := os.Stat(staleInstr); !os.IsNotExist(err) {
		t.Errorf("stale instructions.md kept: %v", err)
	}
	if len(res.Prompts) != 1 || len(res.NoPrompt) != 4 || res.NoPrompt[0] != "usa/fiscal_liquidity" {
		t.Errorf("prompts = %v, no prompt = %v", res.Prompts, res.NoPrompt)
	}
	if last, ok, err := st.LastExport(context.Background(), KindWeekly, "usa"); err != nil || !ok || !last.Equal(ts("2026-10-25T15:00:04Z")) {
		t.Errorf("exports row = %v %v %v", last, ok, err)
	}
	sunkMD := 0
	for _, p := range sunk[0].Files {
		if strings.HasSuffix(p, ".md") {
			sunkMD++
		}
	}
	if len(sunk) != 1 || sunk[0].Kind != KindWeekly || sunkMD != 23 { // 8 files × (exchange + archive), uncategorized, news_filtered, previous_report, instructions × 2, forecast_scorecard × 2
		t.Errorf("sink = %+v", sunk)
	}
	// Phase 13: no master reports yet — an empty scorecard in the master's folder.
	if res.Forecast == nil || res.ForecastErr != "" || res.Forecast.Path != filepath.Join("usa", "master", "forecast_scorecard.md") {
		t.Errorf("forecast = %+v, err %q", res.Forecast, res.ForecastErr)
	} else if b, err := os.ReadFile(filepath.Join(cfg.Export.ExchangeDir, res.Forecast.Path)); err != nil || !strings.Contains(string(b), "Прогнозов пока нет.") {
		t.Errorf("scorecard %q, %v", b, err)
	}

	// A dry run renders the same and writes nothing more: a changed prompt
	// is not copied, a missing one does not remove the copy.
	os.WriteFile(filepath.Join(cfg.Export.PromptsDir, "usa", "monetary.md"), []byte("---\nversion: 3\n---\n# new\n"), 0o644)
	keep := filepath.Join(cfg.Export.ExchangeDir, "usa", "news", InstructionsFile)
	os.WriteFile(keep, []byte("kept"), 0o644)
	dry, err := x.Run(context.Background(), end, true)
	if err != nil || len(dry.Written) != 0 || dry.Files[0].Body != res.Files[0].Body || len(dry.Prompts)+len(dry.NoPrompt) != 0 {
		t.Errorf("dry run: %d written, %v", len(dry.Written), err)
	}
	if b, _ := os.ReadFile(filepath.Join(cfg.Export.ExchangeDir, "usa", "monetary", InstructionsFile)); strings.Contains(string(b), "# new") {
		t.Error("dry run copied the prompt")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("dry run removed instructions.md: %v", err)
	}
}

type sinkFunc func(Exported)

func (f sinkFunc) Exported(_ context.Context, e Exported) error { f(e); return nil }

func TestPeriodEnd(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Bratislava")
	for week, want := range map[string]string{
		"2026-W43": "2026-10-25T16:00:00+01:00", // after the switch to winter time at 03:00
		"2026-W01": "2026-01-04T16:00:00+01:00",
		"2026-W53": "2027-01-03T16:00:00+01:00",
		"2026-W30": "2026-07-26T16:00:00+02:00",
	} {
		got, err := PeriodEndOfWeek(week, loc)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Errorf("%s = %s, %v; want %s", week, got.Format(time.RFC3339), err, want)
		}
	}
	for _, bad := range []string{"2026-43", "2026-W54", "2025-W53"} {
		if _, err := PeriodEndOfWeek(bad, loc); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
	// The cron fires at 16:00 on Sunday: that period; mid-week: the last finished one.
	for now, want := range map[string]string{
		"2026-10-25T16:00:00+01:00": "2026-10-25T16:00:00+01:00",
		"2026-10-25T15:59:00+01:00": "2026-10-18T16:00:00+02:00",
		"2026-10-28T09:00:00+01:00": "2026-10-25T16:00:00+01:00",
	} {
		if got := LastPeriodEnd(ts(now), loc); got.Format(time.RFC3339) != want {
			t.Errorf("LastPeriodEnd(%s) = %s, want %s", now, got.Format(time.RFC3339), want)
		}
	}
}

// A past week renders the same files after later data arrives (§17.1):
// releases and revisions fetched after the period end stay out.
func TestWeeklyPastWeekUnchangedByLaterData(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, cfg, st)
	seedPosts(t, st, weekPosts)
	seedMicro(t, cfg, st)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
	end, err := PeriodEndOfWeek("2026-W43", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}

	// The next week: new claims and a revision of this week's, September CPI
	// revised and October's out (its obs_date 2026-10-01 is inside W43),
	// a revised H.4.1 week and Net Liquidity, a revised price.
	s := seeder{t, st}
	later := ts("2026-10-29T12:31:00Z")
	last := func(source, id string) model.Observation {
		obs, err := st.Observations(ctx, source, id, time.Time{})
		if err != nil || len(obs) == 0 {
			t.Fatalf("%s/%s: %v", source, id, err)
		}
		return obs[len(obs)-1]
	}
	upsert := func(source, id string, obs ...model.Observation) {
		s.write(func(tx *sql.Tx) error {
			_, err := store.UpsertObservations(tx, source, id, obs, later)
			return err
		})
	}
	icsa := last("fred", "ICSA")
	upsert("fred", "ICSA", model.Observation{Date: icsa.Date, Value: icsa.Value + 7000},
		model.Observation{Date: icsa.Date.AddDate(0, 0, 7), Value: icsa.Value - 5000})
	for _, id := range []string{"CPIAUCSL", "CPILFESL", "UNRATE"} {
		o := last("fred", id)
		upsert("fred", id, model.Observation{Date: o.Date, Value: o.Value * 1.001},
			model.Observation{Date: o.Date.AddDate(0, 1, 0), Value: o.Value * 1.004})
	}
	for _, id := range []string{"WALCL", "DGS10", "DFF"} {
		o := last("fred", id)
		upsert("fred", id, model.Observation{Date: o.Date, Value: o.Value + 1})
	}
	nl := last("derived", "NET_LIQUIDITY")
	upsert("derived", "NET_LIQUIDITY", model.Observation{Date: nl.Date, Value: nl.Value + 50})
	for _, y := range cfg.Yahoo.Symbols {
		o := last("yahoo", y.Symbol)
		upsert("yahoo", y.Symbol, model.Observation{Date: o.Date, Value: o.Value * 1.02})
		break
	}

	after, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("files: %d, want %d", len(after), len(before))
	}
	for i := range before {
		if after[i].Body != before[i].Body {
			t.Errorf("%s changed after later data", before[i].Path)
		}
	}

	// The live export of the next week does see them.
	end44, _ := PeriodEndOfWeek("2026-W44", cfg.Location)
	files, _, err := x.Render(ctx, end44)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files[0].Body, "ICSA") || files[0].Body == before[0].Body {
		t.Errorf("W44 monetary does not show the new claims")
	}
}

// A coupon auction whose CMT cannot be out yet reads "ждём CMT" in ≈tail,
// not "—" (FIXES_W44 A5): Friday's DGS10 is on FRED on Monday 16:15 ET.
func TestWeeklyTailWaitsForCMT(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, cfg, st)
	s := seeder{t, st}
	s.write(func(tx *sql.Tx) error {
		a := auction("2026-10-23", model.KindNote, model.KindNote, "10-Year", "10-Year", "91282CRX1", false, 4.2, 2.5, 65, 20, 15, 42e9)
		if _, err := store.UpsertAuctions(tx, []model.Auction{a}, initialLoadAt); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM observations WHERE source = 'fred' AND series_id = 'DGS10' AND obs_date >= '2026-10-23'`)
		return err
	})
	end, err := PeriodEndOfWeek("2026-W43", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range mdFiles(files) {
		if strings.Contains(f.Path, "fiscal_liquidity") && !strings.Contains(f.Body, "| 10Y | 42.0 млрд $ | 4.200% | ждём CMT 10-23 |") {
			t.Errorf("%s: no wait for the CMT of 2026-10-23", f.Path)
		}
	}
}

// A failed Treasury auctions fetch fails "treasury" in the calendar too
// (its rows show past auctions), and the error is shown in full, its cause
// after the URL included.
func TestWeeklyTreasuryFailure(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, cfg, st)
	s := seeder{t, st}
	msg := `treasury auctions Bill 2026-10-15…2026-11-23: GET https://www.treasurydirect.gov/TA_WS/securities/search?endDate=2026-11-23&format=json&startDate=2026-10-15&type=Bill: ` +
		`Get "https://www.treasurydirect.gov/TA_WS/securities/search?endDate=2026-11-23&format=json&startDate=2026-10-15&type=Bill": dial tcp: lookup www.treasurydirect.gov: no such host`
	s.write(func(tx *sql.Tx) error {
		return store.SetFetchError(tx, "treasury:auctions", errString(msg), ts("2026-10-25T05:15:00Z"))
	})
	end, err := PeriodEndOfWeek("2026-W43", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range mdFiles(files) {
		switch {
		case strings.HasSuffix(f.Path, "calendar_usa.md"):
			if !strings.Contains(f.Body, "sources_ok: [fred, fed, earnings]\nsources_failed: [treasury]\n") {
				t.Errorf("%s: treasury not failed:\n%s", f.Path, f.Body[:400])
			}
		case strings.Contains(f.Path, "fiscal_liquidity"):
			if !strings.Contains(f.Body, "— "+msg+".\n") {
				t.Errorf("%s: error not shown in full", f.Path)
			}
		}
	}
}

// FRED dates ICE BofA Indices on a Saturday at times (2026-10-31): no data
// comes out then, so the timed-out Saturday event does not make HY/IG
// stale; the Friday one, after the last fetch, does.
func TestWeeklyWeekendDailyReleaseNotStale(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		at    string
		stale bool
	}{{"2026-10-24T12:30:00Z", false}, {"2026-10-23T12:30:00Z", true}} {
		t.Run(c.at, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig(t)
			ctx := context.Background()
			st, err := store.Open(ctx, cfg.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			seed(t, cfg, st)
			s := seeder{t, st}
			s.events(model.ReleaseEvent{Source: "fred_release", Name: "ICE BofA Indices", ScheduledAt: ts(c.at), Region: "usa", Importance: "low",
				Status: model.StatusTimeout, Related: model.Related{ReleaseID: 209, Series: []string{"BAMLC0A0CM", "BAMLH0A0HYM2"}}})
			end, err := PeriodEndOfWeek("2026-W43", cfg.Location)
			if err != nil {
				t.Fatal(err)
			}
			x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
			files, _, err := x.Render(ctx, end)
			st.Close()
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range mdFiles(files) {
				if strings.HasSuffix(f.Path, "markets_usa.md") {
					if got := strings.Contains(f.Body, "stale_series: [BAMLH0A0HYM2, BAMLC0A0CM]"); got != c.stale {
						t.Errorf("event %s: HY/IG stale = %v, want %v", c.at, got, c.stale)
					}
				}
			}
		})
	}
}

// Section 1 shows the transform a release is read by (FIXES_W44 B1): CPI
// MoM · YoY with a revision of the previous month in the same units,
// payrolls the change in thousands, GDP QoQa; claims keep the level.
func TestWeeklyReleasedFacts(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, cfg, st)
	s := seeder{t, st}
	at := ts("2026-10-20T12:31:00Z") // before the week: the known history
	set := func(id string, obs ...model.Observation) {
		s.write(func(tx *sql.Tx) error {
			_, err := store.UpsertObservations(tx, "fred", id, obs, at)
			return err
		})
	}
	o := func(date string, v float64) model.Observation { return model.Observation{Date: day(date), Value: v} }
	// CPI: August 100 → September 100.2 (MoM 0.2%, YoY 4.4% on 96),
	// revised this week to 100.1 (0.1%, 4.3%); October 100.4 (0.3%, 3.5% on 97).
	set("CPIAUCSL", o("2025-09-01", 96), o("2025-10-01", 97), o("2026-08-01", 100), o("2026-09-01", 100.2))
	set("PAYEMS", o("2026-08-01", 158000), o("2026-09-01", 158100))
	at = ts("2026-10-29T12:31:00Z")
	set("CPIAUCSL", o("2026-09-01", 100.1), o("2026-10-01", 100.4))
	set("PAYEMS", o("2026-10-01", 158250))
	set("GDPC1", o("2026-07-01", 24100))
	set("ICSA", o("2026-10-24", 230000))

	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-11-01T15:00:04Z") }}
	end, _ := PeriodEndOfWeek("2026-W44", cfg.Location)
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	body := files[0].Body
	sec := body[strings.Index(body, "## 1."):strings.Index(body, "## 2.")]
	row := func(id string) string {
		for _, l := range strings.Split(sec, "\n") {
			if strings.Contains(l, "| "+id+" |") {
				return l
			}
		}
		t.Fatalf("no %s row in\n%s", id, sec)
		return ""
	}
	if r := row("CPIAUCSL"); !strings.Contains(r, "| MoM 0.3% · YoY 3.5% | MoM 0.1% · YoY 4.3% | MoM +0.2 п.п. · YoY −0.8 п.п. | MoM 0.2% · YoY 4.4% → MoM 0.1% · YoY 4.3% |") {
		t.Errorf("CPI row: %s", r)
	}
	if r := row("PAYEMS"); !strings.Contains(r, "| +150 тыс. | +100 тыс. | +50 тыс. |") {
		t.Errorf("PAYEMS row: %s", r)
	}
	if r := row("GDPC1"); !strings.Contains(r, "| QoQa ") {
		t.Errorf("GDPC1 row: %s", r)
	}
	if r := row("ICSA"); !strings.Contains(r, "| 230 000 |") {
		t.Errorf("ICSA row: %s", r)
	}
}

// fomc_diff.md is re-rendered by a weekly export in a week without a
// statement, opening with the age of the last meeting (FIXES_W44 B5).
func TestWeeklyFOMCRefresh(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, cfg, st)
	s := seeder{t, st}
	for _, d := range []model.Document{
		{Source: fedtexts.Source, ExternalID: "monetary20260916a", DocType: fedtexts.DocStatement, Region: "usa", PublishedAt: ts("2026-09-16T18:00:00Z"),
			Title: "Federal Reserve issues FOMC statement", Body: "The Committee decided to raise the target range for the federal funds rate by 1/4 percentage point to 3-3/4 to 4 percent.\n"},
		{Source: fedtexts.Source, ExternalID: "monetary20261028a", DocType: fedtexts.DocStatement, Region: "usa", PublishedAt: ts("2026-10-28T18:00:00Z"),
			Title: "Federal Reserve issues FOMC statement", Body: "The Federal Open Market Committee approved the following statement for release by a 10 – 2 vote:\n" +
				"The Committee decided to maintain the target range for the federal funds rate at 3-3/4 to 4 percent.\n" +
				"Voting against the monetary policy action were Lisa D. Cook and Beth M. Hammack, who preferred to raise the target range for the federal funds rate by 1/4 percentage point at this meeting.\n"},
		{Source: fedtexts.Source, ExternalID: "monetary20261028a1", DocType: fedtexts.DocImplementationNote, Region: "usa", PublishedAt: ts("2026-10-28T18:00:00Z"),
			Title: "Implementation Note issued October 28, 2026", Body: "The Board voted to maintain IORB at 3.90 percent."},
		{Source: fedtexts.Source, ExternalID: "FOMCpresconf20261028", DocType: fedtexts.DocPressConference, Region: "usa", PublishedAt: ts("2026-10-29T15:22:11Z"),
			Title: "Transcript of Chair's Press Conference", Body: "CHAIRMAN. Good day."},
	} {
		s.write(func(tx *sql.Tx) error { _, _, err := store.UpsertDocument(tx, d, initialLoadAt); return err })
	}
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-11-08T15:00:04Z") }}
	find := func(iso string) (File, bool) {
		end, _ := PeriodEndOfWeek(iso, cfg.Location)
		files, _, err := x.Render(ctx, end)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range mdFiles(files) {
			if f.Path == filepath.Join("usa", "monetary", "fomc_diff.md") {
				return f, true
			}
		}
		return File{}, false
	}
	// W44 had the meeting: no notice; the transcript posted after the event
	// export is in.
	f, ok := find("2026-W44")
	if !ok || strings.Contains(f.Body, "⚠") ||
		!strings.Contains(f.Body, "\n## Implementation Note (2026-10-28 14:00 ET)\n\nThe Board voted to maintain IORB at 3.90 percent.\n") ||
		!strings.Contains(f.Body, "\n## Пресс-конференция: Transcript of Chair's Press Conference (расшифровка опубликована 2026-10-29 11:22 ET)\n\nCHAIRMAN. Good day.\n") {
		t.Errorf("W44 fomc_diff.md:\n%s", f.Body)
	}
	f, ok = find("2026-W45")
	if !ok {
		t.Fatal("W45 did not re-render fomc_diff.md")
	}
	for _, want := range []string{
		"---\n\n⚠ Последнее заседание: 2026-10-28 (1 нед. назад). Следующее: 2026-12-09\n\n# USA Monetary — FOMC statement — 2026-10-28",
		"| Решение | без изменений, 3.75–4.00% | повышение на 25 б.п. → 3.75–4.00% |",
		"| Голосование | 10–2 | — |",
		"| Против (позиция) | Lisa D. Cook, Beth M. Hammack — за повышение на 25 б.п. | — |",
	} {
		if !strings.Contains(f.Body, want) {
			t.Errorf("missing %q in\n%s", want, f.Body)
		}
	}
}
