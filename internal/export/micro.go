package export

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/guidance"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/irtranscript"
	"macro-collector/internal/sources/nasdaq"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// USA Micro (examples/usa_micro_current_week.md). Numbers come from the
// stored XBRL facts and Yahoo closes through transform; texts from the
// stored press releases. Everything is as of the period end: facts filed,
// reports accepted and closes after it are left out, so a past week renders
// the same later.

const (
	// Days after a quarter end by which the 10-Q (10-K for a fiscal Q4) is
	// due for large filers.
	tenQDays = 45
	tenKDays = 60
)

// microCutSteps are the Outlook lengths tried, in turn, when the file is
// over the size limit after dropping the quotes (§12.6).
var microCutSteps = []int{1500, 800, 400}

// company is one company's data as of the period end.
type company struct {
	cfg                config.Company
	fyEnd              string
	revenue, eps, capx []transform.Quarter
	reports            []model.CompanyReport // newest first
	closes             []model.Observation
	fin                transform.Financials // latest quarter with a 10-Q/10-K (section 6)
	hasFin             bool
	in                 transform.FinancialsInput
	inst               []model.CompanyFact // XBRL instance facts: segments, RPO total (phase 5d)
	rpoInstance        bool                // RPO from the instance, not company facts (AMZN)
}

// microReport is a report of the week with its texts.
type microReport struct {
	c        *company
	r        model.CompanyReport
	doc      model.Document
	hasDoc   bool
	outlook  []string
	prev     []string // Outlook of the previous report
	prevRep  model.CompanyReport
	quotes   []string
	call     model.Document // call transcript (phase 5b)
	callOut  []string       // its Outlook paragraphs; empty without one
	callSrc  bool           // the company has a transcript source configured
	cut      int            // Outlook cut to this many runes; 0 = whole
	noQuotes bool
}

// microSignal is a macro signal of the week (section 5).
type microSignal struct {
	ticker, topic, sentence, source string
}

func (w *week) micro(ctx context.Context) (string, error) {
	cs, err := w.companies(ctx)
	if err != nil {
		return "", err
	}
	var week []*microReport
	for _, c := range cs {
		for _, r := range c.reports {
			if r.AcceptedAt.Before(w.from) || !r.AcceptedAt.Before(w.to) {
				continue
			}
			mr, err := w.microReport(ctx, c, r)
			if err != nil {
				return "", err
			}
			week = append(week, mr)
		}
	}
	slices.SortFunc(week, func(a, b *microReport) int { return a.r.AcceptedAt.Compare(b.r.AcceptedAt) })
	season, err := w.season(ctx, cs)
	if err != nil {
		return "", err
	}
	signals := w.signals(week)
	limit := w.cfg.Export.MaxFileChars
	render := func() (string, error) { return w.render("micro", w.microModel(cs, week, season, signals)) }
	fits := func() (bool, error) {
		body, err := render()
		return err == nil && (limit <= 0 || len([]rune(body)) <= limit), err
	}
	if ok, err := fits(); ok || err != nil {
		return w.finishMicro(cs, week, season, signals, err)
	}
	signals = nil // macro signals go first (ТЗ §19.4)
	if ok, err := fits(); ok || err != nil {
		return w.finishMicro(cs, week, season, signals, err)
	}
	for _, m := range week {
		m.noQuotes = true
	}
	for _, n := range microCutSteps {
		if ok, err := fits(); ok || err != nil {
			return w.finishMicro(cs, week, season, signals, err)
		}
		for _, m := range week {
			m.cut = n
		}
	}
	body, err := render()
	if err == nil && len([]rune(body)) > limit {
		w.logWarn("USA Micro still over the size limit after cutting", "chars", len([]rune(body)), "limit", limit)
	}
	return w.finishMicro(cs, week, season, signals, err)
}

// finishMicro renders the file with the cuts decided.
func (w *week) finishMicro(cs []*company, week []*microReport, sv seasonView, signals []microSignal, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return w.report(w.path(), config.ReportMicro, w.microModel(cs, week, sv, signals))
}

// companies loads every EDGAR company as of the period end.
func (w *week) companies(ctx context.Context) ([]*company, error) {
	var out []*company
	approved, err := w.st.ConfirmedGuidance(ctx)
	if err != nil {
		return nil, err
	}
	w.guidance = guidanceAsOf(w.cfg.EDGAR.CapexGuidance, approved, w.asOf)
	o, _ := w.region.Owner(config.ReportMicro)
	for _, cc := range w.cfg.EDGAR.Companies {
		if o.Agent == nil || !slices.Contains(o.Agent.Companies, cc.Ticker) {
			continue
		}
		facts, err := w.st.CompanyFacts(ctx, cc.Ticker)
		if err != nil {
			return nil, err
		}
		facts = slices.DeleteFunc(facts, func(f model.CompanyFact) bool { return f.Filed.After(w.asOf) })
		reports, err := w.st.CompanyReportsOf(ctx, cc.Ticker)
		if err != nil {
			return nil, err
		}
		reports = slices.DeleteFunc(reports, func(r model.CompanyReport) bool { return !r.AcceptedAt.Before(w.to) })
		closes, err := w.observations(ctx, "yahoo", cc.Ticker, w.cfg.EDGAR.History)
		if err != nil {
			return nil, err
		}
		closes = slices.DeleteFunc(closes, func(o model.Observation) bool { return o.Date.After(w.asOf) })
		c := &company{
			cfg: cc, fyEnd: transform.FiscalYearEnd(facts), reports: reports, closes: closes,
			revenue: transform.Quarters(facts, cc.Resolved.Revenue, "USD", false),
			eps:     transform.Quarters(facts, cc.Resolved.EPS, "USD/shares", true),
			capx:    transform.Quarters(facts, cc.Resolved.Capex, "USD", false),
		}
		inst, err := w.st.InstanceFacts(ctx, cc.Ticker)
		if err != nil {
			return nil, err
		}
		c.inst = slices.DeleteFunc(inst, func(f model.CompanyFact) bool { return f.Filed.After(w.asOf) })
		c.in = financialsInput(facts, c, w.cfg.EDGAR.OneOff)
		// RPO filed only with the expected-timing axis (AMZN) is not in
		// company facts; its instance total stands in (ТЗ §20.2a).
		if len(c.in.Revenue) > 0 && !recentPoint(c.in.RPO, c.in.Revenue[len(c.in.Revenue)-1].End) {
			if ps := transform.Instants(slices.DeleteFunc(slices.Clone(c.inst), func(f model.CompanyFact) bool { return f.Member != "" }),
				[]string{edgar.RPOConcept}, "USD"); recentPoint(ps, c.in.Revenue[len(c.in.Revenue)-1].End) {
				c.in.RPO, c.rpoInstance = ps, true
			}
		}
		c.fin, c.hasFin = transform.LatestFinancials(c.in)
		out = append(out, c)
	}
	return out, nil
}

