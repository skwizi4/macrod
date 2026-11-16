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
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
	"macro-collector/internal/units"
)

// kind is how a series' values are written (§17.8).
type kind int

const (
	kindIndex kind = iota // index level, one decimal: 66.1
	kindRate              // rate, yield, spread in %: 3.52%, changes in b.p.
	kindPct               // other percentages: 4.3%, changes in p.p.
	kindBn                // dollar amounts, in billions
	kindThous             // thousands of persons: 7 210 тыс.
	kindCount             // persons, contracts: 231 000
	kindUSD               // a price in dollars, two decimals: 37.24 $
	kindRatio             // 0.99
	kindBP                // already in basis points: 66 б.п.
	kindPx                // an index quote: whole from 1 000 on (28 958), else two decimals (19.45)
	kindUSD0              // a large dollar price: 80 150 $
	kindBnEUR             // euro amounts, in billions
	kindEUR               // a price in euro, two decimals: 31.40 €
	kindFX                // an exchange rate, four decimals: 1.1634
)

func kindOfUnits(id, u string) kind {
	switch {
	case u == "Percent" && units.IsRate(id), u == config.UnitsRate:
		return kindRate
	case u == config.UnitsMillionsEUR:
		return kindBnEUR
	case u == config.UnitsPoints:
		return kindPx
	case strings.HasPrefix(u, "Euro per"):
		return kindEUR
	case u == config.UnitsFX:
		return kindFX
	case strings.HasPrefix(u, "Percent") || u == "Rate":
		return kindPct
	case u == "Basis Points":
		return kindBP
	case u == "Ratio":
		return kindRatio
	case u == "Dollars per Hour" || strings.HasPrefix(u, "Dollars per"):
		return kindUSD
	case strings.Contains(u, "Dollars"):
		return kindBn
	case u == "Thousands of Persons" || u == "Level in Thousands":
		return kindThous
	case u == "Number" || u == "Contracts":
		return kindCount
	}
	return kindIndex
}

// toBillions is the factor that turns stored amounts of kind k into
// billions of their currency.
func toBillions(k kind, unitsText string) (float64, error) {
	if k == kindBnEUR {
		return 0.001, nil // config.UnitsMillionsEUR is the only euro amount
	}
	return units.ToBillions(unitsText)
}

// level formats a level of kind k.
func (k kind) level(v transform.Value) string {
	if !v.OK {
		return dash
	}
	switch k {
	case kindRate:
		return rate2(v)
	case kindPct:
		return pct1(v)
	case kindBn:
		return billions(v, false)
	case kindBnEUR:
		return billionsNum(v, false) + " млрд €"
	case kindEUR:
		return num(v.V, 2, false) + " €"
	case kindFX:
		return num(v.V, 4, false)
	case kindThous:
		return num(v.V, 0, false) + " тыс."
	case kindCount:
		return num(v.V, 0, false)
	case kindUSD:
		return num(v.V, 2, false) + " $"
	case kindUSD0:
		return num(v.V, 0, false) + " $"
	case kindRatio:
		return num(v.V, 2, false)
	case kindBP:
		return num(v.V, 0, false) + " б.п."
	case kindPx:
		if math.Abs(v.V) >= 1000 {
			return num(v.V, 0, false)
		}
		return num(v.V, 2, false)
	}
	return num(v.V, 1, false)
}

// shownDecimals is how many decimals level writes for kind k; ok is false
// where that depends on the value (amounts, quotes).
func (k kind) shownDecimals() (dec int, ok bool) {
	switch k {
	case kindRate, kindUSD, kindRatio, kindEUR:
		return 2, true
	case kindPct, kindIndex:
		return 1, true
	case kindThous, kindCount, kindUSD0, kindBP:
		return 0, true
	case kindFX:
		return 4, true
	}
	return 0, false
}

