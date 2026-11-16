package export

import (
	"fmt"
	"slices"

	"macro-collector/internal/config"
	"macro-collector/internal/transform"
)

// Scale is how the dashboard shows a stored series: the units of the reports.
type Scale struct {
	Factor      float64 // multiplies the stored levels
	ChangeScale float64 // transform.Input.ChangeScale
	Relative    bool    // transform.Input.RelativeChanges
	unit        string
	changeUnit  string
}

// ScaleOf is the Scale of a series with the stored units text.
func ScaleOf(source, id, unitsText string) (Scale, error) {
	k, relative := kindOfUnits(id, unitsText), false
	if source == "yahoo" {
		k, relative = yahooKind(id, unitsText)
	}
	if source == config.SourceDerived {
		relative = config.DerivedFormulas[id].Relative
	}
	s := Scale{Factor: 1, ChangeScale: 1, unit: k.levelUnit(), changeUnit: k.changeUnit()}
	if k == kindBn || k == kindBnEUR {
		f, err := toBillions(k, unitsText)
		if err != nil {
			return Scale{}, fmt.Errorf("export scale %s/%s: %w", source, id, err)
		}
		s.Factor = f
	}
	if k == kindRate {
		s.ChangeScale = 100
	}
	s.Relative = relative
	return s, nil
}

// UnitOf is the unit of the values of a transform code.
func (s Scale) UnitOf(code string) string {
	switch {
	case slices.Contains([]string{transform.Level, transform.Avg4W, transform.Sum12M}, code):
		return s.unit
	case slices.Contains([]string{transform.D, transform.D1W, transform.D4W}, code):
		if s.Relative {
			return unitPercent
		}
		return s.changeUnit
	case code == transform.Diff || code == transform.Avg3:
		return s.unit
	case code == transform.Z:
		return "z"
	}
	return unitPercent
}