// financialsInput gathers a company's series for section 6 (ТЗ §18, §19).
func financialsInput(facts []model.CompanyFact, c *company, oneOff config.OneOff) transform.FinancialsInput {
	r := c.cfg.Resolved
	flow := func(tags []string) []transform.Quarter { return transform.Quarters(facts, tags, "USD", false) }
	balance := func(tags []string) []transform.Point { return transform.Instants(facts, tags, "USD") }
	return transform.FinancialsInput{
		Revenue: c.revenue, Capex: c.capx,
		GrossProfit: flow(r.GrossProfit), CostOfRevenue: flow(r.CostOfRevenue),
		OperatingIncome: flow(r.OperatingIncome), NetIncome: flow(r.NetIncome),
		DA:                transform.ComponentQuarters(facts, r.DA, "USD"),
		OperatingCashFlow: flow(r.OperatingCashFlow),
		Buybacks:          flow(r.Buybacks), Dividends: flow(r.Dividends),
		DilutedShares: transform.DirectQuarters(facts, r.DilutedShares, "shares"),
		Cash:          balance(r.Cash), ShortTermInvestments: balance(r.ShortTermInvestments),
		Debt:         transform.ComponentInstants(facts, r.Debt, "USD"),
		NoBuybacks:   slices.Contains(c.cfg.NotPaid, config.NotPaidBuybacks),
		NoDividends:  slices.Contains(c.cfg.NotPaid, config.NotPaidDividends),
		PretaxIncome: flow(r.PretaxIncome), IncomeTax: flow(r.IncomeTax), RPO: balance(r.RPO),
		OneOffRule: transform.OneOffRule{RateChangePP: oneOff.RateChangePP, OtherShareChangePP: oneOff.OtherShareChangePP, MaterialityPct: oneOff.MaterialityPct},
	}
}

// recentPoint reports whether ps has a value within a year before end.
func recentPoint(ps []transform.Point, end time.Time) bool {
	return slices.ContainsFunc(ps, func(p transform.Point) bool {
		return !p.Date.After(end) && end.Sub(p.Date) < 366*24*time.Hour
	})
}

// signals picks the macro signals of the week's press releases and call
// transcripts (ТЗ §19.4): per company, strong phrases first, within
// per_company and per_company_topic; the Q&A of a call only with a number
// or a strong phrase. Then companies (newest report first) take turns up
// to max and the result is grouped by company.
func (w *week) signals(week []*microReport) []microSignal {
	m := w.cfg.EDGAR.MacroSignals
	if !m.Enabled {
		return nil
	}
	rules := edgar.SignalRules{Exclude: m.Excluded, Strong: m.Strongs, MinChars: m.MinChars, MaxChars: m.MaxChars,
		PerTopic: m.PerCompanyTopic, PerCompany: m.PerCompany}
	for _, t := range m.Topics {
		rules.Topics = append(rules.Topics, edgar.Topic{Name: t.Name, Match: t.Match})
	}
	var order []string
	pickers := map[string]*edgar.SignalPicker{}
	for i := len(week) - 1; i >= 0; i-- { // newest first
		r := week[i]
		t := r.c.cfg.Ticker
		if pickers[t] == nil {
			pickers[t] = edgar.NewSignalPicker(rules)
			order = append(order, t)
		}
		day := r.r.AcceptedAt.In(w.et).Format("01-02")
		if r.hasDoc {
			pickers[t].Add(r.doc.Body, r.r.Form+" "+day, false)
		}
		if body := r.call.Body; body != "" {
			qa := irtranscript.QAStart(body)
			pickers[t].Add(body[:qa], "звонок "+day, false)
			pickers[t].Add(body[qa:], "звонок "+day+", Q&A", true)
		}
	}
	byTicker := map[string][]microSignal{}
	for _, t := range order {
		for _, sg := range pickers[t].Signals() {
			byTicker[t] = append(byTicker[t], microSignal{ticker: t, topic: sg.Topic, sentence: sg.Sentence, source: sg.Source})
		}
	}
	taken := map[string]int{}
	for total, more := 0, true; more && total < m.Max; {
		more = false
		for _, t := range order {
			if taken[t] < len(byTicker[t]) && total < m.Max {
				taken[t]++
				total++
				more = true
			}
		}
	}
	var out []microSignal
	for _, t := range order {
		out = append(out, byTicker[t][:taken[t]]...)
	}
	return out
}

func (w *week) microReport(ctx context.Context, c *company, r model.CompanyReport) (*microReport, error) {
	m := &microReport{c: c, r: r}
	doc, ok, err := w.st.Document(ctx, edgar.Source, r.Accession)
	if err != nil {
		return nil, err
	}
	if ok {
		m.doc, m.hasDoc = doc, true
		m.outlook, m.quotes = edgar.Outlook(doc.Body), edgar.Quotes(doc.Body)
	}
	_, m.callSrc = w.cfg.EDGAR.Transcripts.Transcript(c.cfg.Ticker)
	call, ok, err := w.st.Document(ctx, irtranscript.Source, r.Accession)
	if err != nil {
		return nil, err
	}
	if ok && !call.FetchedAt.After(w.asOf) {
		m.call = call
		for _, p := range irtranscript.Outlook(call.Body) {
			m.callOut = append(m.callOut, "• "+p)
		}
	}
	for _, p := range c.reports { // newest first: the first older one
		if p.AcceptedAt.Before(r.AcceptedAt) {
			m.prevRep = p
			if d, ok, err := w.st.Document(ctx, edgar.Source, p.Accession); err != nil {
				return nil, err
			} else if ok {
				m.prev = edgar.Outlook(d.Body)
			}
			break
		}
	}
	return m, nil
}

// quarterLabel is "Q3 2026" for a calendar fiscal year, "FQ1 FY27" otherwise.
func (c *company) quarterLabel(end time.Time) string {
	fyEnd := c.fyEnd
	if fyEnd == "" {
		fyEnd = "1231"
	}
	fy, q, err := transform.FiscalQuarter(end, fyEnd)
	if err != nil {
		return dash
	}
	if strings.HasPrefix(fyEnd, "12") {
		return fmt.Sprintf("Q%d %d", q, fy)
	}
	return fmt.Sprintf("FQ%d FY%02d", q, fy%100)
}

var sessionNames = map[string]string{
	transform.SessionAfterClose: "после закрытия", transform.SessionBeforeOpen: "до открытия", transform.SessionIntraday: "во время торгов",
}

// bnQty is an amount in dollars as billions.
func bnQty(v transform.Value) Quantity {
	if !v.OK {
		return qty(v, unitUSDBn)
	}
	return qty(some(v.V/1e9), unitUSDBn)
}

// moneyCell is "12.3 млрд $".
func moneyCell(q Quantity) string {
	v := q.val()
	if !v.OK {
		return dash
	}
	return num(v.V, 1, false) + " млрд $"
}

// epsCell is the EPS of a quarter, ≈ for a derived fiscal Q4.
func epsCell(q Quantity, approx bool) string {
	v := q.val()
	if !v.OK {
		return dash
	}
	s := num(v.V, 2, false) + " $"
	if approx {
		s = "≈" + s
	}
	return s
}

// MicroReaction is the price reaction to a report and the market value
// change on it (ТЗ §19.5); WaitingClose is set before the reacting session
// has closed.
type MicroReaction struct {
	Pct          Quantity `json:"pct"`
	Date         Day      `json:"date"`
	WaitingClose Day      `json:"waiting_close"`
	CapChange    Quantity `json:"cap_change"`
}

// reaction computes the reaction to a report. Waiting is compared with the
// export time, not the period end: a mid-week export runs before the
// reacting session has closed.
func (w *week) reaction(c *company, r model.CompanyReport) (MicroReaction, transform.Value) {
	out := MicroReaction{Pct: qty(transform.Value{}, unitPercent), CapChange: qty(transform.Value{}, unitUSDBn)}
	re := transform.PriceReaction(c.closes, r.ReportDate, r.Session)
	if !re.Pct.OK {
		if !re.Waiting.IsZero() && !re.Waiting.Before(model.DayIn(w.now, w.et)) {
			out.WaitingClose = Day{re.Waiting}
		}
		return out, transform.Value{}
	}
	change := transform.MarketCapChange(re, c.shares(r.PeriodEnd))
	out.Pct, out.Date, out.CapChange = qty(re.Pct, unitPercent), Day{re.Date}, bnQty(change)
	return out, change
}

// reactionWaiting is "ждём close 11-03", empty when not waiting.
func reactionWaiting(r MicroReaction) string {
	if r.WaitingClose.IsZero() {
		return ""
	}
	return "ждём close " + r.WaitingClose.Format("01-02")
}

