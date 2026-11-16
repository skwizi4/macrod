package export

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/derived"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/transform"
)

// GrowthRow is a row of "2.1 Рост"; Growth is QoQa, MoM or d by series.
type GrowthRow struct {
	Metric
	Level  Quantity `json:"level"`
	Growth *Tagged  `json:"growth"`
	YoY    Quantity `json:"yoy"`
	A3M    Quantity `json:"a3m"`
	Z      Quantity `json:"z"`
}

// SentimentRow is a row of "2.2 Сентимент".
type SentimentRow struct {
	Metric
	Level  Quantity `json:"level"`
	Change Quantity `json:"change"`
	Z      Quantity `json:"z"`
}

// LaborRow is a row of "2.3 Рынок труда": Change is d, diff or qoqa,
// Average is a3m, avg3 or avg4w by series, null without one.
type LaborRow struct {
	Metric
	Level   Quantity `json:"level"`
	Change  *Tagged  `json:"change"`
	YoY     Quantity `json:"yoy"`
	Average *Tagged  `json:"average"`
	Z       Quantity `json:"z"`
}

// InflationRow is a row of "2.4 Инфляция".
type InflationRow struct {
	Metric
	YoY Quantity `json:"yoy"`
	MoM Quantity `json:"mom"`
	A3M Quantity `json:"a3m"`
	A6M Quantity `json:"a6m"`
}

// RateRow is a row of levels and Δ1w/Δ4w in basis points; a series without
// a level (M2) has its growth rates instead (Growth set).
type RateRow struct {
	Metric
	Level    Quantity `json:"level"`
	Change1W Quantity `json:"change_1w"`
	Change4W Quantity `json:"change_4w"`
	// Change is the last change (transform d) of a monthly or quarterly
	// series, null without one: Europe's expectations table.
	Change Quantity `json:"change"`
	Growth []Tagged `json:"growth"`
}

// EnergyRow is a row of "2.7 Энергия"; changes are relative, %.
type EnergyRow struct {
	Metric
	Level    Quantity `json:"level"`
	Change1W Quantity `json:"change_1w"`
	Change4W Quantity `json:"change_4w"`
	YoY      Quantity `json:"yoy"`
}

// DerivedRow is a row of "3. Производные".
type DerivedRow struct {
	Metric
	Level  Quantity `json:"level"`
	Change Quantity `json:"change"`
	Z      Quantity `json:"z"`
}

// FedPathRow is a contract of the rate path.
type FedPathRow struct {
	Month     Day      `json:"month"`
	Meeting   Day      `json:"meeting"`
	Implied   Quantity `json:"implied"`
	VsDFF     Quantity `json:"vs_dff"`
	MeetingBP Quantity `json:"meeting_bp"`
	WeekAgo   Quantity `json:"week_ago"`
	NoTrades  bool     `json:"no_trades"`
	Thin      bool     `json:"thin"`
	New       bool     `json:"new"`
}

// FedPathReport is FED_PATH as of the period end.
type FedPathReport struct {
	Rows           []FedPathRow `json:"rows"`
	PriceDate      Day          `json:"price_date"`
	Horizon        Day          `json:"horizon"`
	HorizonWeekAgo Day          `json:"horizon_week_ago"`
	PathBP         Quantity     `json:"path_bp"`
	PathBPWeekAgo  Quantity     `json:"path_bp_week_ago"`
	Cuts           Quantity     `json:"cuts"`
	CutsWeekAgo    Quantity     `json:"cuts_week_ago"`
	NearMeetings   int          `json:"near_meetings"`
	NearMeeting    Day          `json:"near_meeting"`
	NearBP         Quantity     `json:"near_bp"`
	NearBPWeekAgo  Quantity     `json:"near_bp_week_ago"`
}

// FOMCMeeting is a decision of the FOMC, its time in ET.
type FOMCMeeting struct {
	Meeting  string    `json:"meeting"`
	Decision time.Time `json:"decision"`
}

// FedTexts is section 4 of USA Monetary; Held is the meeting of the period,
// Next the following one when there was none.
type FedTexts struct {
	Documents []DocumentItem `json:"documents"`
	Held      *FOMCMeeting   `json:"held"`
	Next      *FOMCMeeting   `json:"next"`
}

