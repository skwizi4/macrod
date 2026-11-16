package transform

import (
	"math"
	"testing"
	"time"

	"macro-collector/internal/model"
)

// Reference values in this file were computed independently in Python
// (formula in the comment next to each).

const eps = 1e-9

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func obs(pairs ...any) []model.Observation {
	var out []model.Observation
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, model.Observation{Date: day(pairs[i].(string)), Value: toF(pairs[i+1])})
	}
	return out
}

func toF(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case float64:
		return x
	}
	panic("bad value")
}

func monthsFrom(start string, values ...float64) []model.Observation {
	d := day(start)
	out := make([]model.Observation, len(values))
	for i, v := range values {
		out[i] = model.Observation{Date: d.AddDate(0, i, 0), Value: v}
	}
	return out
}

func compute(t *testing.T, in Input, codes ...string) Result {
	t.Helper()
	r, err := Compute(in, codes)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func want(t *testing.T, r Result, code string, v float64, tol float64) {
	t.Helper()
	got := r.Values[code]
	if !got.OK {
		t.Errorf("%s: not computed, want %v", code, v)
		return
	}
	if math.Abs(got.V-v) > tol {
		t.Errorf("%s = %.10f, want %.10f", code, got.V, v)
	}
}

func wantMissing(t *testing.T, r Result, code string) {
	t.Helper()
	if v := r.Values[code]; v.OK {
		t.Errorf("%s = %v, want not computed", code, v.V)
	}
}

func TestMonthlyPriceIndex(t *testing.T) {
	// 2025-01 … 2026-01
	in := Input{Freq: "m", Obs: monthsFrom("2025-01-01",
		100, 100.5, 101, 101.2, 101.8, 102, 102.3, 102.9, 103.1, 103.4, 104, 104.2, 104.9)}
	r := compute(t, in, Level, YoY, MoM, A3M, A6M)
	if !r.Date.Equal(day("2026-01-01")) {
		t.Errorf("date = %v", r.Date)
	}
	want(t, r, Level, 104.9, eps)
	want(t, r, YoY, 4.9000000000000155, eps) // (104.9/100 − 1)·100
	want(t, r, MoM, 0.6717850287907945, eps) // (104.9/104.2 − 1)·100
	want(t, r, A3M, 5.930201340428942, eps)  // ((104.9/103.4)^4 − 1)·100
	want(t, r, A6M, 5.14768343734382, eps)   // ((104.9/102.3)^2 − 1)·100
}

func TestMonthlyNeedsExactBaseMonth(t *testing.T) {
	// 2025-01 is missing, so YoY for 2026-01 has no base.
	in := Input{Freq: "m", Obs: monthsFrom("2025-02-01", 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12)}
	r := compute(t, in, YoY, MoM)
	wantMissing(t, r, YoY)
	want(t, r, MoM, (12.0/11-1)*100, eps)
}

func TestQuarterly(t *testing.T) {
	in := Input{Freq: "q", Obs: obs("2025-07-01", 100, "2025-10-01", 101)}
	r := compute(t, in, QoQA, D)
	want(t, r, QoQA, 4.060401000000002, eps) // (1.01^4 − 1)·100
	want(t, r, D, 1, eps)
}

func TestDailyRateChangesInBasisPoints(t *testing.T) {
	in := Input{
		Freq:        "d",
		ChangeScale: 100,
		AsOf:        day("2026-10-24"),
		Obs: obs(
			"2026-09-25", 4.00, // Fri, 28 days before 10-23
			"2026-09-28", 4.05,
			"2026-10-16", 4.10, // Fri, 7 days before 10-23
			"2026-10-19", 4.12,
			"2026-10-22", 4.20,
			"2026-10-23", 4.18,
			"2026-10-26", 9.99, // published ahead (IORB): after AsOf, ignored
		),
	}
	r := compute(t, in, Level, D, D1W, D4W)
	if !r.Date.Equal(day("2026-10-23")) {
		t.Errorf("date = %v, future observation not ignored", r.Date)
	}
	want(t, r, Level, 4.18, eps)
	want(t, r, D, -2, 1e-9)   // (4.18 − 4.20)·100
	want(t, r, D1W, 8, 1e-9)  // (4.18 − 4.10)·100
	want(t, r, D4W, 18, 1e-9) // (4.18 − 4.00)·100
}

func TestD1WUsesLastValueBeforeTarget(t *testing.T) {
	// 7 days before Monday 10-19 is Monday 10-12, a holiday: use Friday 10-09.
	in := Input{Freq: "d", ChangeScale: 100, Obs: obs("2026-10-09", 4.00, "2026-10-13", 4.30, "2026-10-19", 4.10)}
	want(t, compute(t, in, D1W), D1W, 10, 1e-9)
}

func TestPayrolls(t *testing.T) {
	in := Input{Freq: "m", Obs: monthsFrom("2026-05-01", 159000, 159100, 159250, 159300, 159460)}
	r := compute(t, in, Diff, Avg3)
	want(t, r, Diff, 160, eps)
	want(t, r, Avg3, 120, eps) // (160 + 50 + 150) / 3

	// A missing month breaks the 3-month average.
	gap := Input{Freq: "m", Obs: obs("2026-05-01", 1, "2026-06-01", 2, "2026-08-01", 3, "2026-09-01", 4)}
	wantMissing(t, compute(t, gap, Avg3), Avg3)
}

func TestClaimsAvg4W(t *testing.T) {
	in := Input{Freq: "w", Obs: obs(
		"2026-09-19", 999999,
		"2026-09-26", 220000, "2026-10-03", 230000, "2026-10-10", 225000, "2026-10-17", 241000)}
	want(t, compute(t, in, Avg4W), Avg4W, 229000, eps)
	wantMissing(t, compute(t, Input{Freq: "w", Obs: in.Obs[2:]}, Avg4W), Avg4W)
}

func TestSum12M(t *testing.T) {
	in := Input{Freq: "m", Obs: monthsFrom("2025-09-01", 1000, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12)}
	want(t, compute(t, in, Sum12M), Sum12M, 78, eps)

	short := Input{Freq: "m", Obs: in.Obs[2:]}
	wantMissing(t, compute(t, short, Sum12M), Sum12M)
}

// A payment moved from one September to the next August doubles the
// monthly YoY but leaves the 12-month sums alone.
func TestSum12MYoY(t *testing.T) {
	var vals []float64
	for i := 0; i < 24; i++ {
		vals = append(vals, 100)
	}
	vals[0] = 50 // 2024-10: 50 → sum a year earlier 1150
	vals[22], vals[23] = 150, 50
	in := Input{Freq: "m", Obs: monthsFrom("2024-10-01", vals...)}
	want(t, compute(t, in, Sum12MYoY), Sum12MYoY, (1200.0/1150-1)*100, eps)

	// Deficits: a larger deficit is a positive change; a change of sign has none.
	neg := make([]float64, 24)
	for i := range neg {
		neg[i] = -100
	}
	neg[23] = -220 // sum −1320 vs −1200
	want(t, compute(t, Input{Freq: "m", Obs: monthsFrom("2024-10-01", neg...)}, Sum12MYoY), Sum12MYoY, 10, eps)
	neg[23] = 1300 // sum +200 vs −1200
	wantMissing(t, compute(t, Input{Freq: "m", Obs: monthsFrom("2024-10-01", neg...)}, Sum12MYoY), Sum12MYoY)
	wantMissing(t, compute(t, Input{Freq: "m", Obs: in.Obs[1:]}, Sum12MYoY), Sum12MYoY)
}

func TestZScore(t *testing.T) {
	// 20 weekly values alternating 1 and 3 (mean 2, sample sd √(20/19)),
	// then the current value 5.
	var o []model.Observation
	d := day("2026-01-03")
	for i := range 20 {
		o = append(o, model.Observation{Date: d.AddDate(0, 0, 7*i), Value: float64(1 + 2*(i%2))})
	}
	o = append(o, model.Observation{Date: d.AddDate(0, 0, 7*20), Value: 5})
	in := Input{Freq: "w", Obs: o, ZWindowDailyWeekly: Window{Days: 364}}
	want(t, compute(t, in, Z), Z, 2.9240383034426887, 1e-12) // (5 − 2) / sd

	// 19 values in the window: not enough.
	in.Obs = o[1:]
	wantMissing(t, compute(t, in, Z), Z)

	// The window excludes observations older than 52 weeks.
	old := append([]model.Observation{{Date: day("2020-01-04"), Value: 1000}}, o...)
	in = Input{Freq: "w", Obs: old, ZWindowDailyWeekly: Window{Days: 364}}
	want(t, compute(t, in, Z), Z, 2.9240383034426887, 1e-12)

	// Monthly series use the monthly window.
	m := Input{Freq: "m", Obs: o, ZWindowDailyWeekly: Window{Days: 364}}
	wantMissing(t, compute(t, m, Z), Z)
}

func TestMovingAverageDistance(t *testing.T) {
	var o []model.Observation
	d := day("2026-01-01")
	for i := range 50 {
		o = append(o, model.Observation{Date: d.AddDate(0, 0, i), Value: float64(i + 1)})
	}
	r := compute(t, Input{Freq: "d", Obs: o}, MA50, MA200)
	want(t, r, MA50, 96.078431372549, 1e-9) // (50 / 25.5 − 1)·100
	wantMissing(t, r, MA200)
}

func TestEdgeCases(t *testing.T) {
	if _, err := Compute(Input{Freq: "m"}, []string{"zz"}); err == nil {
		t.Error("unknown code accepted")
	}
	r := compute(t, Input{Freq: "m"}, Level, YoY)
	wantMissing(t, r, Level)
	wantMissing(t, r, YoY)

	// Everything after AsOf: nothing left.
	r = compute(t, Input{Freq: "d", AsOf: day("2026-01-01"), Obs: obs("2026-02-01", 1)}, Level)
	wantMissing(t, r, Level)

	// One observation: no change.
	r = compute(t, Input{Freq: "d", Obs: obs("2026-02-01", 1)}, D, Diff, D1W)
	wantMissing(t, r, D)
	wantMissing(t, r, Diff)
	wantMissing(t, r, D1W)
}

func TestParseWindow(t *testing.T) {
	for s, w := range map[string]Window{"52w": {Days: 364}, "5y": {Years: 5}, "6m": {Months: 6}, "10d": {Days: 10}} {
		got, err := ParseWindow(s)
		if err != nil || got != w {
			t.Errorf("ParseWindow(%q) = %+v, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "w", "0w", "-1y", "5x", "abc"} {
		if _, err := ParseWindow(s); err == nil {
			t.Errorf("ParseWindow(%q) accepted", s)
		}
	}
}

func TestRelativeChanges(t *testing.T) {
	obs := []model.Observation{
		{Date: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), Value: 50},
		{Date: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), Value: 100},
		{Date: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), Value: 102},
	}
	r, err := Compute(Input{Obs: obs, Freq: "d", AsOf: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), RelativeChanges: true}, []string{D1W, D4W})
	if err != nil {
		t.Fatal(err)
	}
	if v := r.Values[D1W]; !v.OK || math.Abs(v.V-2) > 1e-9 {
		t.Errorf("d1w = %+v, want +2%%", v)
	}
	if v := r.Values[D4W]; !v.OK || math.Abs(v.V-104) > 1e-9 {
		t.Errorf("d4w = %+v, want +104%%", v)
	}
}