// change formats a change of kind k; withUnit adds the unit where the
// column does not state it.
func (k kind) change(v transform.Value, withUnit bool) string {
	if !v.OK {
		return dash
	}
	unit := func(s, u string) string {
		if withUnit {
			return s + " " + u
		}
		return s
	}
	switch k {
	case kindRate, kindBP:
		return unit(bp(v), "б.п.")
	case kindPct:
		return unit(pp(v), "п.п.")
	case kindBn:
		if withUnit {
			return billions(v, true)
		}
		return billionsNum(v, true)
	case kindBnEUR:
		if withUnit {
			return billionsNum(v, true) + " млрд €"
		}
		return billionsNum(v, true)
	case kindThous:
		return unit(num(v.V, 0, true), "тыс.")
	case kindCount:
		return num(v.V, 0, true)
	case kindUSD, kindRatio, kindPx, kindEUR:
		return num(v.V, 2, true)
	case kindUSD0:
		return num(v.V, 0, true)
	case kindFX:
		return num(v.V, 4, true)
	}
	return num(v.V, 1, true)
}

// ref names a stored series.
type ref struct{ Source, ID string }

// view is a series as of the export: its observations up to the end of the
// period and the transforms the export shows.
type view struct {
	ref
	Label string
	Freq  string
	Kind  kind
	Codes []string // configured transforms; cells show only these
	// Factor converts stored values to the shown ones (dollars → billions).
	Factor float64
	Obs    []model.Observation
	Last   time.Time // date of the last observation, zero if none
	// LastSeen is when the last observation's value became known (first
	// fetch or revision, up to the period end); LastInitial: it came with
	// the initial history load.
	LastSeen    time.Time
	LastInitial bool
	V           map[string]transform.Value
	Flags       []string
	// Quality is config.Quality* of the series; empty reads as official.
	Quality string
	// Relative: the weekly changes are % changes (prices, index ratios).
	Relative bool
}

// has reports whether code is configured for the series.
func (v view) has(code string) bool { return slices.Contains(v.Codes, code) }

// get returns a configured transform, absent otherwise.
func (v view) get(code string) transform.Value {
	if !v.has(code) {
		return transform.Value{}
	}
	return v.V[code]
}

// level is the last value.
func (v view) level() transform.Value { return v.V[transform.Level] }

// saarSeries are FRED series stored as a seasonally adjusted annual rate.
var saarSeries = map[string]bool{"A091RC1Q027SBEA": true, "GCE": true}

func (v view) stale() bool { return slices.Contains(v.Flags, transform.FlagStale) }

// allCodes are computed for every series; cells pick by Codes.
var allCodes = []string{
	transform.Level, transform.D, transform.Diff, transform.D1W, transform.D4W, transform.MoM, transform.YoY,
	transform.A3M, transform.A6M, transform.QoQA, transform.QoQ, transform.Avg3, transform.Avg4W, transform.Sum12M, transform.Sum12MYoY,
	transform.Z, transform.MA50, transform.MA200,
}

// spec is what the export knows about a series beyond the store.
type spec struct {
	Label    string
	Codes    []string
	Kind     *kind    // nil: from units
	FlagIfGt *float64 // flag_if_gt
	Relative bool     // d1w/d4w as % changes (prices)
	// ShortHistory: no z until transform.MinZShortHistory observations.
	ShortHistory bool
}

