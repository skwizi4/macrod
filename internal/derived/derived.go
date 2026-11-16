package derived

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
	"macro-collector/internal/units"
)

// Source is the observations.source of derived series.
const Source = "derived"

// defaultSource is where an input is read from when the Computer has no
// config to look the series up in.
const defaultSource = "fred"

// fallbackLag: an input with a stand-in is given up when its last
// observation is this much older than the other leg's.
const fallbackLag = 5 * 24 * time.Hour

// FallbackMark precedes, in the fetch_state cursor of a derived series, the
// name of the stand-in series its last computation used.
const FallbackMark = " fallback:"

// Units stored in series_meta for derived series.
const (
	UnitsBillionsUSD = "Billions of U.S. Dollars"
	UnitsBasisPoints = "Basis Points"
	UnitsRatio       = "Ratio"
	UnitsIndex       = "Index"
	UnitsMillionsEUR = config.UnitsMillionsEUR
)

// inputs maps an input name to its observations, already scaled.
type inputs map[string][]model.Observation

// formula is one implemented derived series: what the config knows about it
// (the formula text and the inputs, of any source) and how it is computed.
type formula struct {
	config.DerivedFormula
	Dollars   bool // inputs are dollar amounts, converted to billions
	Frequency string
	Units     string
	Compute   func(in inputs) []model.Observation
}

func spread(name string) formula {
	def := config.DerivedFormulas[name]
	a, b := def.Inputs[0], def.Inputs[1]
	return formula{
		DerivedFormula: def, Frequency: "d", Units: UnitsBasisPoints,
		Compute: func(in inputs) []model.Observation { return SpreadBP(in[a], in[b]) },
	}
}

// Formulas are the derived series stored as observations, by the names of
// config.DerivedFormulas. FED_PATH is computed when exporting
// (ComputeFedPath): its contracts change with the as-of date.
var Formulas = map[string]formula{
	"NET_LIQUIDITY": {
		DerivedFormula: config.DerivedFormulas["NET_LIQUIDITY"],
		Dollars:        true, Frequency: "w", Units: UnitsBillionsUSD,
		Compute: func(in inputs) []model.Observation {
			return NetLiquidity(in["WALCL"], in["WDTGAL"], in["RRPONTSYD"])
		},
	},
	"U_V": {
		DerivedFormula: config.DerivedFormulas["U_V"],
		Frequency:      "m", Units: UnitsRatio,
		Compute: func(in inputs) []model.Observation { return Ratio(in["UNEMPLOY"], in["JTSJOL"]) },
	},
	"SOFR_IORB":       spread("SOFR_IORB"),
	"UST_2S10S":       spread("UST_2S10S"),
	"UST_3M10Y":       spread("UST_3M10Y"),
	"BTP_BUND":        spread("BTP_BUND"),
	"OAT_BUND":        spread("OAT_BUND"),
	"UST_BUND":        spread("UST_BUND"),
	"BUND_2S10S":      spread("BUND_2S10S"),
	"ECB_ALL_AAA_10Y": spread("ECB_ALL_AAA_10Y"),
	"ESTR_DFR":        spread("ESTR_DFR"),
	// Net exports of goods and services, current prices (Eurostat has no
	// B11 line in namq_10_gdp; P6 − P7 is the same balance).
	"NETEXP_EA": {
		DerivedFormula: config.DerivedFormulas["NETEXP_EA"],
		Frequency:      "q", Units: UnitsMillionsEUR,
		Compute: func(in inputs) []model.Observation { return Combine(in["EXPORTS_EA"], in["IMPORTS_EA"], -1) },
	},
	// Excess liquidity, approximately: banks' current accounts with the
	// Eurosystem plus the deposit facility (minimum reserves, about 1% of
	// it, are not subtracted: the weekly statement does not show them).
	"EXCESS_LIQ": {
		DerivedFormula: config.DerivedFormulas["EXCESS_LIQ"],
		Frequency:      "w", Units: UnitsMillionsEUR,
		Compute: func(in inputs) []model.Observation { return Combine(in["ILM_CURRENT_ACC"], in["ILM_DEPOSIT_FAC"], 1) },
	},
	"GER40_NDX": {
		DerivedFormula: config.DerivedFormulas["GER40_NDX"],
		Frequency:      "d", Units: UnitsIndex,
		Compute: func(in inputs) []model.Observation { return Normalize(Ratio(in["^GDAXI"], in["^NDX"])) },
	},
}

// Outcome of one derived series.
type Outcome string

const (
	OutcomeUpdated        Outcome = "updated"
	OutcomeNotImplemented Outcome = "not_implemented"
	OutcomeAtExport       Outcome = "at_export" // computed by the export, not stored
	OutcomeFailed         Outcome = "failed"
)

// Result is the result of one derived series.
type Result struct {
	Name    string
	Outcome Outcome
	Rows    int // computed observations
	// Fallback names the stand-in series used for an input that had no
	// data (BTP_BUND against the Bundesbank Bund yield); empty normally.
	Fallback string
	store.UpsertResult
	Err error
}

// Computer recomputes derived series from stored inputs.
type Computer struct {
	Store *store.Store
	// Config tells which source an input comes from; nil: every input is
	// a FRED series.
	Config *config.Config
	Defs   []config.Derived
	Log    *slog.Logger
	Now    func() time.Time
}

