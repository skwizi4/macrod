package export

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/transform"
)

// The report models (ТЗ §21.2, D18): every export is a Go structure, rendered
// to Markdown by a template and to JSON as it is. Every field needed by a
// template is in the JSON: no field of a model may be hidden from it.

// SchemaVersion is the version of the JSON contract, docs/schema/*.json.
const SchemaVersion = 1

// Units of a Quantity: raw values in the normalized units of §17.4.
const (
	unitPercent     = "percent"
	unitBP          = "bp"
	unitPP          = "pp"
	unitUSDBn       = "usd_bn"
	unitUSD         = "usd"
	unitEURBn       = "eur_bn"
	unitEUR         = "eur"
	unitThous       = "thousands_persons"
	unitCount       = "count"
	unitIndex       = "index"
	unitRatio       = "ratio"
	unitSigma       = "sigma"
	unitMoves       = "moves_25bp"
	unitCorrelation = "correlation"
	unitTimes       = "times"
)

var kindNames = [...]string{
	kindIndex: "index", kindRate: "rate", kindPct: "pct", kindBn: "bn", kindThous: "thous", kindCount: "count",
	kindUSD: "usd", kindRatio: "ratio", kindBP: "bp", kindPx: "px", kindUSD0: "usd0",
	kindBnEUR: "bn_eur", kindEUR: "eur", kindFX: "fx",
}

// MarshalText implements encoding.TextMarshaler: kind is how the Markdown
// writes a row's values, and part of the contract.
func (k kind) MarshalText() ([]byte, error) {
	if int(k) < 0 || int(k) >= len(kindNames) {
		return nil, fmt.Errorf("export: unknown kind %d", int(k))
	}
	return []byte(kindNames[k]), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (k *kind) UnmarshalText(b []byte) error {
	for i, n := range kindNames {
		if n == string(b) {
			*k = kind(i)
			return nil
		}
	}
	return fmt.Errorf("export: unknown kind %q", b)
}

// levelUnit is the unit of a level of kind k.
func (k kind) levelUnit() string {
	switch k {
	case kindRate, kindPct:
		return unitPercent
	case kindBn:
		return unitUSDBn
	case kindThous:
		return unitThous
	case kindCount:
		return unitCount
	case kindUSD, kindUSD0:
		return unitUSD
	case kindBnEUR:
		return unitEURBn
	case kindEUR:
		return unitEUR
	case kindRatio:
		return unitRatio
	case kindBP:
		return unitBP
	}
	return unitIndex
}

// changeUnit is the unit of a change of kind k, in the series' own terms.
func (k kind) changeUnit() string {
	switch k {
	case kindRate, kindBP:
		return unitBP
	case kindPct:
		return unitPP
	}
	return k.levelUnit()
}

// Quantity is one raw number with its unit; Value is null when it is
// missing.
type Quantity struct {
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
}

// qty is a Quantity of v: a missing, NaN or infinite value is null.
func qty(v transform.Value, unit string) Quantity {
	if !v.OK || math.IsNaN(v.V) || math.IsInf(v.V, 0) {
		return Quantity{Unit: unit}
	}
	x := v.V
	return Quantity{Value: &x, Unit: unit}
}

// val is q as a transform value, for the formatters.
func (q Quantity) val() transform.Value {
	if q.Value == nil {
		return transform.Value{}
	}
	return some(*q.Value)
}

// Tagged is a value with the code of the transform it is: one cell of a
// table may show different transforms by row.
type Tagged struct {
	Code  string   `json:"code"`
	Value Quantity `json:"value"`
}

// Day is a calendar date: "2026-10-30" in JSON, null when zero. Its wall
// clock is the date, whatever the location.
type Day struct{ time.Time }

// MarshalJSON implements json.Marshaler.
func (d Day) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(d.Format(time.DateOnly))
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Day) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == nil {
		d.Time = time.Time{}
		return nil
	}
	t, err := time.Parse(time.DateOnly, *s)
	if err != nil {
		return fmt.Errorf("export: day %q: %w", *s, err)
	}
	d.Time = t
	return nil
}

// Period is the period of a file (§17.1), its times in the location TZ.
type Period struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	TZ   string    `json:"tz"`
}

// Header is the front matter and the title of a file (§12.1). A nil
// StaleSeries means the file does not report stale series at all.
type Header struct {
	SchemaVersion int       `json:"schema_version"`
	Region        string    `json:"region"`
	Agent         string    `json:"agent"`
	File          string    `json:"file,omitempty"`
	ISOWeek       string    `json:"iso_week"`
	Period        Period    `json:"period"`
	GeneratedAt   time.Time `json:"generated_at"`
	Kind          string    `json:"kind"`
	SourcesOK     []string  `json:"sources_ok"`
	SourcesFailed []string  `json:"sources_failed"`
	StaleSeries   []string  `json:"stale_series"`
	Title         string    `json:"title"`
}

// Metric identifies a series in a table row.
type Metric struct {
	ID     string   `json:"id"`
	Source string   `json:"source"`
	Label  string   `json:"label"`
	Date   Day      `json:"date"`
	Freq   string   `json:"freq"`
	Kind   kind     `json:"kind"`
	Flags  []string `json:"flags"`
	// Quality is official, proxy (a stand-in for the indicator) or manual
	// (typed in by the user).
	Quality string `json:"quality"`
}

func metricOf(v view) Metric {
	q := v.Quality
	if q == "" {
		q = config.QualityOfficial
	}
	return Metric{ID: v.ID, Source: v.Source, Label: v.Label, Date: Day{v.Last}, Freq: v.Freq, Kind: v.Kind, Flags: append([]string{}, v.Flags...), Quality: q}
}

// levelQty is the last value of v in its unit.
func levelQty(v view) Quantity { return qty(v.level(), v.Kind.levelUnit()) }

// zQty is the z-score of v.
func zQty(v view) Quantity { return qty(v.get(transform.Z), unitSigma) }

// pctQty is a configured transform of v in percent.
func pctQty(v view, code string) Quantity { return qty(v.get(code), unitPercent) }
