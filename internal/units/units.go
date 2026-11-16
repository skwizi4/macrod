// Package units holds the expected units of series used in calculations
// (ТЗ §17.4). Values are FRED's `units` strings, checked against fred/series
// on 2026-10-24.
package units

import (
	"fmt"
	"strings"
)

// FRED unit strings.
const (
	MillionsUSD    = "Millions of U.S. Dollars"
	MillionsDollar = "Millions of Dollars"
	BillionsUSD    = "Billions of US Dollars"
	Thousands      = "Thousands of Persons"
	LevelThousands = "Level in Thousands"
	Number         = "Number"
	Percent        = "Percent"
	// Dollars are Treasury MTS amounts (treasury.UnitsUSD).
	Dollars = "U.S. Dollars"
)

// ExpectedFRED maps a FRED series id to the units the calculations assume.
// A series not listed here is not checked.
var ExpectedFRED = map[string]string{
	"WALCL":       MillionsUSD,
	"WDTGAL":      MillionsUSD,
	"WRESBAL":     MillionsUSD,
	"RRPONTSYD":   BillionsUSD,
	"RPONTSYD":    BillionsUSD,
	"MTSDS133FMS": MillionsDollar,
	"PAYEMS":      Thousands,
	"UNEMPLOY":    Thousands,
	"JTSJOL":      LevelThousands,
	"ICSA":        Number,
	"CCSA":        Number,
	// Rates and yields: changes are reported in basis points.
	"DFF":          Percent,
	"SOFR":         Percent,
	"IORB":         Percent,
	"DGS3MO":       Percent,
	"DGS2":         Percent,
	"DGS3":         Percent,
	"DGS5":         Percent,
	"DGS7":         Percent,
	"DGS10":        Percent,
	"DGS20":        Percent,
	"DGS30":        Percent,
	"DFII10":       Percent,
	"T5YIE":        Percent,
	"T5YIFR":       Percent,
	"T10YIE":       Percent,
	"THREEFYTP10":  Percent,
	"BAMLH0A0HYM2": Percent,
	"BAMLC0A0CM":   Percent,
}

// ToBillions returns the factor that converts a dollar amount in the given
// FRED units to billions of dollars.
func ToBillions(u string) (float64, error) {
	switch u {
	case MillionsUSD, MillionsDollar:
		return 0.001, nil
	case BillionsUSD, "Billions of U.S. Dollars", "Billions of Dollars":
		return 1, nil
	case Dollars:
		return 1e-9, nil
	}
	// "Billions of Chained 2017 Dollars" and the like.
	switch {
	case strings.HasPrefix(u, "Billions of ") && strings.HasSuffix(u, "Dollars"):
		return 1, nil
	case strings.HasPrefix(u, "Millions of ") && strings.HasSuffix(u, "Dollars"):
		return 0.001, nil
	}
	return 0, fmt.Errorf("units %q: not a dollar amount in millions or billions", u)
}

// CheckFRED returns an error if id is listed and got differs from the
// expected units.
func CheckFRED(id, got string) error {
	want, ok := ExpectedFRED[id]
	if !ok || got == want {
		return nil
	}
	return fmt.Errorf("units mismatch for %s: FRED reports %q, ТЗ §17.4 expects %q", id, got, want)
}

// IsRate reports whether id is a rate, yield or spread in percent: its
// changes are shown in basis points (§17.4).
func IsRate(id string) bool { return ExpectedFRED[id] == Percent }
