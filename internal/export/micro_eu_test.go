package export

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/esef"
	"macro-collector/internal/sources/irpress"
	"macro-collector/internal/sources/irxlsx"
	"macro-collector/internal/store"
)

// trend is n weekday closes ending on last, from start growing by step a day.
func trend(last string, n int, start, step float64) []model.Observation {
	obs := weekdays(last, n, 0)
	for i := range obs {
		obs[i].Value = start + step*float64(i)
	}
	return obs
}

// eu/micro/current_week.md of W46 from recorded facts (SAP companyfacts,
// Airbus ESEF), releases (Siemens RSS, an Airbus release page) and closes:
// reports of the week with the reaction, the next dates, prices, annual
// numbers in €, releases with outlook paragraphs and quotes.
func TestEuropeMicroGolden(t *testing.T) {
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
	read := func(parts ...string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{repoRoot, "testdata"}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	sap, _ := cfg.Issuers.Issuer("SAP")
	air, _ := cfg.Issuers.Issuer("AIR")
	sie, _ := cfg.Issuers.Issuer("SIE")
	sapFacts, err := edgar.ParseCompanyFactsIn(read("edgar", "companyfacts_SAP.json"), "ifrs-full", sap.Facts.Concepts.Tags())
	if err != nil {
		t.Fatal(err)
	}
	filings, err := esef.ParseFilings(read("esef", "airbus_filings.json"))
	if err != nil {
		t.Fatal(err)
	}
	airFacts, err := esef.ParseFacts(read("esef", "airbus_2025.json"), filings[len(filings)-1], "ifrs-full", air.Facts.Concepts.Tags())
	if err != nil {
		t.Fatal(err)
	}
	var quarterFacts []model.CompanyFact
	for _, x := range []struct {
		ticker, file, name string
		fy, q              int
	}{{"SIE", "siemens-2026-q3.xlsx", "2026-q3-Financial-Results-Excel.xlsx", 2026, 3}, {"DTE", "dt-26q3.xlsx", "dt-26q3-backup.xlsx", 2026, 3}} {
		is, _ := cfg.Issuers.Issuer(x.ticker)
		pairs, err := irxlsx.Parse(read("irxlsx", x.file), *is.Quarterly, x.fy, x.q)
		if err != nil {
			t.Fatal(err)
		}
		quarterFacts = append(quarterFacts, collect.QuarterFacts(is, irxlsx.File{Name: x.name, FY: x.fy, Q: x.q}, pairs, time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))...)
	}
	sieDocs, err := irpress.ParseFeed(read("irpress", "siemens.rss"), *sie.Press, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := irpress.PageText(read("irpress", "airbus_article.html"), air.Press.Body)
	if err != nil {
		t.Fatal(err)
	}
	const cathay = "https://www.airbus.com/en/newsroom/press-releases/2026-11-the-cathay-group-orders-14-additional-airbus-aircraft"
	docs := append(sieDocs, model.Document{ExternalID: cathay, URL: cathay, Title: "The Cathay Group orders 14 additional Airbus aircraft",
		PublishedAt: time.Date(2026, 11, 11, 3, 59, 24, 0, time.UTC), Body: body})
	for i := range docs {
		docs[i].DocType, docs[i].Region = irpress.DocType, "eu"
		docs[i].Source = irpress.Source("SIE")
		if docs[i].URL == cathay {
			docs[i].Source = irpress.Source("AIR")
		}
	}
	fetched := time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)
	const last = "2026-11-13"
	closes := map[string][]model.Observation{
		"^GDAXI": trend(last, 40, 25000, 10), "SAP.DE": trend(last, 40, 210, -0.5), "SIE.DE": trend(last, 40, 230, 0.4),
		"ALV.DE": trend(last, 40, 360, 0.2), "AIR.DE": trend(last, 40, 200, 0.3), "DTE.DE": trend(last, 40, 28, 0.01),
		"ENR.DE": trend(last, 40, 110, 0.6), "RHM.DE": trend(last, 40, 1700, -4),
		// no MBG.DE: the missing section names it
	}
	// Siemens reported on 11-12 before the open: a jump that day.
	for i, o := range closes["SIE.DE"] {
		if !o.Date.Before(day("2026-11-12")) {
			closes["SIE.DE"][i].Value += 12
		}
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		for sym, obs := range closes {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: "yahoo", SeriesID: sym, Frequency: "d", Units: "EUR"}); err != nil {
				return err
			}
			if _, err := store.UpsertObservations(tx, "yahoo", sym, obs, fetched); err != nil {
				return err
			}
		}
		for ticker, fs := range map[string][]model.CompanyFact{"SAP": sapFacts, "AIR": airFacts} {
			if _, err := store.UpsertCompanyFacts(tx, ticker, fs, fetched); err != nil {
				return err
			}
		}
		for _, f := range quarterFacts {
			if _, err := store.UpsertCompanyFacts(tx, f.Ticker, []model.CompanyFact{f}, fetched); err != nil {
				return err
			}
		}
		for _, d := range docs {
			if _, _, err := store.UpsertDocument(tx, d, fetched); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	region := &cfg.Regions[0]
	x := &Weekly{Store: st, Config: cfg, Region: region, Now: func() time.Time { return time.Date(2026, 11, 15, 15, 0, 4, 0, time.UTC) }}
	end, err := PeriodEndOfWeek("2026-W46", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	var md string
	var mine []File
	for _, f := range files {
		if strings.HasPrefix(f.Path, "eu/micro/") {
			mine = append(mine, f)
			if strings.HasSuffix(f.Path, ".md") {
				md = f.Body
			}
		}
		if strings.HasPrefix(f.Path, "json/eu/micro/") || f.Report == config.ReportIssuers {
			mine = append(mine, f)
		}
	}
	if md == "" {
		t.Fatal("no eu/micro/current_week.md")
	}
	checkReportJSON(t, mine)
	checkGolden(t, "eu_micro_current_week.md", md)
	// LOOKUP 2026-11-11: SAP revenue 2025 36.8 bn €, diluted EPS 6.10 €;
	// Airbus 73.42 bn €, EBIT 6.082 bn €.
	for _, want := range []string{"| SAP | 2025 | EDGAR 20-F, 2026-02-26 | 36.80 (+7.7%)", "6.10 €", "| Airbus | 2025 | ESEF, 2026-03-04 | 73.42 (+6.1%) | 6.08",
		"| Siemens | 2026-11-12, до открытия | Q4 FY2026 | +5.1% (11-12)", "- MBG: нет цен Yahoo (MBG.DE).",
		// step 18: Siemens Q3 FY2026 and DT Q3 2026 from the issuers' Excel
		"| Siemens | Q3 FY2026 (до 2026-06-30) | 20.79 (+7.3%) | 3.52 (+25.0%) (Profit Industrial Business) | 2.27 (+10.9%) | 2.93 €",
		"| Deutsche Telekom | Q3 2026 (до 2026-09-30) | 31.10 (+7.5%) (Net revenue) | 7.38 (+22.5%) (EBIT) | 2.76 (+13.8%) | — (нет в файле)"} {
		if !strings.Contains(md, want) {
			t.Errorf("no %q", want)
		}
	}
	if n := len([]rune(md)); n > 25000 {
		t.Errorf("file is %d characters, want under 25 000", n)
	}
}
