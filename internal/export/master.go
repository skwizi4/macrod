package export

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/cboe"
	"macro-collector/internal/sources/cftc"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/sources/nasdaq"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/transform"
)

// The master files of a region (§12.5, DATA_MAP USA MASTER, examples/usa_master_*.md).

// market loads a market row: a Yahoo symbol (with FRED fallback) or a
// series of another source.
func (w *week) market(ctx context.Context, id string) (view, error) {
	return w.resolve(ctx, id)
}

// changeQty is Δ1w/Δ4w of a market row in its unit.
func changeQty(v view, code string) Quantity {
	if v.Source != "fred" && v.Relative || v.Source == "fred" && v.Kind != kindRate && slices.Contains(v.Flags, flagFallback) {
		return pctQty(v, code)
	}
	return qty(v.get(code), v.Kind.changeUnit())
}

// marketChange writes a change of a market row; rates state their unit.
func marketChange(k kind, q Quantity) string {
	switch {
	case q.Unit == unitPercent:
		return signedPct1(q.val())
	case k == kindRate:
		return k.change(q.val(), true)
	}
	return k.change(q.val(), false)
}

// MarketRow is one row of markets_usa.md.
type MarketRow struct {
	Metric
	Level     Quantity `json:"level"`
	Change1W  Quantity `json:"change_1w"`
	Change4W  Quantity `json:"change_4w"`
	FromMA50  Quantity `json:"from_ma50"`
	FromMA200 Quantity `json:"from_ma200"`
	Z         Quantity `json:"z"`
}

// MarketsReport is markets_<region>.md. COT are the CFTC contracts tagged
// with the agent's series tags (EU Master: Euro FX), none for USA.
type MarketsReport struct {
	Header  Header         `json:"header"`
	Rows    []MarketRow    `json:"rows"`
	COT     []COTRow       `json:"cot,omitempty"`
	Missing MissingSection `json:"missing"`
}

func (w *week) markets(ctx context.Context) (string, error) {
	var vs []view
	for _, id := range w.table("rows") {
		v, err := w.market(ctx, id)
		if err != nil {
			return "", err
		}
		vs = append(vs, v)
	}
	sources := []string{"yahoo", "fred"}
	for _, v := range vs {
		if !slices.Contains(sources, v.Source) {
			sources = append(sources, v.Source) // rows of the other numeric sources
		}
	}
	contracts := w.cfg.CFTC.ContractsOfTags(w.agent.SeriesTags)
	if len(contracts) > 0 {
		sources = append(sources, cftc.Source)
	}
	m := MarketsReport{Header: w.newHeader("markets", w.title(), sources, staleIDs(vs), w.from, w.to)}
	for _, k := range contracts {
		row, err := w.cot(ctx, k.Code, k.Name)
		if err != nil {
			return "", err
		}
		m.COT = append(m.COT, row)
	}
	for _, v := range vs {
		m.Rows = append(m.Rows, MarketRow{Metric: metricOf(v), Level: levelQty(v), Change1W: changeQty(v, transform.D1W), Change4W: changeQty(v, transform.D4W),
			FromMA50: pctQty(v, transform.MA50), FromMA200: pctQty(v, transform.MA200), Z: zQty(v)})
	}
	var err error
	if m.Missing, err = w.missingSection(ctx, vs, sources); err != nil {
		return "", err
	}
	return w.report(w.path(), config.ReportMarkets, &m)
}

var stars = map[string]string{"high": "★★★", "medium": "★★", "low": "★"}

// CalendarPrevious is the "Прошлое значение" of an event, by Form:
// "series" (Name: Value of transform Code, on Date), "auction" (Value is
// the rate, BTC the bid-to-cover, on Date), "dff" (Value of DFF).
type CalendarPrevious struct {
	Form  string   `json:"form"`
	Name  string   `json:"name"`
	Code  string   `json:"code"`
	Kind  kind     `json:"kind"`
	Value Quantity `json:"value"`
	BTC   Quantity `json:"btc"`
	Date  Day      `json:"date"`
	Freq  string   `json:"freq"`
}

