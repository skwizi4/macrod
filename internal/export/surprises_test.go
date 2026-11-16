package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

// Real posts of marketnews_ua and macro_views, 2026-W43 and W44.
func TestExtractSurprisesRealPosts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "export", "surprise_posts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var posts []struct{ Ref, Text string }
	if err := json.Unmarshal(raw, &posts); err != nil {
		t.Fatal(err)
	}
	want := map[string][]surprise{
		// W44: facts after PCE and GDP; ✅ lines have no expectation.
		"marketnews_ua/37305": {
			{label: "GDP Q3 (1st)", actual: "3.4%", expected: "3.2%", diff: "+0.2 п.п."},
			{label: "IJC", actual: "194 тис.", expected: "198 тис.", diff: "−4 тис."},
		},
		"marketnews_ua/37271": nil, // calendar announcement: "- очікування: 4.00%", no actual value
		"marketnews_ua/37294": nil, // calendar announcement
		"marketnews_ua/37296": nil, // expectation before the release
		"marketnews_ua/37262": nil, // a bank's forecast, not a release
		// W43 earnings: the cashtag names the company.
		"marketnews_ua/37205": {
			{label: "$SPCX Revenue", actual: "$14.58 млрд", expected: "$12.87 млрд", diff: "+$1.71 млрд"},
			{label: "$SPCX EPS", actual: "$0.35", expected: "$0.13", diff: "+$0.22"},
		},
		"marketnews_ua/37217": {
			{label: "$META Перегляд CapEx", actual: "$135-145 млрд", expected: "$132 млрд", diff: "—"},
		},
		"macro_views/377": nil, // "против consensus 3,4%" inside prose is not parsed
	}
	if len(posts) != len(want) {
		t.Fatalf("%d posts in testdata, %d cases", len(posts), len(want))
	}
	for _, p := range posts {
		got := extractSurprises(p.Text)
		if !slices.Equal(got, want[p.Ref]) {
			t.Errorf("%s:\n got %+v\nwant %+v", p.Ref, got, want[p.Ref])
		}
	}
}

func TestExtractSurprisesFormats(t *testing.T) {
	tests := []struct {
		line string
		want []surprise
	}{
		{"CPI YoY 3.1% (est. 3.0%)", []surprise{{"CPI YoY", "3.1%", "3.0%", "+0.1 п.п."}}},
		{"• Nonfarm Payrolls: 120K (consensus 150K)", []surprise{{"Nonfarm Payrolls", "120K", "150K", "−30K"}}},
		{"Безработица 4,3% (ожидания 4,3%)", []surprise{{"Безработица", "4,3%", "4,3%", "0"}}},
		{"Retail sales 0.4% (прогноз: 0.2 п.п.)", []surprise{{"Retail sales", "0.4%", "0.2 п.п.", "—"}}},
		{"ISM 49.1 (expected: 50)", []surprise{{"ISM", "49.1", "50", "−0.9"}}},
		{"Ставка без змін (очік. 4.00%)", nil}, // no actual value
		{"GDP Q3 (очік. 3.2%)", nil}, // "Q3" is not a value
	}
	for _, tt := range tests {
		if got := extractSurprises(tt.line); !slices.Equal(got, tt.want) {
			t.Errorf("%q:\n got %+v\nwant %+v", tt.line, got, tt.want)
		}
	}
}

