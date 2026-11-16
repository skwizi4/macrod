package eurostat

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "eurostat", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func at(obs []model.Observation, date string) float64 {
	for _, o := range obs {
		if o.Date.Format(time.DateOnly) == date {
			return o.Value
		}
	}
	return math.NaN()
}

func TestParseQuarterly(t *testing.T) {
	obs, err := Parse(read(t, "gdp_ea20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 11 || obs[0].Date.Format(time.DateOnly) != "2024-01-01" || obs[10].Date.Format(time.DateOnly) != "2026-07-01" {
		t.Fatalf("observations = %d, %v … %v", len(obs), obs[0], obs[len(obs)-1])
	}
	// Q3 2026 on Q2: +0.4% (the flash of 2026-10-30).
	q := (at(obs, "2026-07-01")/at(obs, "2026-04-01") - 1) * 100
	if math.Round(q*10)/10 != 0.4 {
		t.Errorf("GDP q/q = %.2f, want 0.4", q)
	}
}

func TestParseMonthly(t *testing.T) {
	obs, err := Parse(read(t, "ici_de"))
	if err != nil {
		t.Fatal(err)
	}
	if v := at(obs, "2026-10-01"); v != -7.3 {
		t.Errorf("industrial confidence 2026-10 = %v, want -7.3", v)
	}
	if obs[0].Date.Format(time.DateOnly) != "2025-01-01" {
		t.Errorf("first = %v", obs[0])
	}
}

func TestParseDebt(t *testing.T) {
	obs, err := Parse(read(t, "debt_de"))
	if err != nil {
		t.Fatal(err)
	}
	if v := at(obs, "2026-04-01"); v != 63.4 {
		t.Errorf("debt 2026-Q2 = %v, want 63.4", v)
	}
}

// A key that leaves a dimension open would mix three series.
func TestParseRejectsOpenDimension(t *testing.T) {
	_, err := Parse(read(t, "deficit_it_open_s_adj"))
	if err == nil || !strings.Contains(err.Error(), "dimension s_adj has 3 categories") {
		t.Errorf("err = %v", err)
	}
}

func TestParseForms(t *testing.T) {
	head := `{"class":"dataset","id":["geo","time"],"size":[1,3],"dimension":{"geo":{"category":{"index":{"DE":0}}},"time":{"category":{"index":{"2026-Q1":0,"2026-Q2":1,"2026-Q3":2}}}},"value":`
	obs, err := Parse([]byte(head + `[1.5,null,2.5]}`))
	if err != nil || len(obs) != 2 || obs[1].Date.Format(time.DateOnly) != "2026-07-01" {
		t.Errorf("dense values: %v, %v", obs, err)
	}
	obs, err = Parse([]byte(head + `{"2":2.5}}`))
	if err != nil || len(obs) != 1 || obs[0].Value != 2.5 {
		t.Errorf("sparse values: %v, %v", obs, err)
	}
	for name, body := range map[string]string{
		"no series": strings.Replace(head, `"size":[1,3]`, `"size":[0,3]`, 1) + `{}}`,
		"error":     `{"error":[{"status":400,"label":"Invalid dataset"}]}`,
		"html":      `<html></html>`,
		"not time":  `{"class":"dataset","id":["time","geo"],"size":[1,1],"value":{}}`,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestURL(t *testing.T) {
	c := New("https://example.org/data/", nil, nil)
	got, err := c.URL("namq_10_gdp?geo=EA20&unit=CLV20_MEUR", time.Date(2021, 7, 1, 0, 0, 0, 0, time.UTC), time.Time{})
	want := "https://example.org/data/namq_10_gdp?format=JSON&geo=EA20&lang=EN&sinceTimePeriod=2021&unit=CLV20_MEUR"
	if err != nil || got != want {
		t.Errorf("URL = %s, %v", got, err)
	}
	if _, err := c.URL("namq_10_gdp", time.Time{}, time.Time{}); err == nil {
		t.Error("a key without filters is accepted")
	}
}
