package transform

import (
	"math"
	"testing"

	"macro-collector/internal/model"
)

func inst(concept, end string, v float64, accn, filed string) model.CompanyFact {
	return model.CompanyFact{Concept: concept, Unit: "USD", End: d(end), Value: v, Accession: accn, Form: "10-Q", Filed: d(filed)}
}

func near(t *testing.T, name string, got Value, want float64) {
	t.Helper()
	if !got.OK || math.Abs(got.V-want) > 1e-9 {
		t.Errorf("%s = %+v, want %v", name, got, want)
	}
}

func missing(t *testing.T, name string, got Value) {
	t.Helper()
	if got.OK {
		t.Errorf("%s = %v, want missing", name, got.V)
	}
}

// GOOGL dividends (LOOKUP T3): Q1 2026 only under PaymentsOfDividends, H1
// and 9M only under PaymentsOfOrdinaryDividends. Q2 = 5.23 − 2.54 joins the
// two tags; Q3 = 8.17 − 5.23 stays within one.
func TestQuartersAcrossTagSwitch(t *testing.T) {
	facts := []model.CompanyFact{
		fact("PaymentsOfDividends", "USD", "2026-01-01", "2026-03-31", 2.54, "q1", "10-Q", "2026-04-29"),
		fact("PaymentsOfOrdinaryDividends", "USD", "2026-01-01", "2026-06-30", 5.23, "q2", "10-Q", "2026-07-29"),
		fact("PaymentsOfOrdinaryDividends", "USD", "2026-01-01", "2026-09-30", 8.17, "q3", "10-Q", "2026-10-29"),
	}
	qs := Quarters(facts, []string{"PaymentsOfOrdinaryDividends", "PaymentsOfDividends"}, "USD", false)
	want := map[string]float64{"2026-03-31": 2.54, "2026-06-30": 2.69, "2026-09-30": 2.94}
	if len(qs) != len(want) {
		t.Fatalf("quarters = %+v", qs)
	}
	for _, q := range qs {
		if w := want[q.End.Format("2006-01-02")]; math.Abs(q.Value-w) > 1e-9 {
			t.Errorf("%s = %v, want %v", q.End.Format("2006-01-02"), q.Value, w)
		}
	}
}

// A weighted average share count of a fiscal Q4 is not FY − 9M: only filed
// quarters count.
func TestDirectQuartersDropDerived(t *testing.T) {
	const sh = "WeightedAverageNumberOfDilutedSharesOutstanding"
	facts := []model.CompanyFact{
		fact(sh, "shares", "2025-07-01", "2026-03-31", 7450, "q3", "10-Q", "2026-04-29"),
		fact(sh, "shares", "2026-01-01", "2026-03-31", 7440, "q3", "10-Q", "2026-04-29"),
		fact(sh, "shares", "2025-07-01", "2026-06-30", 7445, "fy", "10-K", "2026-07-29"),
	}
	qs := DirectQuarters(facts, []string{sh}, "shares")
	if len(qs) != 1 || !qs[0].End.Equal(d("2026-03-31")) {
		t.Errorf("quarters = %+v, want only the filed Q3", qs)
	}
}

