package dashboard

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

// The Europe preset (phase 11, step 20): its pages come from the config,
// series of the new sources answer, and the demo site publishes only the
// allowed ones — the ECB, the Bundesbank's own Bund yields, Eurostat; not
// Yahoo, STOXX, ZEW, hand input or the Bundesbank's Destatis series.
func TestEuropePresetDashboard(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvDataDir, dir)
	cfg, err := config.LoadAt(filepath.Join("..", "..", "presets", "eu-equities.yaml"), filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	w, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	seed := func(source, id, units, freq string, v float64) {
		err := w.Write(ctx, func(tx *sql.Tx) error {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: source, SeriesID: id, Label: id, Agent: "x", Frequency: freq, Units: units}); err != nil {
				return err
			}
			_, err := store.UpsertObservations(tx, source, id, []model.Observation{{Date: day.AddDate(0, 0, -1), Value: v}, {Date: day, Value: v}}, day)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("ecb", "ECB_DFR", config.UnitsRate, "d", 2.0)
	seed("ecb", "ESTR", config.UnitsRate, "d", 1.93)
	seed("bundesbank", "BUND_10Y", config.UnitsRate, "d", 3.28)
	seed("eurostat", "EC_ICI_DE", "Balance", "m", -12.5)
	seed("bundesbank", "DE_ORDERS", "Index", "m", licensed(1))
	seed("yahoo", "^GDAXI", "EUR", "d", licensed(2))
	seed("stoxx", "VDAX_NEW", "Index Points", "d", licensed(3))
	seed("zew", "ZEW_SENTIMENT", "Balance", "m", licensed(4))
	seed("manual", "IFO_CLIMATE", "Index", "m", licensed(5))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := store.OpenReadOnly(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d := &Data{Config: cfg, Store: r, Now: func() time.Time { return day }}

	meta := d.Meta()
	if len(meta.Regions) != 1 || meta.Regions[0].ID != "eu" {
		t.Fatalf("regions %+v", meta.Regions)
	}
	var ids []string
	for _, p := range meta.Regions[0].Pages {
		ids = append(ids, p.ID)
	}
	if !slices.Equal(ids, []string{"overview", "ecb", "bonds", "economy", "labor", "credit", "micro", "markets", "reactions", "news-sentiment", "calendar", "reports"}) {
		t.Errorf("pages %v", ids)
	}
	parts := map[string]string{}
	for _, p := range meta.Regions[0].Pages {
		for _, c := range p.Charts {
			for _, s := range c.Series {
				if s.ID == "WAGE_TRACKER" {
					parts[c.Title] = s.Part
				}
			}
		}
	}
	if len(parts) != 2 || parts["Wage tracker ЕЦБ: факт"] != config.PartActual || parts["Wage tracker ЕЦБ: прогноз по подписанным договорам"] != config.PartForecast {
		t.Errorf("wage tracker charts %v", parts)
	}
	for _, ref := range []string{"ecb/ECB_DFR", "bundesbank/BUND_10Y", "eurostat/EC_ICI_DE", "stoxx/VDAX_NEW", "zew/ZEW_SENTIMENT", "yahoo/^GDAXI", "manual/IFO_CLIMATE"} {
		source, id, _ := strings.Cut(ref, "/")
		s, err := d.Series(ctx, source, id, "", time.Time{})
		if err != nil || len(s.Points) != 2 {
			t.Errorf("%s: %+v %v", ref, s, err)
		}
	}

	issuersView := func() (ViewMeta, bool) {
		for _, p := range d.Meta().Regions[0].Pages {
			for _, v := range p.Views {
				if v.View == "issuers" {
					return v, true
				}
			}
		}
		return ViewMeta{}, false
	}
	if v, ok := issuersView(); !ok || v.Agent != "micro" || v.Hidden {
		t.Errorf("issuers view %+v %v", v, ok)
	}

	d.Demo = true
	if v, _ := issuersView(); !v.Hidden {
		t.Error("issuer data is open in the demo")
	}
	site := filepath.Join(t.TempDir(), "site")
	if _, err := d.BuildSite(ctx, site, frontend); err != nil {
		t.Fatal(err)
	}
	files := siteFiles(t, site)
	var published []string
	for path, body := range files {
		for i := 1; i <= 5; i++ {
			if strings.Contains(body, num(licensed(i))) {
				t.Errorf("%s contains a closed value", path)
			}
		}
		if strings.HasPrefix(path, "api/series/") {
			parts := strings.Split(path, "/")
			ref := parts[2] + "/" + parts[3]
			published = append(published, ref)
			if !slices.Contains(cfg.Dashboard.DemoAllow, ref) {
				t.Errorf("series outside demo_allow: %s", path)
			}
			for _, closed := range []string{"yahoo/", "stoxx/", "zew/", "manual/", "bundesbank/DE_"} {
				if strings.HasPrefix(ref, closed) {
					t.Errorf("closed source published: %s", path)
				}
			}
		}
	}
	for _, want := range []string{"ecb/ECB_DFR", "bundesbank/BUND_10Y", "eurostat/EC_ICI_DE"} {
		if !slices.Contains(published, want) {
			t.Errorf("allowed series %s missing from the demo (published %v)", want, published)
		}
	}
}

// licensed is a distinctive value for a closed series.
func licensed(i int) float64 { return 87654.3 + float64(i)/100 }

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
