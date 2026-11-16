// Package transform computes the metrics of ТЗ §10 from stored levels.
// All functions are pure: they take observations in date order and return
// values; nothing here touches the database.
package transform

import (
	"fmt"
	"math"
	"slices"
	"time"

	"macro-collector/internal/model"
)

// Transform codes (ТЗ §10).
const (
	Level  = "level"  // last value
	D      = "d"      // change vs previous observation
	D1W    = "d1w"    // change over 1 week (§17.5)
	D4W    = "d4w"    // change over 4 weeks
	MoM    = "mom"    // % vs one month earlier
	YoY    = "yoy"    // % vs one year earlier
	A3M    = "a3m"    // 3-month change, annualised %
	A6M    = "a6m"    // 6-month change, annualised %
	QoQA   = "qoqa"   // quarter on quarter, annualised %
	QoQ    = "qoq"    // quarter on quarter, % (the European convention for GDP)
	Diff   = "diff"   // absolute change vs previous observation (no scaling)
	Avg3   = "avg3"   // 3-month average of the monthly change (payrolls)
	Avg4W  = "avg4w"  // 4-week average of the level (claims)
	Sum12M = "sum12m" // 12-month rolling sum
	// Sum12MYoY is the % change of the 12-month rolling sum on a year
	// earlier: a monthly YoY of a budget flow swings with the calendar
	// (payments moved between months), the rolling sum does not.
	Sum12MYoY = "sum12m_yoy"
	Z         = "z"     // z-score of the level
	MA50      = "ma50"  // % distance to the 50-observation average
	MA200     = "ma200" // % distance to the 200-observation average
)

// Codes lists every valid transform code.
var Codes = []string{Level, D, D1W, D4W, MoM, YoY, A3M, A6M, QoQA, QoQ, Diff, Avg3, Avg4W, Sum12M, Sum12MYoY, Z, MA50, MA200}

// MinZObservations is the smallest window for a z-score (§17.5).
const MinZObservations = 20

// MinZShortHistory is how many observations a series marked as having a
// short history must have stored before its z-score is shown (§17.5): the
// observations themselves are counted, not the days since the first fetch,
// so a loaded history counts.
const MinZShortHistory = 120

// Value is a computed number; OK is false when there is not enough data.
type Value struct {
	V  float64
	OK bool
}

func some(v float64) Value { return Value{V: v, OK: true} }

// Input describes one series.
type Input struct {
	Obs  []model.Observation // ascending by date
	Freq string              // d | w | bw | m | q (FRED frequency_short, lower case)
	AsOf time.Time           // observations after this date are ignored (IORB publishes ahead)
	// ChangeScale multiplies d, d1w and d4w: 100 turns percent into basis
	// points for rates and yields; 0 means 1.
	ChangeScale float64
	// RelativeChanges reports d1w and d4w as % changes (prices and
	// indices, examples/: "WTI … Δ1w +2.1%") instead of differences.
	RelativeChanges bool
	// ShortHistory: z is not computed until the series has
	// MinZShortHistory observations up to AsOf (see ZHistory).
	ShortHistory bool
	// Z-score windows from config.metrics.
	ZWindowDailyWeekly      Window
	ZWindowMonthlyQuarterly Window
}

// Result holds the computed transforms of a series at its last observation.
type Result struct {
	Date   time.Time // date of the last observation ≤ AsOf
	Values map[string]Value
}

// Compute evaluates codes for in. An unknown code is an error; missing
// history only makes that value !OK.
func Compute(in Input, codes []string) (Result, error) {
	s := newSeries(in)
	res := Result{Values: make(map[string]Value, len(codes))}
	if s.n() > 0 {
		res.Date = s.date(s.last())
	}
	for _, code := range codes {
		if !slices.Contains(Codes, code) {
			return Result{}, fmt.Errorf("transform: unknown code %q", code)
		}
		if s.n() == 0 {
			res.Values[code] = Value{}
			continue
		}
		res.Values[code] = s.compute(code)
	}
	return res, nil
}

type series struct {
	obs   []model.Observation
	freq  string
	scale float64
	in    Input
}

