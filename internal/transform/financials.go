package transform

import (
	"math"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/model"
)

// Company financials of USA Micro (phase 5b, ТЗ §18): the latest quarter's
// profitability, cash flow, shareholder returns and debt, from XBRL facts as
// filed. Flows are quarters (Quarters, ComponentQuarters); balance sheet
// lines are values on the quarter end date (Instants).

// Point is a balance sheet value on a date.
type Point struct {
	Date      time.Time
	Value     float64
	Concept   string // tag the value came from ("A+B" for components)
	Accession string // filing of the value
	Revised   bool   // an earlier filing reported a different value
}

// Instants returns the balance sheet values of a metric, ascending by date:
// per date the first tag in priority order with a fact, from its latest
// filing.
func Instants(facts []model.CompanyFact, tags []string, unit string) []Point {
	byDate := map[time.Time]Point{}
	for _, tag := range tags {
		tagPoints := map[time.Time]Point{}
		filed := map[time.Time]model.CompanyFact{}
		for _, f := range facts {
			if f.Concept != tag || f.Unit != unit || !f.Start.IsZero() {
				continue
			}
			day := model.Day(f.End)
			cur, ok := tagPoints[day]
			if !ok {
				tagPoints[day], filed[day] = Point{Date: day, Value: f.Value, Concept: tag, Accession: f.Accession}, f
				continue
			}
			cur.Revised = cur.Revised || !sameValue(cur.Value, f.Value)
			if last := filed[day]; f.Filed.After(last.Filed) || (f.Filed.Equal(last.Filed) && f.Accession > last.Accession) {
				cur.Value, cur.Accession, filed[day] = f.Value, f.Accession, f
			}
			tagPoints[day] = cur
		}
		for day, p := range tagPoints {
			if _, ok := byDate[day]; !ok {
				byDate[day] = p
			}
		}
	}
	out := make([]Point, 0, len(byDate))
	for _, p := range byDate {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Point) int { return a.Date.Compare(b.Date) })
	return out
}

// PointBack returns the value about n quarters before date: n×91 days
// earlier within 15 days (52/53-week years end on varying dates).
func PointBack(ps []Point, date time.Time, n int) (Point, bool) {
	target := model.Day(date).AddDate(0, 0, -91*n)
	for _, p := range ps {
		if d := days(p.Date, target); d >= -15 && d <= 15 {
			return p, true
		}
	}
	return Point{}, false
}

// PointAt returns the value on date.
func PointAt(ps []Point, date time.Time) (Point, bool) {
	i := slices.IndexFunc(ps, func(p Point) bool { return p.Date.Equal(model.Day(date)) })
	if i < 0 {
		return Point{}, false
	}
	return ps[i], true
}

// ComponentQuarters sums the quarters of several components (each a tag
// priority list): AVGO D&A = depreciation + amortization of intangibles. A
// quarter needs every component, or the sum would be silently short.
func ComponentQuarters(facts []model.CompanyFact, components [][]string, unit string) []Quarter {
	if len(components) == 0 {
		return nil
	}
	out := Quarters(facts, components[0], unit, false)
	for _, tags := range components[1:] {
		other := Quarters(facts, tags, unit, false)
		out = slices.DeleteFunc(out, func(q Quarter) bool {
			_, ok := QuarterAt(other, q.End)
			return !ok
		})
		for i := range out {
			o, _ := QuarterAt(other, out[i].End)
			out[i].Value += o.Value
			out[i].Concept += "+" + o.Concept
			out[i].Derived = out[i].Derived || o.Derived
			out[i].Revised = out[i].Revised || o.Revised
		}
	}
	return out
}

// ComponentInstants sums balance sheet components on each date of the first
// one; the others count as 0 when absent: companies leave out a zero line
// (META's current debt, GOOGL's commercial paper; LOOKUP T4).
func ComponentInstants(facts []model.CompanyFact, components [][]string, unit string) []Point {
	if len(components) == 0 {
		return nil
	}
	out := Instants(facts, components[0], unit)
	for _, tags := range components[1:] {
		other := Instants(facts, tags, unit)
		for i := range out {
			if o, ok := PointAt(other, out[i].Date); ok {
				out[i].Value += o.Value
				out[i].Concept += "+" + o.Concept
				out[i].Revised = out[i].Revised || o.Revised
			}
		}
	}
	return out
}

