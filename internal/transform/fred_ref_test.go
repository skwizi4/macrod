package transform

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"macro-collector/internal/model"
)

// These tests compare our transforms with FRED's own (units=pc1, pch, pca)
// on recorded responses. FRED rounds to 5 decimals.
const fredTol = 1e-4

type fredPoint struct {
	date    time.Time
	value   float64
	missing bool // FRED returned "."
}

func loadFRED(t *testing.T, name string) []fredPoint {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fred", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Observations []struct {
			Date  string `json:"date"`
			Value string `json:"value"`
		} `json:"observations"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	out := make([]fredPoint, len(resp.Observations))
	for i, o := range resp.Observations {
		out[i].date = day(o.Date)
		if o.Value == "." {
			out[i].missing = true
			continue
		}
		if out[i].value, err = strconv.ParseFloat(o.Value, 64); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func levels(points []fredPoint) []model.Observation {
	var out []model.Observation
	for _, p := range points {
		if !p.missing {
			out = append(out, model.Observation{Date: p.date, Value: p.value})
		}
	}
	return out
}

// compareWithFRED computes code at every date of ref using the levels up to
// that date, and checks it against FRED's value. baseMonths is how far back
// the transform looks; dates whose base precedes the fixture are skipped.
func compareWithFRED(t *testing.T, levelFile, refFile, freq, code string, baseMonths int) {
	t.Helper()
	lv := levels(loadFRED(t, levelFile))
	ref := loadFRED(t, refFile)
	first := lv[0].Date
	compared, missingMatched := 0, 0
	for _, p := range ref {
		if p.date.AddDate(0, -baseMonths, 0).Before(first) {
			continue
		}
		r, err := Compute(Input{Freq: freq, Obs: lv, AsOf: p.date}, []string{code})
		if err != nil {
			t.Fatal(err)
		}
		got := r.Values[code]
		switch {
		case p.missing:
			// FRED has no value: either the month itself or its base is
			// missing, and we must not invent one.
			if got.OK && r.Date.Equal(p.date) {
				t.Errorf("%s %s: FRED has no value, we computed %v", code, p.date.Format(time.DateOnly), got.V)
			}
			missingMatched++
		case !r.Date.Equal(p.date):
			t.Errorf("%s %s: no level for this date", code, p.date.Format(time.DateOnly))
		case !got.OK:
			t.Errorf("%s %s: not computed, FRED has %v", code, p.date.Format(time.DateOnly), p.value)
		case math.Abs(got.V-p.value) > fredTol:
			t.Errorf("%s %s = %.6f, FRED %.5f", code, p.date.Format(time.DateOnly), got.V, p.value)
		default:
			compared++
		}
	}
	t.Logf("%s vs %s: %d values match, %d FRED gaps respected", code, refFile, compared, missingMatched)
	if compared < 30 {
		t.Errorf("only %d values compared", compared)
	}
}

func TestYoYMatchesFREDpc1(t *testing.T) {
	compareWithFRED(t, "observations_CPIAUCSL_lin", "observations_CPIAUCSL_pc1", "m", YoY, 12)
}

func TestMoMMatchesFREDpch(t *testing.T) {
	compareWithFRED(t, "observations_CPIAUCSL_lin", "observations_CPIAUCSL_pch", "m", MoM, 1)
}

func TestQoQAMatchesFREDpca(t *testing.T) {
	compareWithFRED(t, "observations_GDPC1_lin", "observations_GDPC1_pca", "q", QoQA, 3)
}
