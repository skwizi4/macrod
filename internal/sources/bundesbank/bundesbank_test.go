package bundesbank

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func fixture(t *testing.T, key string) []model.Observation {
	t.Helper()
	for _, f := range Fixtures {
		if f.Key == key {
			body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "bundesbank", f.File()))
			if err != nil {
				t.Fatal(err)
			}
			if string(body[:3]) != "\xef\xbb\xbf" {
				t.Errorf("%s: the recorded file has no BOM; the live one has", f.File())
			}
			obs, err := Parse(body)
			if err != nil {
				t.Fatal(err)
			}
			return obs
		}
	}
	t.Fatalf("no fixture for %s", key)
	return nil
}

func TestParseDailyYield(t *testing.T) {
	obs := fixture(t, "BBSIS/D.I.ZAR.ZI.EUR.S1311.B.A604.R10XX.R.A.A._Z._Z.A")
	// 1 October – 9 November 2026 has 28 weekdays; weekends come as "." and
	// are skipped.
	if len(obs) != 28 {
		t.Fatalf("%d observations, want 28", len(obs))
	}
	for _, o := range obs {
		if wd := o.Date.Weekday(); wd == time.Saturday || wd == time.Sunday {
			t.Errorf("%s is a weekend day", o.Date.Format(time.DateOnly))
		}
	}
	last := obs[len(obs)-1]
	if last.Date.Format(time.DateOnly) != "2026-11-09" || last.Value != 3.30 {
		t.Errorf("last = %s %v, want 2026-11-09 3.30", last.Date.Format(time.DateOnly), last.Value)
	}
	two := fixture(t, "BBSIS/D.I.ZAR.ZI.EUR.S1311.B.A604.R02XX.R.A.A._Z._Z.A")
	if l := two[len(two)-1]; l.Value != 2.99 {
		t.Errorf("2Y last = %v, want 2.99", l.Value)
	}
}

func TestParseMonthlyIndex(t *testing.T) {
	obs := fixture(t, "BBDE1/M.DE.Y.AEA1.A2P300000.F.C.I21.A")
	if len(obs) < 21 || obs[0].Date.Format(time.DateOnly) != "2025-01-01" {
		t.Fatalf("observations = %d, first %v", len(obs), obs[0])
	}
	for _, o := range obs {
		if o.Date.Format(time.DateOnly) == "2026-09-01" && o.Value != 90.3 {
			t.Errorf("2026-09 = %v, want 90.3", o.Value)
		}
	}
}

func TestParseVariants(t *testing.T) {
	en := "\xef\xbb\xbf\"\",BBSIS.D.X,BBSIS.D.X_FLAGS\n\"\",\"Yields, derived\",\nunit,PROZENT,\n2026-11-06,3.27,\n2026-11-07,.,No value available\n2026-11-09,3.30,\n"
	obs, err := Parse([]byte(en))
	if err != nil || len(obs) != 2 || obs[1].Value != 3.30 {
		t.Errorf("english csv: %v, %v", obs, err)
	}
	for name, body := range map[string]string{
		"empty":        "",
		"html":         "<html><body>Service unavailable</body></html>",
		"bad value":    "\"\";K;K_FLAGS\n2026-11-06;abc;\n",
		"out of order": "\"\";K;K_FLAGS\n2026-11-06;3,27;\n2026-11-05;3,31;\n",
		"junk after":   "\"\";K;K_FLAGS\n2026-11-06;3,27;\nfooter;1;\n",
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestURL(t *testing.T) {
	c := New("https://example.org/rest/data", nil, nil)
	since := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	for key, want := range map[string]string{
		"BBSIS/D.I.ZAR": "https://example.org/rest/data/BBSIS/D.I.ZAR?format=csv&startPeriod=2026-08-15",
		"BBDE1/M.DE.Y":  "https://example.org/rest/data/BBDE1/M.DE.Y?format=csv&startPeriod=2026-08",
		"BBXX/Q.DE":     "https://example.org/rest/data/BBXX/Q.DE?format=csv&startPeriod=2026-Q3",
	} {
		if got := c.URL(key, since, time.Time{}); got != want {
			t.Errorf("URL(%s) = %s", key, got)
		}
	}
}