func newSeries(in Input) *series {
	obs := in.Obs
	if !in.AsOf.IsZero() {
		if i, ok := atOrBefore(obs, model.Day(in.AsOf)); ok {
			obs = obs[:i+1]
		} else {
			obs = nil
		}
	}
	scale := in.ChangeScale
	if scale == 0 {
		scale = 1
	}
	return &series{obs: obs, freq: in.Freq, scale: scale, in: in}
}

func (s *series) n() int               { return len(s.obs) }
func (s *series) last() int            { return len(s.obs) - 1 }
func (s *series) date(i int) time.Time { return model.Day(s.obs[i].Date) }
func (s *series) value(i int) float64  { return s.obs[i].Value }
func (s *series) lastValue() float64   { return s.value(s.last()) }

func (s *series) monthly() bool {
	return s.freq == "m" || s.freq == "q" || s.freq == "sa" || s.freq == "a"
}

// pct is the % change from base to cur.
func pct(cur, base float64) Value { return annualised(cur, base, 1) }

// annualised is ((cur/base)^power − 1) × 100.
func annualised(cur, base, power float64) Value {
	if base == 0 {
		return Value{}
	}
	return some((math.Pow(cur/base, power) - 1) * 100)
}

// atOrBefore returns the last index of obs with date ≤ d.
func atOrBefore(obs []model.Observation, d time.Time) (int, bool) {
	i, found := slices.BinarySearchFunc(obs, d, func(o model.Observation, t time.Time) int {
		return model.Day(o.Date).Compare(t)
	})
	if found {
		return i, true
	}
	return i - 1, i > 0
}

func (s *series) atOrBefore(d time.Time) (int, bool) { return atOrBefore(s.obs, d) }

// back returns the observation `months` months (and `days` days) before the
// last one. Monthly and quarterly series need the exact date; daily and
// weekly ones take the last observation on or before it, but no more than a
// week older (holidays, not gaps).
func (s *series) back(months, days int) (float64, bool) {
	target := s.date(s.last()).AddDate(0, -months, -days)
	i, ok := s.atOrBefore(target)
	if !ok {
		return 0, false
	}
	if s.monthly() {
		if !s.date(i).Equal(target) {
			return 0, false
		}
	} else if s.date(i).Before(target.AddDate(0, 0, -7)) {
		return 0, false
	}
	return s.value(i), true
}

func (s *series) compute(code string) Value {
	t := s.last()
	x := s.lastValue()
	switch code {
	case Level:
		return some(x)
	case D:
		if t < 1 {
			return Value{}
		}
		return some((x - s.value(t-1)) * s.scale)
	case Diff:
		if t < 1 {
			return Value{}
		}
		return some(x - s.value(t-1))
	case D1W, D4W:
		days := 7
		if code == D4W {
			days = 28
		}
		// §17.5: last value vs the last value on or before date − N days.
		i, ok := s.atOrBefore(s.date(t).AddDate(0, 0, -days))
		if !ok {
			return Value{}
		}
		if s.in.RelativeChanges {
			return pct(x, s.value(i))
		}
		return some((x - s.value(i)) * s.scale)
	case MoM:
		if base, ok := s.back(1, 0); ok {
			return pct(x, base)
		}
	case YoY:
		if base, ok := s.back(12, 0); ok {
			return pct(x, base)
		}
	case A3M:
		if base, ok := s.back(3, 0); ok {
			return annualised(x, base, 4)
		}
	case A6M:
		if base, ok := s.back(6, 0); ok {
			return annualised(x, base, 2)
		}
	case QoQA:
		if base, ok := s.back(3, 0); ok {
			return annualised(x, base, 4)
		}
	case QoQ:
		if base, ok := s.back(3, 0); ok {
			return pct(x, base)
		}
	case Avg3:
		return s.avgMonthlyChange(3)
	case Avg4W:
		return s.mean(t-3, t)
	case Sum12M:
		return s.sumMonths(12, 0)
	case Sum12MYoY:
		cur, base := s.sumMonths(12, 0), s.sumMonths(12, 12)
		// Across a change of sign (surplus ↔ deficit) a % change means nothing.
		if !cur.OK || !base.OK || cur.V/base.V <= 0 {
			return Value{}
		}
		return pct(cur.V, base.V)
	case Z:
		return s.zScore()
	case MA50:
		return s.maDistance(50)
	case MA200:
		return s.maDistance(200)
	}
	return Value{}
}

