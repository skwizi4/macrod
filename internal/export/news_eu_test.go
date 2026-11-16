package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

// sharedNewsPosts are posts of the week W45 for both regions' news: US and
// eu_* tags, one channel of each region.
var sharedNewsPosts = []testPost{
	{source: "telegram:marketnews_ua", id: "501", at: "2026-11-03T08:00:00Z",
		body: "🏦Ймовірність утримання ставки ФРС у грудні вже 70% проти 62% тиждень тому.", tags: "usa_monetary:0.95"},
	{source: "telegram:marketnews_ua", id: "502", at: "2026-11-03T10:10:00Z",
		body: "🇩🇪 ZEW Economic Sentiment 31.2 (очік. 30.0🔼)\n\nНімецька економіка тримається краще, ніж чекали.", tags: "eu_macro:0.95"},
	{source: "telegram:marketnews_ua", id: "503", at: "2026-11-04T09:00:00Z",
		body: "🇫🇷 Бюджет Франції знову під питанням: спред OAT-Bund розширився до 80 б.п.", tags: "eu_fiscal:0.9"},
	{source: "telegram:macro_views", id: "601", at: "2026-11-05T09:00:00Z", opinion: true, region: "eu",
		body: "ЕЦБ, на мой взгляд, закончил цикл: депозитная ставка 2.50% останется до лета, евро держится на разнице ставок.", tags: "eu_ecb:0.9,usa_monetary:0.4"},
	{source: "telegram:marketnews_ua", id: "504", at: "2026-11-06T13:40:00Z",
		body: "🛢 Brent +3%: OPEC+ продовжить скорочення видобутку.", tags: "energy_geopolitics:0.9"},
}

// minuteBars are one-minute closes from..to moving by step a minute.
func minuteBars(from, to time.Time, start, step float64) []store.IntradayBar {
	var out []store.IntradayBar
	v := start
	for t := from; t.Before(to); t = t.Add(time.Minute) {
		out = append(out, store.IntradayBar{TS: t, Close: v})
		v += step
	}
	return out
}

// renderedFile is the body of the rendered file whose path ends in suffix.
func renderedFile(t *testing.T, files []File, suffix string) string {
	t.Helper()
	for _, f := range files {
		if strings.HasSuffix(f.Path, suffix) {
			return f.Body
		}
	}
	t.Fatalf("no %s", suffix)
	return ""
}

// checkGolden compares a rendered file with its golden file (-update).
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	golden := filepath.Join(repoRoot, "testdata", "export", name)
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s (go test ./internal/export -run %s -update)\n%s", name, golden, t.Name(), got)
	}
}

// bodyAfterHeader drops the front matter and the title line.
func bodyAfterHeader(s string) string {
	if i := strings.Index(s, "\n---\n"); i >= 0 {
		s = s[i+5:]
	}
	if i := strings.Index(s, "\n## "); i >= 0 {
		s = s[i:]
	}
	return s
}