// load builds the view of r as of the period.
func (w *week) load(ctx context.Context, r ref, sp spec) (view, error) {
	v := view{ref: r, Label: sp.Label, Codes: sp.Codes, Factor: 1, Relative: sp.Relative}
	meta, ok, err := w.st.Meta(ctx, r.Source, r.ID)
	if err != nil {
		return v, err
	}
	if v.Label == "" {
		v.Label = r.ID
		if ok && meta.Label != "" {
			v.Label = meta.Label
		}
	}
	v.Freq = strings.ToLower(meta.Frequency)
	v.Kind = kindOfUnits(r.ID, meta.Units)
	if sp.Kind != nil {
		v.Kind = *sp.Kind
	}
	known, err := w.known(ctx, r.Source, r.ID, time.Time{})
	if err != nil {
		return v, err
	}
	obs := plain(known)
	if v.Kind == kindBn || v.Kind == kindBnEUR {
		f, err := toBillions(v.Kind, meta.Units)
		if err != nil {
			return v, fmt.Errorf("export %s/%s: %w", r.Source, r.ID, err)
		}
		for i := range obs {
			obs[i].Value *= f
		}
		v.Factor = f
	}
	scale := 1.0
	if v.Kind == kindRate {
		scale = 100
	}
	tin := transform.Input{
		Obs: obs, Freq: v.Freq, AsOf: w.asOf, ChangeScale: scale, RelativeChanges: sp.Relative, ShortHistory: sp.ShortHistory,
		ZWindowDailyWeekly: w.cfg.Metrics.ZDailyWeekly, ZWindowMonthlyQuarterly: w.cfg.Metrics.ZMonthlyQuarterly,
	}
	res, err := transform.Compute(tin, allCodes)
	if err != nil {
		return v, fmt.Errorf("export %s/%s: %w", r.Source, r.ID, err)
	}
	have, short := transform.ZHistory(tin)
	zPending := short && have > 0 && slices.Contains(sp.Codes, transform.Z)
	v.V, v.Last = res.Values, res.Date
	for len(obs) > 0 && obs[len(obs)-1].Date.After(w.asOf) {
		obs = obs[:len(obs)-1]
	}
	v.Obs = obs
	// A change next to shown levels is the difference of the shown
	// (rounded) levels: 122.4 → 121.2 is −1.2, not −1.3.
	if dec, ok := v.Kind.shownDecimals(); ok && len(obs) >= 2 && obs[len(obs)-1].Date.Equal(v.Last) {
		p := math.Pow(10, float64(dec))
		round := func(x float64) float64 { return math.Round(x*p) / p }
		d := round(obs[len(obs)-1].Value) - round(obs[len(obs)-2].Value)
		if v.V[transform.D].OK {
			v.V[transform.D] = some(d * scale)
		}
		if v.V[transform.Diff].OK {
			v.V[transform.Diff] = some(d)
		}
	}
	if v.Last.IsZero() {
		return v, nil
	}
	last := known[len(obs)-1]
	v.LastSeen, v.LastInitial = last.Seen, last.Initial
	revs, err := w.st.Revisions(ctx, r.Source, r.ID, w.from, w.to)
	if err != nil {
		return v, err
	}
	revised := false
	for _, rv := range revs {
		_, shown := v.revision(rv)
		revised = revised || shown
	}
	in := transform.FlagInput{
		Freq: v.Freq, Last: v.Last, Value: v.level().V, AsOf: w.asOf, Revised: revised, FlagIfGt: sp.FlagIfGt,
	}
	day, ok, next, err := w.releaseDay(ctx, r.ID)
	if err != nil {
		return v, err
	}
	if ok {
		in.ReleaseDay, in.Seen = day, v.LastSeen
	}
	in.NextRelease = next
	v.Flags = transform.Flags(in)
	if zPending {
		// The z cell is a number: the note goes to the flags of the row.
		v.Flags = append(v.Flags, fmt.Sprintf("z: копим историю (%d/%d)", have, transform.MinZShortHistory))
	}
	return v, nil
}

// revision is the "old → new" cell of a revision; shown is false when both
// round to the same shown level (131.6 → 131.6), which is not a revision to
// the reader.
func (v view) revision(r store.Revision) (cell string, shown bool) {
	o, n := v.Kind.level(some(r.Old*v.Factor)), v.Kind.level(some(r.New*v.Factor))
	if o == n {
		return dash, false
	}
	return o + " → " + n, true
}

// known reads a series as it was known at the period end, so a past week
// exports the same files later: data fetched and revisions detected after
// the period are left out (§17.1).
func (w *week) known(ctx context.Context, source, id string, since time.Time) ([]store.KnownObservation, error) {
	return w.st.ObservationsAsOf(ctx, source, id, since, w.to)
}

// observations is known without the fetch times.
func (w *week) observations(ctx context.Context, source, id string, since time.Time) ([]model.Observation, error) {
	k, err := w.known(ctx, source, id, since)
	if err != nil {
		return nil, err
	}
	return plain(k), nil
}

func plain(k []store.KnownObservation) []model.Observation {
	out := make([]model.Observation, len(k))
	for i, o := range k {
		out[i] = o.Observation
	}
	return out
}

