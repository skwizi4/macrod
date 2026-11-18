// Package dashboard builds the read-only answers of the dashboard (ТЗ §21.4):
// serve sends them over HTTP, site writes them to files, so both show the same.
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/export"
	"macro-collector/internal/model"
	"macro-collector/internal/status"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// ErrNotFound is an unknown series, region, agent or week.
var ErrNotFound = errors.New("dashboard: not found")

// ErrBadRequest is an invalid parameter.
var ErrBadRequest = errors.New("dashboard: bad request")

// Data answers the dashboard questions from the store and the export files.
type Data struct {
	Config *config.Config
	Store  *store.Store
	Now    func() time.Time
	// Demo publishes only what demo_allow and the open sources permit (D24).
	Demo bool
}

// Point is a dated value, written as ["2026-01-02", 4.1].
type Point struct {
	Date  time.Time
	Value float64
}

// MarshalJSON writes the compact form.
func (p Point) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{p.Date.Format(time.DateOnly), p.Value})
}

// Series is the answer of /api/series.
type Series struct {
	Source    string  `json:"source"`
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	LabelEn   string  `json:"label_en,omitempty"`
	Unit      string  `json:"unit"`
	Frequency string  `json:"frequency"`
	Transform string  `json:"transform"`
	Points    []Point `json:"points"`
}

// Series returns one configured series, transformed. A zero from is the whole
// history.
func (d *Data) Series(ctx context.Context, source, id, code string, from time.Time) (Series, error) {
	if code == "" {
		code = transform.Level
	}
	if !slices.Contains(transform.Codes, code) {
		return Series{}, fmt.Errorf("%w: unknown transform %q", ErrBadRequest, code)
	}
	ref, ok := d.Config.SeriesCatalog()[source+"/"+id]
	if !ok || !d.allowed(ref.Ref()) {
		return Series{}, fmt.Errorf("%w: series %s/%s", ErrNotFound, source, id)
	}
	if source == config.SourceRatio {
		return d.ratio(ctx, ref, code, from)
	}
	meta, _, err := d.Store.Meta(ctx, source, id)
	if err != nil {
		return Series{}, err
	}
	obs, err := d.Store.Observations(ctx, source, id, time.Time{})
	if err != nil {
		return Series{}, err
	}
	scale, err := export.ScaleOf(source, id, meta.Units)
	if err != nil {
		return Series{}, err
	}
	for i := range obs {
		obs[i].Value *= scale.Factor
	}
	label := ref.Label
	if meta.Label != "" && label == ref.ID {
		label = meta.Label
	}
	in := d.input(obs, strings.ToLower(meta.Frequency), scale)
	if def, ok := d.Config.ResolveSeries(source + "/" + id); ok {
		in.ShortHistory = def.ShortHistory // the chart shows z where the reports do
	}
	points, err := transform.History(in, code, from)
	if err != nil {
		return Series{}, fmt.Errorf("dashboard series %s/%s: %w", source, id, err)
	}
	return Series{Source: source, ID: id, Label: label, LabelEn: ref.LabelEn, Unit: scale.UnitOf(code), Frequency: in.Freq, Transform: code, Points: pointsOf(points)}, nil
}

func (d *Data) input(obs []model.Observation, freq string, s export.Scale) transform.Input {
	return transform.Input{
		Obs: obs, Freq: freq, ChangeScale: s.ChangeScale, RelativeChanges: s.Relative,
		ZWindowDailyWeekly: d.Config.Metrics.ZDailyWeekly, ZWindowMonthlyQuarterly: d.Config.Metrics.ZMonthlyQuarterly,
	}
}

func (d *Data) ratio(ctx context.Context, ref config.SeriesRef, code string, from time.Time) (Series, error) {
	num, den, _ := strings.Cut(ref.ID, ":")
	a, err := d.Store.Observations(ctx, "yahoo", num, time.Time{})
	if err != nil {
		return Series{}, err
	}
	b, err := d.Store.Observations(ctx, "yahoo", den, time.Time{})
	if err != nil {
		return Series{}, err
	}
	in := transform.Input{Obs: transform.Ratio(a, b), Freq: "d", RelativeChanges: true,
		ZWindowDailyWeekly: d.Config.Metrics.ZDailyWeekly, ZWindowMonthlyQuarterly: d.Config.Metrics.ZMonthlyQuarterly}
	points, err := transform.History(in, code, from)
	if err != nil {
		return Series{}, fmt.Errorf("dashboard ratio %s: %w", ref.ID, err)
	}
	unit := "ratio"
	if code != transform.Level {
		scale, err := export.ScaleOf("yahoo", num, "")
		if err != nil {
			return Series{}, err
		}
		unit = scale.UnitOf(code)
	}
	return Series{Source: ref.Source, ID: ref.ID, Label: ref.Label, LabelEn: ref.LabelEn, Unit: unit, Frequency: "d", Transform: code, Points: pointsOf(points)}, nil
}