// microReactionCell is "+2.1% (10-30)" or what stands in for it.
func microReactionCell(r MicroReaction) string {
	if r.Pct.Value != nil {
		return signedPct1(r.Pct.val()) + " (" + r.Date.Format("01-02") + ")"
	}
	if s := reactionWaiting(r); s != "" {
		return s
	}
	return dash
}

// capChangeCell is "≈ +42 млрд $" or what stands in for it.
func capChangeCell(r MicroReaction) string {
	if r.Pct.Value == nil {
		if s := reactionWaiting(r); s != "" {
			return s
		}
		return dash
	}
	if v := r.CapChange.val(); v.OK {
		return "≈ " + num(v.V, 0, true) + " млрд $"
	}
	return dash
}

// shares is the latest diluted weighted average share count of a quarter
// ending on or before end, 0 without one.
func (c *company) shares(end time.Time) float64 {
	var v float64
	for _, q := range c.in.DilutedShares {
		if !q.End.After(model.Day(end)) {
			v = q.Value
		}
	}
	return v
}

// reportRow is the row of a report in sections 1 and 3: revenue and EPS of
// its quarter, once the 10-Q/10-K has brought the facts.
type reportRow struct {
	rev, eps       transform.Quarter
	hasRev, hasEPS bool
	revYoY, epsYoY transform.Value
	form           string
	oneOff         transform.OneOffCause // ⚠ (ТЗ §19.1)
}

func (c *company) row(r model.CompanyReport) reportRow {
	var x reportRow
	x.rev, x.hasRev = transform.QuarterAt(c.revenue, r.PeriodEnd)
	x.eps, x.hasEPS = transform.QuarterAt(c.eps, r.PeriodEnd)
	if x.hasRev {
		x.revYoY, x.form = transform.QuarterYoY(c.revenue, r.PeriodEnd), x.rev.Form
	}
	if x.hasEPS {
		x.epsYoY = transform.QuarterYoY(c.eps, r.PeriodEnd)
		x.oneOff = transform.OneOffAt(c.in, r.PeriodEnd)
		if x.form == "" {
			x.form = x.eps.Form
		}
	}
	return x
}

// oneOffText is the cause of a one-off item, empty without one.
func oneOffText(c transform.OneOffCause) string {
	if !c.Any() {
		return ""
	}
	return c.String()
}

// epsYoYCell is the EPS change with ≈ for a fiscal Q4 and ⚠ for a one-off.
func epsYoYCell(q Quantity, approx bool, oneOff string) string {
	v := q.val()
	s := signedPct1(v)
	if approx && v.OK {
		s = "≈" + s
	}
	if oneOff != "" && v.OK {
		s += " ⚠ " + oneOff
	}
	return s
}

// seasonRow is one company in section 3.
type seasonRow struct {
	c        *company
	reported bool
	r        model.CompanyReport
	expected time.Time // expected date of a company yet to report
	note     string    // about the expected date: "estimate", "after_close", "before_open"
	sortKey  time.Time // accepted or expected; zero = unknown, last
	quarter  string
}

type seasonView struct {
	quarter transform.CalendarQuarter
	rows    []seasonRow
}

// season is the reporting season of the calendar quarter before the period
// end: reports filed in the quarter after it (Q3 reports come Oct–Dec).
func (w *week) season(ctx context.Context, cs []*company) (seasonView, error) {
	sq := transform.CalendarQuarterOf(w.asOf.AddDate(0, 0, 15)).Prev(1)
	start := time.Date(sq.Year, time.Month(sq.Q*3+1), 1, 0, 0, 0, 0, time.UTC) // first day after the quarter
	end := start.AddDate(0, 3, 0)
	evs, err := w.releasesBetween(ctx, w.to, end.AddDate(0, 0, 1))
	if err != nil {
		return seasonView{}, err
	}
	sv := seasonView{quarter: sq}
	for _, c := range cs {
		row := seasonRow{c: c}
		for _, r := range c.reports {
			if !r.ReportDate.Before(start) && r.ReportDate.Before(end) {
				row.reported, row.r, row.sortKey = true, r, r.AcceptedAt
				row.quarter = c.quarterLabel(r.PeriodEnd)
				break
			}
		}
		if !row.reported {
			if len(c.reports) > 0 {
				row.quarter = c.quarterLabel(transform.NextQuarterEnd(c.reports[0].PeriodEnd))
			} else {
				row.quarter = dash
			}
			for _, e := range evs {
				if e.Source != nasdaq.Source || e.Related.Ticker != c.cfg.Ticker || e.Status == model.StatusRescheduled {
					continue
				}
				row.sortKey, row.expected = e.ScheduledAt, e.ScheduledAt.In(w.et)
				switch {
				case e.Related.Estimated:
					row.note = "estimate"
				case e.Related.ReportTime == nasdaq.TimeAfterClose:
					row.note = "after_close"
				case e.Related.ReportTime == nasdaq.TimeBeforeOpen:
					row.note = "before_open"
				}
				break
			}
		}
		sv.rows = append(sv.rows, row)
	}
	sort.SliceStable(sv.rows, func(i, j int) bool {
		a, b := sv.rows[i], sv.rows[j]
		if a.reported != b.reported {
			return a.reported
		}
		if a.sortKey.IsZero() != b.sortKey.IsZero() {
			return !a.sortKey.IsZero()
		}
		return a.sortKey.Before(b.sortKey)
	})
	return sv, nil
}

// MicroWeekRow is a report of the week, its time in ET.
type MicroWeekRow struct {
	Ticker     string        `json:"ticker"`
	Quarter    string        `json:"quarter"`
	PeriodEnd  Day           `json:"period_end"`
	ReportAt   time.Time     `json:"report_at"`
	Session    string        `json:"session"`
	Revenue    Quantity      `json:"revenue"`
	RevenueYoY Quantity      `json:"revenue_yoy"`
	EPS        Quantity      `json:"eps"`
	EPSApprox  bool          `json:"eps_approx"`
	EPSYoY     Quantity      `json:"eps_yoy"`
	OneOff     string        `json:"one_off"`
	Reaction   MicroReaction `json:"reaction"`
	Form       string        `json:"form"`
}

// SeasonRow is a company in the reporting season: reported, or expected on
// Expected (zero: unknown), with ExpectedNote.
type SeasonRow struct {
	Ticker       string        `json:"ticker"`
	Reported     bool          `json:"reported"`
	ReportAt     *time.Time    `json:"report_at"`
	Session      string        `json:"session"`
	Expected     Day           `json:"expected"`
	ExpectedNote string        `json:"expected_note"`
	Quarter      string        `json:"quarter"`
	RevenueYoY   Quantity      `json:"revenue_yoy"`
	EPSApprox    bool          `json:"eps_approx"`
	EPSYoY       Quantity      `json:"eps_yoy"`
	OneOff       string        `json:"one_off"`
	Reaction     MicroReaction `json:"reaction"`
}

// SeasonSection is section 3; CapSum and CapCount are over the reported
// companies with a market value change.
type SeasonSection struct {
	Quarter  string      `json:"quarter"`
	Reported int         `json:"reported"`
	Median   Quantity    `json:"median"`
	Rows     []SeasonRow `json:"rows"`
	CapSum   Quantity    `json:"cap_sum"`
	CapCount int         `json:"cap_count"`
}

