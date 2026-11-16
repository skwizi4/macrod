package transform

import (
	"fmt"
	"math"
	"slices"
	"time"

	"macro-collector/internal/model"
)

// Company quarters from XBRL facts (USA Micro). Facts are stored as filed:
// quarterly, year-to-date, annual and trailing-twelve-month durations
// (LOOKUP E3). A quarter is a direct ~3-month fact or the difference of two
// cumulative facts with the same start whose ends are one quarter apart
// (Q2 = H1 − Q1, Q3 = 9M − H1, Q4 = FY − 9M). No 10-K carries a fiscal Q4
// fact, and most companies tag capex only year to date.

// Duration bounds in days.
const (
	minQuarterDays = 80
	maxQuarterDays = 100
)

// Quarter is one quarterly value of a metric.
type Quarter struct {
	Start, End time.Time
	Value      float64
	Concept    string    // tag the value came from
	Accession  string    // filing of the value (of the larger fact when derived)
	Form       string    // that filing's form: 10-Q | 10-K
	Filed      time.Time // that filing's date
	Derived    bool      // difference of cumulative facts
	// Approx marks a derived per-share value: FY EPS − 9M EPS is not the
	// Q4 EPS (share counts differ), shown with ≈ (decision 2026-10-25).
	Approx bool
	// Revised: an earlier filing reported a different value for the period
	// (or for a fact the difference is made of).
	Revised bool
}

type period struct{ start, end time.Time }

type filedFact struct {
	model.CompanyFact
	revised bool
}

// Quarters returns the quarters of a metric, ascending by end date. tags
// are in priority order: for each period the first tag with a fact wins
// (companies switch tags over the years). A difference may join facts of
// two tags: GOOGL filed Q1 2026 dividends under one tag and H1 under
// another (LOOKUP T3). Only facts in unit are used. perShare marks EPS-like
// metrics, whose derived values are approximations.
func Quarters(facts []model.CompanyFact, tags []string, unit string, perShare bool) []Quarter {
	latest := map[period]filedFact{}
	for _, tag := range tags {
		for p, f := range latestByPeriod(facts, tag, unit) {
			if _, ok := latest[p]; !ok {
				latest[p] = f
			}
		}
	}
	out := periodQuarters(latest, perShare)
	slices.SortFunc(out, func(a, b Quarter) int { return a.End.Compare(b.End) })
	return out
}

// DirectQuarters are the quarters filed as such, for metrics that cannot be
// derived by subtraction: a weighted average share count of the fiscal Q4
// is not FY − 9M (the 10-K has only the annual one, LOOKUP T5).
func DirectQuarters(facts []model.CompanyFact, tags []string, unit string) []Quarter {
	return slices.DeleteFunc(Quarters(facts, tags, unit, false), func(q Quarter) bool { return q.Derived })
}

func periodQuarters(latest map[period]filedFact, perShare bool) []Quarter {
	var out []Quarter
	direct := map[time.Time]bool{}
	for p, f := range latest {
		if d := days(p.start, p.end); d >= minQuarterDays && d <= maxQuarterDays {
			out = append(out, quarterOf(f, p.start, f.Value, false, false, f.revised))
			direct[p.end] = true
		}
	}
	for p, a := range latest {
		if days(p.start, p.end) <= maxQuarterDays || direct[p.end] {
			continue
		}
		for q, b := range latest {
			gap := days(q.end, p.end)
			if !q.start.Equal(p.start) || gap < minQuarterDays || gap > maxQuarterDays {
				continue
			}
			start := q.end.AddDate(0, 0, 1)
			out = append(out, quarterOf(a, start, a.Value-b.Value, true, perShare, a.revised || b.revised))
			direct[p.end] = true
			break
		}
	}
	return out
}

func quarterOf(f filedFact, start time.Time, v float64, derived, approx, revised bool) Quarter {
	return Quarter{Start: start, End: f.End, Value: v, Concept: f.Concept, Accession: f.Accession, Form: f.Form, Filed: f.Filed,
		Derived: derived, Approx: approx, Revised: revised}
}

// latestByPeriod keeps, per period, the fact from the latest filing and
// whether any earlier filing gave a different value.
func latestByPeriod(facts []model.CompanyFact, tag, unit string) map[period]filedFact {
	out := map[period]filedFact{}
	for _, f := range facts {
		if f.Concept != tag || f.Unit != unit || f.Start.IsZero() {
			continue
		}
		p := period{model.Day(f.Start), model.Day(f.End)}
		cur, ok := out[p]
		if !ok {
			out[p] = filedFact{CompanyFact: f}
			continue
		}
		revised := cur.revised || !sameValue(cur.Value, f.Value)
		if f.Filed.After(cur.Filed) || (f.Filed.Equal(cur.Filed) && f.Accession > cur.Accession) {
			cur.CompanyFact = f
		}
		cur.revised = revised
		out[p] = cur
	}
	return out
}

