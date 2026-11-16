package export

import (
	"context"
	"fmt"
	"slices"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/derived"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/cboe"
	"macro-collector/internal/sources/cftc"
	"macro-collector/internal/sources/predict"
	"macro-collector/internal/transform"
)

// USA Sentiment (FIXES_W44 batch F, D16): what the market does next to what
// News says — event reactions, regime, volatility, breadth, positioning,
// prediction markets and topic counts.

// sentimentSources are the sources whose failures the file reports: those
// of its instruments and sections (USA: yahoo, fred, cboe, cftc, kalshi,
// polymarket).
func (w *week) sentimentSources() []string {
	s := w.sent()
	out := []string{config.SourceYahoo}
	add := func(src string) {
		if !slices.Contains(out, src) {
			out = append(out, src)
		}
	}
	addSeries := func(name string) {
		if def, ok := w.cfg.ResolveSeries(name); ok && def.Source != config.SourceDerived {
			add(def.Source)
		}
	}
	if s.Has(config.SentRegime) {
		addSeries(s.Regime.Rates)
	}
	for _, m := range append(slices.Clone(s.Reaction.Symbols), s.DailyMoves...) {
		if m.Series != "" && s.Has(config.SentReaction) {
			addSeries(m.Series)
		}
	}
	if s.Has(config.SentVolatility) {
		for _, v := range append(slices.Clone(s.Volatility), s.VolRatio.Num, s.VolRatio.Den) {
			addSeries(v)
		}
		if s.PutCallOn() {
			add(cboe.Source)
		}
	}
	if s.Has(config.SentPositioning) {
		add(cftc.Source)
	}
	if s.Has(config.SentPredictions) {
		add(predict.SourceKalshi)
		add(predict.SourcePolymarket)
	}
	return out
}

// sent is the configuration of the sentiment report being built.
func (w *week) sent() *config.Sentiment { return w.cfg.SentimentOf(w.rep) }

// RatioRow is a ratio of two series; changes are in its units or in %.
type RatioRow struct {
	Label    string   `json:"label"`
	Date     Day      `json:"date"`
	Level    Quantity `json:"level"`
	Change1W Quantity `json:"change_1w"`
	Change4W Quantity `json:"change_4w"`
	Flags    []string `json:"flags"`
}

// Regime is section 3: the correlation of NDX returns with 10Y changes.
type Regime struct {
	CorrDays  int      `json:"corr_days"`
	Threshold float64  `json:"threshold"`
	Date      Day      `json:"date"`
	R         Quantity `json:"r"`
	RWeekAgo  Quantity `json:"r_week_ago"`
}

// PutCallRow is a Cboe put/call ratio: the day and its 5-day averages.
type PutCallRow struct {
	Metric
	Level       Quantity `json:"level"`
	Avg5        Quantity `json:"avg_5d"`
	Avg5WeekAgo Quantity `json:"avg_5d_week_ago"`
	Z           Quantity `json:"z"`
}

// Volatility is section 4; PutCall is null when Cboe is off.
type Volatility struct {
	Rows    []ChangesRow `json:"rows"`
	Ratio   RatioRow     `json:"ratio"`
	PutCall []PutCallRow `json:"put_call"`
}

// COTRow is the nets of Leveraged Funds and Asset Managers of a contract.
type COTRow struct {
	Name         string   `json:"name"`
	Date         Day      `json:"date"`
	LevNet       Quantity `json:"lev_net"`
	LevChange    Quantity `json:"lev_change"`
	LevZ         Quantity `json:"lev_z"`
	AMNet        Quantity `json:"am_net"`
	AMChange     Quantity `json:"am_change"`
	AMZ          Quantity `json:"am_z"`
	OpenInterest Quantity `json:"open_interest"`
}

// Positioning is section 6; MMF is null when its series is not configured.
type Positioning struct {
	Contracts []COTRow    `json:"contracts"`
	MMF       *ChangesRow `json:"mmf"`
}

// SentimentReport is usa/news/sentiment.md (FIXES_W44 batch F, D16).
type SentimentReport struct {
	Header      Header          `json:"header"`
	Reaction    ReactionSection `json:"reaction"`
	Moves       MovesSection    `json:"moves"`
	Regime      Regime          `json:"regime"`
	Volatility  Volatility      `json:"volatility"`
	Breadth     []RatioRow      `json:"breadth"`
	Positioning Positioning     `json:"positioning"`
	Predictions Predictions     `json:"predictions"`
	Missing     MissingSection  `json:"missing"`
}