// FinancialsInput is one company's metric series.
type FinancialsInput struct {
	Revenue, GrossProfit, CostOfRevenue, OperatingIncome, NetIncome []Quarter
	DA, OperatingCashFlow, Capex, Buybacks, Dividends               []Quarter
	DilutedShares                                                   []Quarter // direct quarters only
	Cash, ShortTermInvestments, Debt                                []Point
	// The company pays no buybacks/dividends (config not_paid): 0, not
	// missing.
	NoBuybacks, NoDividends bool

	// Phase 5c.
	PretaxIncome, IncomeTax []Quarter
	RPO                     []Point
	OneOffRule              OneOffRule // ⚠ thresholds; zero = no flag
}

// Financials are the metrics of the quarter ending at End. Money is in USD,
// margins and shares in %, margin changes in percentage points.
type Financials struct {
	End time.Time

	Revenue, RevenueYoY Value
	// GrossMargin is computed as revenue − cost of revenue when
	// GrossComputed: the company files no gross profit (decision 2026-10-30).
	GrossMargin, GrossMarginChange Value
	GrossComputed                  bool

	OperatingIncome, OperatingIncomeYoY       Value
	OperatingMargin, OperatingMarginChange    Value
	EBITDA, EBITDAYoY                         Value // operating income + D&A; not GAAP
	NetIncome, NetIncomeYoY                   Value
	OperatingCashFlow, Capex, FCF, FCFMargin  Value // FCF = operating cash flow − capex
	FCFTTM, FCFMarginTTM                      Value
	Buybacks, Dividends, ShareholderReturnTTM Value
	ReturnShareOfFCF                          Value // return TTM / FCF TTM; !OK when FCF TTM ≤ 0
	FCFNotPositive                            bool  // why ReturnShareOfFCF is missing
	NoBuybacks, NoDividends                   bool
	NetDebt                                   Value // debt − cash − short-term investments; < 0 = net cash
	DilutedSharesYoY                          Value

	// Other is pretax − operating income (investment gains, interest, FX);
	// Tax the income tax; both with the same quarter a year earlier.
	Other, OtherYearAgo, Tax, TaxYearAgo Value
	// OneOff: the sources of a one-off item, now or a year earlier.
	OneOff      OneOffCause
	RPO, RPOYoY Value // on End; RPO is filed by some companies only
}

// LatestFinancials returns the financials of the latest quarter with revenue
// (the latest 10-Q/10-K); false without any.
func LatestFinancials(in FinancialsInput) (Financials, bool) {
	if len(in.Revenue) == 0 {
		return Financials{}, false
	}
	return FinancialsAt(in, in.Revenue[len(in.Revenue)-1].End), true
}

// FinancialsAt returns the financials of the quarter ending at end.
func FinancialsAt(in FinancialsInput, end time.Time) Financials {
	f := Financials{End: model.Day(end), NoBuybacks: in.NoBuybacks, NoDividends: in.NoDividends}

	gross, computed := grossProfit(in)
	f.GrossComputed = computed
	ebitda := combine(in.OperatingIncome, in.DA, func(a, b float64) float64 { return a + b })
	fcf := combine(in.OperatingCashFlow, in.Capex, func(a, b float64) float64 { return a - b })
	buybacks, dividends := in.Buybacks, in.Dividends
	if in.NoBuybacks {
		buybacks = zeros(in.Revenue)
	}
	if in.NoDividends {
		dividends = zeros(in.Revenue)
	}
	returns := combine(buybacks, dividends, func(a, b float64) float64 { return a + b })

	f.Revenue, f.RevenueYoY = at(in.Revenue, end), QuarterYoY(in.Revenue, end)
	f.GrossMargin, f.GrossMarginChange = marginAt(gross, in.Revenue, end, 0), marginChange(gross, in.Revenue, end)
	f.OperatingIncome, f.OperatingIncomeYoY = at(in.OperatingIncome, end), QuarterYoY(in.OperatingIncome, end)
	f.OperatingMargin, f.OperatingMarginChange = marginAt(in.OperatingIncome, in.Revenue, end, 0), marginChange(in.OperatingIncome, in.Revenue, end)
	f.EBITDA, f.EBITDAYoY = at(ebitda, end), QuarterYoY(ebitda, end)
	f.NetIncome, f.NetIncomeYoY = at(in.NetIncome, end), QuarterYoY(in.NetIncome, end)

	f.OperatingCashFlow, f.Capex, f.FCF = at(in.OperatingCashFlow, end), at(in.Capex, end), at(fcf, end)
	f.FCFMargin = marginAt(fcf, in.Revenue, end, 0)
	f.FCFTTM = TTM(fcf, end)
	if rev := TTM(in.Revenue, end); f.FCFTTM.OK && rev.OK && rev.V != 0 {
		f.FCFMarginTTM = some(f.FCFTTM.V / rev.V * 100)
	}
	f.Buybacks, f.Dividends = at(buybacks, end), at(dividends, end)
	f.ShareholderReturnTTM = TTM(returns, end)
	switch {
	case f.FCFTTM.OK && f.FCFTTM.V <= 0:
		f.FCFNotPositive = true
	case f.FCFTTM.OK && f.ShareholderReturnTTM.OK:
		f.ReturnShareOfFCF = some(f.ShareholderReturnTTM.V / f.FCFTTM.V * 100)
	}

	if debt, ok := PointAt(in.Debt, end); ok {
		if cash, ok := PointAt(in.Cash, end); ok {
			sti, _ := PointAt(in.ShortTermInvestments, end) // none filed (AVGO) = 0
			f.NetDebt = some(debt.Value - cash.Value - sti.Value)
		}
	}
	f.DilutedSharesYoY = QuarterYoY(in.DilutedShares, end)

	other := combine(in.PretaxIncome, in.OperatingIncome, func(a, b float64) float64 { return a - b })
	f.Other, f.OtherYearAgo = at(other, end), back(other, end, 4)
	f.Tax, f.TaxYearAgo = at(in.IncomeTax, end), back(in.IncomeTax, end, 4)
	f.OneOff = OneOffAt(in, end)
	if p, ok := PointAt(in.RPO, end); ok {
		f.RPO = some(p.Value)
		if b, ok := PointBack(in.RPO, end, 4); ok {
			f.RPOYoY = Growth(p.Value, b.Value)
		}
	}
	return f
}