// Phase 11, step 16: EU News is the shared news file (the same body as USA
// News on the same posts, eu_* posts under «Европа и ЕЦБ») and its own
// sentiment.md over European instruments; COT Euro FX is not repeated.
func TestEuropeNewsAndSentimentGolden(t *testing.T) {
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
	seedPosts(t, st, sharedNewsPosts)
	fetched := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	s := seeder{t, st}
	const last = "2026-11-06"
	s.series("yahoo", "^GDAXI", "GER40", "eu_master", "d", "Index", weekdays(last, 60, 25800), fetched)
	s.series("yahoo", "EURUSD=X", "EUR/USD", "eu_monetary", "d", config.UnitsFX, weekdays(last, 60, 1.1228), fetched)
	s.series("yahoo", "TTF=F", "TTF Gas", "eu_monetary", "d", "Euro per MWh", weekdays(last, 60, 31.4), fetched)
	s.series("yahoo", "^VIX", "VIX", "eu_news", "d", "Index", weekdays(last, 60, 17.2), fetched)
	s.series("bundesbank", "BUND_10Y", "Bund 10Y", "eu_fiscal", "d", config.UnitsRate, weekdays(last, 60, 3.28), fetched)
	s.series("stoxx", "VDAX_NEW", "VDAX-NEW", "eu_master", "d", config.UnitsPoints, weekdays(last, 60, 18.66), fetched)
	s.series("stoxx", "VSTOXX", "VSTOXX", "eu_master", "d", config.UnitsPoints, weekdays(last, 60, 19.31), fetched)
	s.events(
		model.ReleaseEvent{Source: "relcal:zew", Name: "ZEW (Германия)", ScheduledAt: ts("2026-11-03T10:05:00Z"), Region: "eu", Importance: "high"},
		// A US release: Europe's sentiment lists it although the preset has no usa region.
		model.ReleaseEvent{Source: "fred", Name: "Consumer Price Index", ScheduledAt: ts("2026-11-04T13:30:00Z"), Region: "usa", Importance: "high"},
		// Not in the reaction list.
		model.ReleaseEvent{Source: "relcal:eurostat", Name: "Госдолг стран ЕС (квартал)", ScheduledAt: ts("2026-11-05T10:00:00Z"), Region: "eu", Importance: "low"},
	)
	s.write(func(tx *sql.Tx) error {
		for sym, start := range map[string]float64{"^GDAXI": 25800, "EURUSD=X": 1.1228} {
			step := 0.5
			if sym == "EURUSD=X" {
				step = 0.00001
			}
			for _, d := range []string{"2026-11-03", "2026-11-04"} {
				day := ts(d + "T08:00:00Z")
				if err := store.UpsertIntradayBars(tx, sym, minuteBars(day, day.Add(9*time.Hour), start, step), time.Time{}); err != nil {
					return err
				}
			}
		}
		return nil
	})

	x := &Weekly{Store: st, Config: cfg, Region: &cfg.Regions[0], Now: func() time.Time { return time.Date(2026, 11, 8, 15, 0, 4, 0, time.UTC) }}
	end, err := PeriodEndOfWeek("2026-W45", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	news := renderedFile(t, files, "eu/news/current_week.md")
	sent := renderedFile(t, files, "eu/news/sentiment.md")
	var sm SentimentReport
	if err := json.Unmarshal([]byte(renderedFile(t, files, "eu/news/sentiment.json")), &sm); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(news, "### Европа и ЕЦБ") || !strings.Contains(news, "Бюджет Франції") || !strings.Contains(news, "# EU News — 2026-W45") {
		t.Error("EU News lacks the eu_* posts or its title")
	}
	if len(sm.Positioning.Contracts) != 0 || strings.Contains(sent, "Leveraged Funds") || strings.Contains(sent, "Put/call Cboe") {
		t.Error("EU sentiment repeats COT or put/call")
	}
	if got := sm.Header.SourcesOK; strings.Join(append(got, sm.Header.SourcesFailed...), ",") != "yahoo,bundesbank,stoxx" {
		t.Errorf("sources %v / %v, want yahoo, bundesbank, stoxx", sm.Header.SourcesOK, sm.Header.SourcesFailed)
	}
	if len(sm.Reaction.Rows) != 2 || sm.Reaction.Rows[1].Labels[0] != "US CPI" {
		t.Errorf("reaction rows %+v, want ZEW and US CPI", sm.Reaction.Rows)
	}
	checkGolden(t, "eu_news_current_week.md", news)
	checkGolden(t, "eu_news_sentiment.md", sent)

	// The same posts through the USA preset: the same body.
	t.Setenv(config.EnvDataDir, t.TempDir())
	us := testConfig(t)
	us.DBPath = cfg.DBPath
	ux := &Weekly{Store: st, Config: us, Region: &us.Regions[0], Now: x.Now}
	ufiles, _, err := ux.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	// Only the file's title differs ("Число постов USA News …").
	usBody := strings.ReplaceAll(bodyAfterHeader(renderedFile(t, ufiles, "usa/news/current_week.md")), "USA News", "EU News")
	if got, want := usBody, bodyAfterHeader(news); got != want {
		t.Errorf("USA News body differs from EU News:\n%s\n---\n%s", got, want)
	}
}
