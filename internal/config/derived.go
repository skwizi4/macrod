package config

import (
	"errors"
	"fmt"
	"strings"
)

// DerivedFormula is an implemented derived series (package derived computes
// it): the formula text the config must repeat, as a guard against a config
// that means something else, and the series it reads. An input is named the
// way a table names a series (id, or source/id) and may come from any
// source.
type DerivedFormula struct {
	Formula string
	Inputs  []string
	// Fallback names, for an input, the series that stands in when the
	// input has no data or has stopped updating; the result is flagged.
	Fallback map[string]string
	// Relative: weekly changes of the result are shown in percent.
	Relative bool
}

// DerivedFormulas are the derived series stored as observations, by name.
// FED_PATH is not here: the export computes it.
var DerivedFormulas = map[string]DerivedFormula{
	"NET_LIQUIDITY": {Formula: "WALCL - WDTGAL - RRPONTSYD", Inputs: []string{"WALCL", "WDTGAL", "RRPONTSYD"}},
	"U_V":           {Formula: "UNEMPLOY / JTSJOL", Inputs: []string{"UNEMPLOY", "JTSJOL"}},
	"SOFR_IORB":     spreadFormula("SOFR", "IORB"),
	"UST_2S10S":     spreadFormula("DGS10", "DGS2"),
	"UST_3M10Y":     spreadFormula("DGS10", "DGS3MO"),
	// Europe (spec 6.1, D25). Both legs of the country spreads are the
	// Euronext MTS yields of one source; the Bundesbank yield stands in
	// for the Bund leg only when the MTS one is gone.
	"BTP_BUND":        withFallback(spreadFormula("MIT10.AS", "MDE10.AS"), "MDE10.AS", "BUND_10Y"),
	"OAT_BUND":        withFallback(spreadFormula("MFR10.AS", "MDE10.AS"), "MDE10.AS", "BUND_10Y"),
	"UST_BUND":        spreadFormula("DGS10", "BUND_10Y"),
	"BUND_2S10S":      spreadFormula("BUND_10Y", "BUND_2Y"),
	"ECB_ALL_AAA_10Y": spreadFormula("ECB_YC_ALL_10Y", "ECB_YC_AAA_10Y"),
	"GER40_NDX":       {Formula: "^GDAXI / ^NDX (нормировано)", Inputs: []string{"^GDAXI", "^NDX"}, Relative: true},
	// Batch 11f: the money market spread, net exports and excess liquidity.
	"ESTR_DFR":   spreadFormula("ESTR", "ECB_DFR"),
	"NETEXP_EA":  {Formula: "EXPORTS_EA - IMPORTS_EA", Inputs: []string{"EXPORTS_EA", "IMPORTS_EA"}},
	"EXCESS_LIQ": {Formula: "ILM_CURRENT_ACC + ILM_DEPOSIT_FAC", Inputs: []string{"ILM_CURRENT_ACC", "ILM_DEPOSIT_FAC"}},
}

func spreadFormula(a, b string) DerivedFormula {
	return DerivedFormula{Formula: a + " - " + b, Inputs: []string{a, b}}
}

func withFallback(f DerivedFormula, input, standIn string) DerivedFormula {
	f.Fallback = map[string]string{input: standIn}
	return f
}

// validateDerived checks every derived series against the implemented
// formulas: an unknown name would silently never be computed.
func (c *Config) validateDerived() error {
	var errs []error
	seen := map[string]bool{}
	for i, d := range c.Derived {
		where := fmt.Sprintf("derived[%d] %s", i, d.Name)
		if seen[d.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		seen[d.Name] = true
		if d.Name == KeyRowFedPath || d.Name == "" {
			continue
		}
		f, ok := DerivedFormulas[d.Name]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: unknown formula name (want one of %s, %s)", where, strings.Join(sortedKeys(DerivedFormulas), ", "), KeyRowFedPath))
			continue
		}
		if d.Formula != f.Formula {
			errs = append(errs, fmt.Errorf("%s: formula %q differs from the implemented %q", where, d.Formula, f.Formula))
		}
		for _, in := range f.Inputs {
			if s, ok := c.ResolveSeries(in); !ok || s.Source == SourceDerived {
				errs = append(errs, fmt.Errorf("%s: input %s is not a configured series", where, in))
			}
		}
	}
	return errors.Join(errs...)
}