// CalendarRow is an event of the next week, Local and ET the same moment.
type CalendarRow struct {
	Local      time.Time         `json:"local"`
	ET         time.Time         `json:"et"`
	Title      string            `json:"title"`
	Highlight  bool              `json:"highlight"`
	Importance string            `json:"importance"`
	Previous   *CalendarPrevious `json:"previous"`
}

// CalendarDaily is a daily release, named once outside the table.
type CalendarDaily struct {
	Name  string    `json:"name"`
	Local time.Time `json:"local"`
	ET    time.Time `json:"et"`
}

// Expiration is the monthly expiry, the third Friday.
type Expiration struct {
	Date     Day  `json:"date"`
	ThisWeek bool `json:"this_week"`
}

// CalendarMissed is an event of the past week that never came.
type CalendarMissed struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

// CalendarReport is calendar_<region>.md. HourDiffs are the differences
// between the local time and the region's zone (ET, CET/CEST: TZLabel)
// during the week, in hours; UsualHourDiff is the difference outside the
// weeks the two zones change clocks on different days.
type CalendarReport struct {
	Header        Header           `json:"header"`
	TZLabel       string           `json:"tz_label"`
	UsualHourDiff int              `json:"usual_hour_diff"`
	HourDiffs     []int            `json:"hour_diffs"`
	Rows          []CalendarRow    `json:"rows"`
	Daily         []CalendarDaily  `json:"daily"`
	Expiration    Expiration       `json:"expiration"`
	Missed        []CalendarMissed `json:"missed"`
}

func (w *week) calendar(ctx context.Context) (string, error) {
	from, to := w.to, w.to.AddDate(0, 0, 7)
	evs, err := w.releasesBetween(ctx, from, to)
	if err != nil {
		return "", err
	}
	freqs, err := w.st.SeriesFrequencies(ctx, "fred")
	if err != nil {
		return "", err
	}
	daily := func(e model.ReleaseEvent) bool {
		if e.Source == treasury.Source && e.Name == treasury.NameDTS {
			return true
		}
		if e.Source != "fred_release" || len(e.Related.Series) == 0 {
			return false
		}
		for _, id := range e.Related.Series {
			if freqs[id] != "d" {
				return false
			}
		}
		return true
	}
	first := w.to.In(w.loc).AddDate(0, 0, 1)
	m := CalendarReport{
		Header: w.newHeader("calendar", fmt.Sprintf(w.rep.Title+" — следующая неделя (%s → %s)",
			first.Format(time.DateOnly), first.AddDate(0, 0, 4).Format(time.DateOnly)),
			w.calendarSources(), nil, from, to),
		TZLabel: w.region.TimezoneLabel, UsualHourDiff: usualHourDiff(w.loc, w.et, from),
		HourDiffs: []int{}, Rows: []CalendarRow{}, Daily: []CalendarDaily{}, Missed: []CalendarMissed{},
	}
	diffs := map[int]bool{}
	for _, e := range evs {
		if e.Status == model.StatusRescheduled {
			continue
		}
		local, et := e.ScheduledAt.In(w.loc), e.ScheduledAt.In(w.et)
		_, lo := local.Zone()
		_, eo := et.Zone()
		diffs[(lo-eo)/3600] = true
		if daily(e) {
			if !slices.ContainsFunc(m.Daily, func(d CalendarDaily) bool { return d.Name == e.Name }) {
				m.Daily = append(m.Daily, CalendarDaily{Name: e.Name, Local: local, ET: et})
			}
			continue
		}
		prev, err := w.previousFor(ctx, e)
		if err != nil {
			return "", err
		}
		title, highlight := eventTitle(e)
		m.Rows = append(m.Rows, CalendarRow{Local: local, ET: et, Title: title, Highlight: highlight, Importance: e.Importance, Previous: prev})
	}
	for d := range diffs {
		m.HourDiffs = append(m.HourDiffs, d)
	}
	sort.Ints(m.HourDiffs)
	m.Expiration = expiration(from.In(w.loc), to.In(w.loc))
	past, err := w.releasesBetween(ctx, w.from, w.to)
	if err != nil {
		return "", err
	}
	for _, e := range past {
		if e.Status == model.StatusTimeout && e.Importance != "low" {
			m.Missed = append(m.Missed, CalendarMissed{Name: e.Name, At: e.ScheduledAt.In(w.et)})
		}
	}
	return w.report(w.path(), config.ReportCalendar, &m)
}

