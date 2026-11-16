package config

import (
	"strings"
	"testing"
)

func validIssuers() Issuers {
	return Issuers{Enabled: true, PollCron: "30 8 * * 1-5", RequestInterval: "3s", ESEFURL: "https://filings.xbrl.org", UserAgent: "x",
		PressHistoryDays: 30, ReleaseChars: 1500, FullReleases: 3, StaleMonths: 15, Paragraphs: []string{`\boutlook\b`},
		Companies: []Issuer{{Ticker: "AIR", Name: "Airbus", Symbol: "AIR.DE", Currency: "EUR",
			Reports: []IssuerDate{{Date: "2027-02-18", Source: "IR"}, {Date: "2026-11-01", Source: "IR", Session: SessionAfterClose}},
			Facts: &IssuerFacts{Source: FactsESEF, LEI: "MINO79WLOO247M1IL051", Concepts: IssuerConcepts{Revenue: []string{"Revenue"},
				OperatingIncome: []string{"airbus:ProfitLossBeforeFinancialResultAndIncomeTaxes"}, NetIncome: []string{"Xx"}, EPS: []string{"Xx"},
				OperatingCashFlow: []string{"Xx"}, Capex: []string{"Xx"}}},
			Press: &IssuerPress{Kind: PressHTML, URL: "https://www.airbus.com/en", Item: "article", LinkPattern: `/press-releases/`, Body: "main",
				Exclude: []string{"share buybacks for week"}}}}}
}

func TestIssuersValidate(t *testing.T) {
	s := validIssuers()
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	c := s.Companies[0]
	if c.Reports[0].Date != "2026-11-01" || c.Reports[1].Session != SessionPreMarket || c.Importance != "medium" || c.Facts.Taxonomy != "ifrs-full" {
		t.Errorf("resolved %+v", c)
	}
	if !c.Press.Excluded.MatchString("Airbus reports Share Buybacks for week starting 2 November") {
		t.Error("exclude is case-insensitive")
	}
	for name, breakIt := range map[string]func(*Issuers){
		"bad date":  func(s *Issuers) { s.Companies[0].Reports[0].Date = "18.02.2027" },
		"no source": func(s *Issuers) { s.Companies[0].Reports[0].Source = "" },
		"bad lei":   func(s *Issuers) { s.Companies[0].Facts.LEI = "X" },
		"edgar prefix tag": func(s *Issuers) {
			s.Companies[0].Facts.Source, s.Companies[0].Facts.CIK, s.Companies[0].Facts.Forms = FactsEDGAR, "0001000184", []string{"20-F"}
		},
		"html no body": func(s *Issuers) { s.Companies[0].Press.Body = "" },
		"currency":     func(s *Issuers) { s.Companies[0].Currency = "GBP" },
		"no revenue":   func(s *Issuers) { s.Companies[0].Facts.Concepts.Revenue = nil },
	} {
		s := validIssuers()
		breakIt(&s)
		if err := s.validate(); err == nil {
			t.Errorf("%s: no error", name)
		} else if strings.Contains(err.Error(), "panic") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A report's max_chars raises its own size limit (Europe's ecb_diff,
// 2026-11-13); others keep export.max_file_chars.
func TestFileLimit(t *testing.T) {
	c := &Config{Export: Export{MaxFileChars: 60000}}
	if got := c.FileLimit(&Report{Kind: ReportFOMCDiff}); got != 60000 {
		t.Errorf("fomc_diff limit %d", got)
	}
	if got := c.FileLimit(&Report{Kind: ReportECBDiff, MaxChars: 100000}); got != 100000 {
		t.Errorf("ecb_diff limit %d", got)
	}
	if got := c.FileLimit(nil); got != 60000 {
		t.Errorf("no report: %d", got)
	}
}

func TestIssuerCalendarValidate(t *testing.T) {
	cal := func() *IssuerCalendar {
		return &IssuerCalendar{URL: "https://ir.example/cal", Kind: CalendarText,
			Events: []CalendarMatch{{Match: `Q(?P<q>[1-4]) FY(?P<year>\d{4})`, Period: "Q{q} FY{year}"}}}
	}
	s := validIssuers()
	s.Companies[0].Calendar = cal()
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	if c := s.Companies[0].Calendar; c.Slash != "dmy" || c.Lines != 4 || c.Events[0].Re == nil {
		t.Errorf("defaults %+v", c)
	}
	j := cal()
	j.Kind = CalendarJSONAttr
	if err := j.validate("x"); err != nil || j.Attr != "data-event-data" {
		t.Errorf("json_attr %v %q", err, j.Attr)
	}
	for _, tt := range []struct {
		name   string
		mutate func(*IssuerCalendar)
		want   string
	}{
		{"kind", func(c *IssuerCalendar) { c.Kind = "ics" }, `kind "ics"`},
		{"url", func(c *IssuerCalendar) { c.URL = "ftp://x" }, "want an http(s) URL"},
		{"no events", func(c *IssuerCalendar) { c.Events = nil }, "events is empty"},
		{"bad re", func(c *IssuerCalendar) { c.Events[0].Match = "(" }, "match:"},
		{"group", func(c *IssuerCalendar) { c.Events[0].Match = `Q[1-4] FY(?P<year>\d{4})` }, "has no group (?P<q>"},
		{"placeholder", func(c *IssuerCalendar) { c.Events[0].Period = "Q{q} {fy}" }, "only {q} and {year}"},
		{"slash", func(c *IssuerCalendar) { c.Slash = "ymd" }, `slash "ymd"`},
	} {
		c := cal()
		tt.mutate(c)
		if err := c.validate("cal"); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
}