func (w *week) sentiment(ctx context.Context) (string, error) {
	sources := w.sentimentSources()
	m := SentimentReport{Header: w.newHeader("sentiment", w.title(), sources, nil, w.from, w.to),
		Reaction: ReactionSection{Symbols: []string{}, Rows: []ReactionEvent{}}, Moves: MovesSection{Days: []Day{}, Rows: []DailyMovesRow{}},
		Volatility: Volatility{Rows: []ChangesRow{}}, Breadth: []RatioRow{}, Positioning: Positioning{Contracts: []COTRow{}}}
	s := w.sent()
	var loaded []view
	add := func(vs []view, err error) error {
		loaded = append(loaded, vs...)
		return err
	}
	var err error
	if s.Has(config.SentReaction) {
		var vs []view
		if m.Reaction, vs, err = w.sentReaction(ctx); err != nil {
			return "", err
		}
		loaded = append(loaded, vs...)
	}
	for _, step := range []struct {
		section string
		run     func() ([]view, error)
	}{
		{config.SentMoves, func() (vs []view, err error) { m.Moves, vs, err = w.sentMoves(ctx); return }},
		{config.SentRegime, func() (vs []view, err error) { m.Regime, vs, err = w.sentRegime(ctx); return }},
		{config.SentVolatility, func() (vs []view, err error) { m.Volatility, vs, err = w.sentVolatility(ctx); return }},
		{config.SentBreadth, func() (vs []view, err error) { m.Breadth, vs, err = w.sentBreadth(ctx); return }},
		{config.SentPositioning, func() (vs []view, err error) { m.Positioning, vs, err = w.sentPositioning(ctx); return }},
		{config.SentPredictions, func() (vs []view, err error) { m.Predictions, vs, err = w.sentPredictions(ctx); return }},
	} {
		if !s.Has(step.section) {
			continue
		}
		if err := add(step.run()); err != nil {
			return "", err
		}
	}
	if m.Missing, err = w.missingSection(ctx, uniqueViews(loaded), sources); err != nil {
		return "", err
	}
	return w.report(w.path(), config.ReportSentiment, &m)
}

// sentRegime is F2: the 20-day correlation of the index's returns with
// 10Y yield changes (NDX and UST 10Y; GER40 and Bund 10Y). Stocks falling
// as yields rise (r < 0) fear inflation and rates; stocks falling with
// yields (r > 0) fear growth.
func (w *week) sentRegime(ctx context.Context) (Regime, []view, error) {
	s := w.sent()
	ndx, err := w.quote(ctx, s.Regime.Equity)
	if err != nil {
		return Regime{}, nil, err
	}
	y10, err := w.resolve(ctx, s.Regime.Rates)
	if err != nil {
		return Regime{}, nil, err
	}
	days, th := s.CorrDays, s.RegimeThreshold
	return Regime{CorrDays: days, Threshold: th, Date: Day{lastCommon(ndx.Obs, y10.Obs, w.asOf)},
		R:        qty(transform.ReturnsCorrelation(ndx.Obs, y10.Obs, w.asOf, days), unitCorrelation),
		RWeekAgo: qty(transform.ReturnsCorrelation(ndx.Obs, y10.Obs, w.asOf.AddDate(0, 0, -7), days), unitCorrelation),
	}, []view{ndx, y10}, nil
}

// regimeName names the F2 regime of r.
func regimeName(r Quantity, th float64) string {
	v := r.val()
	switch {
	case !v.OK:
		return dash
	case v.V < -th:
		return "боится инфляции / ставок"
	case v.V > th:
		return "боится роста"
	}
	return "нейтрально"
}

// lastCommon is the last date ≤ asOf both series have, zero without one.
func lastCommon(a, b []model.Observation, asOf time.Time) time.Time {
	in := make(map[time.Time]bool, len(b))
	for _, o := range b {
		in[o.Date] = true
	}
	for i := len(a) - 1; i >= 0; i-- {
		if in[a[i].Date] && !a[i].Date.After(asOf) {
			return a[i].Date
		}
	}
	return time.Time{}
}