// calendarSources are the calendars whose events reach the region: the
// fetch_state keys of the header. A calendar of another region's agent is
// left out.
func (w *week) calendarSources() []string {
	cfg, id := w.cfg, w.region.ID
	var out []string
	if slices.ContainsFunc(cfg.FRED.Series, func(s config.Series) bool { return cfg.RegionOfTag(s.Agent) == id }) {
		out = append(out, "calendar:fred")
	}
	if cfg.FedTexts.Agent != "" && cfg.RegionOfTag(cfg.FedTexts.Agent) == id {
		out = append(out, "calendar:fed")
	}
	if cfg.TreasuryRegion() == id {
		out = append(out, "calendar:"+treasury.Source)
	}
	if cfg.Earnings.Enabled && slices.ContainsFunc(cfg.EDGAR.Companies, func(c config.Company) bool {
		r, _, _ := cfg.RegionOfCompany(c.Ticker)
		return r == id
	}) {
		out = append(out, "calendar:"+nasdaq.Source)
	}
	if cfg.ECBTexts.Enabled && cfg.RegionOfTag(cfg.ECBTexts.Agent) == id {
		out = append(out, "calendar:"+ecbtexts.CalendarName)
	}
	for _, c := range cfg.ReleaseCalendars {
		if cfg.RegionOfTag(c.Agent) == id {
			out = append(out, "calendar:"+c.Source())
		}
	}
	if cfg.Finanzagentur.Enabled && cfg.RegionOfTag(cfg.Finanzagentur.Agent) == id {
		out = append(out, "calendar:"+finanzagentur.SourceAuction)
	}
	return out
}

// usualHourDiff is the difference in hours between local and zone in
// mid-January of the week's year: both on winter time.
func usualHourDiff(local, zone *time.Location, at time.Time) int {
	jan := time.Date(at.Year(), 1, 15, 12, 0, 0, 0, time.UTC)
	_, lo := jan.In(local).Zone()
	_, zo := jan.In(zone).Zone()
	return (lo - zo) / 3600
}

// calendarHourNote is the note on the time difference, if it is unusual.
func calendarHourNote(ds []int, usual int) string {
	if len(ds) == 1 && ds[0] != usual {
		return fmt.Sprintf(" Разница на этой неделе %d ч (переход на летнее/зимнее время в Европе и США в разные дни).", ds[0])
	} else if len(ds) > 1 {
		return fmt.Sprintf(" Разница на этой неделе %d–%d ч (переход на летнее/зимнее время).", ds[0], ds[len(ds)-1])
	}
	return ""
}

// calendarDailyList is "Name 14:30 (08:30), …".
func calendarDailyList(ds []CalendarDaily) string {
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		parts = append(parts, d.Name+" "+d.Local.Format("15:04")+" ("+d.ET.Format("15:04")+")")
	}
	return strings.Join(parts, ", ")
}

// calendarPrevious writes the "Прошлое значение" of an event.
func calendarPrevious(p *CalendarPrevious) string {
	if p == nil {
		return dash
	}
	switch p.Form {
	case "series":
		var s string
		switch p.Code {
		case transform.Level:
			s = p.Kind.level(p.Value.val())
		case transform.Diff:
			s = p.Kind.change(p.Value.val(), true)
		default:
			s = pct1(p.Value.val())
		}
		return p.Name + ": " + s + " (" + obsDate(p.Date.Time, p.Freq) + ")"
	case "auction":
		s := "BTC " + num(p.BTC.val().V, 2, false)
		if v := p.Value.val(); v.OK { // FRNs have a discount margin, not a yield
			s = num(v.V, 3, false) + "%, " + s
		}
		return s + " (" + p.Date.Format(time.DateOnly) + ")"
	}
	return "DFF " + p.Kind.level(p.Value.val())
}

// eventTitle names an event of the calendar; highlight: it is written in bold.
func eventTitle(e model.ReleaseEvent) (title string, highlight bool) {
	switch {
	case e.Source == treasury.SourceAuction:
		return "Аукцион " + e.Name, false
	case e.Source == fedtexts.SourceFOMC && e.Name == fedtexts.NameDecision:
		return "FOMC: решение по ставке", true
	case e.Source == nasdaq.Source:
		return earningsTitle(e.Related), false
	case e.Source == finanzagentur.SourceAuction:
		return "Аукцион " + e.Name, false
	}
	return e.Name, false
}

