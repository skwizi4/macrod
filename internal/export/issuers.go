package export

import (
	"context"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/irpress"
	"macro-collector/internal/sources/irxlsx"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// EU Micro (phase 11, ТЗ §18.7): issuers outside the quarterly EDGAR flow.
// Prices and the reaction to reports for all of them; annual numbers where
// they are filed as structured data; press releases for their outlook
// paragraphs and quotes. Everything as of the period end, like USA Micro.

// IssuerReaction is the price reaction to a report (§19.5) and the market
// value change on it, in the issuer's currency.
type IssuerReaction struct {
	Pct          Quantity `json:"pct"`
	Date         Day      `json:"date"`
	WaitingClose Day      `json:"waiting_close"`
	CapChange    Quantity `json:"cap_change"`
	HasShares    bool     `json:"has_shares"` // a share count exists: Δ cap. can follow
}

// IssuerReportRow is a report date of the period (section 1).
type IssuerReportRow struct {
	Ticker   string         `json:"ticker"`
	Name     string         `json:"name"`
	Date     Day            `json:"date"`
	Session  string         `json:"session"`
	Period   string         `json:"period"`
	Estimate bool           `json:"estimate"`
	Source   string         `json:"source"`
	Reaction IssuerReaction `json:"reaction"`
}

// IssuerPrice is a company's last close of the period and its changes;
// VsIndex is Δ1w less the index's Δ1w, p.p.
type IssuerPrice struct {
	Ticker   string   `json:"ticker"`
	Name     string   `json:"name"`
	Symbol   string   `json:"symbol"`
	Date     Day      `json:"date"`
	Close    Quantity `json:"close"`
	Change1W Quantity `json:"change_1w"`
	Change4W Quantity `json:"change_4w"`
	VsIndex  Quantity `json:"vs_index"`
}

// IssuerAnnual is a company's latest fiscal year from structured data and
// the year before; YoY in percent. FCF = operating cash flow − capex.
type IssuerAnnual struct {
	Ticker          string   `json:"ticker"`
	Name            string   `json:"name"`
	Year            int      `json:"year"`
	Source          string   `json:"source"` // "EDGAR 20-F", "ESEF"
	Filed           Day      `json:"filed"`
	Stale           bool     `json:"stale"`
	Revenue         Quantity `json:"revenue"`
	RevenueYoY      Quantity `json:"revenue_yoy"`
	OperatingIncome Quantity `json:"operating_income"`
	OperatingYoY    Quantity `json:"operating_yoy"`
	NetIncome       Quantity `json:"net_income"`
	NetYoY          Quantity `json:"net_yoy"`
	EPS             Quantity `json:"eps"`
	EPSYoY          Quantity `json:"eps_yoy"`
	OperatingCF     Quantity `json:"operating_cf"`
	Capex           Quantity `json:"capex"`
	FCF             Quantity `json:"fcf"`
	FCFYoY          Quantity `json:"fcf_yoy"`
}

// IssuerQuarter is a company's latest quarter from its own Excel (step 18,
// D15) and the same quarter a year earlier; YoY in percent. The operating
// profit is the company's own measure (OperatingLabel), not an IFRS line.
type IssuerQuarter struct {
	Ticker         string   `json:"ticker"`
	Name           string   `json:"name"`
	Quarter        string   `json:"quarter"` // "Q3 FY2026", "Q3 2026"
	End            Day      `json:"end"`
	File           string   `json:"file"`
	Filed          Day      `json:"filed"`
	RevenueLabel   string   `json:"revenue_label"`
	Revenue        Quantity `json:"revenue"`
	RevenueYoY     Quantity `json:"revenue_yoy"`
	OperatingLabel string   `json:"operating_label"`
	Operating      Quantity `json:"operating"`
	OperatingYoY   Quantity `json:"operating_yoy"`
	NetIncome      Quantity `json:"net_income"`
	NetYoY         Quantity `json:"net_yoy"`
	EPS            Quantity `json:"eps"`
	EPSYoY         Quantity `json:"eps_yoy"`
}

// IssuerRelease is a press release of the period: the outlook paragraphs
// and quotes of the first full_releases of a company, a line for the rest.
type IssuerRelease struct {
	Ticker     string   `json:"ticker"`
	Name       string   `json:"name"`
	Published  Day      `json:"published"`
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	Full       bool     `json:"full"`
	Paragraphs []string `json:"paragraphs"`
}

// IssuerNextRow is a company's next report date (section 2); Date zero:
// none in the config.
type IssuerNextRow struct {
	Ticker   string `json:"ticker"`
	Name     string `json:"name"`
	Date     Day    `json:"date"`
	Session  string `json:"session"`
	Period   string `json:"period"`
	Estimate bool   `json:"estimate"`
	Source   string `json:"source"`
}

// IssuerMissing is a company with something missing or out of date.
type IssuerMissing struct {
	Ticker string `json:"ticker"`
	Text   string `json:"text"`
}

// IssuerMicroReport is <region>/micro/current_week.md of issuers.
type IssuerMicroReport struct {
	Header        Header            `json:"header"`
	IndexLabel    string            `json:"index_label"`
	Reports       []IssuerReportRow `json:"reports"`
	Upcoming      []IssuerNextRow   `json:"upcoming"`
	Prices        []IssuerPrice     `json:"prices"`
	Annual        []IssuerAnnual    `json:"annual"`
	Quarterly     []IssuerQuarter   `json:"quarterly"`
	NoFacts       []string          `json:"no_facts"` // names of companies without structured annual or quarterly data
	Releases      []IssuerRelease   `json:"releases"`
	NoPress       []string          `json:"no_press"` // names of companies without an open release list
	ReleaseChars  int               `json:"release_chars"`
	IssuerMissing []IssuerMissing   `json:"issuer_missing"`
	Missing       MissingSection    `json:"missing"`
}

// issuer is one company's data as of the period end.
type issuer struct {
	cfg    config.Issuer
	closes []model.Observation
	facts  []model.CompanyFact
	dates  []config.IssuerDate // report_dates merged with the calendar page (phase 14c)
}

// Sources of the file: fetch_state keys of the facts and the releases.
const (
	issuerFactsSource    = "issuer_facts"
	issuerPressSource    = "ir"
	issuerCalendarSource = "issuer_calendar"
)

// CalendarSource names a date read from the issuer's calendar page.
func CalendarSource(name string) string {
	return name + " IR, финансовый календарь (автоматически)"
}

// MergeReportDates joins the report dates of the config with those read
// from the issuer's calendar page (phase 14c, D29), by period: a confirmed
// config date wins (a different calendar date is noted in its source), a
// calendar date replaces an estimate and adds the periods the config lacks.
func MergeReportDates(name string, cfg []config.IssuerDate, cal []store.IssuerReportDate) []config.IssuerDate {
	out := slices.Clone(cfg)
	for _, c := range cal {
		i := slices.IndexFunc(out, func(d config.IssuerDate) bool { return d.Period == c.Period })
		auto := config.IssuerDate{Date: c.Date.Format(time.DateOnly), Day: c.Date, Session: config.SessionPreMarket, Period: c.Period,
			Source: CalendarSource(name)}
		switch {
		case i < 0:
			out = append(out, auto)
		case out[i].Estimate:
			auto.Session = out[i].Session
			out[i] = auto
		case !out[i].Day.Equal(c.Date):
			out[i].Source += "; в календаре эмитента — " + c.Date.Format(time.DateOnly)
		}
	}
	slices.SortStableFunc(out, func(a, b config.IssuerDate) int { return a.Day.Compare(b.Day) })
	return out
}

func (w *week) issuersMicro(ctx context.Context) (string, error) {
	sec := w.cfg.Issuers
	var cs []*issuer
	calendar := false
	since := w.asOf.AddDate(0, 0, -60)
	for _, t := range w.agent.Companies {
		ic, ok := sec.Issuer(t)
		if !ok {
			continue
		}
		c := &issuer{cfg: ic}
		var err error
		if c.closes, err = w.observations(ctx, "yahoo", ic.Symbol, since); err != nil {
			return "", err
		}
		c.closes = slices.DeleteFunc(c.closes, func(o model.Observation) bool { return o.Date.After(w.asOf) })
		if ic.Facts != nil || ic.Quarterly != nil {
			if c.facts, err = w.st.CompanyFacts(ctx, ic.Ticker); err != nil {
				return "", err
			}
			c.facts = slices.DeleteFunc(c.facts, func(f model.CompanyFact) bool { return f.Filed.After(w.asOf) })
		}
		c.dates = ic.Reports
		if ic.Calendar != nil {
			cal, err := w.st.IssuerReportDates(ctx, ic.Ticker)
			if err != nil {
				return "", err
			}
			// As of the period end: a date read (or moved to) later is not known yet.
			cal = slices.DeleteFunc(cal, func(d store.IssuerReportDate) bool { return d.ChangedAt.After(w.asOf) })
			c.dates = MergeReportDates(ic.Name, ic.Reports, cal)
			calendar = true
		}
		cs = append(cs, c)
	}
	index, err := w.observations(ctx, "yahoo", w.region.Index, since)
	if err != nil {
		return "", err
	}
	index = slices.DeleteFunc(index, func(o model.Observation) bool { return o.Date.After(w.asOf) })

	sources := []string{"yahoo", issuerFactsSource, issuerPressSource}
	if calendar {
		sources = append(sources, issuerCalendarSource)
	}
	m := IssuerMicroReport{Header: w.newHeader("", w.title(), sources, nil, w.from, w.to), IndexLabel: w.indexLabel(),
		Reports: []IssuerReportRow{}, Upcoming: []IssuerNextRow{}, Prices: []IssuerPrice{}, Annual: []IssuerAnnual{}, Quarterly: []IssuerQuarter{},
		NoFacts: []string{}, Releases: []IssuerRelease{}, NoPress: []string{}, ReleaseChars: sec.ReleaseChars, IssuerMissing: []IssuerMissing{}}
	first, last := model.DayIn(w.from, w.et), model.DayIn(w.to, w.et)
	indexWeek := changeOver(index, 7)
	for _, c := range cs {
		annual, hasAnnual := w.issuerAnnual(c)
		for _, d := range c.dates {
			if !d.Day.Before(first) && d.Day.Before(last) {
				m.Reports = append(m.Reports, w.issuerReportRow(c, d, annual))
			}
		}
		next := IssuerNextRow{Ticker: c.cfg.Ticker, Name: c.cfg.Name}
		for _, d := range c.dates {
			if !d.Day.Before(last) {
				next = IssuerNextRow{Ticker: c.cfg.Ticker, Name: c.cfg.Name, Date: Day{d.Day}, Session: d.Session, Period: d.Period,
					Estimate: d.Estimate, Source: d.Source}
				break
			}
		}
		m.Upcoming = append(m.Upcoming, next)

		p := IssuerPrice{Ticker: c.cfg.Ticker, Name: c.cfg.Name, Symbol: c.cfg.Symbol, Close: Quantity{Unit: priceUnit(c.cfg)},
			Change1W: Quantity{Unit: unitPercent}, Change4W: Quantity{Unit: unitPercent}, VsIndex: Quantity{Unit: unitPP}}
		if n := len(c.closes); n > 0 {
			lastClose := c.closes[n-1]
			p.Date, p.Close = Day{lastClose.Date}, qty(transform.Value{V: lastClose.Value, OK: true}, priceUnit(c.cfg))
			w1 := changeOver(c.closes, 7)
			p.Change1W, p.Change4W = qty(w1, unitPercent), qty(changeOver(c.closes, 28), unitPercent)
			if w1.OK && indexWeek.OK {
				p.VsIndex = qty(transform.Value{V: w1.V - indexWeek.V, OK: true}, unitPP)
			}
			if w.asOf.Sub(lastClose.Date) > 5*24*time.Hour {
				m.IssuerMissing = append(m.IssuerMissing, IssuerMissing{Ticker: c.cfg.Ticker,
					Text: "последнее закрытие " + lastClose.Date.Format(time.DateOnly) + " — цена устарела"})
			}
		} else {
			m.IssuerMissing = append(m.IssuerMissing, IssuerMissing{Ticker: c.cfg.Ticker, Text: "нет цен Yahoo (" + c.cfg.Symbol + ")"})
		}
		m.Prices = append(m.Prices, p)

		if c.cfg.Quarterly != nil {
			if qr, ok := issuerQuarter(c); ok {
				m.Quarterly = append(m.Quarterly, qr)
			} else {
				text := "квартальных цифр из Excel эмитента в базе нет"
				if c.cfg.Quarterly.FromFile() {
					text += " — файл присылает пользователь (бот или macroctl issuers file)"
				}
				m.IssuerMissing = append(m.IssuerMissing, IssuerMissing{Ticker: c.cfg.Ticker, Text: text})
			}
		}
		switch {
		case c.cfg.Facts == nil && c.cfg.Quarterly == nil:
			m.NoFacts = append(m.NoFacts, c.cfg.Name)
		case c.cfg.Facts == nil:
		case !hasAnnual:
			m.IssuerMissing = append(m.IssuerMissing, IssuerMissing{Ticker: c.cfg.Ticker, Text: "годовых цифр в базе нет (загрузка " + factsSourceName(c.cfg.Facts) + ")"})
		default:
			m.Annual = append(m.Annual, annual)
			if annual.Stale {
				m.IssuerMissing = append(m.IssuerMissing, IssuerMissing{Ticker: c.cfg.Ticker,
					Text: "годовые цифры за " + itoa(annual.Year) + " — новее нет больше " + itoa(sec.StaleMonths) + " мес."})
			}
		}
		if c.cfg.Press == nil {
			m.NoPress = append(m.NoPress, c.cfg.Name)
			continue
		}
		rels, err := w.issuerReleases(ctx, c)
		if err != nil {
			return "", err
		}
		m.Releases = append(m.Releases, rels...)
	}
	if m.Missing, err = w.missingSection(ctx, nil, sources); err != nil {
		return "", err
	}
	return w.report(w.path(), config.ReportIssuers, &m)
}

// indexLabel names the region's index: its Yahoo label, else the symbol.
func (w *week) indexLabel() string {
	for _, s := range w.cfg.Yahoo.Symbols {
		if s.Symbol == w.region.Index && s.Label != "" {
			return s.Label
		}
	}
	return w.region.Index
}

func priceUnit(c config.Issuer) string {
	if c.CurrencyOf() == "EUR" {
		return unitEUR
	}
	return unitUSD
}

func bnUnit(c config.Issuer) string {
	if c.CurrencyOf() == "EUR" {
		return unitEURBn
	}
	return unitUSDBn
}

func factsSourceName(f *config.IssuerFacts) string {
	if f.Source == config.FactsESEF {
		return "ESEF"
	}
	return "EDGAR " + strings.Join(f.Forms, ", ")
}

// changeOver is the % change of the last close against the last close at
// least days earlier.
func changeOver(obs []model.Observation, days int) transform.Value {
	if len(obs) == 0 {
		return transform.Value{}
	}
	last := obs[len(obs)-1]
	cut := last.Date.AddDate(0, 0, -days)
	for i := len(obs) - 2; i >= 0; i-- {
		if !obs[i].Date.After(cut) {
			return transform.Growth(last.Value, obs[i].Value)
		}
	}
	return transform.Value{}
}

// issuerReportRow is a report of the period with the price reaction.
// German issuers report before Xetra opens: the report day reacts.
func (w *week) issuerReportRow(c *issuer, d config.IssuerDate, a IssuerAnnual) IssuerReportRow {
	row := IssuerReportRow{Ticker: c.cfg.Ticker, Name: c.cfg.Name, Date: Day{d.Day}, Session: d.Session, Period: d.Period,
		Estimate: d.Estimate, Source: d.Source,
		Reaction: IssuerReaction{Pct: Quantity{Unit: unitPercent}, CapChange: Quantity{Unit: bnUnit(c.cfg)}}}
	session := transform.SessionBeforeOpen
	if d.Session == config.SessionAfterClose {
		session = transform.SessionAfterClose
	}
	shares := issuerShares(c)
	row.Reaction.HasShares = shares > 0
	re := transform.PriceReaction(c.closes, d.Day, session)
	if !re.Pct.OK {
		if !re.Waiting.IsZero() && !re.Waiting.Before(model.DayIn(w.now, w.et)) {
			row.Reaction.WaitingClose = Day{re.Waiting}
		}
		return row
	}
	row.Reaction.Pct, row.Reaction.Date = qty(re.Pct, unitPercent), Day{re.Date}
	if shares > 0 {
		v := transform.MarketCapChange(re, shares)
		row.Reaction.CapChange = qty(transform.Value{V: v.V / 1e9, OK: v.OK}, bnUnit(c.cfg))
	}
	return row
}

// issuerShares is the weighted average share count of the latest year.
func issuerShares(c *issuer) float64 {
	if c.cfg.Facts == nil {
		return 0
	}
	end, ok := latestYearEnd(c.facts, c.cfg.Facts.Concepts.Shares, "shares")
	if !ok {
		return 0
	}
	v := annualValue(c.facts, c.cfg.Facts.Concepts.Shares, "shares", end)
	return v.V
}

// latestYearEnd is the end of the latest full-year fact of the tags.
func latestYearEnd(facts []model.CompanyFact, tags []string, unit string) (time.Time, bool) {
	var end time.Time
	for _, f := range facts {
		if slices.Contains(tags, f.Concept) && f.Unit == unit && fullYear(f) && f.End.After(end) {
			end = f.End
		}
	}
	return end, !end.IsZero()
}

func fullYear(f model.CompanyFact) bool {
	if f.Start.IsZero() {
		return false
	}
	d := f.End.Sub(f.Start).Hours() / 24
	return d >= 350 && d <= 380
}

// annualValue is the full-year value of the first tag that has one for the
// year ending on end: the latest filed (a restated comparative wins).
func annualValue(facts []model.CompanyFact, tags []string, unit string, end time.Time) transform.Value {
	for _, t := range tags {
		var best *model.CompanyFact
		for i := range facts {
			f := &facts[i]
			if f.Concept != t || f.Unit != unit || !f.End.Equal(end) || !fullYear(*f) {
				continue
			}
			if best == nil || f.Filed.After(best.Filed) || (f.Filed.Equal(best.Filed) && f.Accession > best.Accession) {
				best = f
			}
		}
		if best != nil {
			return transform.Value{V: best.Value, OK: true}
		}
	}
	return transform.Value{}
}

// issuerAnnual is the latest year of a company with structured data.
func (w *week) issuerAnnual(c *issuer) (IssuerAnnual, bool) {
	f := c.cfg.Facts
	if f == nil {
		return IssuerAnnual{}, false
	}
	cur := c.cfg.CurrencyOf()
	end, ok := latestYearEnd(c.facts, f.Concepts.Revenue, cur)
	if !ok {
		return IssuerAnnual{}, false
	}
	prev := end.AddDate(-1, 0, 0)
	bn := bnUnit(c.cfg)
	amount := func(tags []string, at time.Time) transform.Value {
		v := annualValue(c.facts, tags, cur, at)
		return transform.Value{V: v.V / 1e9, OK: v.OK}
	}
	yoy := func(a, b transform.Value) Quantity {
		if !a.OK || !b.OK {
			return Quantity{Unit: unitPercent}
		}
		return qty(transform.Growth(a.V, b.V), unitPercent)
	}
	fcf := func(at time.Time) transform.Value {
		ocf, capex := amount(f.Concepts.OperatingCashFlow, at), amount(f.Concepts.Capex, at)
		if !ocf.OK || !capex.OK {
			return transform.Value{}
		}
		return transform.Value{V: ocf.V - math.Abs(capex.V), OK: true}
	}
	rev, revP := amount(f.Concepts.Revenue, end), amount(f.Concepts.Revenue, prev)
	op, opP := amount(f.Concepts.OperatingIncome, end), amount(f.Concepts.OperatingIncome, prev)
	net, netP := amount(f.Concepts.NetIncome, end), amount(f.Concepts.NetIncome, prev)
	eps, epsP := annualValue(c.facts, f.Concepts.EPS, cur+"/shares", end), annualValue(c.facts, f.Concepts.EPS, cur+"/shares", prev)
	capex := amount(f.Concepts.Capex, end)
	if capex.OK {
		capex.V = math.Abs(capex.V)
	}
	var filed time.Time
	for _, x := range c.facts {
		if slices.Contains(f.Concepts.Revenue, x.Concept) && x.End.Equal(end) && x.Filed.After(filed) {
			filed = x.Filed
		}
	}
	a := IssuerAnnual{Ticker: c.cfg.Ticker, Name: c.cfg.Name, Year: end.Year(), Source: factsSourceName(f), Filed: Day{filed},
		Stale:   end.AddDate(0, w.cfg.Issuers.StaleMonths, 0).Before(w.asOf),
		Revenue: qty(rev, bn), RevenueYoY: yoy(rev, revP), OperatingIncome: qty(op, bn), OperatingYoY: yoy(op, opP),
		NetIncome: qty(net, bn), NetYoY: yoy(net, netP), EPS: qty(eps, priceUnit(c.cfg)), EPSYoY: yoy(eps, epsP),
		OperatingCF: qty(amount(f.Concepts.OperatingCashFlow, end), bn), Capex: qty(capex, bn),
		FCF: qty(fcf(end), bn), FCFYoY: yoy(fcf(end), fcf(prev))}
	return a, true
}

// quarterValue is the field's value of the quarter ending on end from the
// issuer's Excel: the latest filed (a later file revises).
func quarterValue(facts []model.CompanyFact, field, unit string, end time.Time) (model.CompanyFact, bool) {
	var best *model.CompanyFact
	for i := range facts {
		f := &facts[i]
		if f.Form != irxlsx.Form || f.Concept != field || f.Unit != unit || !f.End.Equal(end) {
			continue
		}
		if best == nil || f.Filed.After(best.Filed) || (f.Filed.Equal(best.Filed) && f.Accession > best.Accession) {
			best = f
		}
	}
	if best == nil {
		return model.CompanyFact{}, false
	}
	return *best, true
}

// issuerQuarter is the latest quarter with revenue from the issuer's Excel.
func issuerQuarter(c *issuer) (IssuerQuarter, bool) {
	q := c.cfg.Quarterly
	cur := c.cfg.CurrencyOf()
	var end time.Time
	var file string
	var filed time.Time
	for _, f := range c.facts {
		if f.Form == irxlsx.Form && f.Concept == config.QuarterRevenue && f.Unit == cur && (f.End.After(end) || (f.End.Equal(end) && f.Filed.After(filed))) {
			end, file, filed = f.End, f.Accession, f.Filed
		}
	}
	if end.IsZero() {
		return IssuerQuarter{}, false
	}
	ago := end.AddDate(-1, 0, 0)
	ago = time.Date(ago.Year(), ago.Month()+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1) // month end
	bn := bnUnit(c.cfg)
	pair := func(field, unit string, scale float64, qunit string) (Quantity, Quantity) {
		a, okA := quarterValue(c.facts, field, unit, end)
		b, okB := quarterValue(c.facts, field, unit, ago)
		v := Quantity{Unit: qunit}
		if okA {
			v = qty(transform.Value{V: a.Value / scale, OK: true}, qunit)
		}
		if !okA || !okB {
			return v, Quantity{Unit: unitPercent}
		}
		return v, qty(transform.Growth(a.Value, b.Value), unitPercent)
	}
	fy, fq := fiscalQuarter(q, end)
	r := IssuerQuarter{Ticker: c.cfg.Ticker, Name: c.cfg.Name, Quarter: q.QuarterLabel(fy, fq), End: Day{end}, File: file, Filed: Day{filed},
		RevenueLabel: q.RevenueLabel, OperatingLabel: q.OperatingName}
	r.Revenue, r.RevenueYoY = pair(config.QuarterRevenue, cur, 1e9, bn)
	r.Operating, r.OperatingYoY = pair(config.QuarterOperating, cur, 1e9, bn)
	r.NetIncome, r.NetYoY = pair(config.QuarterNet, cur, 1e9, bn)
	r.EPS, r.EPSYoY = pair(config.QuarterEPS, cur+"/shares", 1, priceUnit(c.cfg))
	return r, true
}

// fiscalQuarter is the fiscal year and quarter of a quarter end.
func fiscalQuarter(q *config.IssuerQuarterly, end time.Time) (fy, quarter int) {
	months := (int(end.Month()) - q.FiscalYearEnd + 12) % 12 // months after the fiscal year end
	quarter = months / 3
	if quarter == 0 {
		quarter = 4
	}
	fy = end.Year()
	if int(end.Month()) > q.FiscalYearEnd {
		fy++
	}
	return fy, quarter
}

// quoteRe finds a quoted statement: “…” or "…" of some length.
var quoteRe = regexp.MustCompile(`[“"«][^”"»]{40,}[”"»]`)

// issuerReleases are the company's releases of the period, newest first:
// the first full_releases with their outlook paragraphs and quotes (up to
// release_chars), the rest as lines.
func (w *week) issuerReleases(ctx context.Context, c *issuer) ([]IssuerRelease, error) {
	docs, err := w.st.DocumentsBetween(ctx, irpress.Source(c.cfg.Ticker), w.from, w.to)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(docs, func(a, b model.Document) int { return b.PublishedAt.Compare(a.PublishedAt) })
	sec := w.cfg.Issuers
	var out []IssuerRelease
	for i, d := range docs {
		r := IssuerRelease{Ticker: c.cfg.Ticker, Name: c.cfg.Name, Published: Day{d.PublishedAt.In(w.et)}, Title: d.Title, URL: d.URL,
			Full: i < sec.FullReleases, Paragraphs: []string{}}
		if r.Full {
			r.Paragraphs = releaseParagraphs(d.Body, sec.Paragraph, sec.ReleaseChars)
		}
		out = append(out, r)
	}
	return out, nil
}

// releaseParagraphs picks the paragraphs on outlook and guidance and the
// quotes, in text order, up to limit characters (a longer paragraph is
// cut). Numbers are not read from them.
func releaseParagraphs(body string, match *regexp.Regexp, limit int) []string {
	out := []string{}
	left := limit
	for _, p := range strings.Split(body, "\n\n") {
		p = strings.TrimSpace(p)
		if p == "" || left <= 0 {
			continue
		}
		if !(match != nil && match.MatchString(p)) && !quoteRe.MatchString(p) {
			continue
		}
		if r := []rune(p); len(r) > left {
			p = strings.TrimSpace(string(r[:max(left-1, 0)])) + "…"
		}
		out = append(out, p)
		left -= len([]rune(p))
	}
	return out
}

// Template cells of the file.

// issuerReactionCell is "+2.1% (11-12)", "ждём close 11-12" or "—".
func issuerReactionCell(r IssuerReaction) string {
	if r.Pct.Value != nil {
		return signedPct1(r.Pct.val()) + " (" + r.Date.Format("01-02") + ")"
	}
	if !r.WaitingClose.IsZero() {
		return "ждём close " + r.WaitingClose.Format("01-02")
	}
	return dash
}

// issuerCapCell is "≈ +4.2 млрд €" or "—".
func issuerCapCell(r IssuerReaction) string {
	if v := r.CapChange.val(); v.OK && r.Pct.Value != nil {
		return "≈ " + num(v.V, 1, true) + " " + bnLabel(r.CapChange.Unit)
	}
	if r.HasShares && !r.WaitingClose.IsZero() {
		return "ждём close " + r.WaitingClose.Format("01-02")
	}
	return dash
}

func bnLabel(unit string) string {
	if unit == unitEURBn {
		return "млрд €"
	}
	return "млрд $"
}

func currencySign(unit string) string {
	if unit == unitEUR || unit == unitEURBn {
		return "€"
	}
	return "$"
}

// issuerSession is "до открытия" or "после закрытия".
func issuerSession(s string) string {
	if s == config.SessionAfterClose {
		return "после закрытия"
	}
	return "до открытия"
}

// amountCell is "36.8 (+7.7%)": billions with YoY.
func amountCell(v, yoy Quantity) string {
	if v.Value == nil {
		return dash
	}
	s := num(*v.Value, 2, false)
	if yoy.Value != nil {
		s += " (" + signedPct1(yoy.val()) + ")"
	}
	return s
}

// amountOnly is billions without a change.
func amountOnly(v Quantity) string { return amountCell(v, Quantity{}) }

// qPct is a signed percent of a Quantity: "+2.3%".
func qPct(q Quantity) string { return signedPct1(q.val()) }

// qSigned is a signed number with one decimal: "+1.2".
func qSigned(q Quantity) string {
	if q.Value == nil {
		return dash
	}
	return num(*q.Value, 1, true)
}

// moneyValueCell is "6.10 €".
func moneyValueCell(q Quantity) string {
	if q.Value == nil {
		return dash
	}
	return num(*q.Value, 2, false) + " " + currencySign(q.Unit)
}

func itoa(n int) string { return strconv.Itoa(n) }