// MonetaryReport is usa/monetary/current_week.md.
type MonetaryReport struct {
	Header       Header         `json:"header"`
	Released     []ReleaseRow   `json:"released"`
	Growth       []GrowthRow    `json:"growth"`
	Sentiment    []SentimentRow `json:"sentiment"`
	Labor        []LaborRow     `json:"labor"`
	Inflation    []InflationRow `json:"inflation"`
	Expectations []RateRow      `json:"expectations"`
	Fed          []RateRow      `json:"fed"`
	// Credit is the optional table of money and credit (Europe, batch 11f);
	// empty for a region without it.
	Credit   []GrowthRow   `json:"credit"`
	FedPath  FedPathReport `json:"fed_path"`
	Energy   []EnergyRow   `json:"energy"`
	Derived  []DerivedRow  `json:"derived"`
	FedTexts FedTexts      `json:"fed_texts"`
	// ECBTexts is the section of the ECB texts, nil when the agent does not
	// read them.
	ECBTexts *ECBTexts      `json:"ecb_texts,omitempty"`
	Missing  MissingSection `json:"missing"`
}

// ECBCouncil is a Governing Council decision, its time in the region's
// zone.
type ECBCouncil struct {
	Decision time.Time `json:"decision"`
}

// ECBTexts are the ECB documents of the period (decisions, statements, Q&A,
// accounts, speeches, interviews; excerpts) and the meeting held, or the
// next one when there was none.
type ECBTexts struct {
	Documents []DocumentItem `json:"documents"`
	Held      *ECBCouncil    `json:"held"`
	Next      *ECBCouncil    `json:"next"`
}

func growthOf(v view) *Tagged {
	for _, code := range []string{transform.QoQA, transform.QoQ, transform.MoM} {
		if v.has(code) {
			return &Tagged{Code: code, Value: pctQty(v, code)}
		}
	}
	if v.has(transform.D) {
		return &Tagged{Code: transform.D, Value: qty(v.get(transform.D), v.Kind.changeUnit())}
	}
	return nil
}

func laborRow(v view) LaborRow {
	r := LaborRow{Metric: metricOf(v), Level: qty(transform.Value{}, v.Kind.levelUnit()), YoY: pctQty(v, transform.YoY), Z: zQty(v)}
	if v.has(transform.Level) {
		r.Level = levelQty(v)
		r.Change = &Tagged{Code: transform.D, Value: qty(v.V[transform.D], v.Kind.changeUnit())}
	}
	switch {
	case v.has(transform.Diff):
		r.Change = &Tagged{Code: transform.Diff, Value: qty(v.get(transform.Diff), v.Kind.changeUnit())}
	case v.has(transform.QoQA):
		r.Change = &Tagged{Code: transform.QoQA, Value: pctQty(v, transform.QoQA)}
	}
	switch {
	case v.has(transform.A3M):
		r.Average = &Tagged{Code: transform.A3M, Value: pctQty(v, transform.A3M)}
	case v.has(transform.Avg3):
		r.Average = &Tagged{Code: transform.Avg3, Value: qty(v.get(transform.Avg3), v.Kind.changeUnit())}
	case v.has(transform.Avg4W):
		r.Average = &Tagged{Code: transform.Avg4W, Value: qty(v.get(transform.Avg4W), v.Kind.levelUnit())}
	}
	return r
}

func inflationRow(v view) InflationRow {
	yoy := pctQty(v, transform.YoY)
	if !v.has(transform.YoY) && v.Kind == kindPct {
		yoy = qty(v.level(), unitPercent) // already a year-on-year rate (Sticky CPI)
	}
	return InflationRow{Metric: metricOf(v), YoY: yoy, MoM: pctQty(v, transform.MoM), A3M: pctQty(v, transform.A3M), A6M: pctQty(v, transform.A6M)}
}

// rateRow: level and Δ1w/Δ4w in basis points, or the growth rates of a
// series without a level.
func rateRow(v view) RateRow {
	r := RateRow{Metric: metricOf(v), Level: qty(transform.Value{}, v.Kind.levelUnit()),
		Change1W: qty(transform.Value{}, v.Kind.changeUnit()), Change4W: qty(transform.Value{}, v.Kind.changeUnit()),
		Change: qty(transform.Value{}, v.Kind.changeUnit())}
	if !v.has(transform.Level) {
		r.Growth = []Tagged{}
		for _, code := range []string{transform.YoY, transform.A3M} {
			if v.has(code) {
				r.Growth = append(r.Growth, Tagged{Code: code, Value: pctQty(v, code)})
			}
		}
		return r
	}
	r.Level, r.Change1W, r.Change4W = levelQty(v), qty(v.V[transform.D1W], v.Kind.changeUnit()), qty(v.V[transform.D4W], v.Kind.changeUnit())
	r.Change = qty(v.V[transform.D], v.Kind.changeUnit())
	return r
}