// earningsTitle is "Отчёт MSFT (после закрытия)"; the time column of an
// unstated time shows earnings_calendar.unknown_time, so the title says so.
func earningsTitle(r model.Related) string {
	when := "время не объявлено"
	switch r.ReportTime {
	case nasdaq.TimeAfterClose:
		when = "после закрытия"
	case nasdaq.TimeBeforeOpen:
		when = "до открытия"
	}
	if r.Estimated {
		when = "дата — оценка Nasdaq, " + when
	}
	return "Отчёт " + r.Ticker + " (" + when + ")"
}

// expiration is the monthly expiry (third Friday) at or after from.
func expiration(from, to time.Time) Expiration {
	third := func(y int, m time.Month) time.Time {
		d := time.Date(y, m, 1, 0, 0, 0, 0, from.Location())
		for d.Weekday() != time.Friday {
			d = d.AddDate(0, 0, 1)
		}
		return d.AddDate(0, 0, 14)
	}
	t := third(from.Year(), from.Month())
	if t.Before(time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())) {
		n := from.AddDate(0, 1, 0)
		t = third(n.Year(), n.Month())
	}
	return Expiration{Date: Day{t}, ThisWeek: t.Before(to)}
}

// previousFor is the "Прошлое значение" of an event, nil without one.
func (w *week) previousFor(ctx context.Context, e model.ReleaseEvent) (*CalendarPrevious, error) {
	switch {
	case e.Source == "fred_release":
		cs, ok := w.rep.Previous[e.Related.ReleaseID]
		if !ok {
			return nil, nil
		}
		v, err := w.fred(ctx, cs.Series, cs.Code)
		if err != nil {
			return nil, nil // series not configured: nothing to show
		}
		return &CalendarPrevious{Form: "series", Name: cs.Name, Code: cs.Code, Kind: v.Kind, Value: qty(v.V[cs.Code], factUnit(v, cs.Code)),
			BTC: qty(transform.Value{}, unitRatio), Date: Day{v.Last}, Freq: v.Freq}, nil
	case e.Source == treasury.SourceAuction:
		day := time.Date(e.ScheduledAt.In(w.et).Year(), e.ScheduledAt.In(w.et).Month(), e.ScheduledAt.In(w.et).Day(), 0, 0, 0, 0, time.UTC)
		tenor, kind := auctionOf(e.Name)
		prev, err := w.st.AuctionsBefore(ctx, tenor, kind, day, 1)
		if err != nil || len(prev) == 0 {
			return nil, err
		}
		p := prev[0]
		rate := p.HighYield
		if isBill(p) {
			rate = p.HighRate
		}
		return &CalendarPrevious{Form: "auction", Kind: kindPct, Value: qty(transform.Value{V: rate.V, OK: rate.OK}, unitPercent),
			BTC: qty(some(p.BidToCover.V), unitRatio), Date: Day{p.AuctionDate}, Freq: "d"}, nil
	case e.Source == finanzagentur.SourceAuction:
		if e.Related.Tenor == "" {
			return nil, nil // a range of terms: no one previous auction
		}
		day := time.Date(e.ScheduledAt.In(w.et).Year(), e.ScheduledAt.In(w.et).Month(), e.ScheduledAt.In(w.et).Day(), 0, 0, 0, 0, time.UTC)
		prev, err := w.st.AuctionsBeforeOf(ctx, finanzagentur.Source, e.Related.Tenor, e.Related.Security, day, 1)
		if err != nil || len(prev) == 0 {
			return nil, err
		}
		p := prev[0]
		return &CalendarPrevious{Form: "auction", Kind: kindPct, Value: qty(transform.Value{V: p.AvgYield.V, OK: p.AvgYield.OK}, unitPercent),
			BTC: qty(some(p.BidToCover.V), unitRatio), Date: Day{p.AuctionDate}, Freq: "d"}, nil
	case strings.HasPrefix(e.Source, config.SourcePrefix):
		p := w.cfg.ReleasePreviousOf(e.Source, e.Name)
		if p == nil {
			return nil, nil
		}
		v, err := w.resolve(ctx, p.Series, p.Code)
		if err != nil || v.Last.IsZero() {
			return nil, nil // not configured or not loaded yet: nothing to show
		}
		return &CalendarPrevious{Form: "series", Name: p.Name, Code: p.Code, Kind: v.Kind, Value: qty(v.V[p.Code], factUnit(v, p.Code)),
			BTC: qty(transform.Value{}, unitRatio), Date: Day{v.Last}, Freq: v.Freq}, nil
	case e.Source == fedtexts.SourceFOMC && e.Name == fedtexts.NameDecision:
		v, err := w.fred(ctx, "DFF")
		if err != nil {
			return nil, nil
		}
		return &CalendarPrevious{Form: "dff", Name: "DFF", Code: transform.Level, Kind: v.Kind, Value: levelQty(v), BTC: qty(transform.Value{}, unitRatio), Date: Day{v.Last}, Freq: v.Freq}, nil
	}
	return nil, nil
}