// MicroReport is usa/micro/current_week.md.
type MicroReport struct {
	Header               Header          `json:"header"`
	Tickers              []string        `json:"tickers"`
	OneOffRateChangePP   float64         `json:"one_off_rate_change_pp"`
	OneOffMaterialityPct float64         `json:"one_off_materiality_pct"`
	Week                 []MicroWeekRow  `json:"week"`
	Capex                CapexSection    `json:"capex"`
	CapexGuidance        []CapexNote     `json:"capex_guidance"`
	Season               SeasonSection   `json:"season"`
	Guidance             []GuidanceItem  `json:"guidance"`
	Signals              []MicroSignal   `json:"signals"`
	Financials           MicroFinancials `json:"financials"`
	Missing              MicroMissing    `json:"missing"`
}

func microReportAt(m model.CompanyReport, et *time.Location) time.Time { return m.AcceptedAt.In(et) }

func (w *week) microWeekRow(m *microReport) MicroWeekRow {
	x := m.c.row(m.r)
	reaction, _ := w.reaction(m.c, m.r)
	return MicroWeekRow{Ticker: m.c.cfg.Ticker, Quarter: m.c.quarterLabel(m.r.PeriodEnd), PeriodEnd: Day{m.r.PeriodEnd}, ReportAt: microReportAt(m.r, w.et), Session: m.r.Session,
		Revenue: bnQty(valueOf(x.rev, x.hasRev)), RevenueYoY: qty(x.revYoY, unitPercent), EPS: qty(valueOf(x.eps, x.hasEPS), unitUSD), EPSApprox: x.hasEPS && x.eps.Approx,
		EPSYoY: qty(x.epsYoY, unitPercent), OneOff: oneOffText(x.oneOff), Reaction: reaction, Form: x.form}
}

func (w *week) seasonSection(sv seasonView) SeasonSection {
	out := SeasonSection{Quarter: sv.quarter.String(), Rows: []SeasonRow{}, Median: qty(transform.Value{}, unitPercent)}
	var reactions []float64
	var capSum float64
	for _, s := range sv.rows {
		row := SeasonRow{Ticker: s.c.cfg.Ticker, Reported: s.reported, Expected: Day{s.expected}, ExpectedNote: s.note, Quarter: s.quarter,
			RevenueYoY: qty(transform.Value{}, unitPercent), EPSYoY: qty(transform.Value{}, unitPercent),
			Reaction: MicroReaction{Pct: qty(transform.Value{}, unitPercent), CapChange: qty(transform.Value{}, unitUSDBn)}}
		if !s.reported {
			out.Rows = append(out.Rows, row)
			continue
		}
		out.Reported++
		x := s.c.row(s.r)
		if re := transform.PriceReaction(s.c.closes, s.r.ReportDate, s.r.Session); re.Pct.OK {
			reactions = append(reactions, re.Pct.V)
		}
		reaction, change := w.reaction(s.c, s.r)
		if change.OK {
			capSum += change.V
			out.CapCount++
		}
		at := microReportAt(s.r, w.et)
		row.ReportAt, row.Session, row.RevenueYoY, row.EPSApprox, row.EPSYoY, row.OneOff, row.Reaction = &at, s.r.Session,
			qty(x.revYoY, unitPercent), x.hasEPS && x.eps.Approx, qty(x.epsYoY, unitPercent), oneOffText(x.oneOff), reaction
		out.Rows = append(out.Rows, row)
	}
	if len(reactions) > 0 {
		out.Median = qty(some(medianOf(reactions)), unitPercent)
	}
	out.CapSum = qty(some(capSum/1e9), unitUSDBn)
	return out
}

// reportTimeCell is "2026-10-29 16:05, после закрытия".
func reportTimeCell(at time.Time, session string) string {
	return at.Format("2006-01-02 15:04") + ", " + sessionNames[session]
}

// seasonWhen is the report time or the expected date.
func seasonWhen(r SeasonRow) string {
	if r.Reported {
		return reportTimeCell(*r.ReportAt, r.Session)
	}
	if r.Expected.IsZero() {
		return dash
	}
	s := r.Expected.Format(time.DateOnly)
	switch r.ExpectedNote {
	case "estimate":
		s += " (оценка)"
	case "after_close":
		s += ", после закрытия"
	case "before_open":
		s += ", до открытия"
	}
	return s
}

func (w *week) microModel(cs []*company, week []*microReport, sv seasonView, signals []microSignal) *MicroReport {
	m := &MicroReport{
		Header:             w.newHeader("", w.title(), []string{"edgar", "calendar:earnings", "yahoo"}, nil, w.from, w.to),
		Tickers:            make([]string, len(cs)),
		OneOffRateChangePP: w.cfg.EDGAR.OneOff.RateChangePP, OneOffMaterialityPct: w.cfg.EDGAR.OneOff.MaterialityPct,
		Week: []MicroWeekRow{}, Season: w.seasonSection(sv),
	}
	for i, c := range cs {
		m.Tickers[i] = c.cfg.Ticker
	}
	for _, r := range week {
		m.Week = append(m.Week, w.microWeekRow(r))
	}
	m.Capex = w.capexSection(cs)
	m.CapexGuidance = w.capexGuidance(cs)
	m.Guidance = []GuidanceItem{}
	for i := len(week) - 1; i >= 0; i-- { // newest first
		m.Guidance = append(m.Guidance, w.guidanceItem(week[i]))
	}
	m.Signals = []MicroSignal{}
	for _, sg := range signals {
		m.Signals = append(m.Signals, MicroSignal{Ticker: sg.ticker, Topic: sg.topic, Sentence: sg.sentence, Source: sg.source})
	}
	m.Financials = w.microFinancials(cs)
	m.Missing = w.microMissing(cs)
	return m
}

func valueOf(q transform.Quarter, ok bool) transform.Value {
	if !ok {
		return transform.Value{}
	}
	return some(q.Value)
}

