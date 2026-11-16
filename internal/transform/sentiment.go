package transform

import (
	"math"
	"time"

	"macro-collector/internal/model"
)

// Ratio divides num by den on the dates both have (RSP/SPY, VIX/VIX3M);
// a zero denominator skips the date.
func Ratio(num, den []model.Observation) []model.Observation {
	byDate := make(map[time.Time]float64, len(den))
	for _, o := range den {
		byDate[o.Date] = o.Value
	}
	var out []model.Observation
	for _, o := range num {
		if d, ok := byDate[o.Date]; ok && d != 0 {
			out = append(out, model.Observation{Date: o.Date, Value: o.Value / d})
		}
	}
	return out
}

// Correlation is Pearson's r of x and y (equal length, at least 3 pairs,
// neither constant).
func Correlation(x, y []float64) Value {
	n := len(x)
	if n != len(y) || n < 3 {
		return Value{}
	}
	var mx, my float64
	for i := range x {
		mx += x[i]
		my += y[i]
	}
	mx /= float64(n)
	my /= float64(n)
	var sxy, sxx, syy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return Value{}
	}
	return some(sxy / math.Sqrt(sxx*syy))
}

// ReturnsCorrelation correlates the daily % returns of price with the
// daily changes of rate (b.p.) over the last n days both have up to asOf
// (FIXES_W44 F2: NDX vs DGS10). Days missing in either series are left
// out, and a change spans two consecutive common days.
func ReturnsCorrelation(price, rate []model.Observation, asOf time.Time, n int) Value {
	r := make(map[time.Time]float64, len(rate))
	for _, o := range rate {
		if !o.Date.After(asOf) {
			r[o.Date] = o.Value
		}
	}
	type pair struct{ p, r float64 }
	var common []pair
	for _, o := range price {
		if v, ok := r[o.Date]; ok && !o.Date.After(asOf) {
			common = append(common, pair{o.Value, v})
		}
	}
	if len(common) < n+1 {
		return Value{}
	}
	common = common[len(common)-n-1:]
	ret, chg := make([]float64, n), make([]float64, n)
	for i := 1; i < len(common); i++ {
		if common[i-1].p == 0 {
			return Value{}
		}
		ret[i-1] = (common[i].p/common[i-1].p - 1) * 100
		chg[i-1] = (common[i].r - common[i-1].r) * 100
	}
	return Correlation(ret, chg)
}

// MeanLast is the average of the last n observations up to asOf (put/call
// 5-day average); fewer than n observations give !OK.
func MeanLast(obs []model.Observation, asOf time.Time, n int) Value {
	var sum float64
	k := 0
	for i := len(obs) - 1; i >= 0 && k < n; i-- {
		if obs[i].Date.After(asOf) {
			continue
		}
		sum += obs[i].Value
		k++
	}
	if k < n || n == 0 {
		return Value{}
	}
	return some(sum / float64(n))
}

// DayMove is the change of a daily close from the previous one, in %.
type DayMove struct {
	Date time.Time
	Pct  float64
}

// DailyChanges are the day-on-day differences of a level series times
// scale (a rate in % ×100 = b.p.), dated in [from, to]; Pct holds them.
func DailyChanges(obs []model.Observation, from, to time.Time, scale float64) []DayMove {
	var out []DayMove
	for i := 1; i < len(obs); i++ {
		d := obs[i].Date
		if d.Before(from) || d.After(to) {
			continue
		}
		out = append(out, DayMove{Date: d, Pct: (obs[i].Value - obs[i-1].Value) * scale})
	}
	return out
}

// DailyMoves are the close-to-close changes of the days in [from, to]
// (obs ascending); the first day needs a close before it.
func DailyMoves(obs []model.Observation, from, to time.Time) []DayMove {
	var out []DayMove
	for i := 1; i < len(obs); i++ {
		d := obs[i].Date
		if d.Before(from) || d.After(to) || obs[i-1].Value == 0 {
			continue
		}
		out = append(out, DayMove{Date: d, Pct: (obs[i].Value/obs[i-1].Value - 1) * 100})
	}
	return out
}