// auctionOf splits an auction event name ("10-Year Note") into tenor and kind.
func auctionOf(name string) (tenor, kind string) {
	i := strings.LastIndex(name, " ")
	if i < 0 {
		return name, ""
	}
	return name[:i], name[i+1:]
}

// Forms of a row of key_metrics_usa.md.
const (
	keyFormSeries    = "series"    // a level with its changes
	keyFormTransform = "transform" // a transform with its previous value
	keyFormFedPath   = "fed_path"  // moves priced to Horizon, Change1W is a week ago
	keyFormRatio     = "ratio"     // a ratio, changes in its units
	keyFormBreadth   = "breadth"   // a ratio of ETF prices, changes in %
)

// KeyMetricRow is one row of key_metrics_usa.md, written by its Form.
type KeyMetricRow struct {
	N        int      `json:"n"`
	Form     string   `json:"form"`
	Label    string   `json:"label"`
	Date     Day      `json:"date"`
	Freq     string   `json:"freq"`
	Kind     kind     `json:"kind"`
	Value    Quantity `json:"value"`
	Change1W Quantity `json:"change_1w"`
	Change4W Quantity `json:"change_4w"`
	Previous Quantity `json:"previous"`
	Z        Quantity `json:"z"`
	Horizon  Day      `json:"horizon"`
	Flags    []string `json:"flags"`
}

// KeyMetricsReport is key_metrics_usa.md.
type KeyMetricsReport struct {
	Header Header         `json:"header"`
	Rows   []KeyMetricRow `json:"rows"`
}

func keyValue(r KeyMetricRow) string {
	v := r.Value.val()
	switch r.Form {
	case keyFormFedPath:
		if !v.OK {
			return dash
		}
		return moves(v) + " к " + byMonth(r.Horizon.Time)
	case keyFormTransform:
		return pct1(v)
	case keyFormRatio:
		return val(v, 2, false)
	case keyFormBreadth:
		return val(v, 3, false)
	}
	return r.Kind.level(v)
}

func keyChange(r KeyMetricRow, q Quantity) string {
	switch r.Form {
	case keyFormRatio:
		return val(q.val(), 2, true)
	case keyFormBreadth:
		return signedPct1(q.val())
	}
	if q.Unit == unitPercent {
		return signedPct1(q.val())
	}
	return r.Kind.change(q.val(), false)
}

func keyChange1W(r KeyMetricRow) string {
	switch {
	case r.Form == keyFormFedPath:
		if v := r.Change1W.val(); v.OK {
			return "неделю назад " + moves(v)
		}
		return dash
	case r.Form == keyFormTransform, r.Form == keyFormSeries && r.Freq == "m":
		return dash
	}
	return keyChange(r, r.Change1W)
}

func keyChange4W(r KeyMetricRow) string {
	switch {
	case r.Form == keyFormFedPath:
		return dash
	case r.Form == keyFormTransform:
		return "пред.: " + pct1(r.Previous.val())
	case r.Form == keyFormSeries && r.Freq == "m":
		return "пред.: " + r.Kind.level(r.Previous.val())
	}
	return keyChange(r, r.Change4W)
}

