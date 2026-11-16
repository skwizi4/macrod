package irxlsx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"macro-collector/internal/config"
)

// The configurations of presets/eu-equities.yaml.
var (
	siemens = config.IssuerQuarterly{FiscalYearEnd: 9, Tables: []config.QuarterlyTable{
		{Anchor: []string{"Consolidated Statements of Income"}, Scale: 1e6, Rows: map[string]config.QuarterlyRow{
			config.QuarterRevenue: {Labels: []string{"Revenue"}},
			config.QuarterNet:     {Labels: []string{"Shareholders of Siemens AG"}},
			config.QuarterEPS:     {Labels: []string{"Net income"}, Section: "Basic earnings per share"},
		}},
		{Anchor: []string{"Overview of Segment figures"}, Group: "Profit", Scale: 1e6, Rows: map[string]config.QuarterlyRow{
			config.QuarterOperating: {Labels: []string{"Industrial Business"}},
		}},
	}}
	telekom = config.IssuerQuarterly{FiscalYearEnd: 12, Tables: []config.QuarterlyTable{
		{Anchor: []string{"DT CONSOLIDATED INCOME STATEMENT", "AS REPORTED"}, Scale: 1e6, Rows: map[string]config.QuarterlyRow{
			config.QuarterRevenue:   {Labels: []string{"NET REVENUE"}},
			config.QuarterOperating: {Labels: []string{"PROFIT (LOSS) FROM OPERATIONS (EBIT)"}},
			config.QuarterNet:       {Labels: []string{"NET PROFIT (LOSS)"}},
		}},
	}}
	// Allianz Financial supplement (phase 14c): the user sends the file.
	allianz = config.IssuerQuarterly{FiscalYearEnd: 12, Tables: []config.QuarterlyTable{
		{Anchor: []string{"Consolidated financial results", "by quarters"}, Scale: 1e6, Rows: map[string]config.QuarterlyRow{
			config.QuarterRevenue:   {Labels: []string{"Total business volume¹", "Total business volume"}},
			config.QuarterOperating: {Labels: []string{"Operating profit"}},
			config.QuarterNet:       {Labels: []string{"Shareholders"}, Section: "Net income (loss) attributable to:"},
		}},
		{Anchor: []string{"Earnings per share (EPS)", "by quarters"}, Scale: 1e6, Rows: map[string]config.QuarterlyRow{
			config.QuarterEPS: {Labels: []string{"Basic EPS (EUR)", "EPS basic (EUR)"}},
		}},
	}}
)

// Values checked against the issuers' releases (millions of €, EPS €).
func TestParse(t *testing.T) {
	for _, tt := range []struct {
		file  string
		cfg   config.IssuerQuarterly
		fy, q int
		want  map[string]Pair
	}{
		{"siemens-2026-q3.xlsx", siemens, 2026, 3, map[string]Pair{"revenue": {20794e6, 19377e6}, "net_income": {2270e6, 2046e6},
			"eps": {2.93, 2.61}, "operating_income": {3524e6, 2820e6}}},
		{"siemens-2026-q2.xlsx", siemens, 2026, 2, map[string]Pair{"revenue": {19756e6, 19757e6}, "net_income": {2027e6, 2250e6},
			"eps": {2.6, 2.86}, "operating_income": {2969e6, 3239e6}}},
		{"siemens-2025-q4.xlsx", siemens, 2025, 4, map[string]Pair{"revenue": {21427e6, 20811e6}, "net_income": {1619e6, 1900e6},
			"eps": {2.07, 2.42}, "operating_income": {3190e6, 3120e6}}},
		{"dt-26q3.xlsx", telekom, 2026, 3, map[string]Pair{"revenue": {31102e6, 28935e6}, "operating_income": {7383e6, 6027e6}, "net_income": {2762e6, 2427e6}}},
		// Allianz: three quarters of 2026 — the same sheets, anchors and rows; the EPS row is renamed after Q1.
		{"allianz-2026-q1.xlsx", allianz, 2026, 1, map[string]Pair{"revenue": {53022.709950029996e6, 54014.25353088e6},
			"operating_income": {4516.7524541999992e6, 4237.84565054e6}, "net_income": {3690.0991756699996e6, 2422.9926800899998e6},
			"eps": {9.7071965620008775, 6.2787867640808708}}},
		{"allianz-2026-q2.xlsx", allianz, 2026, 2, map[string]Pair{"revenue": {45582.038343050008e6, 44458.910634030006e6},
			"operating_income": {4873.5949497499987e6, 4405.8873995799995e6}, "net_income": {2595.1787029500001e6, 2841.0397624299999e6},
			"eps": {6.4650920766812447, 7.0337958712255082}}},
		{"allianz-2026-q3.xlsx", allianz, 2026, 3, map[string]Pair{"revenue": {44399.631364760004e6, 42774.710019529972e6},
			"operating_income": {4708.7276029099994e6, 4433.4100584299995e6}, "net_income": {2783.7236777500011e6, 2847.2027365200001e6},
			"eps": {7.4204487311275233, 7.4007922136883133}}},
		{"dt-25q4.xlsx", telekom, 2025, 4, map[string]Pair{"revenue": {31720e6, 30932e6}, "operating_income": {5388e6, 8473e6}, "net_income": {1722e6, 4182e6}}},
	} {
		data, err := os.ReadFile(filepath.Join("../../../testdata/irxlsx", tt.file))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Parse(data, tt.cfg, tt.fy, tt.q)
		if err != nil {
			t.Errorf("%s: %v", tt.file, err)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("%s: %d fields, want %d", tt.file, len(got), len(tt.want))
		}
		for f, w := range tt.want {
			if g := got[f]; abs(g.Cur-w.Cur) > 1e-6*abs(w.Cur)+1e-9 || abs(g.Ago-w.Ago) > 1e-6*abs(w.Ago)+1e-9 {
				t.Errorf("%s %s = %+v, want %+v", tt.file, f, g, w)
			}
		}
	}
}