// sentVolatility is F3: VIX term structure, skew and MOVE (Treasury
// volatility, also in master/markets); Europe: VDAX-NEW, VSTOXX, VDAX/VIX.
func (w *week) sentVolatility(ctx context.Context) (Volatility, []view, error) {
	s := w.sent()
	var vs []view
	out := Volatility{Rows: []ChangesRow{}}
	for _, id := range s.Volatility {
		v, err := w.resolve(ctx, id)
		if err != nil {
			return out, nil, err
		}
		vs = append(vs, v)
		out.Rows = append(out.Rows, ChangesRow{Metric: metricOf(v), Level: levelQty(v), Change1W: changeQty(v, transform.D1W), Change4W: changeQty(v, transform.D4W), Z: zQty(v)})
	}
	num, err := w.resolve(ctx, s.VolRatio.Num)
	if err != nil {
		return out, nil, err
	}
	den, err := w.resolve(ctx, s.VolRatio.Den)
	if err != nil {
		return out, nil, err
	}
	vs = append(vs, num, den)
	if out.Ratio, err = w.ratioRow(num, den, s.VolRatio.Label, false, s.VolRatio.Inverts()); err != nil {
		return out, nil, err
	}
	if !s.PutCallOn() {
		return out, uniqueViews(vs), nil
	}
	pc, rows, err := w.putCall(ctx)
	if err != nil {
		return out, nil, err
	}
	out.PutCall = rows
	return out, uniqueViews(append(vs, pc...)), nil
}

// putCallAvgDays smooths the daily put/call ratio, which jumps with
// expiries.
const putCallAvgDays = 5

// putCall is the Cboe put/call table: last day, 5-day average now and a
// week ago, z of the daily ratio. Above ~1 (total) more puts than calls are
// bought: protection, fear. Rows are nil when Cboe is off.
func (w *week) putCall(ctx context.Context) ([]view, []PutCallRow, error) {
	if !w.sent().PutCallOn() {
		return nil, nil, nil
	}
	k := kindRatio
	var vs []view
	rows := []PutCallRow{}
	for _, s := range cboe.Series {
		v, err := w.load(ctx, ref{cboe.Source, s.ID}, spec{Label: s.Label, Codes: []string{transform.Level, transform.Z}, Kind: &k})
		if err != nil {
			return nil, nil, err
		}
		vs = append(vs, v)
		rows = append(rows, PutCallRow{Metric: metricOf(v), Level: levelQty(v), Avg5: qty(transform.MeanLast(v.Obs, w.asOf, putCallAvgDays), unitRatio),
			Avg5WeekAgo: qty(transform.MeanLast(v.Obs, w.asOf.AddDate(0, 0, -7), putCallAvgDays), unitRatio), Z: zQty(v)})
	}
	return vs, rows, nil
}

// ratioRow computes num/den on common days: level, Δ1w and Δ4w (in % when
// relative, else in the ratio's units), and the inversion flag (VIX/VIX3M).
func (w *week) ratioRow(num, den view, label string, relative, inversion bool) (RatioRow, error) {
	r := transform.Ratio(num.Obs, den.Obs)
	res, err := transform.Compute(transform.Input{Obs: r, Freq: "d", AsOf: w.asOf, RelativeChanges: relative},
		[]string{transform.Level, transform.D1W, transform.D4W})
	if err != nil {
		return RatioRow{}, fmt.Errorf("export ratio %s: %w", label, err)
	}
	lvl := res.Values[transform.Level]
	unit := unitRatio
	if relative {
		unit = unitPercent
	}
	row := RatioRow{Label: label, Date: Day{res.Date}, Level: qty(lvl, unitRatio), Change1W: qty(res.Values[transform.D1W], unit), Change4W: qty(res.Values[transform.D4W], unit), Flags: []string{}}
	if inversion && lvl.OK && lvl.V > 1 {
		row.Flags = append(row.Flags, flagInversion)
	}
	return row, nil
}

// sentBreadth is F4: equal weight vs cap weight, small vs large caps,
// sectors vs the index.
func (w *week) sentBreadth(ctx context.Context) ([]RatioRow, []view, error) {
	rows := []RatioRow{}
	var vs []view
	for _, r := range w.sent().Ratios {
		num, err := w.quote(ctx, r.Num)
		if err != nil {
			return nil, nil, err
		}
		den, err := w.quote(ctx, r.Den)
		if err != nil {
			return nil, nil, err
		}
		row, err := w.ratioRow(num, den, r.Label, true, false)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, row)
		vs = append(vs, num, den)
	}
	if len(rows) == 0 {
		return rows, nil, nil
	}
	return rows, uniqueViews(vs), nil
}

// uniqueViews drops repeated series (SPY is the base of several ratios).
func uniqueViews(vs []view) []view {
	seen := map[ref]bool{}
	var out []view
	for _, v := range vs {
		if !seen[v.ref] {
			seen[v.ref] = true
			out = append(out, v)
		}
	}
	return out
}