// The actual of a surprise comes from our series when its release was
// fetched during the week; otherwise the post's value stays (2026-11-03).
func TestNewsSurpriseFacts(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := seeder{t, st}
	var claims []model.Observation
	for i := range 10 {
		claims = append(claims, model.Observation{Date: day("2026-08-15").AddDate(0, 0, 7*i), Value: 210000 + 1000*float64(i)})
	}
	s.series("fred", "ICSA", "Initial Claims", "usa_monetary", "W", "Number", claims, ts("2026-10-01T12:00:00Z"))
	// The week's release, fetched on Thursday.
	s.write(func(tx *sql.Tx) error {
		_, err := store.UpsertObservations(tx, "fred", "ICSA", []model.Observation{{Date: day("2026-10-24"), Value: 191500}}, ts("2026-10-22T12:35:00Z"))
		return err
	})
	// GDP only from the initial history load: not this week's release.
	var gdp []model.Observation
	for i := range 8 {
		gdp = append(gdp, model.Observation{Date: day("2024-10-01").AddDate(0, 3*i, 0), Value: 23000 + 100*float64(i)})
	}
	s.series("fred", "GDPC1", "Real GDP", "usa_monetary", "Q", "Billions of Chained 2017 Dollars", gdp, ts("2026-10-01T12:00:00Z"))
	// Core PCE: the history, then September fetched in the week.
	var pce []model.Observation
	for i := range 12 {
		pce = append(pce, model.Observation{Date: day("2025-09-01").AddDate(0, i, 0), Value: 121.6 + (125.0-121.6)*float64(i)/11})
	}
	s.series("fred", "PCEPILFE", "Core PCE", "usa_monetary", "M", "Index 2017=100", pce, ts("2026-10-01T12:00:00Z"))
	s.write(func(tx *sql.Tx) error {
		_, err := store.UpsertObservations(tx, "fred", "PCEPILFE", []model.Observation{{Date: day("2026-09-01"), Value: 125.4125}}, ts("2026-10-23T12:35:00Z"))
		return err
	})
	seedPosts(t, st, []testPost{
		// A forecast before the release (W44 post 37296); a forecast of
		// a series we do not have is left out.
		{source: "telegram:marketnews_ua", id: "296", at: "2026-10-23T07:00:00Z", tags: "usa_monetary:0.95",
			body: "Очікування щодо сьогоднішнього PCE\n\n• Зростання Core: прогнозують +0,34% m/m (3,1% y/y).\n\n• ISM: прогнозують 49.5%."},
		{source: "telegram:marketnews_ua", id: "110", at: "2026-10-22T12:31:00Z",
			body: "🇺🇸Показники:\n\n▫️GDP Q3 (1st) 3.4% (очік. 3.2%🔼)\n\n▫️IJC 194 тис. (очік. 198 тис.🔽)\n\n▫️Nonfarm Payrolls +29 тис (очік. +89 тис)🔽", tags: "usa_monetary:0.95"},
		// The same line in another channel: one row.
		{source: "telegram:rates_example", id: "5", at: "2026-10-22T13:00:00Z", body: "IJC 194 тис. (очік. 198 тис.)", tags: "usa_monetary:0.9"},
	})
	end, _ := PeriodEndOfWeek("2026-W43", cfg.Location)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
	w, err := x.newWeek(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	w.useReport(config.ReportNews)
	news, _, _, err := w.news(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, sec, _ := strings.Cut(news, "## 1. Сюрпризы недели\n")
	sec, _, _ = strings.Cut(sec, "\n## ")
	for _, want := range []string{
		"| 2026-10-22 | IJC (в посте: 194 тис.) | 198 тис. | 192 тис. | FRED ICSA | −6 тис. | https://t.me/marketnews_ua/110 |",
		"| 2026-10-22 | GDP Q3 (1st) | 3.2% | 3.4% | из поста | +0.2 п.п. | https://t.me/marketnews_ua/110 |",
		"| 2026-10-22 | Nonfarm Payrolls | +89 тис | +29 тис | из поста | −60 тис |",
		"| 2026-10-23 | Core PCE m/m | +0,34% | +0.33% | FRED PCEPILFE | −0.01 п.п. | https://t.me/marketnews_ua/296 |",
		"| 2026-10-23 | Core PCE y/y | 3,1% | 3.1% | FRED PCEPILFE | 0 | https://t.me/marketnews_ua/296 |",
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("no row %q in\n%s", want, sec)
		}
	}
	if strings.Contains(sec, "ISM") {
		t.Errorf("a forecast without our series stays out:\n%s", sec)
	}
	if strings.Count(sec, "| IJC") != 1 {
		t.Errorf("IJC rows not merged:\n%s", sec)
	}
}

func TestFactString(t *testing.T) {
	for _, tt := range []struct {
		v                       float64
		unit, actual, exp, want string
		ok                      bool
	}{
		{191.5, "K", "194 тис.", "198 тис.", "192 тис.", true}, // decimals of the expectation
		{191.5, "K", "194.0 тис.", "198.0 тис.", "191.5 тис.", true},
		{191, "K", "194K", "198K", "191K", true},
		{0.34, "%", "0.3%", "0.2%", "0.3%", true},
		{-12, "K", "+29 тис", "+89 тис", "−12 тис", true},
		{42, "K", "+29 тис", "+89 тис", "+42 тис", true},
		{3.1, "%", "194 тис.", "198 тис.", "", false},
		{3.1, "%", "3.75%-4.00%", "3.75%-4.00%", "", false},
	} {
		got, ok := factString(tt.v, tt.unit, tt.actual, tt.exp)
		if got != tt.want || ok != tt.ok {
			t.Errorf("factString(%v, %s, %s) = %q %v, want %q %v", tt.v, tt.unit, tt.exp, got, ok, tt.want, tt.ok)
		}
	}
}

func TestMatchSurpriseFact(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	for label, want := range map[string]string{
		"Core CPI MoM": "CPILFESL/mom", "CPI YoY": "CPIAUCSL/yoy", "CPI m/m": "CPIAUCSL/mom", "Core PPI MoM": "",
		"РРІ МоМ": "PPIFIS/mom", "IJC": "ICSA/level", "Private Nonfarm": "", "Nonfarm Payrolls": "PAYEMS/diff",
		"Рівень безробіття": "UNRATE/level", "GDP Q3 (1st)": "GDPC1/qoqa", "$META EPS": "",
	} {
		got := ""
		if i, ok := matchSurpriseFact(cfg.News.SurpriseFacts, label, false); ok {
			got = cfg.News.SurpriseFacts[i].Series + "/" + cfg.News.SurpriseFacts[i].Transform
		}
		if got != want {
			t.Errorf("%q → %q, want %q", label, got, want)
		}
	}
}

func TestExtractForecasts(t *testing.T) {
	text := "Очікування щодо сьогоднішнього PCE\n\n• Зростання Core: прогнозують +0,34% m/m (3,1% y/y).\n\nЦе стане найвищим місячним показником із січня, коли Core PCE зріс на 0,40%."
	want := []surprise{
		{label: "Очікування щодо сьогоднішнього PCE — Зростання Core m/m", expected: "+0,34%"},
		{label: "Очікування щодо сьогоднішнього PCE — Зростання Core y/y", expected: "3,1%"},
	}
	if got := extractForecasts(text); !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	for _, s := range []string{
		"😨Goldman Sachs тримає прогноз ВВП Q3 на рівні 3.3%.", // a bank's forecast
		"🏦🇺🇸 Federal Funds Rate - 20:00\n- очікування: 4.00%", // calendar announcement
		"▫️GDP Q3 (1st) 3.4% (очік. 3.2%🔼)",                   // actual with expectation
	} {
		if got := extractForecasts(s); got != nil {
			t.Errorf("%q: %+v", s, got)
		}
	}
}