func (w *week) keyMetrics(ctx context.Context) (string, error) {
	rows := []KeyMetricRow{}
	var loaded []view
	for i, km := range w.rep.Rows {
		r := KeyMetricRow{N: i + 1, Label: km.Label, Flags: []string{}}
		if km.ID == config.KeyRowFedPath {
			path, err := w.fedPath(ctx)
			if err != nil {
				return "", err
			}
			r.Form, r.Freq = keyFormFedPath, "d"
			r.Value, r.Change1W, r.Change4W = qty(path.Cuts, unitMoves), qty(path.CutsWeekAgo, unitMoves), qty(transform.Value{}, unitMoves)
			r.Previous, r.Z = qty(transform.Value{}, unitMoves), qty(transform.Value{}, unitSigma)
			if path.Cuts.OK {
				r.Date, r.Horizon = Day{path.PriceDate}, Day{path.Horizon}
			}
			rows = append(rows, r)
			continue
		}
		v, err := w.market(ctx, km.ID)
		if err != nil {
			return "", err
		}
		loaded = append(loaded, v)
		r.Date, r.Freq, r.Kind, r.Flags = Day{v.Last}, v.Freq, v.Kind, metricOf(v).Flags
		if km.Code != "" {
			r.Form = keyFormTransform
			none := qty(transform.Value{}, unitPercent)
			r.Value, r.Change1W, r.Change4W, r.Previous, r.Z = qty(v.V[km.Code], unitPercent), none, none, qty(w.previous(v, km.Code), unitPercent), qty(transform.Value{}, unitSigma)
			rows = append(rows, r)
			continue
		}
		r.Form, r.Value, r.Z = keyFormSeries, levelQty(v), zQty(v)
		cu := v.Kind.changeUnit()
		r.Change1W, r.Change4W = qty(v.V[transform.D1W], cu), qty(v.V[transform.D4W], cu)
		switch {
		case v.ID == "^VIX" || v.ID == "^MOVE":
			r.Change1W, r.Change4W = qty(v.get(transform.D1W), cu), qty(v.get(transform.D4W), cu)
		case v.Source == "yahoo":
			r.Change1W, r.Change4W = pctQty(v, transform.D1W), pctQty(v, transform.D4W)
		}
		r.Previous = qty(transform.Value{}, v.Kind.levelUnit())
		if v.Freq == "m" {
			r.Previous = qty(w.previous(v, transform.Level), v.Kind.levelUnit())
		}
		rows = append(rows, r)
	}
	var ids []string
	for _, km := range w.rep.Rows {
		if km.ID != config.KeyRowFedPath {
			ids = append(ids, km.ID)
		}
	}
	sources := w.seriesSources(ids)
	if w.rep.SentimentRows {
		sent, vs, err := w.sentimentKeyRows(ctx, len(rows)+1)
		if err != nil {
			return "", err
		}
		rows, loaded = append(rows, sent...), append(loaded, vs...)
		for _, s := range []string{config.SourceYahoo, cboe.Source, kalshiSource} {
			if !slices.Contains(sources, s) {
				sources = append(sources, s)
			}
		}
	}
	return w.report(w.path(), config.ReportKeyMetrics, &KeyMetricsReport{
		Header: w.newHeader("key_metrics", w.title(), sources, staleIDs(loaded), w.from, w.to),
		Rows:   rows,
	})
}

// previous is a transform of v as of its previous observation.
func (w *week) previous(v view, code string) transform.Value {
	if len(v.Obs) < 2 {
		return transform.Value{}
	}
	scale := 1.0
	if v.Kind == kindRate {
		scale = 100
	}
	res, err := transform.Compute(transform.Input{Obs: v.Obs[:len(v.Obs)-1], Freq: v.Freq, AsOf: w.asOf, ChangeScale: scale,
		ZWindowDailyWeekly: w.cfg.Metrics.ZDailyWeekly, ZWindowMonthlyQuarterly: w.cfg.Metrics.ZMonthlyQuarterly}, []string{code})
	if err != nil {
		return transform.Value{}
	}
	return res.Values[code]
}

// kalshiSource is the fetch_state key of the Kalshi markets (P(recession)).
const kalshiSource = "kalshi"