// resolve loads a configured series by the name a table gives it: its id,
// or source/id where two sources share an id. The source decides how the
// series is read; extra codes are added to its configured transforms (e.g.
// d for a "Изменение" column).
func (w *week) resolve(ctx context.Context, name string, extra ...string) (view, error) {
	def, ok := w.cfg.ResolveSeries(name)
	if !ok {
		return view{}, fmt.Errorf("export: %s is not a configured series", name)
	}
	var (
		v   view
		err error
	)
	switch def.Source {
	case config.SourceYahoo:
		v, err = w.quote(ctx, def.ID)
	case config.SourceTreasury:
		v, err = w.load(ctx, ref{def.Source, def.ID}, treasurySpecs[def.ID])
	default:
		label := def.Label
		if label == "" && def.Source == config.SourceDerived {
			label = derivedLabels[def.ID]
		}
		if label == "" && def.Source == config.SourceDerived {
			label = def.ID
		}
		sp := spec{Label: label, Codes: append(slices.Clone(def.Transforms), extra...), FlagIfGt: def.FlagIfGt, ShortHistory: def.ShortHistory}
		if def.Source == config.SourceDerived {
			sp.Relative = config.DerivedFormulas[def.ID].Relative
		}
		v, err = w.load(ctx, ref{def.Source, def.ID}, sp)
		if err == nil && def.Source == config.SourceDerived {
			v.Flags = append(v.Flags, w.fallbackFlags(def.ID)...)
		}
	}
	if err != nil {
		return v, err
	}
	v.Quality = def.Quality
	if def.Forecast {
		if f, ok, err := w.forecastFlag(ctx, def, v); err != nil {
			return v, err
		} else if ok {
			v.Flags = append(v.Flags, f)
		}
	}
	if mark := qualityFlags[def.Quality]; mark != "" {
		v.Flags = append(v.Flags, mark)
	}
	return v, nil
}

// fallbackFlags says that a derived series was computed with a stand-in
// input (derived.FallbackMark in its fetch_state cursor): the Bund leg of
// BTP-Bund taken from the Bundesbank when the MTS yield is gone (D25).
func (w *week) fallbackFlags(id string) []string {
	_, standIn, ok := strings.Cut(w.states[derived.Source+":"+id].Cursor, derived.FallbackMark)
	if !ok {
		return nil
	}
	label := standIn
	if def, ok := w.cfg.ResolveSeries(standIn); ok && def.Label != "" {
		label = def.Label + ", " + def.Source
	}
	return []string{"запасная нога: " + label}
}

// qualityFlags mark in the files the values that are not the source's own
// figure of the indicator.
var qualityFlags = map[string]string{config.QualityProxy: "прокси", config.QualityManual: "ручной ввод"}

// fred loads a configured FRED or derived series.
func (w *week) fred(ctx context.Context, id string, extra ...string) (view, error) {
	return w.resolve(ctx, id, extra...)
}

// derivedLabels name the derived series in the files.
var derivedLabels = map[string]string{
	"NET_LIQUIDITY": "Net Liquidity", "U_V": "U/V", "SOFR_IORB": "SOFR − IORB",
	"UST_2S10S": "2s10s", "UST_3M10Y": "3m10y",
}

// views loads several FRED/derived series.
func (w *week) views(ctx context.Context, ids ...string) ([]view, error) {
	out := make([]view, 0, len(ids))
	for _, id := range ids {
		v, err := w.fred(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// forecastFlag names the last point of a series published ahead (the ECB
// wage tracker): "прогноз на 2027-09: 2.3%". The row itself shows the value
// at the period end, the fact.
func (w *week) forecastFlag(ctx context.Context, def config.SeriesDef, v view) (string, bool, error) {
	known, err := w.known(ctx, def.Source, def.ID, w.asOf)
	if err != nil {
		return "", false, err
	}
	if len(known) == 0 {
		return "", false, nil
	}
	last := known[len(known)-1].Observation
	if !last.Date.After(w.asOf) {
		return "", false, nil
	}
	period := last.Date.Format("2006-01")
	if v.Freq == "q" {
		period = fmt.Sprintf("%d-Q%d", last.Date.Year(), (int(last.Date.Month())+2)/3)
	}
	return "прогноз на " + period + ": " + v.Kind.level(some(last.Value*v.Factor)), true, nil
}
