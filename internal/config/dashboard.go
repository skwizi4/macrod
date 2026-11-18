package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"macro-collector/internal/transform"
)

// Chart kinds of a dashboard page.
const (
	ChartLine  = "line"
	ChartBar   = "bar"
	ChartCurve = "curve" // the series are tenors: a yield curve now and compare_days ago
)

var chartKinds = []string{ChartLine, ChartBar, ChartCurve}

// Parts of a series that publishes ahead (chart series part).
const (
	PartActual   = "actual"
	PartForecast = "forecast"
)

// SourceRatio is the source of the price ratios of sentiment.ratios in chart
// references: ratio/RSP:SPY.
const SourceRatio = "ratio"

// viewReports are the dashboard views and the report kind each reads; "" is a
// view that reads no report.
var viewReports = map[string]string{
	"key_metrics":     ReportKeyMetrics,
	"status":          "",
	"fed_path":        ReportMonetary,
	"auctions":        ReportFiscal,
	"supply":          ReportFiscal,
	"capex":           ReportMicro,
	"financials":      ReportMicro,
	"reactions":       ReportMicro,
	"issuers":         ReportIssuers, // EU Micro (phase 11, batch 11f)
	"event_reactions": ReportSentiment,
	"surprises":       ReportNews,
	"topics":          ReportNews,
	"news_feed":       ReportNews,
	"calendar":        "",
	"reports":         "",
}

// ViewReport is the report kind a dashboard view reads ("" for none).
func ViewReport(view string) string { return viewReports[view] }

// DashboardConfig is the section dashboard: of the config.
type DashboardConfig struct {
	// DemoAllow lists the series references site --demo may publish: only
	// those whose licence allows redistribution (D21, D24).
	DemoAllow []string `yaml:"demo_allow"`
}

// RegionDashboard lists the dashboard pages of a region, in menu order.
type RegionDashboard struct {
	Pages []DashboardPage `yaml:"pages"`
}

// DashboardPage is one page: charts and views, in that order.
type DashboardPage struct {
	ID      string     `yaml:"id"`
	Title   string     `yaml:"title"`
	TitleEn string     `yaml:"title_en"` // dashboard in English; empty: title
	Charts  []Chart    `yaml:"charts"`
	Views   []PageView `yaml:"views"`
}

// Chart is one chart of a page.
type Chart struct {
	Title       string        `yaml:"title"`
	TitleEn     string        `yaml:"title_en"`
	Kind        string        `yaml:"kind"`
	CompareDays int           `yaml:"compare_days"` // curve: the earlier date
	Series      []ChartSeries `yaml:"series"`
}

// ChartSeries is one line of a chart.
type ChartSeries struct {
	Ref       string `yaml:"ref"`       // source/id
	Transform string `yaml:"transform"` // default level
	Label     string `yaml:"label"`     // default: the label of the series
	LabelEn   string `yaml:"label_en"`  // default: the English label of the series
	// Part of a series that publishes ahead (forecast: true): actual (up to
	// today) or forecast (after it); empty: all points.
	Part string `yaml:"part"`
}

// PageView is a table or block drawn by the frontend.
type PageView struct {
	View string `yaml:"view"`
}

// SeriesRef is a series the dashboard may ask for.
type SeriesRef struct {
	Source, ID, Label string
	LabelEn           string // empty: Label
}

// Ref is the reference of the series in charts and in demo_allow.
func (s SeriesRef) Ref() string { return s.Source + "/" + s.ID }

// SeriesCatalog is every series of the config by its reference.
func (c *Config) SeriesCatalog() map[string]SeriesRef {
	out := map[string]SeriesRef{}
	add := func(source, id, label, labelEn string) {
		s := SeriesRef{Source: source, ID: id, Label: label, LabelEn: labelEn}
		out[s.Ref()] = s
	}
	for _, s := range c.Series() {
		if s.Source == SourceDerived && s.ID == KeyRowFedPath { // not a stored series
			continue
		}
		label := s.Label
		if label == "" {
			label = s.ID
		}
		add(s.Source, s.ID, label, s.LabelEn)
	}
	for _, r := range c.Sentiment.Ratios {
		add(SourceRatio, r.Num+":"+r.Den, r.Label, r.LabelEn)
	}
	return out
}