func pointsOf(obs []model.Observation) []Point {
	out := make([]Point, len(obs))
	for i, o := range obs {
		out[i] = Point{Date: o.Date, Value: o.Value}
	}
	return out
}

// allowed tells whether the mode may publish a series.
func (d *Data) allowed(ref string) bool {
	return !d.Demo || slices.Contains(d.Config.Dashboard.DemoAllow, ref)
}

// Meta is the answer of /api/meta: what the frontend draws.
type Meta struct {
	Demo        bool         `json:"demo"`
	GeneratedAt time.Time    `json:"generated_at"`
	Regions     []RegionMeta `json:"regions"`
}

// RegionMeta is a region and its menu.
type RegionMeta struct {
	ID      string     `json:"id"`
	Agents  []string   `json:"agents"`
	KeyRows []KeyRow   `json:"key_rows"`
	Pages   []PageMeta `json:"pages"`
}

// KeyRow links a key metrics row (by position) to its series.
type KeyRow struct {
	Label   string `json:"label"`
	LabelEn string `json:"label_en,omitempty"` // English; empty: label
	Ref     string `json:"ref,omitempty"`
	// Transform is the code the row shows; its sparkline draws the same.
	Transform string `json:"transform,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
}

// PageMeta is a page of the menu.
type PageMeta struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	TitleEn string      `json:"title_en,omitempty"` // English; empty: title
	Charts  []ChartMeta `json:"charts"`
	Views   []ViewMeta  `json:"views"`
}

// ChartMeta is a chart; Hidden when no line of it may be shown.
type ChartMeta struct {
	Title       string       `json:"title"`
	TitleEn     string       `json:"title_en,omitempty"`
	Kind        string       `json:"kind"`
	CompareDays int          `json:"compare_days,omitempty"`
	Hidden      bool         `json:"hidden,omitempty"`
	Series      []SeriesMeta `json:"series"`
}

// SeriesMeta is a line of a chart.
type SeriesMeta struct {
	Source    string `json:"source"`
	ID        string `json:"id"`
	Transform string `json:"transform"`
	Label     string `json:"label"`
	LabelEn   string `json:"label_en,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
	// Part: actual or forecast points of a series published ahead; the page
	// splits them at generated_at.
	Part string `json:"part,omitempty"`
}