// AVGO D&A = depreciation + amortization of intangibles (LOOKUP T2). A
// quarter without amortization is left out, not counted as depreciation
// alone.
func TestComponentQuarters(t *testing.T) {
	facts := []model.CompanyFact{
		fact("Depreciation", "USD", "2025-11-03", "2026-02-01", 0.15, "q1", "10-Q", "2026-03-12"),
		fact("Depreciation", "USD", "2025-11-03", "2026-05-03", 0.31, "q2", "10-Q", "2026-06-11"),
		fact("Depreciation", "USD", "2025-08-04", "2025-11-02", 0.14, "fy", "10-K", "2025-12-18"),
		fact("AmortizationOfIntangibleAssets", "USD", "2025-11-03", "2026-02-01", 1.969, "q1", "10-Q", "2026-03-12"),
		fact("AmortizationOfIntangibleAssets", "USD", "2026-02-02", "2026-05-03", 1.967, "q2", "10-Q", "2026-06-11"),
	}
	qs := ComponentQuarters(facts, [][]string{{"Depreciation"}, {"AmortizationOfIntangibleAssets"}}, "USD")
	if len(qs) != 2 {
		t.Fatalf("quarters = %+v", qs)
	}
	if math.Abs(qs[0].Value-2.119) > 1e-9 || math.Abs(qs[1].Value-(0.16+1.967)) > 1e-9 {
		t.Errorf("values = %v, %v", qs[0].Value, qs[1].Value)
	}
	if qs[0].Concept != "Depreciation+AmortizationOfIntangibleAssets" || !qs[1].Derived {
		t.Errorf("quarter = %+v", qs[1])
	}
}

// Balance sheet values: the latest filing per date wins and marks a
// revision; tags by priority per date (NVDA switched its short-term
// investments tag in 2026, LOOKUP T4).
func TestInstants(t *testing.T) {
	facts := []model.CompanyFact{
		inst("MarketableSecuritiesCurrent", "2025-10-26", 49.12, "a", "2025-11-19"),
		inst("MarketableSecuritiesCurrent", "2025-10-26", 49.10, "b", "2026-02-25"),
		inst("DebtSecuritiesCurrent", "2026-07-26", 34.143, "c", "2026-08-26"),
		{Concept: "MarketableSecuritiesCurrent", Unit: "USD", Start: d("2025-01-01"), End: d("2025-10-26"), Value: 1, Filed: d("2025-11-19")},
	}
	ps := Instants(facts, []string{"DebtSecuritiesCurrent", "MarketableSecuritiesCurrent"}, "USD")
	if len(ps) != 2 {
		t.Fatalf("points = %+v", ps)
	}
	if p := ps[0]; p.Value != 49.10 || p.Accession != "b" || !p.Revised {
		t.Errorf("2025-10-26 = %+v", p)
	}
	if p, ok := PointAt(ps, d("2026-07-26")); !ok || p.Concept != "DebtSecuritiesCurrent" || p.Value != 34.143 {
		t.Errorf("2026-07-26 = %+v", p)
	}
}

// Debt components: the first one sets the dates, the others are 0 when not
// filed (META's current debt, LOOKUP T4).
func TestComponentInstants(t *testing.T) {
	facts := []model.CompanyFact{
		inst("LongTermDebtNoncurrent", "2026-06-30", 83.66, "q2", "2026-07-30"),
		inst("LongTermDebtNoncurrent", "2026-09-30", 80.92, "q3", "2026-10-22"),
		inst("LongTermDebtCurrent", "2026-09-30", 2.75, "q3", "2026-10-22"),
		inst("LongTermDebtCurrent", "2026-12-31", 3.0, "q4", "2027-01-28"), // no noncurrent: no date
	}
	ps := ComponentInstants(facts, [][]string{{"LongTermDebtNoncurrent"}, {"LongTermDebtCurrent"}}, "USD")
	if len(ps) != 2 || ps[0].Value != 83.66 || math.Abs(ps[1].Value-83.67) > 1e-9 {
		t.Errorf("points = %+v", ps)
	}
}

// quarters builds a series of direct quarters ending at the given dates.
func quarters(concept string, ends []string, vs ...float64) []Quarter {
	out := make([]Quarter, len(vs))
	for i, v := range vs {
		out[i] = Quarter{End: d(ends[i]), Value: v, Concept: concept}
	}
	return out
}

func points(dates []string, vs ...float64) []Point {
	out := make([]Point, len(vs))
	for i, v := range vs {
		out[i] = Point{Date: d(dates[i]), Value: v}
	}
	return out
}

var fiveQ = []string{"2025-09-30", "2025-12-31", "2026-03-31", "2026-06-30", "2026-09-30"}

