package derived

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/transform"
)

func dt(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

// FOMC 2026–2027 decision days (federalreserve.gov, 2026-10-24).
var meetings = []time.Time{
	dt("2026-09-16"), dt("2026-10-28"), dt("2026-12-09"), dt("2027-01-27"), dt("2027-03-17"),
	dt("2027-04-28"), dt("2027-06-09"), dt("2027-07-28"), dt("2027-09-15"), dt("2027-10-27"), dt("2027-12-08"),
}

var zq = config.FedFundsFutures{SymbolFormat: "ZQ{month}{yy}.CBT"}

// Reference: the recorded ZQ strip of 2026-10-23 (testdata/yahoo) and a
// DFF of 3.88; implied rates and cuts computed by hand.
func TestComputeFedPathFromRecordedStrip(t *testing.T) {
	prices, volumes := map[string][]model.Observation{}, map[string][]model.Observation{}
	after := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, f := range yahoo.Fixtures {
		if f.Symbol[:2] != "ZQ" {
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "yahoo", f.File()))
		if err != nil {
			t.Fatal(err)
		}
		s, err := yahoo.Parse(b, after)
		if err != nil {
			t.Fatal(err)
		}
		prices[f.Symbol], volumes[f.Symbol] = s.Observations(), s.Volumes()
	}
	dff := []model.Observation{{Date: dt("2026-10-15"), Value: 3.86}, {Date: dt("2026-10-22"), Value: 3.88}}
	p := ComputeFedPath(FedPathInput{
		AsOf: dt("2026-10-23"), MonthsAhead: 12, Meetings: meetings, Symbol: zq.Symbol,
		Prices: prices, Volumes: volumes, DFF: dff,
	})

	// Meetings from 10-28 on; the month after each; up to October 2027.
	var months []string
	for _, r := range p.Rows {
		months = append(months, r.Month.Format("2006-01")+" "+r.Symbol)
	}
	want := []string{
		"2026-11 ZQX26.CBT", "2027-01 ZQF27.CBT", "2027-02 ZQG27.CBT", "2027-04 ZQJ27.CBT",
		"2027-05 ZQK27.CBT", "2027-07 ZQN27.CBT", "2027-08 ZQQ27.CBT", "2027-10 ZQV27.CBT",
	}
	if !slices.Equal(months, want) {
		t.Fatalf("rows = %v", months)
	}
	near := func(name string, v transform.Value, w float64) {
		t.Helper()
		if !v.OK || math.Abs(v.V-w) > 1e-9 {
			t.Errorf("%s = %+v, want %v", name, v, w)
		}
	}
	near("DFF", p.DFF, 3.88)
	near("DFF week ago", p.DFFWeekAgo, 3.86) // 10-16: last value on or before is 10-15
	near("Nov implied", p.Rows[0].Implied, 100-96.095)
	near("Nov vs DFF", p.Rows[0].VsDFFBP, (100-96.095-3.88)*100)
	near("Nov week ago", p.Rows[0].WeekAgo, 100-96.08)
	if !p.Rows[0].Meeting.Equal(dt("2026-10-28")) {
		t.Errorf("Nov meeting = %s", p.Rows[0].Meeting)
	}
	if !p.Horizon.Equal(dt("2027-10-01")) {
		t.Errorf("horizon = %s", p.Horizon)
	}
	// October 2027: 100 − 95.445 = 4.555 → +67.5 b.p. → 2.7 hikes (cuts −2.7);
	// a week ago 100 − 95.38 = 4.62 vs 3.86 → +76 b.p. → 3.04 hikes.
	near("path", p.PathBP, (4.555-3.88)*100)
	near("cuts", p.Cuts, -(4.555-3.88)*100/25)
	near("path week ago", p.PathBPWeekAgo, (4.62-3.86)*100)
	near("cuts week ago", p.CutsWeekAgo, -(4.62-3.86)*100/25)
	// Per meeting: November vs DFF, January vs November.
	near("Nov meeting", p.Rows[0].MeetingBP, (100-96.095-3.88)*100)
	near("Jan meeting", p.Rows[1].MeetingBP, (p.Rows[1].Implied.V-p.Rows[0].Implied.V)*100)
	for _, r := range p.Rows {
		if r.NoTrades {
			t.Errorf("%s flagged without trades; the recording has volume", r.Symbol)
		}
	}
}