// ViewMeta is a view; Agent owns the report it reads.
type ViewMeta struct {
	View   string `json:"view"`
	Agent  string `json:"agent,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

// demoViews are the views site --demo shows: their data is public. Mixed
// reports are cut field by field in demoReport.
var demoViews = []string{"key_metrics", "auctions", "supply", "capex", "financials", "calendar"}

// Meta describes the regions and pages.
func (d *Data) Meta() Meta {
	m := Meta{Demo: d.Demo, GeneratedAt: d.Now()}
	catalog := d.Config.SeriesCatalog()
	for i := range d.Config.Regions {
		r := &d.Config.Regions[i]
		rm := RegionMeta{ID: r.ID, KeyRows: d.keyRows(r), Pages: []PageMeta{}}
		for j := range r.Agents {
			rm.Agents = append(rm.Agents, r.Agents[j].ID)
		}
		for _, p := range pagesOf(r) {
			rm.Pages = append(rm.Pages, d.pageMeta(r, p, catalog))
		}
		m.Regions = append(m.Regions, rm)
	}
	return m
}

// pagesOf are the pages of a region; without its own, the generic ones.
func pagesOf(r *config.Region) []config.DashboardPage {
	if len(r.Dashboard.Pages) > 0 {
		return r.Dashboard.Pages
	}
	var out []config.DashboardPage
	if _, ok := r.Owner(config.ReportKeyMetrics); ok {
		out = append(out, config.DashboardPage{ID: "overview", Title: "Обзор", TitleEn: "Overview", Views: []config.PageView{{View: "key_metrics"}, {View: "status"}}})
	} else {
		out = append(out, config.DashboardPage{ID: "overview", Title: "Обзор", TitleEn: "Overview", Views: []config.PageView{{View: "status"}}})
	}
	return append(out,
		config.DashboardPage{ID: "calendar", Title: "Календарь", TitleEn: "Calendar", Views: []config.PageView{{View: "calendar"}}},
		config.DashboardPage{ID: "reports", Title: "Отчёты", TitleEn: "Reports", Views: []config.PageView{{View: "reports"}}})
}

func (d *Data) keyRows(r *config.Region) []KeyRow {
	out := []KeyRow{}
	owner, ok := r.Owner(config.ReportKeyMetrics)
	if !ok {
		return out
	}
	for _, row := range owner.Report.Rows {
		ref, stored := d.Config.KeyRowRef(row)
		out = append(out, KeyRow{Label: row.Label, LabelEn: row.LabelEn, Ref: ref, Transform: row.Code, Hidden: !stored && d.Demo || stored && !d.allowed(ref)})
	}
	return out
}

func (d *Data) pageMeta(r *config.Region, p config.DashboardPage, catalog map[string]config.SeriesRef) PageMeta {
	pm := PageMeta{ID: p.ID, Title: p.Title, TitleEn: p.TitleEn, Charts: []ChartMeta{}, Views: []ViewMeta{}}
	for _, ch := range p.Charts {
		cm := ChartMeta{Title: ch.Title, TitleEn: ch.TitleEn, Kind: ch.Kind, CompareDays: ch.CompareDays, Hidden: true}
		for _, s := range ch.Series {
			ref := catalog[s.Ref]
			code := s.Transform
			if code == "" {
				code = transform.Level
			}
			label, labelEn := s.Label, s.LabelEn
			if label == "" {
				label = ref.Label
			}
			if labelEn == "" && s.Label == "" {
				labelEn = ref.LabelEn
			}
			hidden := !d.allowed(s.Ref)
			cm.Hidden = cm.Hidden && hidden
			cm.Series = append(cm.Series, SeriesMeta{Source: ref.Source, ID: ref.ID, Transform: code, Label: label, LabelEn: labelEn, Hidden: hidden, Part: s.Part})
		}
		pm.Charts = append(pm.Charts, cm)
	}
	for _, v := range p.Views {
		vm := ViewMeta{View: v.View, Hidden: d.Demo && !slices.Contains(demoViews, v.View)}
		if kind := config.ViewReport(v.View); kind != "" {
			if owner, ok := r.Owner(kind); ok {
				vm.Agent = owner.Agent.ID
			}
		}
		pm.Views = append(pm.Views, vm)
	}
	return pm
}

var weekPattern = regexp.MustCompile(`^\d{4}-W\d{2}$`)

// ReportFile is one export file of an agent.
type ReportFile struct {
	Report string          `json:"report"`
	File   string          `json:"file"`
	MD     string          `json:"md,omitempty"`
	JSON   json.RawMessage `json:"json,omitempty"`
}

// Reports is the answer of /api/reports.
type Reports struct {
	Region string       `json:"region"`
	Agent  string       `json:"agent"`
	Week   string       `json:"week"`
	Weeks  []string     `json:"weeks"`
	Files  []ReportFile `json:"files"`
	Answer string       `json:"answer,omitempty"` // the saved answer of the agent
}

// Reports returns the export files of an agent for a week (empty: the latest).
// Paths come from the config; a parameter only selects among them.
func (d *Data) Reports(region, agent, week string) (Reports, error) {
	r, ok := d.Config.Region(region)
	if !ok {
		return Reports{}, fmt.Errorf("%w: region %q", ErrNotFound, region)
	}
	var a *config.Agent
	for i := range r.Agents {
		if r.Agents[i].ID == agent {
			a = &r.Agents[i]
		}
	}
	if a == nil {
		return Reports{}, fmt.Errorf("%w: agent %q", ErrNotFound, agent)
	}
	weeks, err := d.Weeks()
	if err != nil {
		return Reports{}, err
	}
	out := Reports{Region: region, Agent: agent, Weeks: weeks, Files: []ReportFile{}}
	if week == "" {
		if len(weeks) == 0 {
			return out, nil
		}
		week = weeks[0]
	}
	if !weekPattern.MatchString(week) {
		return Reports{}, fmt.Errorf("%w: week %q", ErrBadRequest, week)
	}
	if !slices.Contains(weeks, week) {
		return Reports{}, fmt.Errorf("%w: week %q", ErrNotFound, week)
	}
	out.Week = week
	archive := filepath.Join(d.Config.Export.ExchangeDir, "archive", week)
	for _, rep := range a.Reports {
		if d.Demo && !slices.Contains(demoReports, rep.Kind) {
			continue
		}
		rel := r.Path(a, rep.File)
		f := ReportFile{Report: rep.Kind, File: rep.File}
		if !d.Demo {
			if body, err := readOptional(filepath.Join(archive, rel)); err != nil {
				return Reports{}, err
			} else {
				f.MD = body
			}
		}
		js, err := readOptional(filepath.Join(archive, "json", strings.TrimSuffix(rel, ".md")+".json"))
		if err != nil {
			return Reports{}, err
		}
		if js != "" {
			f.JSON = json.RawMessage(js)
			if d.Demo {
				if f.JSON, err = d.demoReport(r, rep.Kind, f.JSON); err != nil {
					return Reports{}, err
				}
			}
		}
		if f.MD != "" || f.JSON != nil {
			out.Files = append(out.Files, f)
		}
	}
	if !d.Demo {
		answer, err := readOptional(filepath.Join(d.Config.Export.ReportsDir, region, week, agent+".md"))
		if err != nil {
			return Reports{}, err
		}
		out.Answer = answer
	}
	return out, nil
}

// Weeks are the archived export weeks, newest first.
func (d *Data) Weeks() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(d.Config.Export.ExchangeDir, "archive"))
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dashboard weeks: %w", err)
	}
	weeks := []string{}
	for _, e := range entries {
		if e.IsDir() && weekPattern.MatchString(e.Name()) {
			weeks = append(weeks, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(weeks)))
	return weeks, nil
}

func readOptional(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("dashboard read %s: %w", filepath.Base(path), err)
	}
	return string(raw), nil
}

// CalendarEvent is one event of /api/calendar.
type CalendarEvent struct {
	At         time.Time `json:"at"`
	Name       string    `json:"name"`
	Region     string    `json:"region"`
	Importance string    `json:"importance"`
	Source     string    `json:"source"`
	Status     string    `json:"status"`
}

// demoCalendarSources are the official schedules; earnings dates are not.
var demoCalendarSources = []string{"fred_release", "fomc", "treasury", "treasury_auction", "ecb"}

// Calendar returns the events from a week ago to days ahead.
func (d *Data) Calendar(ctx context.Context, days int) ([]CalendarEvent, error) {
	now := d.Now()
	events, err := d.Store.ActiveReleases(ctx, now.AddDate(0, 0, -7), now.AddDate(0, 0, days))
	if err != nil {
		return nil, err
	}
	out := []CalendarEvent{}
	for _, e := range events {
		if d.Demo && !slices.Contains(demoCalendarSources, e.Source) {
			continue
		}
		out = append(out, CalendarEvent{At: e.ScheduledAt, Name: e.Name, Region: e.Region, Importance: e.Importance, Source: e.Source, Status: e.Status})
	}
	return out, nil
}

// SourceStatus is the state of one source.
type SourceStatus struct {
	Source string    `json:"source"`
	Series int       `json:"series"`
	Errors int       `json:"errors"`
	Stale  int       `json:"stale"`
	Never  int       `json:"never"`
	LastOK time.Time `json:"last_ok"`
}

// SeriesProblem is a series that needs attention.
type SeriesProblem struct {
	Source  string    `json:"source"`
	ID      string    `json:"id"`
	LastObs time.Time `json:"last_obs"`
	Stale   bool      `json:"stale"`
	Error   string    `json:"error,omitempty"`
}

// StatusAnswer is the answer of /api/status. Paths are left out and error
// texts cleaned: they can carry keys of source URLs.
type StatusAnswer struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Sources     []SourceStatus  `json:"sources"`
	Problems    []SeriesProblem `json:"problems"`
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(api[_-]?key|access_token|token|secret|password)=[^&\s"']+`),
	regexp.MustCompile(`\bbot\d+:[A-Za-z0-9_-]+`),
}

// redact removes credentials from an error text.
func redact(s string) string {
	s = secretPatterns[0].ReplaceAllString(s, "$1=***")
	return secretPatterns[1].ReplaceAllString(s, "bot***")
}

// Status reports the sources.
func (d *Data) Status(ctx context.Context) (StatusAnswer, error) {
	now := d.Now()
	rep, err := status.Build(ctx, d.Config, d.Store, now)
	if err != nil {
		return StatusAnswer{}, err
	}
	out := StatusAnswer{GeneratedAt: now, Sources: []SourceStatus{}, Problems: []SeriesProblem{}}
	for _, s := range rep.Summaries() {
		out.Sources = append(out.Sources, SourceStatus{Source: s.Source, Series: s.Series, Errors: s.Errors, Stale: s.Stale, Never: s.Never, LastOK: s.LastOK})
	}
	for _, r := range rep.Rows {
		if !r.OK() {
			out.Problems = append(out.Problems, SeriesProblem{Source: r.Source, ID: r.ID, LastObs: r.LastObs, Stale: r.Stale, Error: redact(r.Error)})
		}
	}
	return out, nil
}
