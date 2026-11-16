package transform

import (
	"testing"
	"time"

	"macro-collector/internal/model"
)

func historyObs(n int) []model.Observation {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]model.Observation, n)
	for i := range out {
		out[i] = model.Observation{Date: start.AddDate(0, i, 0), Value: 100 + float64(i*i)}
	}
	return out
}

func TestHistoryLastPointEqualsCompute(t *testing.T) {
	in := Input{Obs: historyObs(30), Freq: "m"}
	for _, code := range []string{Level, YoY, A3M, MoM} {
		points, err := History(in, code, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		res, err := Compute(in, []string{code})
		if err != nil {
			t.Fatal(err)
		}
		last := points[len(points)-1]
		if last.Value != res.Values[code].V || !last.Date.Equal(res.Date) {
			t.Errorf("%s: last point %v %v, Compute %v %v", code, last.Date, last.Value, res.Date, res.Values[code].V)
		}
	}
}

func TestHistoryRespectsFromAndSkipsShortHistory(t *testing.T) {
	obs := historyObs(30)
	in := Input{Obs: obs, Freq: "m"}
	points, err := History(in, YoY, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 18 || !points[0].Date.Equal(obs[12].Date) {
		t.Fatalf("yoy: %d points from %v, want 18 from %v", len(points), points[0].Date, obs[12].Date)
	}
	points, err = History(in, Level, obs[28].Date)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("from: %d points, want 2", len(points))
	}
	if _, err := History(in, "nope", time.Time{}); err == nil {
		t.Fatal("unknown code accepted")
	}
}