// mmfSeries is the money market funds series (F5): retail funds from the
// Fed H.6 via FRED; ICI totals refuse automated downloads (LOOKUP).
const mmfSeries = "WRMFNS"

// sentPositioning is F5: CFTC COT (moved from master/markets, D16) and money
// market fund assets.
func (w *week) sentPositioning(ctx context.Context) (Positioning, []view, error) {
	out := Positioning{Contracts: []COTRow{}}
	for _, k := range w.cfg.CFTC.ContractsOfTags(w.agent.SeriesTags) {
		row, err := w.cot(ctx, k.Code, k.Name)
		if err != nil {
			return out, nil, err
		}
		out.Contracts = append(out.Contracts, row)
	}
	var vs []view
	if w.fredConfigured(mmfSeries) {
		v, err := w.fred(ctx, mmfSeries)
		if err != nil {
			return out, nil, err
		}
		vs = append(vs, v)
		u := v.Kind.changeUnit()
		out.MMF = &ChangesRow{Metric: metricOf(v), Level: levelQty(v), Change1W: qty(v.get(transform.D1W), u), Change4W: qty(v.get(transform.D4W), u), Z: qty(transform.Value{}, unitSigma)}
	}
	return out, vs, nil
}

// fredConfigured reports whether id is in fred.series.
func (w *week) fredConfigured(id string) bool {
	for _, s := range w.cfg.FRED.Series {
		if s.ID == id {
			return true
		}
	}
	return false
}

// cot is one COT row: nets of Leveraged Funds and Asset Managers.
func (w *week) cot(ctx context.Context, code, name string) (COTRow, error) {
	get := func(field string) ([]model.Observation, error) {
		return w.observations(ctx, cftc.Source, cftc.SeriesID(code, field), time.Time{})
	}
	net := func(long, short string) (transform.Result, error) {
		l, err := get(long)
		if err != nil {
			return transform.Result{}, err
		}
		s, err := get(short)
		if err != nil {
			return transform.Result{}, err
		}
		return transform.Compute(transform.Input{Obs: derived.Net(l, s), Freq: "w", AsOf: w.asOf,
			ZWindowDailyWeekly: w.cfg.Metrics.ZDailyWeekly, ZWindowMonthlyQuarterly: w.cfg.Metrics.ZMonthlyQuarterly},
			[]string{transform.Level, transform.D, transform.Z})
	}
	lev, err := net(cftc.FieldLevLong, cftc.FieldLevShort)
	if err != nil {
		return COTRow{}, err
	}
	am, err := net(cftc.FieldAMLong, cftc.FieldAMShort)
	if err != nil {
		return COTRow{}, err
	}
	oi, err := get(cftc.FieldOpenInterest)
	if err != nil {
		return COTRow{}, err
	}
	oiV := transform.Value{}
	for i := len(oi) - 1; i >= 0; i-- {
		if !oi[i].Date.After(w.asOf) {
			oiV = some(oi[i].Value)
			break
		}
	}
	return COTRow{Name: name, Date: Day{lev.Date},
		LevNet: qty(lev.Values[transform.Level], unitCount), LevChange: qty(lev.Values[transform.D], unitCount), LevZ: qty(lev.Values[transform.Z], unitSigma),
		AMNet: qty(am.Values[transform.Level], unitCount), AMChange: qty(am.Values[transform.D], unitCount), AMZ: qty(am.Values[transform.Z], unitSigma),
		OpenInterest: qty(oiV, unitCount)}, nil
}