func fedPathReport(p derived.FedPath) FedPathReport {
	out := FedPathReport{
		Rows: []FedPathRow{}, PriceDate: Day{p.PriceDate}, Horizon: Day{p.Horizon}, HorizonWeekAgo: Day{p.HorizonWeekAgo},
		PathBP: qty(p.PathBP, unitBP), PathBPWeekAgo: qty(p.PathBPWeekAgo, unitBP), Cuts: qty(p.Cuts, unitMoves), CutsWeekAgo: qty(p.CutsWeekAgo, unitMoves),
		NearMeetings: derived.NearMeetings, NearMeeting: Day{p.NearMeeting}, NearBP: qty(p.NearBP, unitBP), NearBPWeekAgo: qty(p.NearBPWeekAgo, unitBP),
	}
	for _, r := range p.Rows {
		out.Rows = append(out.Rows, FedPathRow{Month: Day{r.Month}, Meeting: Day{r.Meeting}, Implied: qty(r.Implied, unitPercent), VsDFF: qty(r.VsDFFBP, unitBP),
			MeetingBP: qty(r.MeetingBP, unitBP), WeekAgo: qty(r.WeekAgo, unitPercent), NoTrades: r.NoTrades, Thin: r.Thin, New: r.New})
	}
	return out
}

func (w *week) monetary(ctx context.Context) (string, error) {
	load := func(ids []string) ([]view, error) { return w.views(ctx, ids...) }
	growth, err := load(w.table("growth"))
	if err != nil {
		return "", err
	}
	sentiment, err := load(w.table("sentiment"))
	if err != nil {
		return "", err
	}
	labor, err := load(w.table("labor"))
	if err != nil {
		return "", err
	}
	inflation, err := load(w.table("inflation"))
	if err != nil {
		return "", err
	}
	expect, err := load(w.table("expectations"))
	if err != nil {
		return "", err
	}
	fed, err := load(w.table("fed"))
	if err != nil {
		return "", err
	}
	credit, err := load(w.table("credit"))
	if err != nil {
		return "", err
	}
	var energy []view
	for _, sym := range w.table("energy") {
		v, err := w.quote(ctx, sym)
		if err != nil {
			return "", err
		}
		energy = append(energy, v)
	}
	// The FOMC path and texts belong to an agent that reads the Fed texts.
	readsFed := slices.Contains(w.agent.Texts, config.TextsFed)
	var path derived.FedPath
	if readsFed {
		if path, err = w.fedPath(ctx); err != nil {
			return "", err
		}
	}
	all := concat(growth, sentiment, labor, inflation, expect, fed, credit)
	var ids []string
	for _, t := range []string{"growth", "sentiment", "labor", "inflation", "expectations", "fed", "credit", "energy"} {
		ids = append(ids, w.table(t)...)
	}
	sources := w.sourcesWithTexts(w.seriesSources(ids)...)

	m := MonetaryReport{Header: w.newHeader("", w.title(), sources, staleIDs(all, energy), w.from, w.to), FedPath: fedPathReport(path), Credit: []GrowthRow{}}
	if m.Released, err = w.releasedRows(ctx, all); err != nil {
		return "", err
	}
	for _, v := range growth {
		m.Growth = append(m.Growth, GrowthRow{Metric: metricOf(v), Level: levelQty(v), Growth: growthOf(v), YoY: pctQty(v, transform.YoY), A3M: pctQty(v, transform.A3M), Z: zQty(v)})
	}
	for _, v := range sentiment {
		m.Sentiment = append(m.Sentiment, SentimentRow{Metric: metricOf(v), Level: levelQty(v), Change: qty(v.get(transform.D), v.Kind.changeUnit()), Z: zQty(v)})
	}
	for _, v := range labor {
		m.Labor = append(m.Labor, laborRow(v))
		if v.Source == "derived" {
			m.Derived = append(m.Derived, DerivedRow{Metric: metricOf(v), Level: levelQty(v), Change: qty(v.get(transform.D), v.Kind.changeUnit()), Z: zQty(v)})
		}
	}
	for _, v := range inflation {
		m.Inflation = append(m.Inflation, inflationRow(v))
	}
	for _, v := range expect {
		m.Expectations = append(m.Expectations, rateRow(v))
	}
	for _, v := range fed {
		m.Fed = append(m.Fed, rateRow(v))
	}
	for _, v := range credit {
		m.Credit = append(m.Credit, GrowthRow{Metric: metricOf(v), Level: levelQty(v), Growth: growthOf(v), YoY: pctQty(v, transform.YoY), A3M: pctQty(v, transform.A3M), Z: zQty(v)})
	}
	for _, v := range energy {
		m.Energy = append(m.Energy, EnergyRow{Metric: metricOf(v), Level: levelQty(v), Change1W: pctQty(v, transform.D1W), Change4W: pctQty(v, transform.D4W), YoY: pctQty(v, transform.YoY)})
	}
	m.FedTexts = FedTexts{Documents: []DocumentItem{}}
	if readsFed {
		if m.FedTexts, err = w.fedTexts(ctx); err != nil {
			return "", err
		}
	}
	if slices.Contains(w.agent.Texts, config.TextsECB) {
		t, err := w.ecbTexts(ctx)
		if err != nil {
			return "", err
		}
		m.ECBTexts = &t
	}
	if m.Missing, err = w.missingSection(ctx, concat(all, energy), sources); err != nil {
		return "", err
	}
	return w.report(w.path(), config.ReportMonetary, &m)
}