// A file of another quarter, a missing row and an ambiguous label are
// errors, not guesses.
func TestParseErrors(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/irxlsx/siemens-2026-q3.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data, siemens, 2026, 2); err == nil || !strings.Contains(err.Error(), "Q2") {
		t.Errorf("wrong quarter: %v", err)
	}
	missing := siemens
	missing.Tables = []config.QuarterlyTable{{Anchor: []string{"Consolidated Statements of Income"}, Scale: 1e6,
		Rows: map[string]config.QuarterlyRow{"revenue": {Labels: []string{"Sales"}}}}}
	if _, err := Parse(data, missing, 2026, 3); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing row: %v", err)
	}
	ambiguous := siemens
	ambiguous.Tables = []config.QuarterlyTable{{Anchor: []string{"Consolidated Statements of Income"}, Scale: 1e6,
		Rows: map[string]config.QuarterlyRow{"eps": {Labels: []string{"Net income"}}}}}
	if _, err := Parse(data, ambiguous, 2026, 3); err == nil || !strings.Contains(err.Error(), "times") {
		t.Errorf("ambiguous row: %v", err)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestLinks(t *testing.T) {
	sie := regexp.MustCompile(`^(?P<year>\d{4})-q(?P<q>[1-4])-Financial-Results-Excel\.xlsx$`)
	dt := []*regexp.Regexp{regexp.MustCompile(`(?i)^dt-(?P<year>\d{2})q(?P<q>[1-4])-backup(?:-de)?\.xlsx$`),
		regexp.MustCompile(`(?i)^DT (?P<year>\d{2}|\d{4})Q(?P<q>[1-4]) Backup\.xlsx$`)}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("../../../testdata/irxlsx", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	got := Links(read("siemens-financial-results.html"), []*regexp.Regexp{sie}, 3)
	if len(got) != 3 || got[0].Name != "2026-q3-Financial-Results-Excel.xlsx" || got[0].FY != 2026 || got[0].Q != 3 ||
		!strings.HasPrefix(got[0].URL, "https://assets.new.siemens.com/") || got[1].Q != 2 || got[2].FY != 2025 || got[2].Q != 4 {
		t.Errorf("siemens %+v", got)
	}
	got = Links(read("dt-financial-results-2026.html"), dt, 4)
	if len(got) != 2 || got[0].Name != "dt-26q3-backup.xlsx" || got[1].Name != "dt-26q2-backup-de.xlsx" || !strings.HasSuffix(got[0].URL, "?attachment=true") {
		t.Errorf("dt 2026 %+v", got) // the consensus files are not backups
	}
	got = Links(read("dt-financial-results-2025.html"), dt, 4)
	if len(got) != 4 || got[0].Name != "DT 25Q4 Backup.xlsx" || got[0].FY != 2025 || got[0].Q != 4 {
		t.Errorf("dt 2025 %+v", got)
	}
}

func TestParseQuarterAndFileOf(t *testing.T) {
	for _, tt := range []struct {
		in    string
		fy, q int
		ok    bool
	}{
		{"ALV Q3 2026", 2026, 3, true}, {"3Q 2026", 2026, 3, true}, {"q4 FY2025", 2025, 4, true}, {"Q2 26", 2026, 2, true},
		{"ALV", 0, 0, false}, {"Q5 2026", 0, 0, false}, {"9M 2026", 0, 0, false},
	} {
		fy, q, ok := ParseQuarter(tt.in)
		if fy != tt.fy || q != tt.q || ok != tt.ok {
			t.Errorf("ParseQuarter(%q) = %d %d %v", tt.in, fy, q, ok)
		}
	}
	res := []*regexp.Regexp{regexp.MustCompile(`(?i)^(?P<q>[1-4])Q_?(?P<year>\d{2})_FS\.xlsx$`)}
	if f, ok := FileOf("3Q_26_FS.xlsx", res); !ok || f.FY != 2026 || f.Q != 3 || f.Name != "3Q_26_FS.xlsx" {
		t.Errorf("FileOf = %+v %v", f, ok)
	}
	if _, ok := FileOf("report.xlsx", res); ok {
		t.Error("FileOf matched an unrelated name")
	}
}
