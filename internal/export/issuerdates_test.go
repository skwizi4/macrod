package export

import (
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/store"
)

func TestMergeReportDates(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	cfg := []config.IssuerDate{
		{Date: "2026-11-12", Day: day("2026-11-12"), Session: config.SessionPreMarket, Period: "Q4 FY2026", Source: "Siemens IR, финансовый календарь"},
		{Date: "2027-02-10", Day: day("2027-02-10"), Session: config.SessionAfterClose, Period: "Q1 FY2027", Estimate: true, Source: "Investing.com, оценка"},
		{Date: "2027-05-12", Day: day("2027-05-12"), Session: config.SessionPreMarket, Period: "Q2 FY2027", Source: "Siemens IR"},
	}
	cal := []store.IssuerReportDate{
		{Period: "Q4 FY2026", Date: day("2026-11-12")}, // same: config kept as is
		{Period: "Q1 FY2027", Date: day("2027-02-11")}, // beats the estimate, keeps its session
		{Period: "Q2 FY2027", Date: day("2027-05-13")}, // differs from a confirmed date: noted
		{Period: "Q3 FY2027", Date: day("2027-08-05")}, // not in the config: added
	}
	got := MergeReportDates("Siemens", cfg, cal)
	var lines []string
	for _, d := range got {
		lines = append(lines, strings.Join([]string{d.Day.Format(time.DateOnly), d.Period, d.Session, d.Source}, " | "))
		if d.Estimate {
			t.Errorf("%s still an estimate", d.Period)
		}
	}
	want := []string{
		"2026-11-12 | Q4 FY2026 | pre_market | Siemens IR, финансовый календарь",
		"2027-02-11 | Q1 FY2027 | after_close | Siemens IR, финансовый календарь (автоматически)",
		"2027-05-12 | Q2 FY2027 | pre_market | Siemens IR; в календаре эмитента — 2027-05-13",
		"2027-08-05 | Q3 FY2027 | pre_market | Siemens IR, финансовый календарь (автоматически)",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("merged:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if cfg[1].Day.Format(time.DateOnly) != "2027-02-10" || cfg[2].Source != "Siemens IR" {
		t.Fatal("config dates modified")
	}
}