func TestComputeFedPathGaps(t *testing.T) {
	sym := zq.Symbol
	day := func(s string, v float64) model.Observation { return model.Observation{Date: dt(s), Value: v} }
	zero := func(from string, n int) []model.Observation {
		var out []model.Observation
		for i := range n {
			out = append(out, day(dt(from).AddDate(0, 0, i).Format(time.DateOnly), 0))
		}
		return out
	}
	in := FedPathInput{
		AsOf: dt("2026-10-23"), MonthsAhead: 3, Meetings: meetings, Symbol: sym,
		Prices: map[string][]model.Observation{
			"ZQX26.CBT": {day("2026-10-23", 96.1)},
			// January: last price older than a week → no implied rate.
			"ZQF27.CBT": {day("2026-10-01", 95.9)},
		},
		Volumes: map[string][]model.Observation{"ZQX26.CBT": zero("2026-10-19", 5)},
		DFF:     []model.Observation{day("2026-10-22", 3.88)},
	}
	p := ComputeFedPath(in)
	// MonthsAhead 3: November and January only (February is month +4).
	if len(p.Rows) != 2 || p.Rows[1].Implied.OK || !p.Rows[0].NoTrades {
		t.Fatalf("rows = %+v", p.Rows)
	}
	// January has no price: no move of its own.
	if !p.Rows[0].MeetingBP.OK || p.Rows[1].MeetingBP.OK {
		t.Errorf("meeting moves = %+v / %+v", p.Rows[0].MeetingBP, p.Rows[1].MeetingBP)
	}
	// January has no price and November no trades: no path.
	if !p.Horizon.IsZero() || p.PathBP.OK {
		t.Errorf("path = %+v / %v", p.Horizon, p.PathBP)
	}
	// With trades, the path falls back to the last row with a price.
	in.Volumes = nil
	if p := ComputeFedPath(in); !p.Horizon.Equal(dt("2026-11-01")) || !p.PathBP.OK || p.PathBPWeekAgo.OK {
		t.Errorf("path = %+v / %v / %+v", p.Horizon, p.PathBP, p.PathBPWeekAgo)
	}
	// Without DFF there is no path.
	in.DFF = nil
	if p := ComputeFedPath(in); p.PathBP.OK || p.Rows[0].VsDFFBP.OK {
		t.Errorf("no DFF: %+v", p)
	}
}

func TestMoveProbability(t *testing.T) {
	for _, c := range []struct{ bp, want float64 }{{0, 0}, {-13, 0.52}, {6.25, 0.25}, {-25, 1}, {-40, 1}} {
		if got := MoveProbability(c.bp); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("MoveProbability(%v) = %v, want %v", c.bp, got, c.want)
		}
	}
}

// W44 (FIXES_W44 B3 review): Friday's bar of the far contracts came with the
// initial load of the contract new in the window; the rows must still be
// read on one day, the new contract and a thin one stay out of the totals,
// and the weekly change compares the same contract.
func TestComputeFedPathOneDayAndHorizon(t *testing.T) {
	sym := zq.Symbol
	bars := func(price float64, vol float64, days ...string) ([]model.Observation, []model.Observation) {
		var p, v []model.Observation
		for _, d := range days {
			p = append(p, model.Observation{Date: dt(d), Value: price})
			v = append(v, model.Observation{Date: dt(d), Value: vol})
		}
		return p, v
	}
	week := []string{"2026-10-19", "2026-10-20", "2026-10-21", "2026-10-22", "2026-10-23", "2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29"}
	prices, vols := map[string][]model.Observation{}, map[string][]model.Observation{}
	// 2027: Jan, Feb, Apr (near), Jul (thin vs Apr), Aug, Oct, Nov (new, with Friday's bar).
	for _, c := range []struct {
		sym   string
		price float64
		vol   float64
	}{{"ZQF27.CBT", 96, 50000}, {"ZQG27.CBT", 95.9, 30000}, {"ZQJ27.CBT", 95.8, 20000}, {"ZQK27.CBT", 95.7, 10000},
		{"ZQN27.CBT", 95.6, 1000}, {"ZQQ27.CBT", 95.5, 3000}, {"ZQV27.CBT", 95.45, 3000}, {"ZQX27.CBT", 95.45, 2000}} {
		prices[c.sym], vols[c.sym] = bars(c.price, c.vol, week...)
	}
	// Week ago (10-22) Oct was 0.10 lower in price; Friday's Nov bar jumps.
	prices["ZQV27.CBT"][3].Value = 95.35
	prices["ZQX27.CBT"] = append(prices["ZQX27.CBT"], model.Observation{Date: dt("2026-10-30"), Value: 95.56})
	in := FedPathInput{
		AsOf: dt("2026-11-01"), MonthsAhead: 12, Meetings: meetings, Symbol: sym, Prices: prices, Volumes: vols,
		DFF: []model.Observation{{Date: dt("2026-10-21"), Value: 3.88}, {Date: dt("2026-10-29"), Value: 3.88}},
	}
	p := ComputeFedPath(in)
	if !p.PriceDate.Equal(dt("2026-10-29")) {
		t.Fatalf("price date = %s", p.PriceDate)
	}
	byMonth := map[string]FedPathRow{}
	for _, r := range p.Rows {
		byMonth[r.Month.Format("2006-01")] = r
	}
	nov, jul := byMonth["2027-11"], byMonth["2027-07"]
	if !nov.New || math.Abs(nov.Implied.V-(100-95.45)) > 1e-9 || !jul.Thin || byMonth["2027-08"].Thin || byMonth["2027-10"].New {
		t.Errorf("rows: nov %+v, jul %+v", nov, jul)
	}
	near := func(name string, v transform.Value, w float64) {
		t.Helper()
		if !v.OK || math.Abs(v.V-w) > 1e-9 {
			t.Errorf("%s = %+v, want %v", name, v, w)
		}
	}
	// Horizon October: +67 now, +77 a week ago (same contract).
	if !p.Horizon.Equal(dt("2027-10-01")) || !p.HorizonWeekAgo.Equal(dt("2027-10-01")) {
		t.Errorf("horizon = %s, week ago %s", p.Horizon, p.HorizonWeekAgo)
	}
	near("path", p.PathBP, (100-95.45-3.88)*100)
	near("path week ago", p.PathBPWeekAgo, (100-95.35-3.88)*100)
	// Next three meetings (Dec, Jan, Mar → April contract).
	if !p.NearMeeting.Equal(dt("2027-03-17")) {
		t.Errorf("near meeting = %s", p.NearMeeting)
	}
	near("near", p.NearBP, (100-95.8-3.88)*100)
}