// Hand reference for the latest quarter (2026-09-30):
// gross margin 60/100 = 60%, a year ago 45/90 = 50% → +10 p.p.;
// EBITDA 30 + 5 = 35, a year ago 20 + 5 = 25 → +40%;
// FCF 40 − 25 = 15, margin 15%; FCF TTM (15+13+12+15) = 55 over revenue
// TTM 95+97+98+100 = 390 → 14.1026%;
// return TTM (4+1)·4 = 20 → 20/55 = 36.36%;
// net debt 50 − 30 − 10 = 10; shares 98/100 − 1 = −2%.
func TestFinancialsAt(t *testing.T) {
	in := FinancialsInput{
		Revenue:              quarters("Rev", fiveQ, 90, 95, 97, 98, 100),
		GrossProfit:          quarters("GP", fiveQ, 45, 50, 52, 55, 60),
		OperatingIncome:      quarters("OI", fiveQ, 20, 22, 25, 27, 30),
		NetIncome:            quarters("NI", fiveQ, 16, 18, 20, 21, 24),
		DA:                   quarters("DA", fiveQ, 5, 5, 5, 5, 5),
		OperatingCashFlow:    quarters("OCF", fiveQ, 30, 35, 35, 35, 40),
		Capex:                quarters("Capex", fiveQ, 20, 22, 23, 20, 25),
		Buybacks:             quarters("BB", fiveQ, 4, 4, 4, 4, 4),
		Dividends:            quarters("Div", fiveQ, 1, 1, 1, 1, 1),
		DilutedShares:        quarters("Sh", fiveQ, 100, 99.5, 99, 98.5, 98),
		Debt:                 points([]string{"2026-09-30"}, 50),
		Cash:                 points([]string{"2026-09-30"}, 30),
		ShortTermInvestments: points([]string{"2026-09-30"}, 10),
	}
	f, ok := LatestFinancials(in)
	if !ok || !f.End.Equal(d("2026-09-30")) {
		t.Fatalf("latest = %+v", f)
	}
	near(t, "revenue", f.Revenue, 100)
	near(t, "revenue yoy", f.RevenueYoY, 100.0/90*100-100)
	near(t, "gross margin", f.GrossMargin, 60)
	near(t, "gross margin change", f.GrossMarginChange, 10)
	near(t, "operating margin", f.OperatingMargin, 30)
	near(t, "operating margin change", f.OperatingMarginChange, 30-20.0/90*100)
	near(t, "ebitda", f.EBITDA, 35)
	near(t, "ebitda yoy", f.EBITDAYoY, 40)
	near(t, "net income yoy", f.NetIncomeYoY, 50)
	near(t, "fcf", f.FCF, 15)
	near(t, "fcf margin", f.FCFMargin, 15)
	near(t, "fcf ttm", f.FCFTTM, 55)
	near(t, "fcf margin ttm", f.FCFMarginTTM, 55.0/390*100)
	near(t, "return ttm", f.ShareholderReturnTTM, 20)
	near(t, "return share", f.ReturnShareOfFCF, 20.0/55*100)
	near(t, "net debt", f.NetDebt, 10)
	near(t, "shares yoy", f.DilutedSharesYoY, -2)
	if f.GrossComputed || f.FCFNotPositive {
		t.Errorf("flags = %+v", f)
	}
}