// qoq is the plain quarterly change (Eurostat: EA20 GDP Q3 2026 +0.4%),
// qoqa its annualised form.
func TestQuarterOnQuarter(t *testing.T) {
	q := func(d string, v float64) model.Observation {
		day, err := time.Parse(time.DateOnly, d)
		if err != nil {
			t.Fatal(err)
		}
		return model.Observation{Date: day, Value: v}
	}
	in := Input{Freq: "q", Obs: []model.Observation{q("2026-01-01", 3318920.7), q("2026-04-01", 3340317.5), q("2026-07-01", 3354133.4)}}
	r := compute(t, in, QoQ, QoQA)
	want(t, r, QoQ, 0.41361, 1e-4)
	want(t, r, QoQA, 1.66476, 1e-4)
	// A missing quarter is not bridged.
	in.Obs = []model.Observation{in.Obs[0], in.Obs[2]}
	wantMissing(t, compute(t, in, QoQ), QoQ)
}

// §17.5: a series marked as having a short history shows no z until it has
// 120 observations, however many days the window holds.
func TestZScoreShortHistory(t *testing.T) {
	var obs []model.Observation
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < MinZShortHistory; i++ {
		obs = append(obs, model.Observation{Date: day.AddDate(0, 0, i), Value: 4 + float64(i%7)/10})
	}
	w, err := ParseWindow("52w")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Freq: "d", ZWindowDailyWeekly: w, ShortHistory: true, Obs: obs[:MinZShortHistory-1]}
	wantMissing(t, compute(t, in, Z), Z)
	if have, short := ZHistory(in); have != 119 || !short {
		t.Errorf("ZHistory = %d, %v", have, short)
	}
	// The same series without the mark has a z: 119 observations are plenty.
	plain := in
	plain.ShortHistory = false
	if v := compute(t, plain, Z).Values[Z]; !v.OK {
		t.Error("unmarked series: z not computed")
	}
	if _, short := ZHistory(plain); short {
		t.Error("unmarked series is short")
	}
	in.Obs = obs
	if v := compute(t, in, Z).Values[Z]; !v.OK {
		t.Error("120 observations: z not computed")
	}
	if have, short := ZHistory(in); have != 120 || short {
		t.Errorf("ZHistory = %d, %v", have, short)
	}
	// Observations after AsOf do not count.
	in.AsOf = obs[99].Date
	if have, short := ZHistory(in); have != 100 || !short {
		t.Errorf("ZHistory as of the 100th day = %d, %v", have, short)
	}
}