// growthCell is the QoQa / MoM column of "2.1 Рост".
func growthCell(k kind, g *Tagged) string {
	if g == nil {
		return dash
	}
	switch g.Code {
	case transform.QoQA, transform.QoQ, transform.MoM:
		return pct1(g.Value.val())
	case transform.D:
		return "Δ " + k.change(g.Value.val(), false)
	}
	return dash
}

// laborChange is the change column of "2.3 Рынок труда".
func laborChange(k kind, c *Tagged) string {
	if c == nil {
		return dash
	}
	switch c.Code {
	case transform.D:
		return k.change(c.Value.val(), k == kindThous)
	case transform.Diff:
		return k.change(c.Value.val(), true)
	case transform.QoQA:
		return "QoQa " + pct1(c.Value.val())
	}
	return dash
}

// laborAverage is the average column of "2.3 Рынок труда".
func laborAverage(k kind, a *Tagged) string {
	if a == nil {
		return dash
	}
	switch a.Code {
	case transform.A3M:
		return pct1(a.Value.val())
	case transform.Avg3:
		return "3m ср.: " + k.change(a.Value.val(), true)
	case transform.Avg4W:
		return "4w ср.: " + k.level(a.Value.val())
	}
	return dash
}

// growthRates is the level cell of a series without a level: "YoY 4.1% · 3mA 3.0%".
func growthRates(gs []Tagged) string {
	names := map[string]string{transform.YoY: "YoY", transform.A3M: "3mA"}
	var parts []string
	for _, g := range gs {
		parts = append(parts, names[g.Code]+" "+pct1(g.Value.val()))
	}
	return strings.Join(parts, " · ")
}

// fedPathFlags is the flags column of the rate path.
func fedPathFlags(r FedPathRow) string {
	var fls []string
	if r.NoTrades {
		fls = append(fls, "нет сделок")
	}
	if r.Thin {
		fls = append(fls, "мало сделок")
	}
	if r.New {
		fls = append(fls, "новый в окне")
	}
	return strings.Join(fls, ", ")
}

// impliedCell is the implied rate of a contract.
func impliedCell(q Quantity) string {
	if q.Value == nil {
		return "не котируется"
	}
	return rate2(q.val())
}

func concat(ls ...[]view) []view {
	var out []view
	for _, l := range ls {
		out = append(out, l...)
	}
	return out
}

// flagFallback marks a Yahoo row replaced by its FRED series.
const flagFallback = "FRED fallback"

// flagNoClose: the last bar day's close of a Yahoo symbol is not in yet.
const flagNoClose = "нет close"

