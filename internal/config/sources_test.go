package config

import (
	"strings"
	"testing"
)

const ecbSection = `
ecb:
  enabled: true
  endpoint: "https://data-api.ecb.europa.eu/service/data"
  poll_cron: "20 12 * * *"
  series:
    - { id: ECB_DFR, key: "FM/D.U2.EUR.4F.KR.DFR.LEV", agent: usa_fiscal, label: "DFR", frequency: d, units: "Percent per Annum", transforms: [level, d] }
manual:
  series:
    - { id: IFO_CLIMATE, agent: usa_fiscal, label: "Ifo", frequency: m, units: "Index", transforms: [level, d], release_dates: ["2026-12-17", "2026-11-24"] }
`

func TestNumericSourceSeries(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal+ecbSection))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := cfg.ResolveSeries("ECB_DFR")
	if !ok || s.Source != SourceECB || s.Quality != QualityOfficial || s.Label != "DFR" {
		t.Errorf("ECB_DFR = %+v, %v", s, ok)
	}
	if s, ok := cfg.ResolveSeries("manual/IFO_CLIMATE"); !ok || s.Quality != QualityManual {
		t.Errorf("IFO_CLIMATE = %+v, %v", s, ok)
	}
	if _, ok := cfg.SeriesCatalog()["ecb/ECB_DFR"]; !ok {
		t.Error("the dashboard catalog has no ecb/ECB_DFR")
	}
	if sec, ok := cfg.NumericSourceByName(SourceECB); !ok || len(sec.Series) != 1 {
		t.Errorf("ecb section = %+v, %v", sec, ok)
	}
	if _, ok := cfg.NumericSourceByName(SourceZEW); ok {
		t.Error("zew is enabled without a section")
	}
	days := cfg.ConfigReleaseDays()["IFO_CLIMATE"]
	if len(days) != 2 || days[0].Format("2006-01-02") != "2026-11-24" {
		t.Errorf("release days = %v", days)
	}
}

func TestNumericSourceValidation(t *testing.T) {
	for _, tt := range []struct{ name, old, new, want string }{
		{"endpoint", `endpoint: "https://data-api.ecb.europa.eu/service/data"`, `endpoint: "data-api"`, `ecb.endpoint "data-api": want an https URL`},
		{"cron", `poll_cron: "20 12 * * *"`, `poll_cron: "noon"`, `ecb.poll_cron "noon"`},
		{"key", `key: "FM/D.U2.EUR.4F.KR.DFR.LEV", `, ``, `ecb.series[0] ECB_DFR: key is required`},
		{"frequency", `frequency: d`, `frequency: daily`, `frequency "daily": want one of d, w, m, q`},
		{"units", `units: "Percent per Annum"`, `units: "pct"`, `units "pct": want one of`},
		{"agent", `id: ECB_DFR, key: "FM/D.U2.EUR.4F.KR.DFR.LEV", agent: usa_fiscal`, `id: ECB_DFR, key: "FM/D.U2.EUR.4F.KR.DFR.LEV", agent: nobody`, `unknown agent "nobody"`},
		{"transform", `transforms: [level, d] }
manual`, `transforms: [level, wow] }
manual`, `unknown transform "wow"`},
		{"id of two sources", `id: ECB_DFR`, `id: DGS10`, `series "DGS10" is configured in fred and ecb`},
		{"release date", `"2026-11-24"`, `"24.11.2026"`, `manual.series[0] IFO_CLIMATE: release_dates`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Replace(ecbSection, tt.old, tt.new, 1)
			if body == ecbSection {
				t.Fatal("fixture changed")
			}
			_, err := Load(writeConfig(t, minimal+body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
	// A disabled section is not checked and adds no series.
	body := strings.Replace(ecbSection, "enabled: true", "enabled: false", 1)
	body = strings.Replace(body, `endpoint: "https://data-api.ecb.europa.eu/service/data"`, `endpoint: ""`, 1)
	cfg, err := Load(writeConfig(t, minimal+body))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.ResolveSeries("ECB_DFR"); ok {
		t.Error("a disabled section adds series")
	}
}

func TestYahooSymbolOptions(t *testing.T) {
	plain := `- { symbol: "^GDAXI", agent: eu_master, label: "GER40", transforms: [level] }`
	if !strings.Contains(minimal, plain) {
		t.Fatal("fixture changed")
	}
	mts := `- { symbol: "MIT10.AS", agent: eu_master, label: "BTP 10Y", transforms: [level, z], interval: 1h, exchange_tz: "Europe/Amsterdam", units: "Percent per Annum", short_history: true }`
	cfg, err := Load(writeConfig(t, strings.Replace(minimal, plain, plain+"\n    "+mts, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if h := cfg.Yahoo.HourlySymbols(); len(h) != 1 || h[0] != "MIT10.AS" {
		t.Errorf("hourly = %v", h)
	}
	ny := cfg.Calendar.ReleaseLocation
	if loc := cfg.Yahoo.ExchangeOf("MIT10.AS", ny); loc.String() != "Europe/Amsterdam" {
		t.Errorf("exchange of MIT10.AS = %v", loc)
	}
	if loc := cfg.Yahoo.ExchangeOf("^GDAXI", ny); loc != ny {
		t.Errorf("exchange of ^GDAXI = %v, want the default", loc)
	}
	if s, _ := cfg.Yahoo.Symbol("MIT10.AS"); !s.ShortHistory || s.Units != UnitsRate {
		t.Errorf("symbol = %+v", s)
	}
	for _, tt := range []struct{ old, new, want string }{
		{"interval: 1h", "interval: 5m", `interval "5m": want 1h or nothing`},
		{`exchange_tz: "Europe/Amsterdam"`, `exchange_tz: "Europe/Nowhere"`, `exchange_tz "Europe/Nowhere": invalid`},
		{`units: "Percent per Annum"`, `units: "Yield"`, `units "Yield": want one of`},
	} {
		bad := strings.Replace(mts, tt.old, tt.new, 1)
		_, err := Load(writeConfig(t, strings.Replace(minimal, plain, plain+"\n    "+bad, 1)))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("err = %v, want %q", err, tt.want)
		}
	}
}

func TestManualReminder(t *testing.T) {
	body := ecbSection + `  reminder: { name: Ifo, series: [IFO_CLIMATE], timezone: Europe/Berlin }
`
	cfg, err := Load(writeConfig(t, minimal+body))
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Manual.Reminder
	if r == nil || r.FromDay != 20 || r.Clock != (ClockTime{10, 5}) || r.Keyword() != "ifo" || r.Cron() != "CRON_TZ=Europe/Berlin 5 10 * * 1-5" {
		t.Fatalf("reminder %+v, cron %q", r, r.Cron())
	}
	for _, tt := range []struct{ old, new, want string }{
		{"series: [IFO_CLIMATE]", "series: [IFO_X]", "IFO_X: not in manual.series"},
		{"series: [IFO_CLIMATE]", "series: []", "manual.reminder.series is empty"},
		{"name: Ifo", "name: \"I fo\"", "want one word"},
		{"timezone: Europe/Berlin", "timezone: Europe/Berlin, from_day: 31", "from_day 31"},
		{"timezone: Europe/Berlin", "timezone: Europe/Berlin, time: \"25:00\"", "want HH:MM"},
		{"frequency: m", "frequency: q", "frequency q, want m"},
	} {
		_, err := Load(writeConfig(t, minimal+strings.Replace(body, tt.old, tt.new, 1)))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s → %v, want %q", tt.new, err, tt.want)
		}
	}
}