// sentimentKeyRows are the rows of key_metrics_usa.md from sentiment.md
// (FIXES_W44 F8), numbered from n: VIX/VIX3M, put/call, RSP/SPY and the
// Kalshi probability of a recession next year.
func (w *week) sentimentKeyRows(ctx context.Context, n int) ([]KeyMetricRow, []view, error) {
	var rows []KeyMetricRow
	var vs []view
	nextN := func() int { n++; return n - 1 }
	ratio := func(a, b string) ([]model.Observation, view, view, error) {
		va, err := w.quote(ctx, a)
		if err != nil {
			return nil, view{}, view{}, err
		}
		vb, err := w.quote(ctx, b)
		if err != nil {
			return nil, view{}, view{}, err
		}
		return transform.Ratio(va.Obs, vb.Obs), va, vb, nil
	}
	compute := func(obs []model.Observation, relative bool) (transform.Result, error) {
		return transform.Compute(transform.Input{Obs: obs, Freq: "d", AsOf: w.asOf, RelativeChanges: relative},
			[]string{transform.Level, transform.D1W, transform.D4W})
	}
	noneZ := qty(transform.Value{}, unitSigma)
	noneR := qty(transform.Value{}, unitRatio)

	// VIX/VIX3M: above 1 the near-term fear is higher than the 3-month one.
	obs, vix, vix3m, err := ratio("^VIX", "^VIX3M")
	if err != nil {
		return nil, nil, err
	}
	vs = append(vs, vix, vix3m)
	r, err := compute(obs, false)
	if err != nil {
		return nil, nil, err
	}
	lvl := r.Values[transform.Level]
	fl := []string{}
	if lvl.OK && lvl.V > 1 {
		fl = append(fl, flagInversion)
	}
	rows = append(rows, KeyMetricRow{N: nextN(), Form: keyFormRatio, Label: "VIX/VIX3M", Date: Day{r.Date}, Freq: "d", Kind: kindRatio,
		Value: qty(lvl, unitRatio), Change1W: qty(r.Values[transform.D1W], unitRatio), Change4W: qty(r.Values[transform.D4W], unitRatio), Previous: noneR, Z: noneZ, Flags: fl})

	// Put/call of equities, 5-day average.
	if w.cfg.Cboe.Enabled {
		k := kindRatio
		v, err := w.load(ctx, ref{cboe.Source, cboe.SeriesEquity}, spec{Label: "Put/call акции", Codes: []string{transform.Level, transform.Z}, Kind: &k})
		if err != nil {
			return nil, nil, err
		}
		vs = append(vs, v)
		avg := func(back int) transform.Value {
			return transform.MeanLast(v.Obs, w.asOf.AddDate(0, 0, -back), putCallAvgDays)
		}
		diff := func(a, b transform.Value) transform.Value {
			if !a.OK || !b.OK {
				return transform.Value{}
			}
			return some(a.V - b.V)
		}
		now := avg(0)
		rows = append(rows, KeyMetricRow{N: nextN(), Form: keyFormRatio, Label: "Put/call акции (среднее 5д, Cboe)", Date: Day{v.Last}, Freq: v.Freq, Kind: kindRatio,
			Value: qty(now, unitRatio), Change1W: qty(diff(now, avg(7)), unitRatio), Change4W: qty(diff(now, avg(28)), unitRatio), Previous: noneR, Z: zQty(v), Flags: metricOf(v).Flags})
	}

	// RSP/SPY: market breadth.
	obs, rsp, spy, err := ratio("RSP", "SPY")
	if err != nil {
		return nil, nil, err
	}
	vs = append(vs, rsp, spy)
	if r, err = compute(obs, true); err != nil {
		return nil, nil, err
	}
	rows = append(rows, KeyMetricRow{N: nextN(), Form: keyFormBreadth, Label: "RSP/SPY (ширина рынка)", Date: Day{r.Date}, Freq: "d", Kind: kindRatio,
		Value: qty(r.Values[transform.Level], unitRatio), Change1W: qty(r.Values[transform.D1W], unitPercent), Change4W: qty(r.Values[transform.D4W], unitPercent), Previous: noneR, Z: noneZ, Flags: []string{}})

	// P(recession next year), Kalshi.
	if w.cfg.Prediction.Enabled {
		year := w.asOf.Year() + 1
		id := predict.SeriesID("recession", fmt.Sprintf("KXRECSSNBER-%02d", year%100))
		k := kindPct
		v, err := w.load(ctx, ref{predict.SourceKalshi, id}, spec{Label: fmt.Sprintf("P(рецессия в %d, Kalshi)", year),
			Codes: []string{transform.Level, transform.D1W, transform.D4W}, Kind: &k})
		if err != nil {
			return nil, nil, err
		}
		vs = append(vs, v)
		rows = append(rows, KeyMetricRow{N: nextN(), Form: keyFormSeries, Label: v.Label, Date: Day{v.Last}, Freq: v.Freq, Kind: v.Kind, Value: levelQty(v),
			Change1W: qty(v.get(transform.D1W), v.Kind.changeUnit()), Change4W: qty(v.get(transform.D4W), v.Kind.changeUnit()), Previous: qty(transform.Value{}, unitPercent), Z: noneZ, Flags: metricOf(v).Flags})
	}
	return rows, uniqueViews(vs), nil
}

// flagInversion marks VIX above VIX3M.
const flagInversion = "⚠ инверсия"