// OneOffRule are the ⚠ thresholds (ТЗ §19.1, rule of 2026-10-31).
type OneOffRule struct {
	RateChangePP       float64 // effective tax rate change against a year earlier
	OtherShareChangePP float64 // other income share of pretax income change
	MaterialityPct     float64 // the change must move net income by more
}

// OneOffCause names the sources of a one-off item.
type OneOffCause struct{ Tax, Other bool }

// Any reports whether there is a one-off item.
func (c OneOffCause) Any() bool { return c.Tax || c.Other }

// String is "налог", "прочие" or both.
func (c OneOffCause) String() string {
	var s []string
	if c.Tax {
		s = append(s, "налог")
	}
	if c.Other {
		s = append(s, "прочие")
	}
	return strings.Join(s, ", ")
}

// OneOffAt checks the quarter ending at end against a year earlier for a
// one-off item. Not the divergence of net and operating income growth: a
// fast grower with steady interest costs diverges with nothing one-off
// (AVGO, review of W44). The cause itself is checked: the effective tax
// rate (tax / pretax income) or the share of other income (pretax −
// operating) in pretax income moved by more than the thresholds, and that
// move times the quarter's pretax income is more than MaterialityPct of
// the quarter's net income in either quarter (a small base would flag
// noise otherwise). Pretax income ≤ 0 or zero net income: no flag.
func OneOffAt(in FinancialsInput, end time.Time) OneOffCause {
	r := in.OneOffRule
	if r.RateChangePP <= 0 || r.OtherShareChangePP <= 0 || r.MaterialityPct <= 0 {
		return OneOffCause{}
	}
	type q struct{ pretax, tax, other, net float64 }
	get := func(n int) (q, bool) {
		pick := func(qs []Quarter) (float64, bool) {
			if n == 0 {
				x, ok := QuarterAt(qs, end)
				return x.Value, ok
			}
			x, ok := QuarterBack(qs, end, n)
			return x.Value, ok
		}
		pt, ok1 := pick(in.PretaxIncome)
		tx, ok2 := pick(in.IncomeTax)
		oi, ok3 := pick(in.OperatingIncome)
		ni, ok4 := pick(in.NetIncome)
		if !ok1 || !ok2 || !ok3 || !ok4 || pt <= 0 || ni == 0 {
			return q{}, false
		}
		return q{pretax: pt, tax: tx, other: pt - oi, net: ni}, true
	}
	cur, ok1 := get(0)
	ago, ok2 := get(4)
	if !ok1 || !ok2 {
		return OneOffCause{}
	}
	// material: the share change (a fraction of pretax income) moves either
	// quarter's net income by more than the materiality threshold.
	material := func(change float64) bool {
		for _, x := range []q{cur, ago} {
			if math.Abs(change)*x.pretax/math.Abs(x.net)*100 > r.MaterialityPct {
				return true
			}
		}
		return false
	}
	rate := cur.tax/cur.pretax - ago.tax/ago.pretax
	share := cur.other/cur.pretax - ago.other/ago.pretax
	return OneOffCause{
		Tax:   math.Abs(rate)*100 > r.RateChangePP && material(rate),
		Other: math.Abs(share)*100 > r.OtherShareChangePP && material(share),
	}
}