func medianOf(vs []float64) float64 {
	s := slices.Clone(vs)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// CapexStale says a capex guidance in the config is out of date: a report
// came after it.
type CapexStale struct {
	// Approved is the candidate id of a guidance approved in the database
	// (phase 14b); 0: config.yaml.
	Approved int64   `json:"approved,omitempty"`
	Ticker   string  `json:"ticker"`
	Form     string  `json:"form"`
	Report   Day     `json:"report"`
	Year     int     `json:"year"`
	Low      float64 `json:"low"`
	High     float64 `json:"high"`
	Given    string  `json:"given"`
}

// CapexRest is what the full-year guidance leaves for the rest of the year.
// Quarter is the last reported calendar quarter.
type CapexRest struct {
	Approved     int64    `json:"approved,omitempty"` // as in CapexStale
	Ticker       string   `json:"ticker"`
	Year         int      `json:"year"`
	Quarter      int      `json:"quarter"`
	RestLow      Quantity `json:"rest_low"`
	RestHigh     Quantity `json:"rest_high"`
	GuidanceLow  float64  `json:"guidance_low"`
	GuidanceHigh float64  `json:"guidance_high"`
	YearToDate   Quantity `json:"year_to_date"`
	LastQuarter  Quantity `json:"last_quarter"`
	Source       string   `json:"source"`
}

// CapexNote is a line under the capex table: one of the two is set.
type CapexNote struct {
	Stale *CapexStale `json:"stale"`
	Rest  *CapexRest  `json:"rest"`
}

// guidanceAsOf is the guidance in force at asOf: config.yaml and the
// approved candidates given by then, the newer given winning (phase 14b;
// a candidate that is not approved is never read).
func guidanceAsOf(cfg []config.CapexGuidance, approved []store.ConfirmedGuidance, asOf time.Time) []guidance.Value {
	cfg = slices.DeleteFunc(slices.Clone(cfg), func(g config.CapexGuidance) bool { return g.GivenAt.After(asOf) })
	approved = slices.DeleteFunc(slices.Clone(approved), func(g store.ConfirmedGuidance) bool { return g.Given.After(asOf) })
	return guidance.Effective(cfg, approved)
}

// capexGuidance is what the full-year guidance leaves for the rest of the
// year — only for companies whose guidance is defined as our capex (ТЗ
// §19.3a; entered by hand in config.yaml or approved from a candidate).
// An earnings 8-K after the guidance date makes it stale: a warning asks
// to update config.yaml or to check the candidates.
func (w *week) capexGuidance(cs []*company) []CapexNote {
	out := []CapexNote{}
	for _, g := range w.guidance {
		i := slices.IndexFunc(cs, func(c *company) bool { return c.cfg.Ticker == g.Ticker })
		if i < 0 {
			continue
		}
		c := cs[i]
		for _, r := range c.reports { // newest first, accepted before the period end
			if day := model.DayIn(r.AcceptedAt, w.et); day.After(g.Given) {
				out = append(out, CapexNote{Stale: &CapexStale{Approved: g.CandidateID, Ticker: g.Ticker, Form: r.Form, Report: Day{day}, Year: g.Year,
					Low: g.Low, High: g.High, Given: g.Given.Format(time.DateOnly)}})
				w.logWarn("capex guidance is stale: update edgar.capex_guidance", "ticker", g.Ticker, "given", g.Given, "report", day.Format(time.DateOnly))
				break
			}
		}
		var last transform.Quarter
		found := false
		for _, q := range c.capx {
			if CalendarYear(q) == g.Year {
				last, found = q, true
			}
		}
		if !found {
			continue
		}
		q := transform.CalendarQuarterOf(last.End)
		if q.Q == 4 {
			continue // the year is closed
		}
		ytd := transform.YearToDate(c.capx, q)
		lo, hi := transform.GuidanceRest(g.Low*1e9, g.High*1e9, ytd)
		if !lo.OK {
			continue
		}
		out = append(out, CapexNote{Rest: &CapexRest{Approved: g.CandidateID, Ticker: g.Ticker, Year: g.Year, Quarter: q.Q, RestLow: bnQty(lo), RestHigh: bnQty(hi), GuidanceLow: g.Low, GuidanceHigh: g.High,
			YearToDate: bnQty(ytd), LastQuarter: bnQty(some(last.Value)), Source: g.Source}})
	}
	return out
}

// capexNoteLine writes a line under the capex table.
func capexNoteLine(n CapexNote) string {
	if s := n.Stale; s != nil && s.Approved != 0 {
		return fmt.Sprintf("%s: ⚠ guidance устарел: после него вышел отчёт %s %s; подтверждён #%d — %d год, %s–%s млрд $, от %s. Проверь кандидатов: macroctl guidance list.",
			s.Ticker, s.Form, s.Report.Format(time.DateOnly), s.Approved, s.Year, num(s.Low, 0, false), num(s.High, 0, false), s.Given)
	}
	if s := n.Stale; s != nil {
		return fmt.Sprintf("%s: ⚠ guidance устарел, обнови в config.yaml (edgar.capex_guidance): после него вышел отчёт %s %s; в конфиге — %d год, %s–%s млрд $, от %s.",
			s.Ticker, s.Form, s.Report.Format(time.DateOnly), s.Year, num(s.Low, 0, false), num(s.High, 0, false), s.Given)
	}
	r := n.Rest
	rest := fmt.Sprintf("implied Q4 %d", r.Year)
	if r.Quarter < 3 {
		rest = fmt.Sprintf("остаток %d года (Q%d–Q4)", r.Year, r.Quarter+1)
	}
	origin := "Guidance внесён вручную (" + r.Source + ")"
	if r.Approved != 0 {
		origin = fmt.Sprintf("Guidance подтверждён пользователем (#%d, %s)", r.Approved, r.Source)
	}
	return fmt.Sprintf("%s: %s ≈ %s–%s млрд $ (guidance на %d год %s–%s − %s за Q1–Q%d; в Q%d — %s). %s; определение совпадает с нашим capex.",
		r.Ticker, rest, num(r.RestLow.val().V, 1, false), num(r.RestHigh.val().V, 1, false), r.Year, num(r.GuidanceLow, 0, false), num(r.GuidanceHigh, 0, false),
		num(r.YearToDate.val().V, 1, false), r.Quarter, r.Quarter, num(r.LastQuarter.val().V, 1, false), origin)
}

// CalendarYear is the calendar year of a quarter (AAPL's June quarter ends
// in June).
func CalendarYear(q transform.Quarter) int { return transform.CalendarQuarterOf(q.End).Year }

// bnCell is a Quantity in billions with one decimal, unit in the header.
func bnCell(q Quantity) string {
	v := q.val()
	if !v.OK {
		return dash
	}
	return num(v.V, 1, false)
}

// bnSigned is an amount in billions with a sign.
func bnSigned(q Quantity) string {
	v := q.val()
	if !v.OK {
		return dash
	}
	return num(v.V, 1, true)
}

// marginCell is "68.4% (−0.6 п.п.)": the margin and its change in a year.
func marginCell(v, change Quantity, mark string) string {
	if v.Value == nil {
		return dash
	}
	s := pct1(v.val()) + mark
	if change.Value != nil {
		s += " (" + pp(change.val()) + " п.п.)"
	}
	return s
}

// MicroSignal is a macro signal of the week (section 5, ТЗ §19.4).
type MicroSignal struct {
	Ticker   string `json:"ticker"`
	Topic    string `json:"topic"`
	Sentence string `json:"sentence"`
	Source   string `json:"source"`
}

// pairCell is "−8.2 (+12.8)": a quarter and the same quarter a year earlier.
func pairCell(cur, ago Quantity) string {
	if cur.Value == nil {
		return dash
	}
	s := bnCell(cur)
	if ago.Value != nil {
		s += " (" + bnCell(ago) + ")"
	}
	return s
}

// FinancialRow is a company's latest quarter (ТЗ §18.3, §19): null values
// without a 10-Q/10-K (HasData false).
type FinancialRow struct {
	Ticker             string   `json:"ticker"`
	HasData            bool     `json:"has_data"`
	Quarter            string   `json:"quarter"`
	End                Day      `json:"end"`
	Revenue            Quantity `json:"revenue"`
	RevenueYoY         Quantity `json:"revenue_yoy"`
	GrossMargin        Quantity `json:"gross_margin"`
	GrossMarginChange  Quantity `json:"gross_margin_change"`
	GrossComputed      bool     `json:"gross_computed"`
	GrossNotShown      bool     `json:"gross_not_shown"`
	OperatingIncome    Quantity `json:"operating_income"`
	OperatingIncomeYoY Quantity `json:"operating_income_yoy"`
	OperatingMargin    Quantity `json:"operating_margin"`
	OperatingMarginChg Quantity `json:"operating_margin_change"`
	EBITDA             Quantity `json:"ebitda"`
	EBITDAYoY          Quantity `json:"ebitda_yoy"`
	Other              Quantity `json:"other"`
	OtherYearAgo       Quantity `json:"other_year_ago"`
	Tax                Quantity `json:"tax"`
	TaxYearAgo         Quantity `json:"tax_year_ago"`
	NetIncome          Quantity `json:"net_income"`
	NetIncomeYoY       Quantity `json:"net_income_yoy"`
	OneOff             string   `json:"one_off"`
	OperatingCashFlow  Quantity `json:"operating_cash_flow"`
	Capex              Quantity `json:"capex"`
	FCF                Quantity `json:"fcf"`
	FCFMargin          Quantity `json:"fcf_margin"`
	FCFTTM             Quantity `json:"fcf_ttm"`
	FCFMarginTTM       Quantity `json:"fcf_margin_ttm"`
	Buybacks           Quantity `json:"buybacks"`
	NoBuybacks         bool     `json:"no_buybacks"`
	Dividends          Quantity `json:"dividends"`
	NoDividends        bool     `json:"no_dividends"`
	ReturnTTM          Quantity `json:"return_ttm"`
	ReturnShareOfFCF   Quantity `json:"return_share_of_fcf"`
	FCFNotPositive     bool     `json:"fcf_not_positive"`
	NetDebt            Quantity `json:"net_debt"`
	DilutedSharesYoY   Quantity `json:"diluted_shares_yoy"`
}

// RPORow is the backlog of a company at the end of its latest quarter.
type RPORow struct {
	Ticker       string   `json:"ticker"`
	Date         Day      `json:"date"`
	RPO          Quantity `json:"rpo"`
	RPOYoY       Quantity `json:"rpo_yoy"`
	FromInstance bool     `json:"from_instance"`
}

// SegmentRow is a row of table 6.4, by Kind: "segment", "no_data" (a
// company without segment data), "unallocated" (operating income less its
// segments').
type SegmentRow struct {
	Ticker     string   `json:"ticker"`
	Kind       string   `json:"kind"`
	Label      string   `json:"label"`
	Revenue    Quantity `json:"revenue"`
	RevenueYoY Quantity `json:"revenue_yoy"`
	Profit     Quantity `json:"profit"`
	Margin     Quantity `json:"margin"`
	Gross      bool     `json:"gross"`
}

// MicroFinancials is section 6; NoRPO are the companies filing no backlog.
type MicroFinancials struct {
	Rows     []FinancialRow `json:"rows"`
	RPO      []RPORow       `json:"rpo"`
	NoRPO    []string       `json:"no_rpo"`
	Segments []SegmentRow   `json:"segments"`
}

func financialRow(c *company) FinancialRow {
	r := FinancialRow{Ticker: c.cfg.Ticker, HasData: c.hasFin}
	f := c.fin
	r.Revenue, r.RevenueYoY = bnQty(f.Revenue), qty(f.RevenueYoY, unitPercent)
	r.GrossMargin, r.GrossMarginChange, r.GrossComputed = qty(f.GrossMargin, unitPercent), qty(f.GrossMarginChange, unitPP), f.GrossComputed
	r.OperatingIncome, r.OperatingIncomeYoY = bnQty(f.OperatingIncome), qty(f.OperatingIncomeYoY, unitPercent)
	r.OperatingMargin, r.OperatingMarginChg = qty(f.OperatingMargin, unitPercent), qty(f.OperatingMarginChange, unitPP)
	r.EBITDA, r.EBITDAYoY = bnQty(f.EBITDA), qty(f.EBITDAYoY, unitPercent)
	r.Other, r.OtherYearAgo, r.Tax, r.TaxYearAgo = bnQty(f.Other), bnQty(f.OtherYearAgo), bnQty(f.Tax), bnQty(f.TaxYearAgo)
	r.NetIncome, r.NetIncomeYoY, r.OneOff = bnQty(f.NetIncome), qty(f.NetIncomeYoY, unitPercent), oneOffText(f.OneOff)
	r.OperatingCashFlow, r.Capex, r.FCF, r.FCFMargin = bnQty(f.OperatingCashFlow), bnQty(f.Capex), bnQty(f.FCF), qty(f.FCFMargin, unitPercent)
	r.FCFTTM, r.FCFMarginTTM = bnQty(f.FCFTTM), qty(f.FCFMarginTTM, unitPercent)
	r.Buybacks, r.NoBuybacks, r.Dividends, r.NoDividends = bnQty(f.Buybacks), f.NoBuybacks, bnQty(f.Dividends), f.NoDividends
	r.ReturnTTM, r.ReturnShareOfFCF, r.FCFNotPositive = bnQty(f.ShareholderReturnTTM), qty(f.ReturnShareOfFCF, unitPercent), f.FCFNotPositive
	r.NetDebt, r.DilutedSharesYoY = bnQty(f.NetDebt), qty(f.DilutedSharesYoY, unitPercent)
	if c.hasFin {
		r.Quarter, r.End = c.quarterLabel(f.End), Day{f.End}
		r.GrossNotShown = !f.GrossComputed && !f.GrossMargin.OK && len(c.cfg.Resolved.CostOfRevenue) == 0 // AMZN: none since 2009
	}
	return r
}

// microFinancials is section 6: every company's latest quarter.
func (w *week) microFinancials(cs []*company) MicroFinancials {
	out := MicroFinancials{Rows: []FinancialRow{}, RPO: []RPORow{}, NoRPO: []string{}, Segments: w.segmentRows(cs)}
	for _, c := range cs {
		out.Rows = append(out.Rows, financialRow(c))
		// None filed within a year of the latest quarter (META, AAPL).
		if !c.hasFin || !recentPoint(c.in.RPO, c.fin.End) {
			out.NoRPO = append(out.NoRPO, c.cfg.Ticker)
			continue
		}
		out.RPO = append(out.RPO, RPORow{Ticker: c.cfg.Ticker, Date: Day{c.fin.End}, RPO: bnQty(c.fin.RPO), RPOYoY: qty(c.fin.RPOYoY, unitPercent), FromInstance: c.rpoInstance && c.fin.RPO.OK})
	}
	return out
}

// segmentRows is table 6.4 (ТЗ §20.3): each configured segment for the
// company's latest quarter (6.1), from its XBRL instances.
func (w *week) segmentRows(cs []*company) []SegmentRow {
	rows := []SegmentRow{}
	empty := func(ticker, kind, label string) SegmentRow {
		none := qty(transform.Value{}, unitUSDBn)
		return SegmentRow{Ticker: ticker, Kind: kind, Label: label, Revenue: none, RevenueYoY: qty(transform.Value{}, unitPercent), Profit: none, Margin: qty(transform.Value{}, unitPercent)}
	}
	for _, c := range cs {
		sg, ok := w.cfg.EDGAR.Segments[c.cfg.Ticker]
		if !ok || !c.hasFin {
			continue
		}
		n := len(rows)
		for _, m := range sg.Members {
			s := transform.SegmentAt(c.inst, m.Member, sg.Revenue, sg.Profit, c.fin.End)
			if !s.Revenue.OK && !s.Profit.OK {
				continue // a segment of another period (MSFT renamed its segments in FY27)
			}
			rows = append(rows, SegmentRow{Ticker: c.cfg.Ticker, Kind: "segment", Label: m.Label, Revenue: bnQty(s.Revenue), RevenueYoY: qty(s.RevenueYoY, unitPercent),
				Profit: bnQty(s.Profit), Margin: qty(s.Margin, unitPercent), Gross: sg.Gross})
		}
		if len(rows) == n {
			rows = append(rows, empty(c.cfg.Ticker, "no_data", ""))
			continue
		}
		if u := unallocated(c, sg); u.OK {
			r := empty(c.cfg.Ticker, "unallocated", "")
			r.Profit = bnQty(u)
			rows = append(rows, r)
		}
	}
	return rows
}

// grossCell is the gross margin column of 6.1.
func grossCell(r FinancialRow) string {
	switch {
	case r.GrossComputed:
		return marginCell(r.GrossMargin, r.GrossMarginChange, "**")
	case r.GrossNotShown:
		return "— (не показывает)"
	}
	return marginCell(r.GrossMargin, r.GrossMarginChange, "")
}

// netIncomeYoY is the net income change with ⚠ for a one-off.
func netIncomeYoY(r FinancialRow) string {
	s := signedPct1(r.NetIncomeYoY.val())
	if r.OneOff != "" && r.NetIncomeYoY.Value != nil {
		s += " ⚠ " + r.OneOff
	}
	return s
}

func buybacksCell(r FinancialRow) string {
	if r.NoBuybacks {
		return "0 (не выкупает)"
	}
	return bnCell(r.Buybacks)
}

func dividendsCell(r FinancialRow) string {
	if r.NoDividends {
		return "0 (не платит)"
	}
	return bnCell(r.Dividends)
}

func returnCell(r FinancialRow) string {
	if r.NoBuybacks && r.NoDividends && r.ReturnTTM.Value != nil {
		return "0"
	}
	return bnCell(r.ReturnTTM)
}

func returnShareCell(r FinancialRow) string {
	if r.FCFNotPositive {
		return "— (FCF ≤ 0)"
	}
	return pct1(r.ReturnShareOfFCF.val())
}

// rpoFromInstance reports whether a row's backlog needs the footnote.
func rpoFromInstance(rows []RPORow) bool {
	return slices.ContainsFunc(rows, func(r RPORow) bool { return r.FromInstance })
}

// noRPOLine is "Не подают в XBRL: …".
func noRPOLine(none []string) string {
	line := "Не подают в XBRL: " + strings.Join(none, ", ")
	if slices.Contains(none, "META") {
		line += " (у META бэклога нет — рекламный бизнес почти не работает по долгосрочным контрактам)"
	}
	return line + "."
}

// segmentMargin is the margin column of 6.4.
func segmentMargin(r SegmentRow) string {
	s := pct1(r.Margin.val())
	if r.Gross && r.Margin.Value != nil {
		s += " вал."
	}
	return s
}

// segmentProfit is the profit column of 6.4: TSLA has only gross profit.
func segmentProfit(r SegmentRow) string {
	if r.Gross {
		return dash
	}
	return bnCell(r.Profit)
}

// unallocated is the company's operating income less its segments' for the
// latest quarter (ТЗ §20.3); !OK for gross-profit segments or a segment
// without a profit.
func unallocated(c *company, sg config.Segments) transform.Value {
	if sg.Gross || !c.fin.OperatingIncome.OK {
		return transform.Value{}
	}
	sum, n := 0.0, 0
	for _, m := range sg.Members {
		s := transform.SegmentAt(c.inst, m.Member, sg.Revenue, sg.Profit, c.fin.End)
		if !s.Revenue.OK && !s.Profit.OK {
			continue // a segment of another period
		}
		if !s.Profit.OK {
			return transform.Value{}
		}
		sum += s.Profit.V
		n++
	}
	if n == 0 {
		return transform.Value{}
	}
	return some(c.fin.OperatingIncome.V - sum)
}

// CapexRow is a hyperscaler's capex by the quarters of the table.
// PartialYTD is the year to date up to PartialQuarter of a company that has
// not reported the latest quarter (0: none).
type CapexRow struct {
	Ticker         string     `json:"ticker"`
	Values         []Quantity `json:"values"`
	QoQ            Quantity   `json:"qoq"`
	YoY            Quantity   `json:"yoy"`
	YTD            Quantity   `json:"ytd"`
	PartialYTD     Quantity   `json:"partial_ytd"`
	PartialQuarter int        `json:"partial_quarter"`
}

// CapexSum is the sum of a quarter: null Sum and zero Count when no company
// reported it, Complete when all have.
type CapexSum struct {
	Known    bool     `json:"known"`
	Sum      Quantity `json:"sum"`
	Count    int      `json:"count"`
	Complete bool     `json:"complete"`
}

// CapexTrailing is the sum of 4 quarters at the latest complete quarter;
// Through is that quarter when it is not the latest of the table.
type CapexTrailing struct {
	Sum     Quantity `json:"sum"`
	YearAgo Quantity `json:"year_ago"`
	Growth  Quantity `json:"growth"`
	Through string   `json:"through"`
}

// CapexSection is section 2. GrowthQoQ and GrowthYoY of the total are null
// without complete quarters to compare.
type CapexSection struct {
	HasData   bool           `json:"has_data"`
	Companies int            `json:"companies"`
	Quarters  []string       `json:"quarters"`
	Year      int            `json:"year"`
	Rows      []CapexRow     `json:"rows"`
	Totals    []CapexSum     `json:"totals"`
	GrowthQoQ *Quantity      `json:"growth_qoq"`
	GrowthYoY *Quantity      `json:"growth_yoy"`
	TotalYTD  Quantity       `json:"total_ytd"`
	Trailing  *CapexTrailing `json:"trailing"`
}

// capexSection is the hyperscalers' capex table: the last five calendar
// quarters up to the latest one any of them reported.
func (w *week) capexSection(cs []*company) CapexSection {
	var hs []*company // in edgar.capex_companies order
	byTicker := map[string][]transform.Quarter{}
	for _, t := range w.cfg.EDGAR.CapexCompanies {
		if i := slices.IndexFunc(cs, func(c *company) bool { return c.cfg.Ticker == t }); i >= 0 {
			hs = append(hs, cs[i])
			byTicker[t] = cs[i].capx
		}
	}
	sums := transform.SumByCalendarQuarter(byTicker)
	out := CapexSection{Quarters: []string{}, Rows: []CapexRow{}, Totals: []CapexSum{}, TotalYTD: qty(transform.Value{}, unitUSDBn)}
	if len(hs) == 0 || len(sums) == 0 {
		return out
	}
	n := len(hs)
	latest := sums[len(sums)-1].Quarter
	quarters := make([]transform.CalendarQuarter, 5)
	for i := range quarters {
		quarters[i] = latest.Prev(4 - i)
		out.Quarters = append(out.Quarters, quarters[i].String())
	}
	out.HasData, out.Companies, out.Year = true, n, latest.Year
	var ytdSum float64
	ytdAll := true
	for _, c := range hs {
		row := CapexRow{Ticker: c.cfg.Ticker, Values: []Quantity{}, PartialYTD: qty(transform.Value{}, unitUSDBn)}
		var last transform.Quarter
		var hasLast bool
		for _, q := range quarters {
			cell := transform.Value{}
			for _, x := range c.capx {
				if transform.CalendarQuarterOf(x.End) == q {
					cell = some(x.Value)
					if q == latest {
						last, hasLast = x, true
					}
				}
			}
			row.Values = append(row.Values, bnQty(cell))
		}
		qoq, yoy := transform.Value{}, transform.Value{}
		if hasLast {
			qoq, yoy = transform.QuarterQoQ(c.capx, last.End), transform.QuarterYoY(c.capx, last.End)
		}
		row.QoQ, row.YoY = qty(qoq, unitPercent), qty(yoy, unitPercent)
		ytd := transform.YearToDate(c.capx, latest)
		row.YTD = bnQty(ytd)
		if ytd.OK {
			ytdSum += ytd.V
		} else {
			ytdAll = false
			// The company has not reported the latest quarter yet: up to its last.
			if prev := latest.Prev(1); prev.Year == latest.Year {
				if v := transform.YearToDate(c.capx, prev); v.OK {
					row.PartialYTD, row.PartialQuarter = bnQty(v), prev.Q
				}
			}
		}
		out.Rows = append(out.Rows, row)
	}
	sumOf := func(q transform.CalendarQuarter) (transform.AlignedSum, bool) {
		i := slices.IndexFunc(sums, func(s transform.AlignedSum) bool { return s.Quarter == q })
		if i < 0 {
			return transform.AlignedSum{}, false
		}
		return sums[i], true
	}
	for _, q := range quarters {
		s, ok := sumOf(q)
		cell := CapexSum{Known: ok, Sum: qty(transform.Value{}, unitUSDBn), Count: s.Count, Complete: ok && s.Complete(n)}
		if ok {
			cell.Sum = bnQty(some(s.Sum))
		}
		out.Totals = append(out.Totals, cell)
	}
	growth := func(back int) *Quantity {
		cur, ok1 := sumOf(latest)
		base, ok2 := sumOf(latest.Prev(back))
		if !ok1 || !ok2 || !cur.Complete(n) || !base.Complete(n) {
			return nil
		}
		q := qty(transform.Growth(cur.Sum, base.Sum), unitPercent)
		return &q
	}
	out.GrowthQoQ, out.GrowthYoY = growth(1), growth(4)
	if ytdAll {
		out.TotalYTD = bnQty(some(ytdSum))
	}

	// Trailing four quarters at the latest complete quarter.
	var full transform.CalendarQuarter
	found := false
	for i := len(sums) - 1; i >= 0; i-- {
		if sums[i].Complete(n) {
			full, found = sums[i].Quarter, true
			break
		}
	}
	if !found {
		return out
	}
	cur, prev := transform.TrailingSum(sums, full, n), transform.TrailingSum(sums, full.Prev(4), n)
	if !cur.OK {
		return out
	}
	t := &CapexTrailing{Sum: bnQty(some(cur.V)), YearAgo: qty(transform.Value{}, unitUSDBn), Growth: qty(transform.Value{}, unitPercent)}
	if prev.OK {
		t.YearAgo, t.Growth = bnQty(some(prev.V)), qty(transform.Growth(cur.V, prev.V), unitPercent)
	}
	if full != latest {
		t.Through = full.String()
	}
	out.Trailing = t
	return out
}

// capexYTD is the year-to-date column of a company.
func capexYTD(r CapexRow) string {
	if r.YTD.Value == nil && r.PartialQuarter > 0 {
		return fmt.Sprintf("%s (до Q%d)", bnCell(r.PartialYTD), r.PartialQuarter)
	}
	return bnCell(r.YTD)
}

// capexTotalCell is a quarter of the total row.
func capexTotalCell(s CapexSum, n int) string {
	switch {
	case !s.Known:
		return dash
	case s.Complete:
		return "**" + bnCell(s.Sum) + "**"
	}
	return fmt.Sprintf("частично, %d/%d", s.Count, n)
}

// capexGrowthCell is the bold growth of the total, a dash without one.
func capexGrowthCell(q *Quantity) string {
	if q == nil {
		return dash
	}
	return "**" + signedPct1(q.val()) + "**"
}

// capexTotalYTD is the year to date of the total.
func capexTotalYTD(q Quantity) string {
	if q.Value == nil {
		return dash
	}
	return "**" + bnCell(q) + "**"
}

// capexTrailingLine is the sentence under the capex table.
func capexTrailingLine(s CapexSection) string {
	t := s.Trailing
	line := fmt.Sprintf("Скользящая сумма 4 кварталов (%d компании): %s", s.Companies, moneyCell(t.Sum))
	if t.YearAgo.Value != nil {
		line += fmt.Sprintf(" (год назад: %s, %s)", moneyCell(t.YearAgo), signedPct1(t.Growth.val()))
	} else {
		line += " (год назад: —)"
	}
	if t.Through != "" {
		line += fmt.Sprintf(" — по %s, последнему кварталу, где отчитались все %d", t.Through, s.Companies)
	}
	return line + "."
}

// GuidanceItem is section 4 for one report, its Outlook texts already cut
// to the file's size limit.
type GuidanceItem struct {
	Date         Day      `json:"date"`
	Ticker       string   `json:"ticker"`
	Form         string   `json:"form"`
	Quarter      string   `json:"quarter"`
	HasDocument  bool     `json:"has_document"`
	Outlook      string   `json:"outlook"`
	CallSource   bool     `json:"call_source"`
	Previous     string   `json:"previous"`
	PreviousForm string   `json:"previous_form"`
	PreviousDate Day      `json:"previous_date"`
	Quotes       []string `json:"quotes"`
	CallOutlook  string   `json:"call_outlook"`
	URL          string   `json:"url"`
	CallURL      string   `json:"call_url"`
}

func (w *week) guidanceItem(m *microReport) GuidanceItem {
	item := GuidanceItem{Date: Day{m.r.AcceptedAt.In(w.et)}, Ticker: m.c.cfg.Ticker, Form: m.r.Form, Quarter: m.c.quarterLabel(m.r.PeriodEnd), HasDocument: m.hasDoc, CallSource: m.callSrc, Quotes: []string{}}
	if !m.hasDoc {
		return item
	}
	item.URL, item.CallURL = m.doc.URL, m.call.URL
	if len(m.outlook) > 0 {
		item.Outlook = w.cutOutlook(m.outlook, m.cut, m.doc.URL)
		if len(m.prev) > 0 {
			item.Previous, item.PreviousForm, item.PreviousDate = w.cutOutlook(m.prev, m.cut, ""), m.prevRep.Form, Day{m.prevRep.AcceptedAt.In(w.et)}
		}
	}
	if !m.noQuotes {
		item.Quotes = append(item.Quotes, m.quotes...)
	}
	if len(m.callOut) > 0 {
		item.CallOutlook = w.cutOutlook(m.callOut, m.cut, m.call.URL)
	}
	return item
}

// cutOutlook joins Outlook paragraphs (list items on consecutive lines),
// cut to n runes when n > 0.
func (w *week) cutOutlook(paras []string, n int, url string) string {
	var b strings.Builder
	for i, p := range paras {
		if i > 0 {
			if strings.HasPrefix(p, "•") {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(p)
	}
	s := b.String()
	if n > 0 && len([]rune(s)) > n {
		s = excerpt(s, n)
		if url != "" {
			s += "\n[обрезано, полный текст: " + url + "]"
		}
	}
	return s
}

// MicroLate is a report whose 10-Q/10-K is overdue.
type MicroLate struct {
	Ticker  string `json:"ticker"`
	Quarter string `json:"quarter"`
	End     Day    `json:"end"`
}

// MicroMissing is section 7.
type MicroMissing struct {
	Late   []MicroLate `json:"late"`
	Errors []string    `json:"errors"`
}

// lateList is "MSFT (Q3 2026, до 2026-09-30), …".
func lateList(ls []MicroLate) string {
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, fmt.Sprintf("%s (%s, до %s)", l.Ticker, l.Quarter, l.End.Format(time.DateOnly)))
	}
	return strings.Join(parts, ", ")
}

func (w *week) microMissing(cs []*company) MicroMissing {
	out := MicroMissing{Late: []MicroLate{}, Errors: []string{}}
	for _, c := range cs {
		if len(c.reports) == 0 {
			continue
		}
		r := c.reports[0]
		_, q, _ := transform.FiscalQuarter(r.PeriodEnd, c.fyEnd)
		due := tenQDays
		if c.fyEnd != "" && q == 4 {
			due = tenKDays
		}
		if _, ok := transform.QuarterAt(c.revenue, r.PeriodEnd); !ok && w.asOf.Sub(r.PeriodEnd) > time.Duration(due)*24*time.Hour {
			out.Late = append(out.Late, MicroLate{Ticker: c.cfg.Ticker, Quarter: c.quarterLabel(r.PeriodEnd), End: Day{r.PeriodEnd}})
		}
	}
	for key, st := range w.states {
		if st.LastError == "" {
			continue
		}
		ticker, isEDGAR := strings.CutPrefix(key, edgar.Source+":")
		isYahoo := slices.ContainsFunc(cs, func(c *company) bool { return key == "yahoo:"+c.cfg.Ticker })
		callTicker, isCall := strings.CutPrefix(key, irtranscript.Source+":")
		isCall = isCall && slices.ContainsFunc(cs, func(c *company) bool { return c.cfg.Ticker == callTicker })
		if isEDGAR && slices.ContainsFunc(cs, func(c *company) bool { return c.cfg.Ticker == ticker }) || isYahoo || isCall || key == "calendar:earnings" {
			out.Errors = append(out.Errors, key)
		}
	}
	sort.Strings(out.Errors)
	return out
}
