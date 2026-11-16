package transform

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/yahoo"
)

func d(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func fact(concept, unit, start, end string, v float64, accn, form, filed string) model.CompanyFact {
	return model.CompanyFact{Concept: concept, Unit: unit, Start: d(start), End: d(end), Value: v, Accession: accn, Form: form, Filed: d(filed)}
}

const capex = "PaymentsToAcquirePropertyPlantAndEquipment"

// A June fiscal year (MSFT): Q1 direct, Q2 and Q3 only year to date, Q4
// only in the 10-K annual total. Hand reference: Q2 = 49.27 − 19.394,
// Q3 = 80.146 − 49.27, Q4 = 115.948 − 80.146.
func TestQuartersFromYearToDate(t *testing.T) {
	facts := []model.CompanyFact{
		fact(capex, "USD", "2025-07-01", "2025-09-30", 19.394, "q1", "10-Q", "2025-10-29"),
		fact(capex, "USD", "2025-07-01", "2025-12-31", 49.270, "q2", "10-Q", "2026-01-28"),
		fact(capex, "USD", "2025-07-01", "2026-03-31", 80.146, "q3", "10-Q", "2026-04-29"),
		fact(capex, "USD", "2025-07-01", "2026-06-30", 115.948, "fy", "10-K", "2026-07-29"),
	}
	qs := Quarters(facts, []string{capex}, "USD", false)
	want := []struct {
		start, end string
		v          float64
		derived    bool
		accn       string
	}{
		{"2025-07-01", "2025-09-30", 19.394, false, "q1"},
		{"2025-10-01", "2025-12-31", 29.876, true, "q2"},
		{"2026-01-01", "2026-03-31", 30.876, true, "q3"},
		{"2026-04-01", "2026-06-30", 35.802, true, "fy"},
	}
	if len(qs) != len(want) {
		t.Fatalf("quarters = %+v", qs)
	}
	for i, w := range want {
		q := qs[i]
		if !q.Start.Equal(d(w.start)) || !q.End.Equal(d(w.end)) || math.Abs(q.Value-w.v) > 1e-9 || q.Derived != w.derived || q.Accession != w.accn || q.Approx {
			t.Errorf("quarter %d = %+v, want %+v", i, q, w)
		}
	}
}

// FY EPS − 9M EPS is not the Q4 EPS: derived per-share values are marked.
func TestPerShareQ4IsApprox(t *testing.T) {
	const eps = "EarningsPerShareDiluted"
	facts := []model.CompanyFact{
		fact(eps, "USD/shares", "2025-07-01", "2025-09-30", 3.72, "q1", "10-Q", "2025-10-29"),
		fact(eps, "USD/shares", "2026-01-01", "2026-03-31", 4.10, "q3", "10-Q", "2026-04-29"),
		fact(eps, "USD/shares", "2025-07-01", "2026-03-31", 11.80, "q3", "10-Q", "2026-04-29"),
		fact(eps, "USD/shares", "2025-07-01", "2026-06-30", 16.10, "fy", "10-K", "2026-07-29"),
	}
	qs := Quarters(facts, []string{eps}, "USD/shares", true)
	q4, ok := QuarterAt(qs, d("2026-06-30"))
	if !ok || !q4.Approx || !q4.Derived || math.Abs(q4.Value-4.30) > 1e-9 {
		t.Errorf("Q4 = %+v, %v", q4, ok)
	}
	if q3, _ := QuarterAt(qs, d("2026-03-31")); q3.Approx || q3.Value != 4.10 {
		t.Errorf("direct Q3 = %+v", q3)
	}
}

// AMZN files trailing twelve months in 10-Qs; a TTM fact must not be taken
// for a fiscal year (its start is not the fiscal year start, so it pairs
// with nothing).
func TestTrailingTwelveMonthsIgnored(t *testing.T) {
	const c = "PaymentsToAcquireProductiveAssets"
	facts := []model.CompanyFact{
		fact(c, "USD", "2025-07-01", "2026-06-30", 173.028, "q2", "10-Q", "2026-07-31"), // TTM
		fact(c, "USD", "2025-07-01", "2025-09-30", 35.095, "q3_25", "10-Q", "2025-10-31"),
		fact(c, "USD", "2026-01-01", "2026-06-30", 98.411, "q2", "10-Q", "2026-07-31"),
		fact(c, "USD", "2026-01-01", "2026-03-31", 44.203, "q1", "10-Q", "2026-04-30"),
	}
	qs := Quarters(facts, []string{c}, "USD", false)
	if len(qs) != 3 {
		t.Fatalf("quarters = %+v", qs)
	}
	if q2, _ := QuarterAt(qs, d("2026-06-30")); math.Abs(q2.Value-54.208) > 1e-9 || !q2.Start.Equal(d("2026-04-01")) {
		t.Errorf("Q2 2026 = %+v, want 98.411 − 44.203 from the YTD pair", q2)
	}
}

func TestRevisionKeepsLatestFiling(t *testing.T) {
	facts := []model.CompanyFact{
		fact(capex, "USD", "2026-01-01", "2026-03-31", 30.0, "a", "10-Q", "2026-04-29"),
		fact(capex, "USD", "2026-01-01", "2026-03-31", 31.5, "b", "10-Q", "2027-04-28"), // restated as comparative
		fact(capex, "USD", "2026-04-01", "2026-06-30", 35.0, "c", "10-Q", "2026-07-29"),
		fact(capex, "USD", "2026-04-01", "2026-06-30", 35.0, "d", "10-Q", "2027-07-28"), // same value repeated
	}
	qs := Quarters(facts, []string{capex}, "USD", false)
	if len(qs) != 2 {
		t.Fatalf("quarters = %+v", qs)
	}
	if qs[0].Value != 31.5 || !qs[0].Revised || qs[0].Accession != "b" {
		t.Errorf("restated quarter = %+v", qs[0])
	}
	if qs[1].Revised || qs[1].Accession != "d" {
		t.Errorf("repeated quarter = %+v", qs[1])
	}
}

// Companies switch tags: the first tag with a value for a quarter wins.
func TestTagPriority(t *testing.T) {
	const old, cur = "RevenueFromContractWithCustomerExcludingAssessedTax", "Revenues"
	facts := []model.CompanyFact{
		fact(old, "USD", "2021-11-01", "2022-01-30", 7.6, "x", "10-K", "2022-03-18"),
		fact(cur, "USD", "2021-11-01", "2022-01-30", 7.7, "x", "10-K", "2022-03-18"),
		fact(cur, "USD", "2022-05-02", "2022-07-31", 6.7, "y", "10-Q", "2022-08-31"),
		fact(old, "EUR", "2022-05-02", "2022-07-31", 1, "y", "10-Q", "2022-08-31"), // other unit
	}
	qs := Quarters(facts, []string{old, cur}, "USD", false)
	if len(qs) != 2 || qs[0].Concept != old || qs[0].Value != 7.6 || qs[1].Concept != cur {
		t.Errorf("quarters = %+v", qs)
	}
}

func TestGrowthAndChanges(t *testing.T) {
	if g := Growth(0.5, -0.5); !g.OK || g.V != 200 {
		t.Errorf("Growth(0.5, −0.5) = %+v", g)
	}
	if g := Growth(1, 0); g.OK {
		t.Error("Growth with zero base should be !OK")
	}
	qs := []Quarter{
		{End: d("2025-06-28"), Value: 100}, {End: d("2025-09-27"), Value: 110},
		{End: d("2025-12-27"), Value: 120}, {End: d("2026-03-28"), Value: 90}, {End: d("2026-06-27"), Value: 125},
	}
	if v := QuarterYoY(qs, d("2026-06-27")); !v.OK || math.Abs(v.V-25) > 1e-9 {
		t.Errorf("YoY = %+v", v)
	}
	if v := QuarterQoQ(qs, d("2026-06-27")); !v.OK || math.Abs(v.V-(125.0/90-1)*100) > 1e-9 {
		t.Errorf("QoQ = %+v", v)
	}
	if v := QuarterYoY(qs, d("2025-09-27")); v.OK {
		t.Errorf("YoY without a year of history = %+v", v)
	}
}

func TestFiscalQuarter(t *testing.T) {
	for _, tt := range []struct {
		end, fyEnd string
		fy, q      int
	}{
		{"2026-09-30", "0630", 2027, 1}, // MSFT FQ1 FY27
		{"2026-06-30", "0630", 2026, 4},
		{"2026-06-27", "0926", 2026, 3}, // AAPL
		{"2026-09-26", "0926", 2026, 4},
		{"2026-07-26", "0131", 2027, 2}, // NVDA FY ends late January
		{"2026-01-25", "0131", 2026, 4},
		{"2026-08-02", "1101", 2026, 3}, // AVGO
		{"2026-09-30", "1231", 2026, 3},
		{"2026-12-31", "1231", 2026, 4},
	} {
		fy, q, err := FiscalQuarter(d(tt.end), tt.fyEnd)
		if err != nil || fy != tt.fy || q != tt.q {
			t.Errorf("FiscalQuarter(%s, %s) = FY%d Q%d, %v; want FY%d Q%d", tt.end, tt.fyEnd, fy, q, err, tt.fy, tt.q)
		}
	}
	if _, _, err := FiscalQuarter(d("2026-09-30"), "June"); err == nil {
		t.Error("bad fiscal year end accepted")
	}
}

func TestCalendarQuarter(t *testing.T) {
	if c := CalendarQuarterOf(d("2026-06-27")); c != (CalendarQuarter{2026, 2}) || c.String() != "Q2 2026" {
		t.Errorf("AAPL June quarter = %v", c)
	}
	if c := CalendarQuarterOf(d("2026-01-02")); c != (CalendarQuarter{2025, 4}) {
		t.Errorf("early January end = %v", c)
	}
	if p := (CalendarQuarter{2026, 1}).Prev(1); p != (CalendarQuarter{2025, 4}) {
		t.Errorf("Prev = %v", p)
	}
	if p := (CalendarQuarter{2026, 3}).Prev(4); p != (CalendarQuarter{2025, 3}) {
		t.Errorf("Prev(4) = %v", p)
	}
}

func loadFacts(t *testing.T, ticker string, tags ...string) []model.CompanyFact {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edgar", "companyfacts_"+ticker+".json"))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := edgar.ParseCompanyFacts(b, tags)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

// Capex of the four hyperscalers from the recorded company facts, checked
// by hand against their cash flow statements (LOOKUP E4, billions $).
func TestHyperscalerCapexFixtures(t *testing.T) {
	tags := map[string][]string{
		"MSFT": {capex}, "GOOGL": {capex}, "META": {capex},
		"AMZN": {"PaymentsToAcquireProductiveAssets"},
	}
	by := map[string][]Quarter{}
	for ticker, tg := range tags {
		by[ticker] = Quarters(loadFacts(t, ticker, tg...), tg, "USD", false)
	}
	for ticker, want := range map[string]float64{"MSFT": 35.802, "GOOGL": 44.92, "AMZN": 54.208, "META": 30.12} {
		q, ok := QuarterAt(by[ticker], d("2026-06-30"))
		if !ok || math.Abs(q.Value/1e9-want) > 0.01 {
			t.Errorf("%s Q2 2026 capex = %.3f bn, %v; want %.3f", ticker, q.Value/1e9, ok, want)
		}
	}
	sums := SumByCalendarQuarter(by)
	want := map[CalendarQuarter]struct {
		sum   float64
		count int
	}{
		{2025, 3}: {97.27, 4}, {2025, 4}: {118.63, 4}, {2026, 1}: {129.75, 4}, {2026, 2}: {165.05, 4}, {2026, 3}: {33.94, 1},
	}
	for _, s := range sums {
		w, ok := want[s.Quarter]
		if !ok {
			continue
		}
		if s.Count != w.count || math.Abs(s.Sum/1e9-w.sum) > 0.01 {
			t.Errorf("%v: sum %.2f bn of %d, want %.2f of %d", s.Quarter, s.Sum/1e9, s.Count, w.sum, w.count)
		}
		delete(want, s.Quarter)
	}
	if len(want) > 0 {
		t.Errorf("missing quarters %v", want)
	}
	if v := TrailingSum(sums, CalendarQuarter{2026, 2}, 4); !v.OK || math.Abs(v.V/1e9-510.70) > 0.02 {
		t.Errorf("trailing 4 quarters to Q2 2026 = %+v", v)
	}
	if v := TrailingSum(sums, CalendarQuarter{2026, 3}, 4); v.OK {
		t.Error("trailing sum over a partial quarter should be !OK")
	}
	// Q4 capex comes only from FY − 9M: GOOGL tags no December quarter.
	if q, _ := QuarterAt(by["GOOGL"], d("2025-12-31")); !q.Derived || q.Approx {
		t.Errorf("GOOGL Q4 2025 = %+v", q)
	}
}

// Revenue and EPS of the latest reports, against the press releases
// (TSLA Q3 2026 revenue $28.0B; MSFT FQ4 EPS only as an approximation).
func TestRevenueEPSFixtures(t *testing.T) {
	rev := Quarters(loadFacts(t, "TSLA", "Revenues"), []string{"Revenues"}, "USD", false)
	q3, ok := QuarterAt(rev, d("2026-09-30"))
	if !ok || q3.Value != 28_046_000_000 || q3.Derived {
		t.Errorf("TSLA Q3 2026 revenue = %+v", q3)
	}
	if v := QuarterYoY(rev, d("2026-09-30")); !v.OK {
		t.Error("TSLA revenue YoY missing")
	}
	const eps = "EarningsPerShareDiluted"
	e := Quarters(loadFacts(t, "MSFT", eps), []string{eps}, "USD/shares", true)
	q4, ok := QuarterAt(e, d("2026-06-30"))
	if !ok || !q4.Approx {
		t.Errorf("MSFT FQ4 FY26 EPS = %+v, %v; want an approximation", q4, ok)
	}
	if q1, _ := QuarterAt(e, d("2025-09-30")); q1.Approx || q1.Derived {
		t.Errorf("MSFT FQ1 FY26 EPS = %+v; want direct", q1)
	}
}

func TestSession(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for at, want := range map[string]string{
		"2026-07-29T20:04:53Z": SessionAfterClose, // MSFT 16:04 EDT
		"2026-01-28T21:04:38Z": SessionAfterClose, // 16:04 EST
		"2026-10-02T13:04:26Z": SessionBeforeOpen, // TSLA deliveries 09:04 EDT
		"2026-10-02T15:00:00Z": SessionIntraday,
		"2026-10-24T14:00:00Z": SessionAfterClose, // Saturday
	} {
		ts, _ := time.Parse(time.RFC3339, at)
		if got := Session(ts, ny); got != want {
			t.Errorf("%s: %s, want %s", at, got, want)
		}
	}
}

func TestPriceReaction(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "yahoo", "chart_META.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := yahoo.Parse(b, d("2026-10-25"))
	if err != nil {
		t.Fatal(err)
	}
	r := PriceReaction(s.Observations(), d("2026-10-21"), SessionAfterClose)
	if !r.Pct.OK || math.Abs(r.Pct.V-(-6.98)) > 0.01 || !r.Date.Equal(d("2026-10-22")) {
		t.Errorf("META Q3 reaction = %+v, want −6.98%% on 2026-10-22", r)
	}

	bars := []model.Observation{
		{Date: d("2026-11-25"), Value: 100}, // Wed
		{Date: d("2026-11-27"), Value: 105}, // Fri; Thanksgiving closed
		{Date: d("2026-11-30"), Value: 99},  // Mon
	}
	for _, tt := range []struct {
		name, day, session, date string
		pct                      float64
		ok                       bool
	}{
		{"after close, next session after holiday", "2026-11-25", SessionAfterClose, "2026-11-27", 5, true},
		{"before open on holiday reacts next session", "2026-11-26", SessionBeforeOpen, "2026-11-27", 5, true},
		{"before open on a trading day", "2026-11-27", SessionBeforeOpen, "2026-11-27", 5, true},
		{"intraday", "2026-11-30", SessionIntraday, "2026-11-30", (99.0/105 - 1) * 100, true},
		{"reacting session not closed yet", "2026-11-30", SessionAfterClose, "", 0, false},
		{"no session before the report", "2026-11-25", SessionBeforeOpen, "", 0, false},
	} {
		r := PriceReaction(bars, d(tt.day), tt.session)
		if r.Pct.OK != tt.ok || (tt.ok && (math.Abs(r.Pct.V-tt.pct) > 1e-9 || !r.Date.Equal(d(tt.date)))) {
			t.Errorf("%s: %+v", tt.name, r)
		}
	}
}