// YearToDate sums a metric's quarters of q's calendar year up to q; !OK
// when one of them is missing (phase 5c, capex since the start of the year).
func YearToDate(qs []Quarter, q CalendarQuarter) Value {
	var sum float64
	for i := 1; i <= q.Q; i++ {
		want := CalendarQuarter{Year: q.Year, Q: i}
		j := slices.IndexFunc(qs, func(x Quarter) bool { return CalendarQuarterOf(x.End) == want })
		if j < 0 {
			return Value{}
		}
		sum += qs[j].Value
	}
	return some(sum)
}

// GuidanceRest is what a full-year guidance range leaves for the rest of
// the year after the year to date: implied Q4 capex after Q1–Q3 (ТЗ
// §19.3a). Both bounds are !OK without a year to date.
func GuidanceRest(low, high float64, ytd Value) (Value, Value) {
	if !ytd.OK {
		return Value{}, Value{}
	}
	return some(low - ytd.V), some(high - ytd.V)
}

func back(qs []Quarter, end time.Time, n int) Value {
	q, ok := QuarterBack(qs, end, n)
	if !ok {
		return Value{}
	}
	return some(q.Value)
}

// TTM is the sum of the four quarters ending at end; !OK when one is
// missing.
func TTM(qs []Quarter, end time.Time) Value {
	cur, ok := QuarterAt(qs, end)
	if !ok {
		return Value{}
	}
	sum := cur.Value
	for n := 1; n <= 3; n++ {
		q, ok := QuarterBack(qs, end, n)
		if !ok {
			return Value{}
		}
		sum += q.Value
	}
	return some(sum)
}

// grossProfit is the filed gross profit, or revenue − cost of revenue for a
// company that files none but has a cost of revenue configured.
func grossProfit(in FinancialsInput) ([]Quarter, bool) {
	if len(in.GrossProfit) > 0 || len(in.CostOfRevenue) == 0 {
		return in.GrossProfit, false
	}
	return combine(in.Revenue, in.CostOfRevenue, func(a, b float64) float64 { return a - b }), true
}

// combine joins two series quarter by quarter (same end date).
func combine(a, b []Quarter, op func(x, y float64) float64) []Quarter {
	var out []Quarter
	for _, x := range a {
		y, ok := QuarterAt(b, x.End)
		if !ok {
			continue
		}
		x.Value = op(x.Value, y.Value)
		x.Concept = strings.Join([]string{x.Concept, y.Concept}, ",")
		x.Derived, x.Revised = x.Derived || y.Derived, x.Revised || y.Revised
		out = append(out, x)
	}
	return out
}

// zeros is a 0 for every quarter of like.
func zeros(like []Quarter) []Quarter {
	out := make([]Quarter, len(like))
	for i, q := range like {
		out[i] = Quarter{Start: q.Start, End: q.End}
	}
	return out
}

func at(qs []Quarter, end time.Time) Value {
	q, ok := QuarterAt(qs, end)
	if !ok {
		return Value{}
	}
	return some(q.Value)
}

// marginAt is num/revenue in % for the quarter n quarters before end.
func marginAt(num, revenue []Quarter, end time.Time, n int) Value {
	var x, r Quarter
	var ok1, ok2 bool
	if n == 0 {
		x, ok1 = QuarterAt(num, end)
		r, ok2 = QuarterAt(revenue, end)
	} else {
		x, ok1 = QuarterBack(num, end, n)
		r, ok2 = QuarterBack(revenue, end, n)
	}
	if !ok1 || !ok2 || r.Value == 0 {
		return Value{}
	}
	return some(x.Value / r.Value * 100)
}

// marginChange is the margin change against a year earlier, in p.p.
func marginChange(num, revenue []Quarter, end time.Time) Value {
	cur, base := marginAt(num, revenue, end, 0), marginAt(num, revenue, end, 4)
	if !cur.OK || !base.OK {
		return Value{}
	}
	return some(cur.V - base.V)
}
