package export

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/transform"
)

// treasurySpecs are the transforms shown for the Treasury series.
var treasurySpecs = map[string]spec{
	treasury.SeriesTGA:         {Label: "TGA daily (DTS)", Codes: []string{transform.Level, transform.D1W, transform.D4W}},
	treasury.SeriesMTSReceipts: {Label: "MTS: receipts", Codes: []string{transform.Level, transform.Sum12M, transform.Sum12MYoY}},
	treasury.SeriesMTSOutlays:  {Label: "MTS: outlays", Codes: []string{transform.Level, transform.Sum12M, transform.Sum12MYoY}},
}

// series loads a series of a table.
func (w *week) series(ctx context.Context, id string) (view, error) {
	return w.resolve(ctx, id)
}

func (w *week) seriesList(ctx context.Context, ids []string) ([]view, error) {
	out := make([]view, 0, len(ids))
	for _, id := range ids {
		v, err := w.series(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// ChangesRow is a row of a level, its weekly and four-week changes and z.
type ChangesRow struct {
	Metric
	Level    Quantity `json:"level"`
	Change1W Quantity `json:"change_1w"`
	Change4W Quantity `json:"change_4w"`
	Z        Quantity `json:"z"`
}

// BudgetRow is a row of "2.3 Бюджет и долг"; YoY is of the 12-month sum
// where the series has one.
type BudgetRow struct {
	Metric
	SAAR   bool     `json:"saar"`
	Level  Quantity `json:"level"`
	Change Quantity `json:"change"`
	Sum12M Quantity `json:"sum_12m"`
	YoY    Quantity `json:"yoy"`
}

// CouponAuction is a coupon auction of the period. WaitCMT: the tail waits
// for the CMT of the auction day.
type CouponAuction struct {
	Date        Day      `json:"date"`
	ClosingTime string   `json:"closing_time"`
	Tenor       string   `json:"tenor"`
	AuctionKind string   `json:"auction_kind"`
	Reopening   bool     `json:"reopening"`
	Amount      Quantity `json:"amount"`
	HighYield   Quantity `json:"high_yield"`
	TailBP      Quantity `json:"tail_bp"`
	WaitCMT     bool     `json:"wait_cmt"`
	BTC         Quantity `json:"btc"`
	BTCDev      Quantity `json:"btc_dev"`
	Indirect    Quantity `json:"indirect"`
	IndirectDev Quantity `json:"indirect_dev"`
	Direct      Quantity `json:"direct"`
	DirectDev   Quantity `json:"direct_dev"`
	Dealers     Quantity `json:"dealers"`
	DealersDev  Quantity `json:"dealers_dev"`
}

// BillAuctions is the bills of one tenor in the period, rate and BTC of the
// latest auction.
type BillAuctions struct {
	Name     string   `json:"name"`
	Count    int      `json:"count"`
	Amount   Quantity `json:"amount"`
	HighRate Quantity `json:"high_rate"`
	BTC      Quantity `json:"btc"`
	BTCDev   Quantity `json:"btc_dev"`
}

// SupplyGroup is net issuance of one kind of securities, by window: the
// week, the quarter to date, the previous quarter.
type SupplyGroup struct {
	Name     string     `json:"name"`
	Issued   []Quantity `json:"issued"`
	Redeemed []Quantity `json:"redeemed"`
	Net      []Quantity `json:"net"`
}

// Buyback is a Treasury buyback of the period.
type Buyback struct {
	Date        Day      `json:"date"`
	StartTime   string   `json:"start_time"`
	Type        string   `json:"type"`
	Security    string   `json:"security"`
	Bucket      string   `json:"bucket"`
	ParOffered  Quantity `json:"par_offered"`
	ParAccepted Quantity `json:"par_accepted"`
}

// Supply is "2.5 Предложение долга и байбэки".
type Supply struct {
	QuarterStart Day           `json:"quarter_start"`
	LastDTS      Day           `json:"last_dts"`
	Groups       []SupplyGroup `json:"groups"`
	Total        []Quantity    `json:"total"`
	BillsShare   []Quantity    `json:"bills_share"`
	Buybacks     []Buyback     `json:"buybacks"`
}

// TreasuryNext is an upcoming refunding release, its time in ET.
type TreasuryNext struct {
	Title string    `json:"title"`
	At    time.Time `json:"at"`
}

// TreasuryTexts is section 4 of USA Fiscal & Liquidity.
type TreasuryTexts struct {
	Documents []DocumentItem `json:"documents"`
	Next      []TreasuryNext `json:"next"`
}

// FiscalReport is usa/fiscal_liquidity/current_week.md.
type FiscalReport struct {
	Header        Header          `json:"header"`
	Released      []ReleaseRow    `json:"released"`
	Liquidity     []ChangesRow    `json:"liquidity"`
	Funding       []ChangesRow    `json:"funding"`
	Budget        []BudgetRow     `json:"budget"`
	AuctionWindow int             `json:"auction_window"`
	Coupons       []CouponAuction `json:"coupons"`
	Bills         []BillAuctions  `json:"bills"`
	Supply        Supply          `json:"supply"`
	Curve         []ChangesRow    `json:"curve"`
	Derived       []ChangesRow    `json:"derived"`
	Treasury      TreasuryTexts   `json:"treasury"`
	Missing       MissingSection  `json:"missing"`
}

func changesRow(v view) ChangesRow {
	u := v.Kind.changeUnit()
	return ChangesRow{Metric: metricOf(v), Level: levelQty(v), Change1W: qty(v.V[transform.D1W], u), Change4W: qty(v.V[transform.D4W], u), Z: zQty(v)}
}

func changesRows(vs []view) []ChangesRow {
	out := make([]ChangesRow, 0, len(vs))
	for _, v := range vs {
		out = append(out, changesRow(v))
	}
	return out
}

func budgetRow(v view) BudgetRow {
	yoy := v.get(transform.YoY)
	if v.has(transform.Sum12MYoY) {
		yoy = v.get(transform.Sum12MYoY)
	}
	return BudgetRow{Metric: metricOf(v), SAAR: saarSeries[v.ID], Level: levelQty(v), Change: qty(v.get(transform.D), v.Kind.changeUnit()),
		Sum12M: qty(v.get(transform.Sum12M), unitUSDBn), YoY: qty(yoy, unitPercent)}
}

func (w *week) fiscal(ctx context.Context) (string, error) {
	liq, err := w.seriesList(ctx, w.table("liquidity"))
	if err != nil {
		return "", err
	}
	funding, err := w.seriesList(ctx, w.table("funding"))
	if err != nil {
		return "", err
	}
	budget, err := w.seriesList(ctx, w.table("budget"))
	if err != nil {
		return "", err
	}
	curve, err := w.seriesList(ctx, w.table("curve"))
	if err != nil {
		return "", err
	}
	der, err := w.seriesList(ctx, w.table("derived"))
	if err != nil {
		return "", err
	}
	all := concat(liq, funding, budget, curve)
	sources := w.sourcesWithTexts("fred")
	m := FiscalReport{Header: w.newHeader("", w.title(), sources, staleIDs(all), w.from, w.to),
		Liquidity: changesRows(liq), Funding: changesRows(funding), Curve: changesRows(curve), Derived: changesRows(der), AuctionWindow: transform.AuctionWindow}
	for _, v := range budget {
		m.Budget = append(m.Budget, budgetRow(v))
	}
	if m.Released, err = w.releasedRows(ctx, all); err != nil {
		return "", err
	}
	if m.Coupons, m.Bills, err = w.auctions(ctx); err != nil {
		return "", err
	}
	if m.Supply, err = w.supply(ctx); err != nil {
		return "", err
	}
	if slices.Contains(w.agent.Texts, config.TextsTreasury) {
		if m.Treasury, err = w.treasuryTexts(ctx); err != nil {
			return "", err
		}
	}
	if m.Missing, err = w.missingSection(ctx, all, sources); err != nil {
		return "", err
	}
	return w.report(w.path(), config.ReportFiscal, &m)
}

// periodDays are the first and last ET calendar days of the period.
func (w *week) periodDays() (time.Time, time.Time) {
	f, t := w.from.In(w.et), w.to.In(w.et)
	return time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.UTC), time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func isBill(a model.Auction) bool { return a.Kind == model.KindBill || a.Kind == model.KindCMB }

// shortTenor: "20-Year" → "20Y", "13-Week" → "13W".
func shortTenor(t string) string {
	n, unit, ok := strings.Cut(t, "-")
	if !ok || unit == "" {
		return t
	}
	return n + unit[:1]
}

// numQty is a stored number in unit.
func numQty(n model.Num, unit string) Quantity { return qty(transform.Value{V: n.V, OK: n.OK}, unit) }

// auctions are the auctions of the period with results (§12.2, DATA_MAP #60).
func (w *week) auctions(ctx context.Context) ([]CouponAuction, []BillAuctions, error) {
	first, last := w.periodDays()
	all, err := w.st.Auctions(ctx, first, last)
	if err != nil {
		return nil, nil, err
	}
	type billAgg struct {
		name   string
		weeks  int
		n      int
		amount float64
		last   model.Auction
		m      transform.AuctionMetrics
	}
	coupons := []CouponAuction{}
	bills := map[string]*billAgg{}
	for _, a := range all {
		if !a.HasResults() {
			continue
		}
		prev, err := w.st.AuctionsBefore(ctx, a.Tenor, a.Kind, a.AuctionDate, transform.AuctionWindow)
		if err != nil {
			return nil, nil, err
		}
		cmt, waitCMT := transform.Value{}, false
		if id := transform.CMTFor(a); id != "" {
			obs, err := w.observations(ctx, "fred", id, a.AuctionDate)
			if err != nil {
				return nil, nil, err
			}
			if len(obs) > 0 && obs[0].Date.Equal(a.AuctionDate) {
				cmt = some(obs[0].Value)
			}
			// The data is read as of the period end; a mid-week export
			// runs earlier still.
			cutoff := w.to
			if w.now.Before(cutoff) {
				cutoff = w.now
			}
			waitCMT = !cmt.OK && cutoff.Before(transform.CMTPublished(a.AuctionDate, w.et))
		}
		m := transform.CompareAuction(a, prev, cmt)
		if isBill(a) {
			name := shortTenor(a.Tenor)
			if a.Kind == model.KindCMB {
				name = "CMB " + a.Term
			}
			g := bills[name]
			if g == nil {
				g = &billAgg{name: name, weeks: termWeeks(a.Term)}
				bills[name] = g
			}
			g.n++
			g.amount += a.OfferingAmt.V / 1e9
			if !a.AuctionDate.Before(g.last.AuctionDate) {
				g.last, g.m = a, m
			}
			continue
		}
		coupons = append(coupons, CouponAuction{Date: Day{a.AuctionDate}, ClosingTime: a.ClosingTime, Tenor: a.Tenor, AuctionKind: a.Kind, Reopening: a.Reopening,
			Amount: qty(some(a.OfferingAmt.V/1e9), unitUSDBn), HighYield: numQty(a.HighYield, unitPercent), TailBP: qty(m.TailBP, unitBP), WaitCMT: waitCMT,
			BTC: qty(some(a.BidToCover.V), unitRatio), BTCDev: qty(m.BTCDev, unitRatio),
			Indirect: numQty(a.IndirectPct, unitPercent), IndirectDev: qty(m.IndirectDev, unitPP),
			Direct: numQty(a.DirectPct, unitPercent), DirectDev: qty(m.DirectDev, unitPP),
			Dealers: numQty(a.DealerPct, unitPercent), DealersDev: qty(m.DealerDev, unitPP)})
	}
	aggs := make([]*billAgg, 0, len(bills))
	for _, g := range bills {
		aggs = append(aggs, g)
	}
	sort.Slice(aggs, func(i, j int) bool {
		if aggs[i].weeks != aggs[j].weeks {
			return aggs[i].weeks < aggs[j].weeks
		}
		return aggs[i].name < aggs[j].name
	})
	out := []BillAuctions{}
	for _, g := range aggs {
		out = append(out, BillAuctions{Name: g.name, Count: g.n, Amount: qty(some(g.amount), unitUSDBn), HighRate: numQty(g.last.HighRate, unitPercent),
			BTC: qty(some(g.last.BidToCover.V), unitRatio), BTCDev: qty(g.m.BTCDev, unitRatio)})
	}
	return coupons, out, nil
}

// couponName is "10Y (reopening)", "5Y TIPS".
func couponName(a CouponAuction) string {
	sec := shortTenor(a.Tenor)
	if a.AuctionKind == model.KindTIPS || a.AuctionKind == model.KindFRN {
		sec += " " + a.AuctionKind
	}
	if a.Reopening {
		sec += " (reopening)"
	}
	return sec
}

// couponWhen is the date column of a coupon auction.
func couponWhen(a CouponAuction) string {
	when := a.Date.Format(time.DateOnly)
	if a.ClosingTime != "" {
		when += " " + a.ClosingTime
	}
	return when
}

// couponTail is the tail column of a coupon auction.
func couponTail(a CouponAuction) string {
	if a.WaitCMT {
		return "ждём CMT " + a.Date.Format("01-02")
	}
	return bp(a.TailBP.val())
}

// yield3 is an auction yield, three decimals as published.
func yield3(q Quantity) string {
	v := q.val()
	if !v.OK {
		return dash
	}
	return num(v.V, 3, false) + "%"
}

func ppUnit(q Quantity) string {
	v := q.val()
	if !v.OK {
		return dash
	}
	return pp(v) + " п.п."
}

// termWeeks orders bills by length ("6-Week" → 6, "10-Day" → 1).
func termWeeks(term string) int {
	n, unit, _ := strings.Cut(term, "-")
	v, _ := strconv.Atoi(n)
	if strings.HasPrefix(unit, "Day") {
		return v / 7
	}
	return v
}

// supply is net issuance of marketable securities from the DTS public debt
// transactions (cash basis) for the week, the quarter to date and the
// previous quarter, the gross bills share of auctions as a secondary line
// (DATA_MAP #61, FIXES_W44 C4), and the buybacks of the period.
func (w *week) supply(ctx context.Context) (Supply, error) {
	first, last := w.periodDays()
	qStart := time.Date(last.Year(), time.Month((int(last.Month())-1)/3*3+1), 1, 0, 0, 0, 0, time.UTC)
	prevStart := qStart.AddDate(0, -3, 0)

	// DTS amounts by settlement day, billions; lastDay is the latest DTS day.
	dts := map[string][]model.Observation{}
	var lastDay time.Time
	for _, id := range []string{treasury.SeriesBillsIssued, treasury.SeriesBillsRedeemed, treasury.SeriesCouponsIssued, treasury.SeriesCouponsRedeemed} {
		obs, err := w.observations(ctx, treasury.Source, id, prevStart)
		if err != nil {
			return Supply{}, err
		}
		dts[id] = obs
		if n := len(obs); n > 0 && obs[n-1].Date.After(lastDay) && !obs[n-1].Date.After(last) {
			lastDay = obs[n-1].Date
		}
	}
	sumDTS := func(id string, from, to time.Time) transform.Value {
		var sum float64
		var any bool
		for _, o := range dts[id] {
			if !o.Date.Before(from) && !o.Date.After(to) {
				sum += o.Value / 1000
				any = true
			}
		}
		if !any {
			return transform.Value{}
		}
		return some(sum)
	}
	net := func(issued, redeemed transform.Value) transform.Value {
		if !issued.OK || !redeemed.OK {
			return transform.Value{}
		}
		return some(issued.V - redeemed.V)
	}
	type window struct{ from, to time.Time }
	windows := []window{{first, last}, {qStart, last}, {prevStart, qStart.AddDate(0, 0, -1)}}
	out := Supply{QuarterStart: Day{qStart}, LastDTS: Day{lastDay}, Groups: []SupplyGroup{}, Buybacks: []Buyback{}}
	total := make([]transform.Value, len(windows))
	for i := range total {
		total[i] = some(0)
	}
	for _, g := range []struct{ name, issued, redeemed string }{
		{"Bills", treasury.SeriesBillsIssued, treasury.SeriesBillsRedeemed},
		{"Купоны (notes, bonds; вкл. TIPS, FRN)", treasury.SeriesCouponsIssued, treasury.SeriesCouponsRedeemed},
	} {
		sg := SupplyGroup{Name: g.name}
		for i, x := range windows {
			is, rd := sumDTS(g.issued, x.from, x.to), sumDTS(g.redeemed, x.from, x.to)
			n := net(is, rd)
			sg.Issued, sg.Redeemed, sg.Net = append(sg.Issued, qty(is, unitUSDBn)), append(sg.Redeemed, qty(rd, unitUSDBn)), append(sg.Net, qty(n, unitUSDBn))
			total[i] = net(total[i], some(-n.V))
			if !n.OK {
				total[i] = transform.Value{}
			}
		}
		out.Groups = append(out.Groups, sg)
	}
	for _, t := range total {
		out.Total = append(out.Total, qty(t, unitUSDBn))
	}

	// Gross bills share of auction sales (by auction date), secondary.
	share := func(from, to time.Time) (transform.Value, error) {
		as, err := w.st.Auctions(ctx, from, to)
		if err != nil {
			return transform.Value{}, err
		}
		var bills, all float64
		for _, a := range as {
			if !a.TotalAccepted.OK {
				continue
			}
			all += a.TotalAccepted.V
			if isBill(a) {
				bills += a.TotalAccepted.V
			}
		}
		if all == 0 {
			return transform.Value{}, nil
		}
		return some(bills / all * 100), nil
	}
	for _, x := range windows {
		v, err := share(x.from, x.to)
		if err != nil {
			return Supply{}, err
		}
		out.BillsShare = append(out.BillsShare, qty(v, unitPercent))
	}

	bbs, err := w.st.Buybacks(ctx, first, last)
	if err != nil {
		return Supply{}, err
	}
	bn := func(n model.Num) Quantity { return qty(transform.Value{V: n.V / 1e9, OK: n.OK}, unitUSDBn) }
	for _, x := range bbs {
		out.Buybacks = append(out.Buybacks, Buyback{Date: Day{x.OperationDate}, StartTime: x.StartTime, Type: x.OperationType, Security: x.SecurityType,
			Bucket: x.MaturityBucket, ParOffered: bn(x.ParOffered), ParAccepted: bn(x.ParAccepted)})
	}
	return out, nil
}

// treasuryTexts are the refunding documents of the period and the next
// refunding releases.
func (w *week) treasuryTexts(ctx context.Context) (TreasuryTexts, error) {
	docs, err := w.st.DocumentsBetween(ctx, treasury.Source, w.from, w.to)
	if err != nil {
		return TreasuryTexts{}, err
	}
	evs, err := w.releasesBetween(ctx, w.to, w.to.AddDate(0, 0, 120))
	if err != nil {
		return TreasuryTexts{}, err
	}
	out := TreasuryTexts{Documents: []DocumentItem{}, Next: []TreasuryNext{}}
	for _, d := range docs {
		out.Documents = append(out.Documents, w.documentItem(d))
	}
	for _, e := range evs {
		if e.Source != treasury.Source || e.Status == model.StatusRescheduled {
			continue
		}
		for name, title := range map[string]string{treasury.NameFinancingEstimates: "Financing Estimates", treasury.NameRefundingStatement: "Refunding Statement"} {
			if e.Name == name && !slices.ContainsFunc(out.Next, func(n TreasuryNext) bool { return n.Title == title }) {
				out.Next = append(out.Next, TreasuryNext{Title: title, At: e.ScheduledAt.In(w.et)})
			}
		}
	}
	return out, nil
}

// treasuryNextLine is "Следующие: … (см. calendar_usa.md у мастера).", empty
// without any.
func treasuryNextLine(next []TreasuryNext) string {
	if len(next) == 0 {
		return ""
	}
	parts := make([]string, 0, len(next))
	for _, n := range next {
		parts = append(parts, n.Title+" — "+n.At.Format("2006-01-02 15:04")+" ET")
	}
	return "Следующие: " + strings.Join(parts, ", ") + " (см. calendar_usa.md у мастера)."
}

// levelCell is a level in tables: SAAR amounts say so.
func levelCell(k kind, saar bool, q Quantity) string {
	s := k.level(q.val())
	if saar && q.Value != nil {
		s += " в год (SAAR)"
	}
	return s
}