// quote loads a Yahoo symbol with its config label and transforms; when
// Yahoo failed for it or its data is stale, the FRED series configured as
// its fallback (D7) is used and flagged.
func (w *week) quote(ctx context.Context, symbol string) (view, error) {
	var sym *struct {
		label string
		codes []string
	}
	units, short := "", false
	for _, s := range w.cfg.Yahoo.Symbols {
		if s.Symbol == symbol {
			sym = &struct {
				label string
				codes []string
			}{s.Label, s.Transforms}
			units, short = s.Units, s.ShortHistory
		}
	}
	if sym == nil {
		return view{}, fmt.Errorf("export: yahoo symbol %s is not configured", symbol)
	}
	k, relative := yahooKind(symbol, units)
	v, err := w.load(ctx, ref{"yahoo", symbol}, spec{Label: sym.label, Codes: sym.codes, Kind: &k, Relative: relative, ShortHistory: short})
	if err != nil {
		return v, err
	}
	failed := w.states["yahoo:"+symbol].LastError != ""
	if !failed && !v.Last.IsZero() && !v.stale() {
		// The rows of one table are read on one day: a missing close of
		// the last bar day (Friday's, Saturday's for crypto) shows.
		if want := yahoo.LastBarDay(w.to, symbol, w.cfg.Yahoo.ExchangeOf(symbol, w.et)); v.Last.Before(want) {
			v.Flags = append(v.Flags, flagNoClose+" "+want.Format("01-02"))
		}
		return v, nil
	}
	for _, s := range w.cfg.FRED.Series {
		if s.FallbackFor == symbol {
			fb, err := w.load(ctx, ref{"fred", s.ID}, spec{Label: s.Label, Codes: sym.codes, Kind: &k, Relative: relative})
			if err != nil {
				return v, err
			}
			if !fb.Last.IsZero() {
				fb.Flags = append(fb.Flags, flagFallback)
				return fb, nil
			}
		}
	}
	return v, nil
}

// quoteKind is how a Yahoo symbol is written.
// pointIndex reports the volatility indices whose changes are in points.
func pointIndex(symbol string) bool {
	switch symbol {
	case "^VIX", "^MOVE", "^VIX3M", "^SKEW":
		return true
	}
	return false
}

// yahooKind is how a Yahoo series is written: by the units its symbol
// states in the config (stored in series_meta), otherwise by the symbol.
// relative: weekly changes are shown in percent. Volatility indices move in
// points, yields in basis points, prices in percent.
func yahooKind(symbol, unitsText string) (k kind, relative bool) {
	if slices.Contains(config.YahooUnits, unitsText) {
		k = kindOfUnits(symbol, unitsText)
		return k, k != kindRate
	}
	return quoteKind(symbol), !pointIndex(symbol)
}

func quoteKind(symbol string) kind {
	switch symbol {
	case "GC=F", "HG=F", "CL=F", "NG=F", "BZ=F", "TTF=F":
		return kindUSD
	case "BTC-USD":
		return kindUSD0
	}
	return kindPx
}