func sameValue(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

func days(from, to time.Time) int { return int(model.Day(to).Sub(model.Day(from)).Hours() / 24) }

// QuarterAt returns the quarter ending at end.
func QuarterAt(qs []Quarter, end time.Time) (Quarter, bool) {
	for _, q := range qs {
		if q.End.Equal(model.Day(end)) {
			return q, true
		}
	}
	return Quarter{}, false
}

// QuarterBack returns the quarter n quarters before the one ending at end:
// its end is n×91 days earlier within a 15-day tolerance (52/53-week years
// end on varying dates).
func QuarterBack(qs []Quarter, end time.Time, n int) (Quarter, bool) {
	target := model.Day(end).AddDate(0, 0, -91*n)
	for _, q := range qs {
		if d := days(q.End, target); d >= -15 && d <= 15 {
			return q, true
		}
	}
	return Quarter{}, false
}

// QuarterQoQ and QuarterYoY are % changes against the previous quarter and
// the same quarter a year earlier.
func QuarterQoQ(qs []Quarter, end time.Time) Value { return quarterChange(qs, end, 1) }
func QuarterYoY(qs []Quarter, end time.Time) Value { return quarterChange(qs, end, 4) }

func quarterChange(qs []Quarter, end time.Time, n int) Value {
	cur, ok := QuarterAt(qs, end)
	if !ok {
		return Value{}
	}
	base, ok := QuarterBack(qs, end, n)
	if !ok {
		return Value{}
	}
	return Growth(cur.Value, base.Value)
}

// Growth is the % change from base to cur against |base|, so that EPS going
// from −0.5 to 0.5 reads +200%, not −200%.
func Growth(cur, base float64) Value {
	if base == 0 {
		return Value{}
	}
	return some((cur - base) / math.Abs(base) * 100)
}

// CalendarQuarter is the calendar quarter a fiscal quarter mostly falls in:
// the quarter of end − 15 days (AAPL's June quarter ends on 2026-06-27).
type CalendarQuarter struct{ Year, Q int }

func (c CalendarQuarter) String() string { return fmt.Sprintf("Q%d %d", c.Q, c.Year) }

// CalendarQuarterOf returns the calendar quarter of a quarter ending at end.
func CalendarQuarterOf(end time.Time) CalendarQuarter {
	d := model.Day(end).AddDate(0, 0, -15)
	return CalendarQuarter{Year: d.Year(), Q: (int(d.Month())-1)/3 + 1}
}

// Prev returns the calendar quarter n quarters earlier.
func (c CalendarQuarter) Prev(n int) CalendarQuarter {
	i := c.Year*4 + c.Q - 1 - n
	return CalendarQuarter{Year: i / 4, Q: i%4 + 1}
}

// FiscalQuarter returns the fiscal year and quarter of a quarter ending at
// end for a company whose fiscal year ends on fyEnd (submissions
// fiscalYearEnd, MMDD). The fiscal year is named by the year it ends in
// (MSFT FY27 ends June 2027). 52/53-week years end within days of fyEnd.
func FiscalQuarter(end time.Time, fyEnd string) (fy, q int, err error) {
	t, err := time.Parse("0102", fyEnd)
	if err != nil {
		return 0, 0, fmt.Errorf("fiscal year end %q: want MMDD", fyEnd)
	}
	end = model.Day(end)
	yearEnd := func(y int) time.Time { return time.Date(y, t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	next := yearEnd(end.Year())
	if next.Before(end.AddDate(0, 0, -10)) {
		next = yearEnd(end.Year() + 1)
	}
	prev := yearEnd(next.Year() - 1)
	q = int(math.Round(float64(days(prev, end)) / 91.3))
	return next.Year(), min(max(q, 1), 4), nil
}

// AlignedSum is a metric summed over companies for one calendar quarter.
type AlignedSum struct {
	Quarter CalendarQuarter
	Sum     float64
	Count   int // companies with a value
}

// Complete reports whether every company contributed.
func (a AlignedSum) Complete(companies int) bool { return a.Count == companies }

// SumByCalendarQuarter sums quarters of several companies by calendar
// quarter, ascending. Partial quarters are included with their Count.
func SumByCalendarQuarter(byCompany map[string][]Quarter) []AlignedSum {
	sums := map[CalendarQuarter]*AlignedSum{}
	for _, qs := range byCompany {
		for _, q := range qs {
			cq := CalendarQuarterOf(q.End)
			s, ok := sums[cq]
			if !ok {
				s = &AlignedSum{Quarter: cq}
				sums[cq] = s
			}
			s.Sum += q.Value
			s.Count++
		}
	}
	out := make([]AlignedSum, 0, len(sums))
	for _, s := range sums {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b AlignedSum) int {
		return (a.Quarter.Year*4 + a.Quarter.Q) - (b.Quarter.Year*4 + b.Quarter.Q)
	})
	return out
}

// TrailingSum is the sum of the four complete calendar quarters ending at
// q; !OK when any of them is missing or partial.
func TrailingSum(sums []AlignedSum, q CalendarQuarter, companies int) Value {
	var total float64
	for i := range 4 {
		want := q.Prev(i)
		j := slices.IndexFunc(sums, func(s AlignedSum) bool { return s.Quarter == want })
		if j < 0 || !sums[j].Complete(companies) {
			return Value{}
		}
		total += sums[j].Sum
	}
	return some(total)
}

// NextQuarterEnd estimates the end of the quarter after one ending at end:
// month ends stay month ends (MSFT 06-30 → 09-30), 52/53-week calendars move
// by 13 weeks (AAPL 06-27 → 09-26).
func NextQuarterEnd(end time.Time) time.Time {
	end = model.Day(end)
	if end.AddDate(0, 0, 1).Day() == 1 {
		return time.Date(end.Year(), end.Month()+4, 0, 0, 0, 0, 0, time.UTC)
	}
	return end.AddDate(0, 0, 91)
}

// FiscalYearEnd is the MMDD of the fiscal year end shown by the latest
// annual (10-K, ~1 year) fact, "" without one.
func FiscalYearEnd(facts []model.CompanyFact) string {
	var last time.Time
	for _, f := range facts {
		if f.Form != "10-K" || f.Start.IsZero() {
			continue
		}
		if d := days(f.Start, f.End); d >= 350 && d <= 380 && f.End.After(last) {
			last = f.End
		}
	}
	if last.IsZero() {
		return ""
	}
	return last.Format("0102")
}
