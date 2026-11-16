package zew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Column headers of the recorded workbook; the config names them as keys.
const (
	colSentiment = "ZEW Indicator of Economic Sentiment Germany, balances"
	colSituation = "Economic Situation Germany, balances"
)

// Golden test on the workbook recorded on 2026-11-10: the press release of
// 2026-10-20 gives 28.1 and −35.9 for October.
func TestParseRecordedWorkbook(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "zew", FixtureFile))
	if err != nil {
		t.Fatal(err)
	}
	cols, err := Parse(body, "data")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 {
		t.Fatalf("columns = %v", headers(cols))
	}
	for name, want := range map[string]struct{ first, last float64 }{
		colSentiment: {-59, 28.1},
		colSituation: {9.2, -35.9},
	} {
		obs := cols[name]
		if len(obs) != 419 { // 1991-12 … 2026-10; the "Mean:" row is not a month
			t.Fatalf("%s: %d months, want 419", name, len(obs))
		}
		f, l := obs[0], obs[len(obs)-1]
		if f.Date.Format(time.DateOnly) != "1991-12-01" || f.Value != want.first ||
			l.Date.Format(time.DateOnly) != "2026-10-01" || l.Value != want.last {
			t.Errorf("%s: %s %v … %s %v", name, f.Date.Format(time.DateOnly), f.Value, l.Date.Format(time.DateOnly), l.Value)
		}
	}
	if _, err := Parse(body, "nope"); err == nil || !strings.Contains(err.Error(), `no sheet "nope"`) {
		t.Errorf("missing sheet: %v", err)
	}
	if _, err := Parse([]byte("<html>301 Moved Permanently</html>"), "data"); err == nil {
		t.Error("an HTML page is read as a workbook")
	}
}

func TestParseRowsChecks(t *testing.T) {
	head := []string{"Date", "Sentiment", "Situation", ""}
	good := [][]string{head, {"2026.08", "34.2", "-61.1"}, {"2026.09", "34.7", "-47.1"}, {"2026.10", "28.1", "-35.9", ""}, {"Mean:", "", ""}, nil}
	cols, err := parseRows(good)
	if err != nil || len(cols["Sentiment"]) != 3 || cols["Situation"][2].Value != -35.9 {
		t.Fatalf("good table: %v, %v", cols, err)
	}
	with := func(i int, row ...string) [][]string {
		out := append([][]string{}, good...)
		out[i] = row
		return out
	}
	for name, tt := range map[string]struct {
		rows [][]string
		want string
	}{
		"out of range":   {with(2, "2026.09", "347", "-47.1"), `value "347" in column "Sentiment" is not a balance`},
		"below range":    {with(2, "2026.09", "34.7", "-471"), `column "Situation"`},
		"text value":     {with(2, "2026.09", "n/a", "-47.1"), `value "n/a"`},
		"missing value":  {with(2, "2026.09", "34.7", ""), `no value in column "Situation"`},
		"skipped month":  {with(2, "2026.10", "34.7", "-47.1"), "month 2026-10 does not follow 2026-08"},
		"repeated month": {with(2, "2026.08", "34.7", "-47.1"), "does not follow"},
		"bad date":       {with(2, "Sept 2026", "34.7", "-47.1"), `date "Sept 2026"`},
		"no date":        {with(2, "", "34.7", "-47.1"), "values without a date"},
		"moved header":   {with(0, "Datum", "Sentiment", "Situation"), `does not start with "Date"`},
		"no columns":     {with(0, "Date", "", ""), "no value columns"},
		"only header":    {[][]string{head}, "no months"},
	} {
		_, err := parseRows(tt.rows)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
}

func TestParseMonth(t *testing.T) {
	for in, want := range map[string]string{"2026.10": "2026-10-01", "1991.12": "1991-12-01", "46296": "2026-10-01"} {
		got, err := parseMonth(in)
		if err != nil || got.Format(time.DateOnly) != want {
			t.Errorf("parseMonth(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"2026.13", "2026.1", "10.2026", "12"} {
		if _, err := parseMonth(in); err == nil {
			t.Errorf("parseMonth(%q): no error", in)
		}
	}
}
