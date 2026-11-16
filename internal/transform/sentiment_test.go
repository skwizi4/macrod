package transform

import (
	"math"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func dailyObs(start string, vals ...float64) []model.Observation {
	d, _ := time.Parse(time.DateOnly, start)
	out := make([]model.Observation, len(vals))
	for i, v := range vals {
		out[i] = model.Observation{Date: d.AddDate(0, 0, i), Value: v}
	}
	return out
}

func TestRatioCommonDates(t *testing.T) {
	num := dailyObs("2026-10-26", 10, 20, 30)
	den := dailyObs("2026-10-27", 4, 0, 6) // 10-26 missing, 10-28 zero
	got := Ratio(num, den)
	if len(got) != 1 || got[0].Value != 5 || got[0].Date.Format(time.DateOnly) != "2026-10-27" {
		t.Fatalf("ratio = %+v", got)
	}
}

func TestCorrelation(t *testing.T) {
	if c := Correlation([]float64{1, 2, 3, 4}, []float64{2, 4, 6, 8}); !c.OK || math.Abs(c.V-1) > 1e-12 {
		t.Errorf("perfect = %+v", c)
	}
	if c := Correlation([]float64{1, 2, 3, 4}, []float64{8, 6, 4, 2}); !c.OK || math.Abs(c.V+1) > 1e-12 {
		t.Errorf("inverse = %+v", c)
	}
	if c := Correlation([]float64{1, 1, 1}, []float64{1, 2, 3}); c.OK {
		t.Errorf("constant x gives %+v", c)
	}
}

func TestReturnsCorrelationInflationScare(t *testing.T) {
	// Yields up while stocks fall: negative correlation.
	price := dailyObs("2026-10-01", 100, 99, 100, 98, 99, 97)
	rate := dailyObs("2026-10-01", 4.00, 4.05, 4.01, 4.08, 4.04, 4.10)
	asOf, _ := time.Parse(time.DateOnly, "2026-10-06")
	c := ReturnsCorrelation(price, rate, asOf, 5)
	if !c.OK || c.V > -0.9 {
		t.Fatalf("corr = %+v, want strongly negative", c)
	}
	if c := ReturnsCorrelation(price, rate, asOf, 6); c.OK {
		t.Errorf("not enough days gives %+v", c)
	}
	// A day missing in the rate series is skipped, not filled.
	short := append(append([]model.Observation{}, rate[:2]...), rate[3:]...)
	if c := ReturnsCorrelation(price, short, asOf, 4); !c.OK {
		t.Errorf("gap: %+v", c)
	}
}

func TestMeanLast(t *testing.T) {
	obs := dailyObs("2026-10-26", 1, 2, 3, 4, 100)
	asOf, _ := time.Parse(time.DateOnly, "2026-10-29")
	if m := MeanLast(obs, asOf, 3); !m.OK || m.V != 3 {
		t.Errorf("mean = %+v", m)
	}
	if m := MeanLast(obs, asOf, 5); m.OK {
		t.Errorf("short history gives %+v", m)
	}
}

func TestDailyMoves(t *testing.T) {
	obs := dailyObs("2026-10-23", 100, 102, 99.96, 101)
	from, _ := time.Parse(time.DateOnly, "2026-10-24")
	to, _ := time.Parse(time.DateOnly, "2026-10-25")
	got := DailyMoves(obs, from, to)
	if len(got) != 2 || math.Abs(got[0].Pct-2) > 1e-9 || math.Abs(got[1].Pct+2) > 1e-9 || got[1].Date != to {
		t.Errorf("moves = %+v", got)
	}
}