// Run recomputes every implemented series in Defs over the full history.
// A failure (missing input, unexpected units) is recorded in fetch_state
// under "derived:<NAME>" and does not stop the others. Cancelling ctx
// stops the run without recording anything.
func (c *Computer) Run(ctx context.Context) []Result {
	var out []Result
	for _, def := range c.Defs {
		if ctx.Err() != nil {
			break
		}
		f, ok := Formulas[def.Name]
		if def.Name == FedPathName {
			out = append(out, Result{Name: def.Name, Outcome: OutcomeAtExport})
			continue
		}
		if !ok {
			out = append(out, Result{Name: def.Name, Outcome: OutcomeNotImplemented})
			continue
		}
		res := c.runOne(ctx, def, f)
		if res.Err != nil && ctx.Err() != nil {
			break
		}
		key := Source + ":" + def.Name
		if res.Err != nil {
			c.Log.Error("derived failed", "series", def.Name, "err", res.Err)
			if err := c.Store.Write(ctx, func(tx *sql.Tx) error {
				return store.SetFetchError(tx, key, res.Err, c.now())
			}); err != nil {
				c.Log.Error("record derived error", "series", def.Name, "err", err)
			}
		} else {
			c.Log.Info("derived done", "series", def.Name, "rows", res.Rows,
				"inserted", res.Inserted, "revised", res.Revised)
		}
		out = append(out, res)
	}
	return out
}

func (c *Computer) runOne(ctx context.Context, def config.Derived, f formula) Result {
	res := Result{Name: def.Name, Outcome: OutcomeFailed}
	if def.Formula != f.Formula {
		res.Err = fmt.Errorf("derived %s: config formula %q differs from implemented %q", def.Name, def.Formula, f.Formula)
		return res
	}
	in := make(inputs, len(f.Inputs))
	for _, name := range f.Inputs {
		obs, err := c.input(ctx, name, f.Dollars)
		if err != nil {
			res.Err = fmt.Errorf("derived %s: %w", def.Name, err)
			return res
		}
		in[name] = obs
	}
	for _, name := range f.Inputs {
		standIn, ok := f.Fallback[name]
		if !ok || !gone(in[name], lastOther(in, f.Inputs, name)) {
			continue
		}
		obs, err := c.input(ctx, standIn, f.Dollars)
		if err != nil || len(obs) == 0 {
			continue // no stand-in either: the check below reports the input
		}
		in[name], res.Fallback = obs, standIn
	}
	for _, name := range f.Inputs {
		if len(in[name]) == 0 {
			res.Err = fmt.Errorf("derived %s: input %s has no observations", def.Name, name)
			return res
		}
	}

	computed := f.Compute(in)
	if len(computed) == 0 {
		res.Err = fmt.Errorf("derived %s: inputs share no dates", def.Name)
		return res
	}
	res.Rows = len(computed)
	last := computed[len(computed)-1].Date.Format(time.DateOnly)
	if res.Fallback != "" {
		last += FallbackMark + res.Fallback
	}
	err := c.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
			Source: Source, SeriesID: def.Name, Label: def.Name, Agent: def.Agent,
			Frequency: f.Frequency, Units: f.Units,
		}); err != nil {
			return err
		}
		up, err := store.UpsertObservations(tx, Source, def.Name, computed, c.now())
		if err != nil {
			return err
		}
		res.UpsertResult = up
		return store.SetFetchOK(tx, Source+":"+def.Name, last, c.now())
	})
	if err != nil {
		res.Err = fmt.Errorf("derived %s store: %w", def.Name, err)
		return res
	}
	res.Outcome = OutcomeUpdated
	return res
}

// Uses reports whether a configured derived series reads an input of
// source: after a fetch of that source the derived series are recomputed.
func Uses(cfg *config.Config, source string) bool {
	for _, d := range cfg.Derived {
		f := config.DerivedFormulas[d.Name]
		names := append([]string{}, f.Inputs...)
		for _, standIn := range f.Fallback {
			names = append(names, standIn)
		}
		for _, name := range names {
			if def, ok := cfg.ResolveSeries(name); ok && def.Source == source {
				return true
			}
		}
	}
	return false
}

// input reads the observations of an input series from its source; an
// input that is not configured or not loaded yet has none.
func (c *Computer) input(ctx context.Context, name string, dollars bool) ([]model.Observation, error) {
	source, id := defaultSource, name
	if c.Config != nil {
		def, ok := c.Config.ResolveSeries(name)
		if !ok {
			return nil, nil
		}
		source, id = def.Source, def.ID
	}
	obs, err := c.Store.Observations(ctx, source, id, time.Time{})
	if err != nil || len(obs) == 0 || !dollars {
		return obs, err
	}
	u, _, err := c.Store.SeriesUnits(ctx, source, id)
	if err != nil {
		return nil, err
	}
	factor, err := units.ToBillions(u)
	if err != nil {
		return nil, fmt.Errorf("input %s: %w", name, err)
	}
	return Scale(obs, factor), nil
}

// gone reports whether an input is unusable: empty, or stopped more than
// fallbackLag before the other leg's last observation.
func gone(obs []model.Observation, other time.Time) bool {
	if len(obs) == 0 {
		return true
	}
	return !other.IsZero() && obs[len(obs)-1].Date.Before(other.Add(-fallbackLag))
}

// lastOther is the latest observation date among the inputs other than name.
func lastOther(in inputs, names []string, name string) time.Time {
	var last time.Time
	for _, n := range names {
		if o := in[n]; n != name && len(o) > 0 && o[len(o)-1].Date.After(last) {
			last = o[len(o)-1].Date
		}
	}
	return last
}

func (c *Computer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