// AMZN-like: no gross profit filed and no cost of revenue configured → no
// gross margin; no buybacks or dividends (not_paid) → 0; FCF TTM ≤ 0 → no
// share of FCF; no short-term investments line counts as 0.
func TestFinancialsNotPaidAndNegativeFCF(t *testing.T) {
	in := FinancialsInput{
		Revenue:           quarters("Rev", fiveQ, 180, 210, 190, 195, 200),
		OperatingIncome:   quarters("OI", fiveQ, 17, 21, 19, 20, 23),
		OperatingCashFlow: quarters("OCF", fiveQ, 25, 40, 20, 30, 33),
		Capex:             quarters("Capex", fiveQ, 35, 40, 44, 54, 58),
		NoBuybacks:        true, NoDividends: true,
		Debt: points([]string{"2026-09-30"}, 180),
		Cash: points([]string{"2026-09-30"}, 90),
	}
	f, _ := LatestFinancials(in)
	missing(t, "gross margin", f.GrossMargin)
	missing(t, "ebitda (no D&A)", f.EBITDA)
	near(t, "buybacks", f.Buybacks, 0)
	near(t, "dividends", f.Dividends, 0)
	near(t, "return ttm", f.ShareholderReturnTTM, 0)
	near(t, "fcf ttm", f.FCFTTM, 0+(20-44)+(30-54)+(33-58))
	missing(t, "return share", f.ReturnShareOfFCF)
	if !f.FCFNotPositive || !f.NoBuybacks || !f.NoDividends {
		t.Errorf("flags = %+v", f)
	}
	near(t, "net debt", f.NetDebt, 90)
	missing(t, "shares yoy", f.DilutedSharesYoY)
}

// A company paying buybacks without a filed value for the quarter shows a
// missing value, not 0 (no not_paid).
func TestFinancialsMissingMetric(t *testing.T) {
	in := FinancialsInput{
		Revenue:  quarters("Rev", fiveQ, 90, 95, 97, 98, 100),
		Buybacks: quarters("BB", fiveQ[:4], 4, 4, 4, 4),
	}
	f, _ := LatestFinancials(in)
	missing(t, "buybacks", f.Buybacks)
	missing(t, "return ttm", f.ShareholderReturnTTM)
	missing(t, "net debt", f.NetDebt)
	if _, ok := LatestFinancials(FinancialsInput{}); ok {
		t.Error("latest of an empty input")
	}
}

// META/GOOGL: gross margin = (revenue − cost of revenue) / revenue.
func TestFinancialsComputedGross(t *testing.T) {
	in := FinancialsInput{
		Revenue:       quarters("Rev", fiveQ, 90, 95, 97, 98, 100),
		CostOfRevenue: quarters("CoR", fiveQ, 18, 19, 19, 20, 19),
	}
	f, _ := LatestFinancials(in)
	near(t, "gross margin", f.GrossMargin, 81)
	near(t, "gross margin change", f.GrossMarginChange, 81-72.0/90*100)
	if !f.GrossComputed {
		t.Error("gross margin not marked computed")
	}
}

func TestTTM(t *testing.T) {
	qs := quarters("X", fiveQ, 1, 2, 3, 4, 5)
	near(t, "ttm", TTM(qs, d("2026-09-30")), 14)
	missing(t, "ttm short", TTM(qs, d("2025-12-31")))
}

var rule = OneOffRule{RateChangePP: 15, OtherShareChangePP: 15, MaterialityPct: 10}

// oneOffInput is a company with operating income, pretax income, tax and
// net income a year ago (first) and now (last).
func oneOffInput(oi, pt, tax, ni [2]float64) FinancialsInput {
	two := func(name string, v [2]float64) []Quarter {
		return quarters(name, []string{"2025-09-30", "2026-09-30"}, v[0], v[1])
	}
	return FinancialsInput{Revenue: two("Rev", [2]float64{100, 120}), OperatingIncome: two("OI", oi), PretaxIncome: two("PT", pt),
		IncomeTax: two("Tax", tax), NetIncome: two("NI", ni), OneOffRule: rule}
}