// mean averages values at indices [from, to]; it needs every index.
func (s *series) mean(from, to int) Value {
	if from < 0 {
		return Value{}
	}
	sum := 0.0
	for i := from; i <= to; i++ {
		sum += s.value(i)
	}
	return some(sum / float64(to-from+1))
}

// consecutiveMonths reports whether the last n observations are n
// consecutive months.
func (s *series) consecutiveMonths(n int) bool {
	t := s.last()
	if t-(n-1) < 0 {
		return false
	}
	for k := 1; k < n; k++ {
		if !s.date(t - k).Equal(s.date(t).AddDate(0, -k, 0)) {
			return false
		}
	}
	return true
}

// avgMonthlyChange averages the last n month-on-month changes; it needs
// n+1 consecutive months.
func (s *series) avgMonthlyChange(n int) Value {
	if !s.consecutiveMonths(n + 1) {
		return Value{}
	}
	t := s.last()
	return some((s.value(t) - s.value(t-n)) / float64(n))
}

func (s *series) sumMonths(n, skip int) Value {
	if !s.consecutiveMonths(n + skip) {
		return Value{}
	}
	t := s.last() - skip
	sum := 0.0
	for i := t - n + 1; i <= t; i++ {
		sum += s.value(i)
	}
	return some(sum)
}

// zScore compares the last value with the observations in the window before
// it (the current one excluded), using the sample standard deviation.
func (s *series) zScore() Value {
	w := s.in.ZWindowDailyWeekly
	if s.monthly() {
		w = s.in.ZWindowMonthlyQuarterly
	}
	if w.IsZero() {
		return Value{}
	}
	if s.in.ShortHistory && s.n() < MinZShortHistory {
		return Value{}
	}
	t := s.last()
	start := w.Before(s.date(t))
	var vals []float64
	for i := t - 1; i >= 0 && !s.date(i).Before(start); i-- {
		vals = append(vals, s.value(i))
	}
	if len(vals) < MinZObservations {
		return Value{}
	}
	mean, sd := meanStd(vals)
	if sd == 0 {
		return Value{}
	}
	return some((s.lastValue() - mean) / sd)
}

// ZHistory reports, for a series marked ShortHistory, how many observations
// it has up to AsOf and whether that is still short of MinZShortHistory.
func ZHistory(in Input) (have int, short bool) {
	have = newSeries(in).n()
	return have, in.ShortHistory && have < MinZShortHistory
}

func meanStd(vals []float64) (mean, sd float64) {
	for _, v := range vals {
		mean += v
	}
	mean /= float64(len(vals))
	var ss float64
	for _, v := range vals {
		ss += (v - mean) * (v - mean)
	}
	return mean, math.Sqrt(ss / float64(len(vals)-1))
}

// maDistance is the % distance of the last value to the mean of the last n
// observations (current included).
func (s *series) maDistance(n int) Value {
	m := s.mean(s.last()-n+1, s.last())
	if !m.OK {
		return Value{}
	}
	return pct(s.lastValue(), m.V)
}

// History evaluates code at every observation dated from or later (zero from:
// all), each with the history available at that date, so a chart shows the
// same numbers as the reports. Dates without enough history are left out.
func History(in Input, code string, from time.Time) ([]model.Observation, error) {
	var out []model.Observation
	for _, o := range in.Obs {
		day := model.Day(o.Date)
		if day.Before(from) || (!in.AsOf.IsZero() && day.After(model.Day(in.AsOf))) {
			continue
		}
		at := in
		at.AsOf = day
		res, err := Compute(at, []string{code})
		if err != nil {
			return nil, err
		}
		if v := res.Values[code]; v.OK {
			out = append(out, model.Observation{Date: o.Date, Value: v.V})
		}
	}
	return out, nil
}
