// Package derived computes series that combine other series (ТЗ §6
// `derived`, §10, §17.4–17.5). Results are stored as levels with
// source='derived' so transforms apply to them like to any series.
package derived

import (
	"slices"
	"time"

	"macro-collector/internal/model"
)

// Scale multiplies every value by f.
func Scale(obs []model.Observation, f float64) []model.Observation {
	out := make([]model.Observation, len(obs))
	for i, o := range obs {
		out[i] = model.Observation{Date: o.Date, Value: o.Value * f}
	}
	return out
}

// NetLiquidity is Fed balance sheet − TGA − ON RRP, all in billions, on the
// Wednesday dates of the weekly balance sheet (§17.5). WALCL and WDTGAL
// must share the date; RRP is taken on that date or the last value before.
func NetLiquidity(walcl, tga, rrp []model.Observation) []model.Observation {
	tgaByDate := byDate(tga)
	var out []model.Observation
	for _, w := range walcl {
		t, ok := tgaByDate.at(w.Date)
		if !ok {
			continue
		}
		r, ok := atOrBefore(rrp, w.Date)
		if !ok {
			continue
		}
		out = append(out, model.Observation{Date: w.Date, Value: w.Value - t - r})
	}
	return out
}

// Ratio is a / b on dates present in both; dates with b = 0 are skipped.
func Ratio(a, b []model.Observation) []model.Observation {
	bByDate := byDate(b)
	var out []model.Observation
	for _, x := range a {
		y, ok := bByDate.at(x.Date)
		if !ok || y == 0 {
			continue
		}
		out = append(out, model.Observation{Date: x.Date, Value: x.Value / y})
	}
	return out
}

// Normalize rebases a series to 100 at its first observation: a ratio of
// two indices means nothing as a level, its path does.
func Normalize(obs []model.Observation) []model.Observation {
	if len(obs) == 0 || obs[0].Value == 0 {
		return nil
	}
	return Scale(obs, 100/obs[0].Value)
}

// SpreadBP is (a − b) in basis points for two percent series, on dates
// present in both, without interpolation (§17.5).
func SpreadBP(a, b []model.Observation) []model.Observation {
	bByDate := byDate(b)
	var out []model.Observation
	for _, x := range a {
		y, ok := bByDate.at(x.Date)
		if !ok {
			continue
		}
		out = append(out, model.Observation{Date: x.Date, Value: (x.Value - y) * 100})
	}
	return out
}

// Net is long − short on dates present in both: the net position of a COT
// trader group, in contracts.
func Net(long, short []model.Observation) []model.Observation {
	sByDate := byDate(short)
	var out []model.Observation
	for _, x := range long {
		y, ok := sByDate.at(x.Date)
		if !ok {
			continue
		}
		out = append(out, model.Observation{Date: x.Date, Value: x.Value - y})
	}
	return out
}

// Combine is a + sign·b on dates present in both.
func Combine(a, b []model.Observation, sign float64) []model.Observation {
	bByDate := byDate(b)
	var out []model.Observation
	for _, x := range a {
		y, ok := bByDate.at(x.Date)
		if !ok {
			continue
		}
		out = append(out, model.Observation{Date: x.Date, Value: x.Value + sign*y})
	}
	return out
}

// day is a map key for a calendar date. time.Time keys would also compare
// the location pointer, which differs between parsers.
type day struct {
	y int
	m time.Month
	d int
}

func keyOf(t time.Time) day {
	y, m, d := t.Date()
	return day{y, m, d}
}

type dateIndex map[day]float64

func (ix dateIndex) at(t time.Time) (float64, bool) {
	v, ok := ix[keyOf(t)]
	return v, ok
}

func byDate(obs []model.Observation) dateIndex {
	m := make(dateIndex, len(obs))
	for _, o := range obs {
		m[keyOf(o.Date)] = o.Value
	}
	return m
}

// atOrBefore returns the value on d or the last one before it.
func atOrBefore(obs []model.Observation, d time.Time) (float64, bool) {
	i, found := slices.BinarySearchFunc(obs, d, func(o model.Observation, t time.Time) int {
		return o.Date.Compare(t)
	})
	if found {
		return obs[i].Value, true
	}
	if i == 0 {
		return 0, false
	}
	return obs[i-1].Value, true
}