// The rule of 2026-10-31 on the W44 cases (billions, year ago → now).
func TestOneOffAt(t *testing.T) {
	end := d("2026-09-30")
	for _, c := range []struct {
		name string
		in   FinancialsInput
		want OneOffCause
	}{
		// META: tax rate 87.5% → 24.2% (a charge a year ago).
		{"META tax", oneOffInput([2]float64{20.5, 13.0}, [2]float64{21.7, 13.0}, [2]float64{19.0, 3.1}, [2]float64{2.7, 9.9}), OneOffCause{Tax: true}},
		// GOOGL: other income 29% → −25% of pretax (investment gains a year ago).
		{"GOOGL other", oneOffInput([2]float64{31.2, 40.6}, [2]float64{44.0, 32.4}, [2]float64{9.0, 5.9}, [2]float64{35.0, 26.5}), OneOffCause{Other: true}},
		// AVGO: steady interest (−0.6 → −0.7) on fast growth, rate 21% → 14%: nothing one-off.
		{"AVGO growth", oneOffInput([2]float64{5.9, 16.0}, [2]float64{5.3, 15.3}, [2]float64{1.1, 2.2}, [2]float64{4.2, 13.1}), OneOffCause{}},
		// A rate swing of 20 p.p. on a pretax income that is 5% of net income
		// (a company paying almost no tax on a small pretax base) is not material.
		{"immaterial", oneOffInput([2]float64{1, 1}, [2]float64{0.2, 0.2}, [2]float64{0.01, 0.05}, [2]float64{1.0, 1.0}), OneOffCause{}},
		// A loss year ago: rates and shares are meaningless, no flag.
		{"pretax loss", oneOffInput([2]float64{-1, 5}, [2]float64{-1.5, 5}, [2]float64{0.1, 1}, [2]float64{-1.6, 4}), OneOffCause{}},
	} {
		if got := OneOffAt(c.in, end); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := (OneOffCause{Tax: true, Other: true}).String(); got != "налог, прочие" {
		t.Errorf("cause = %q", got)
	}
	in := oneOffInput([2]float64{20.5, 13.0}, [2]float64{21.7, 13.0}, [2]float64{19.0, 3.1}, [2]float64{2.7, 9.9})
	in.OneOffRule = OneOffRule{}
	if OneOffAt(in, end).Any() {
		t.Error("flagged without thresholds")
	}
}

// Other income, tax and RPO of the latest quarter (META-like).
func TestFinancialsOtherTaxRPO(t *testing.T) {
	in := FinancialsInput{
		Revenue:         quarters("Rev", fiveQ, 51, 55, 56, 60, 64),
		OperatingIncome: quarters("OI", fiveQ, 23.5, 24, 24.5, 25, 25.9),
		PretaxIncome:    quarters("PT", fiveQ, 23.9, 24.1, 24.6, 25.1, 25.9),
		IncomeTax:       quarters("Tax", fiveQ, 18.13, 4, 4, 4.5, 4.9),
		NetIncome:       quarters("NI", fiveQ, 5.77, 20.1, 20.6, 20.6, 21.0),
		RPO:             points([]string{"2025-09-30", "2026-09-30"}, 400, 683),
		OneOffRule:      rule,
	}
	f, _ := LatestFinancials(in)
	if !f.OneOff.Tax || f.OneOff.Other {
		t.Errorf("one-off = %+v, want tax", f.OneOff)
	}
	near(t, "other", f.Other, 0)
	near(t, "other year ago", f.OtherYearAgo, 23.9-23.5)
	near(t, "tax", f.Tax, 4.9)
	near(t, "tax year ago", f.TaxYearAgo, 18.13)
	near(t, "rpo", f.RPO, 683)
	near(t, "rpo yoy", f.RPOYoY, 70.75)
	// No RPO filed (AMZN in company facts): missing, not 0.
	g, _ := LatestFinancials(FinancialsInput{Revenue: in.Revenue})
	missing(t, "rpo without tag", g.RPO)
	missing(t, "other without pretax", g.Other)
}

func TestYearToDate(t *testing.T) {
	qs := quarters("Capex", fiveQ, 19.4, 29.9, 30.9, 35.8, 38.9)
	near(t, "Q3 2026", YearToDate(qs, CalendarQuarter{2026, 3}), 30.9+35.8+38.9)
	near(t, "Q1 2026", YearToDate(qs, CalendarQuarter{2026, 1}), 30.9)
	missing(t, "Q4 2025 (Q1–Q2 2025 missing)", YearToDate(qs, CalendarQuarter{2025, 4}))
}

// After the close on Thursday 2026-10-29 with Thursday's close only: the
// reaction waits for Friday's close; Friday evening it is in, and the
// market value moved by −2% × 230 × 10.7 bn shares.
func TestReactionWaitingAndMarketCap(t *testing.T) {
	bars := []model.Observation{{Date: d("2026-10-28"), Value: 235}, {Date: d("2026-10-29"), Value: 230}}
	r := PriceReaction(bars, d("2026-10-29"), SessionAfterClose)
	if r.Pct.OK || !r.Waiting.Equal(d("2026-10-30")) || r.Base.V != 230 {
		t.Errorf("waiting reaction = %+v", r)
	}
	missing(t, "cap change while waiting", MarketCapChange(r, 10.7e9))
	bars = append(bars, model.Observation{Date: d("2026-10-30"), Value: 225.4})
	r = PriceReaction(bars, d("2026-10-29"), SessionAfterClose)
	got := MarketCapChange(r, 10.7e9)
	near(t, "cap change, bn", Value{V: got.V / 1e9, OK: got.OK}, -0.02*230*10.7)
	// A Friday report waits for Monday.
	if r := PriceReaction(bars[:1], d("2026-10-30"), SessionAfterClose); !r.Waiting.Equal(d("2026-11-02")) {
		t.Errorf("friday report waits for %v", r.Waiting)
	}
}

// A segment from instance facts: Q3 from 9M − H1, YoY against the same
// quarter a year ago; facts of other members are not mixed in; a loss
// larger than the revenue has no margin.
func TestSegmentAt(t *testing.T) {
	const r, o = "RevenueFromContractWithCustomerExcludingAssessedTax", "OperatingIncomeLoss"
	seg := func(member, concept, start, end string, v float64) model.CompanyFact {
		f := fact(concept, "USD", start, end, v, "a", "10-Q", "2026-10-30")
		f.Member = member
		return f
	}
	facts := []model.CompanyFact{
		seg("AWS", r, "2025-07-01", "2025-09-30", 33.0),
		seg("AWS", r, "2026-01-01", "2026-06-30", 79.8),
		seg("AWS", r, "2026-01-01", "2026-09-30", 125.8),
		seg("AWS", o, "2026-07-01", "2026-09-30", 17.8),
		seg("NA", r, "2026-07-01", "2026-09-30", 112.9),
		seg("RL", r, "2026-07-01", "2026-09-30", 0.4),
		seg("RL", o, "2026-07-01", "2026-09-30", -4.1),
	}
	s := SegmentAt(facts, "AWS", r, o, d("2026-09-30"))
	near(t, "AWS revenue", s.Revenue, 46.0)
	near(t, "AWS yoy", s.RevenueYoY, 46.0/33.0*100-100)
	near(t, "AWS margin", s.Margin, 17.8/46.0*100)
	rl := SegmentAt(facts, "RL", r, o, d("2026-09-30"))
	near(t, "RL loss", rl.Profit, -4.1)
	missing(t, "RL margin", rl.Margin)
	missing(t, "unknown member", SegmentAt(facts, "X", r, o, d("2026-09-30")).Revenue)
}

// GOOGL 2026: guidance 195–205 bn after 132.0 bn in Q1–Q3 leaves 63–73 bn
// for Q4.
func TestGuidanceRest(t *testing.T) {
	lo, hi := GuidanceRest(195e9, 205e9, Value{V: 132.0e9, OK: true})
	near(t, "low, bn", Value{V: lo.V / 1e9, OK: lo.OK}, 63)
	near(t, "high, bn", Value{V: hi.V / 1e9, OK: hi.OK}, 73)
	lo, _ = GuidanceRest(195e9, 205e9, Value{})
	missing(t, "without year to date", lo)
}
