package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Sources of the stored numeric series.
const (
	SourceFRED     = "fred"
	SourceDerived  = "derived"
	SourceYahoo    = "yahoo"
	SourceTreasury = "treasury"
)

// Quality says how far a value is from the indicator its row names (spec
// 6.1): the source's own figure, a stand-in for the indicator, or a number
// the user typed in.
const (
	QualityOfficial = "official"
	QualityProxy    = "proxy"
	QualityManual   = "manual"
)

// Qualities are the values of the quality key.
var Qualities = []string{QualityOfficial, QualityProxy, QualityManual}

// SeriesDef is a configured numeric series: what the reports and the
// dashboard know about it beyond the store. A series is addressed by its
// source and id; the id alone is enough while it is unique.
type SeriesDef struct {
	Source, ID string
	Label      string
	LabelEn    string // the dashboard in English; empty: Label
	Transforms []string
	FlagIfGt   *float64
	Quality    string
	// ShortHistory: the z-score waits for transform.MinZShortHistory
	// stored observations (spec 17.5).
	ShortHistory bool
	// Forecast: the source publishes values ahead of today.
	Forecast bool
}

// Ref is the full reference of the series: source/id.
func (s SeriesDef) Ref() string { return s.Source + "/" + s.ID }

// Series lists every configured numeric series. A new source adds its
// series here, and the tables, the key rows and the dashboard see them.
func (c *Config) Series() []SeriesDef {
	var out []SeriesDef
	for _, s := range c.FRED.Series {
		out = append(out, SeriesDef{Source: SourceFRED, ID: s.ID, Label: s.Label, LabelEn: s.LabelEn, Transforms: s.Transforms, FlagIfGt: s.FlagIfGt, Quality: quality(s.Quality)})
	}
	for _, d := range c.Derived {
		out = append(out, SeriesDef{Source: SourceDerived, ID: d.Name, Label: d.Label, LabelEn: d.LabelEn, Transforms: d.Transforms, FlagIfGt: d.FlagIfGt, Quality: quality(d.Quality), ShortHistory: d.ShortHistory})
	}
	for _, y := range c.Yahoo.Symbols {
		out = append(out, SeriesDef{Source: SourceYahoo, ID: y.Symbol, Label: y.Label, LabelEn: y.LabelEn, Transforms: y.Transforms, Quality: quality(y.Quality), ShortHistory: y.ShortHistory})
	}
	for _, id := range TreasurySeries {
		out = append(out, SeriesDef{Source: SourceTreasury, ID: id, Quality: QualityOfficial})
	}
	for _, src := range c.NumericSources() {
		if !src.Enabled {
			continue
		}
		for _, s := range src.Series {
			out = append(out, SeriesDef{Source: src.Name, ID: s.ID, Label: s.Label, LabelEn: s.LabelEn, Transforms: s.Transforms, FlagIfGt: s.FlagIfGt, Quality: quality(s.Quality), Forecast: s.Forecast})
		}
	}
	for _, s := range c.Manual.Series {
		q := s.Quality
		if q == "" {
			q = QualityManual
		}
		out = append(out, SeriesDef{Source: SourceManual, ID: s.ID, Label: s.Label, LabelEn: s.LabelEn, Transforms: s.Transforms, FlagIfGt: s.FlagIfGt, Quality: q})
	}
	return out
}

func quality(q string) string {
	if q == "" {
		return QualityOfficial
	}
	return q
}

// ResolveSeries finds a series by the name a table, a key row or a formula
// gives it: "source/id", or the id alone.
func (c *Config) ResolveSeries(name string) (SeriesDef, bool) {
	all := c.Series()
	if source, id, ok := strings.Cut(name, "/"); ok {
		for _, s := range all {
			if s.Source == source && s.ID == id {
				return s, true
			}
		}
	}
	for _, s := range all {
		if s.ID == name {
			return s, true
		}
	}
	return SeriesDef{}, false
}

// validateSeries checks what the sections cannot check on their own: an id
// used by two sources would make the short name ambiguous.
func (c *Config) validateSeries() error {
	var errs []error
	sources := map[string][]string{}
	for _, s := range c.Series() {
		sources[s.ID] = append(sources[s.ID], s.Source)
		if !slices.Contains(Qualities, s.Quality) {
			errs = append(errs, fmt.Errorf("%s: quality %q: want one of %s", s.Ref(), s.Quality, strings.Join(Qualities, ", ")))
		}
	}
	for _, id := range sortedKeys(sources) {
		if len(sources[id]) > 1 {
			errs = append(errs, fmt.Errorf("series %q is configured in %s: give each its own id", id, strings.Join(sources[id], " and ")))
		}
	}
	return errors.Join(errs...)
}