// fedPath computes FED_PATH as of the period end.
func (w *week) fedPath(ctx context.Context) (derived.FedPath, error) {
	f := w.cfg.Yahoo.FedFundsFutures
	evs, err := w.releasesBetween(ctx, w.from.AddDate(0, 0, -7), w.to.AddDate(0, 0, 400))
	if err != nil {
		return derived.FedPath{}, err
	}
	var meetings []time.Time
	for _, e := range evs {
		if e.Source == fedtexts.SourceFOMC && e.Name == fedtexts.NameDecision && e.Status != model.StatusRescheduled {
			meetings = append(meetings, e.ScheduledAt.In(w.et))
		}
	}
	prices, volumes := map[string][]model.Observation{}, map[string][]model.Observation{}
	first := time.Date(w.asOf.Year(), w.asOf.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= f.MonthsAhead+1; i++ {
		sym := f.Symbol(first.AddDate(0, i, 0))
		if prices[sym], err = w.observations(ctx, "yahoo", sym, w.asOf.AddDate(0, 0, -30)); err != nil {
			return derived.FedPath{}, err
		}
		if volumes[sym], err = w.observations(ctx, "yahoo", sym+"#volume", w.asOf.AddDate(0, 0, -30)); err != nil {
			return derived.FedPath{}, err
		}
	}
	dff, err := w.observations(ctx, "fred", "DFF", w.asOf.AddDate(0, 0, -30))
	if err != nil {
		return derived.FedPath{}, err
	}
	return derived.ComputeFedPath(derived.FedPathInput{
		AsOf: w.asOf, MonthsAhead: f.MonthsAhead, Meetings: meetings, Symbol: f.Symbol,
		Prices: prices, Volumes: volumes, DFF: dff,
	}), nil
}

// moveChance words the chance of a 25 b.p. move at one meeting:
// "снижение 52%".
func moveChance(meetingBP transform.Value) string {
	if !meetingBP.OK {
		return dash
	}
	// From the shown (whole) b.p., so +8 reads 32%, not 30%.
	p := num(derived.MoveProbability(math.Round(meetingBP.V))*100, 0, false) + "%"
	switch {
	case p == "0%":
		return p
	case meetingBP.V < 0:
		return "снижение " + p
	}
	return "повышение " + p
}

// moves words a number of 25 b.p. moves: positive cuts, negative hikes.
func moves(cuts transform.Value) string {
	switch {
	case math.Abs(cuts.V) < 0.05:
		return "изменений ставки нет"
	case cuts.V > 0:
		return "~" + num(cuts.V, 1, false) + " снижения"
	}
	return "~" + num(-cuts.V, 1, false) + " повышения"
}

// pathChange words the weekly change of the path: "+4 б.п. за неделю
// (меньше снижений)".
func pathChange(p FedPathReport) string {
	path, ago := p.PathBP.val(), p.PathBPWeekAgo.val()
	if !ago.OK {
		return dash
	}
	d := path.V - ago.V
	s := num(d, 0, true) + " б.п. за неделю"
	switch r := math.Round(d); {
	case r == 0:
		return s + " (без изменений)"
	case path.V < 0 && r > 0:
		return s + " (меньше снижений)"
	case path.V < 0:
		return s + " (больше снижений)"
	case r > 0:
		return s + " (больше повышений)"
	default:
		return s + " (меньше повышений)"
	}
}

// fedTexts builds section 4 of USA Monetary: Fed documents of the period
// and the FOMC status.
func (w *week) fedTexts(ctx context.Context) (FedTexts, error) {
	docs, err := w.st.DocumentsBetween(ctx, fedtexts.Source, w.from, w.to)
	if err != nil {
		return FedTexts{}, err
	}
	out := FedTexts{Documents: []DocumentItem{}}
	for _, d := range docs {
		out.Documents = append(out.Documents, w.documentItem(d))
	}
	evs, err := w.releasesBetween(ctx, w.from, w.to.AddDate(0, 0, 120))
	if err != nil {
		return FedTexts{}, err
	}
	var held, next *FOMCMeeting
	for _, e := range evs {
		if e.Source != fedtexts.SourceFOMC || e.Name != fedtexts.NameDecision || e.Status == model.StatusRescheduled {
			continue
		}
		meeting := &FOMCMeeting{Meeting: e.Related.Meeting, Decision: e.ScheduledAt.In(w.et)}
		if e.ScheduledAt.Before(w.to) && held == nil {
			held = meeting
		} else if !e.ScheduledAt.Before(w.to) && next == nil {
			next = meeting
		}
	}
	out.Held = held
	if held == nil {
		out.Next = next
	}
	return out, nil
}

// seriesSources are the sources of the configured series ids, in order of
// first use; a derived series is computed from them and is not a source of
// its own.
func (w *week) seriesSources(ids []string) []string {
	var out []string
	for _, id := range ids {
		def, ok := w.cfg.ResolveSeries(id)
		if ok && def.Source != config.SourceDerived && !slices.Contains(out, def.Source) {
			out = append(out, def.Source)
		}
	}
	return out
}

// ecbTexts builds the ECB section of a Monetary file: the ECB documents of
// the period and the Governing Council meeting.
func (w *week) ecbTexts(ctx context.Context) (ECBTexts, error) {
	docs, err := w.st.DocumentsBetween(ctx, ecbtexts.Source, w.from, w.to)
	if err != nil {
		return ECBTexts{}, err
	}
	out := ECBTexts{Documents: []DocumentItem{}}
	for _, d := range docs {
		out.Documents = append(out.Documents, w.documentItem(d))
	}
	evs, err := w.releasesBetween(ctx, w.from, w.to.AddDate(0, 0, 120))
	if err != nil {
		return ECBTexts{}, err
	}
	for _, e := range evs {
		if e.Source != ecbtexts.SourceMeeting || e.Name != ecbtexts.NameDecision || e.Status == model.StatusRescheduled {
			continue
		}
		m := &ECBCouncil{Decision: e.ScheduledAt.In(w.et)}
		if e.ScheduledAt.Before(w.to) && out.Held == nil {
			out.Held = m
		} else if !e.ScheduledAt.Before(w.to) && out.Next == nil {
			out.Next = m
		}
	}
	if out.Held != nil {
		out.Next = nil
	}
	return out, nil
}