// KeyRowRef is the series behind a key metrics row; ok is false for a row
// that is not a stored series (the FOMC path).
func (c *Config) KeyRowRef(row KeyRow) (string, bool) {
	s, ok := c.ResolveSeries(row.ID)
	if !ok || s.Source == SourceDerived && s.ID == KeyRowFedPath {
		return "", false
	}
	return s.Ref(), true
}

// validateDashboard checks every page reference, so a typo fails at startup
// and not as an empty chart.
func (c *Config) validateDashboard() error {
	var errs []error
	catalog := c.SeriesCatalog()
	allowed := map[string]bool{}
	for i, ref := range c.Dashboard.DemoAllow {
		if _, ok := catalog[ref]; !ok {
			errs = append(errs, fmt.Errorf("dashboard.demo_allow[%d]: unknown series %q (want source/id of a configured series)", i, ref))
		}
		if strings.HasPrefix(ref, SourceRatio+"/") || strings.HasPrefix(ref, "yahoo/") {
			errs = append(errs, fmt.Errorf("dashboard.demo_allow[%d]: %q: market prices cannot be redistributed", i, ref))
		}
		if allowed[ref] {
			errs = append(errs, fmt.Errorf("dashboard.demo_allow[%d]: duplicate %q", i, ref))
		}
		allowed[ref] = true
	}
	for i := range c.Regions {
		r := &c.Regions[i]
		ids := map[string]bool{}
		for j := range r.Dashboard.Pages {
			p := &r.Dashboard.Pages[j]
			where := fmt.Sprintf("regions[%d].dashboard.pages[%d]", i, j)
			if !regionID.MatchString(p.ID) {
				errs = append(errs, fmt.Errorf("%s: id %q: want lowercase letters, digits, _ or -", where, p.ID))
			}
			if ids[p.ID] {
				errs = append(errs, fmt.Errorf("%s: duplicate id %q", where, p.ID))
			}
			ids[p.ID] = true
			if p.Title == "" {
				errs = append(errs, fmt.Errorf("%s: empty title", where))
			}
			for k := range p.Charts {
				errs = append(errs, validateChart(fmt.Sprintf("%s.charts[%d]", where, k), &p.Charts[k], catalog))
			}
			for k, v := range p.Views {
				vwhere := fmt.Sprintf("%s.views[%d]", where, k)
				kind, known := viewReports[v.View]
				if !known {
					errs = append(errs, fmt.Errorf("%s: unknown view %q (want one of %s)", vwhere, v.View, strings.Join(sortedKeys(viewReports), ", ")))
					continue
				}
				if kind != "" {
					if _, ok := r.Owner(kind); !ok {
						errs = append(errs, fmt.Errorf("%s: view %q needs a report %q in region %s", vwhere, v.View, kind, r.ID))
					}
				}
			}
		}
	}
	return errors.Join(errs...)
}

func validateChart(where string, ch *Chart, catalog map[string]SeriesRef) error {
	var errs []error
	if ch.Title == "" {
		errs = append(errs, fmt.Errorf("%s: empty title", where))
	}
	if !slices.Contains(chartKinds, ch.Kind) {
		errs = append(errs, fmt.Errorf("%s: unknown kind %q (want one of %s)", where, ch.Kind, strings.Join(chartKinds, ", ")))
	}
	if ch.Kind == ChartCurve && ch.CompareDays <= 0 {
		errs = append(errs, fmt.Errorf("%s: curve needs compare_days", where))
	}
	if len(ch.Series) == 0 {
		errs = append(errs, fmt.Errorf("%s: no series", where))
	}
	for i, s := range ch.Series {
		swhere := fmt.Sprintf("%s.series[%d]", where, i)
		if _, ok := catalog[s.Ref]; !ok {
			errs = append(errs, fmt.Errorf("%s: unknown series %q (want source/id of a configured series)", swhere, s.Ref))
		}
		if s.Part != "" && s.Part != PartActual && s.Part != PartForecast {
			errs = append(errs, fmt.Errorf("%s: part %q: want %s or %s", swhere, s.Part, PartActual, PartForecast))
		}
		if s.Transform != "" && !slices.Contains(transform.Codes, s.Transform) {
			errs = append(errs, fmt.Errorf("%s: unknown transform %q (want one of %s)", swhere, s.Transform, strings.Join(transform.Codes, ", ")))
		}
	}
	return errors.Join(errs...)
}
