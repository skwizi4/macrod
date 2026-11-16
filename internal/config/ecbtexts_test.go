package config

import (
	"strings"
	"testing"
	"time"
)

const ecbTextsSection = `
ecb_texts:
  enabled: true
  base_url: "https://www.ecb.europa.eu"
  rss: "https://www.ecb.europa.eu/rss/press.html"
  lists:
    decisions:  "/press/govcdec/mopo/{year}/html/index_include.en.html"
    statements: "/press/press_conference/monetary-policy-statement/{year}/html/index_include.en.html"
    accounts:   "/press/accounts/{year}/html/index_include.en.html"
  meeting_calendar: "https://www.ecb.europa.eu/press/calendars/mgcgc/html/index.en.html"
  timezone: "Europe/Berlin"
  times: { decision: "14:15", press_conference: "14:45", accounts: "13:30" }
  poll_interval: 15m
  agent: usa_monetary
  check_series: ECB_DFR
  day_mode: { lead: 1m, rss_interval: 20s, list_interval: 60s, fast_for: 75m, alert_decision_at: "14:25", statement_wait: "15:30" }
`

func TestECBTexts(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal+ecbTextsSection))
	if err != nil {
		t.Fatal(err)
	}
	e := cfg.ECBTexts
	if !e.Enabled || e.Location.String() != "Europe/Berlin" || e.Times.DecisionAt != (ClockTime{14, 15}) ||
		e.DayMode.StatementBy.Since() != 15*time.Hour+30*time.Minute || e.DayMode.RSSInterval != 20*time.Second {
		t.Errorf("ecb_texts = %+v", e)
	}
	if len(cfg.Deprecations()) != 0 {
		t.Errorf("deprecations = %v", cfg.Deprecations())
	}
}

// A config written before phase 11 carries the placeholder; it must still
// load (the server's config.yaml) and say the key is obsolete.
func TestECBTextsLegacyPlaceholder(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal+`
ecb_texts:
  # решения, пресс-конференции Лагард, речи, accounts
  feeds: "[[LOOKUP]]"
  meeting_calendar: "[[LOOKUP]]"
  agent: eu_monetary
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ECBTexts.Enabled {
		t.Error("the placeholder enabled the section")
	}
	if d := cfg.Deprecations(); len(d) != 1 || !strings.Contains(d[0], "ecb_texts.feeds") {
		t.Errorf("deprecations = %v", d)
	}
}

func TestECBTextsValidation(t *testing.T) {
	for _, tt := range []struct{ name, old, new, want string }{
		{"list year", `/press/accounts/{year}/`, `/press/accounts/2026/`, "ecb_texts.lists.accounts"},
		{"timezone", `"Europe/Berlin"`, `"Europe/Frankfurt"`, "ecb_texts.timezone"},
		{"time", `decision: "14:15"`, `decision: "2:15pm"`, "ecb_texts.times.decision"},
		{"order", `statement_wait: "15:30"`, `statement_wait: "14:20"`, "want times.decision"},
		{"interval", `rss_interval: 20s`, `rss_interval: 0s`, "ecb_texts.day_mode"},
		{"agent", `agent: usa_monetary
  check_series`, `agent: usa_sport
  check_series`, `ecb_texts: unknown agent "usa_sport"`},
		{"placeholder", `rss: "https://www.ecb.europa.eu/rss/press.html"`, `rss: "[[LOOKUP]]"`, "ecb_texts.rss is empty"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(ecbTextsSection, tt.old) {
				t.Fatalf("section lacks %q", tt.old)
			}
			_, err := Load(writeConfig(t, minimal+strings.Replace(ecbTextsSection, tt.old, tt.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
