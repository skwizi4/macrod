package export

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/transform"
)

// FactValue is one transform a release is read by: its value now, at the
// previous observation and, if revised in the period, the previous
// observation as first known.
type FactValue struct {
	Code          string   `json:"code"`
	Current       Quantity `json:"current"`
	Previous      Quantity `json:"previous"`
	PreviousFirst Quantity `json:"previous_first"`
}

// ReleaseRow is one release of section 1. At is in ET; Timed: it is the
// calendar time of the release, else only its day is known.
type ReleaseRow struct {
	At          time.Time   `json:"at"`
	Timed       bool        `json:"timed"`
	Name        string      `json:"name"`
	SeriesID    string      `json:"series_id"`
	Kind        kind        `json:"kind"`
	SAAR        bool        `json:"saar"`
	Fact        []FactValue `json:"fact"`
	HasPrevious bool        `json:"has_previous"`
	Revised     bool        `json:"revised"`
	LevelChange Quantity    `json:"level_change"`
}

// factUnit is the unit of transform code of v in section 1.
func factUnit(v view, code string) string {
	switch code {
	case transform.Level:
		return v.Kind.levelUnit()
	case transform.Diff:
		return v.Kind.changeUnit()
	}
	return unitPercent
}

// releasedRows are the rows of section 1 (§17.2): non-daily series whose
// latest observation was first stored (or revised) during the period, with
// the release time from the calendar when there is an event for it.
func (w *week) releasedRows(ctx context.Context, vs []view) ([]ReleaseRow, error) {
	events, err := w.releasesBetween(ctx, w.from.AddDate(0, 0, -1), w.to)
	if err != nil {
		return nil, err
	}
	type row struct {
		at time.Time
		r  ReleaseRow
	}
	var rows []row
	for _, v := range vs {
		if v.Freq == "d" || v.Last.IsZero() || len(v.Obs) == 0 {
			continue
		}
		// The initial history load is not a release.
		fetched := v.LastSeen
		if v.LastInitial || fetched.Before(w.from) || !fetched.Before(w.to) {
			continue
		}
		r := ReleaseRow{At: fetched.In(w.et), Name: v.Label, SeriesID: v.ID, Kind: v.Kind, SAAR: saarSeries[v.ID]}
		at := fetched
		for _, e := range events {
			if slices.Contains(e.Related.Series, v.ID) && !e.ScheduledAt.After(fetched) && e.Status != model.StatusRescheduled {
				r.At, r.Timed, r.Name, at = e.ScheduledAt.In(w.et), true, e.Name, e.ScheduledAt
			}
		}
		f := factOf(v)
		cur := v.V
		if f != factLevel {
			cur = v.factValues(f, v.Obs)
		}
		var prev, first map[string]transform.Value
		if n := len(v.Obs); n >= 2 {
			r.HasPrevious = true
			prev = v.factValues(f, v.Obs[:n-1])
			revs, err := w.st.Revisions(ctx, v.Source, v.ID, w.from, w.to)
			if err != nil {
				return nil, err
			}
			// The previous fact as first known: each observation before the
			// last at its value before the period's first revision of it.
			old := slices.Clone(v.Obs[:n-1])
			for i := range old {
				for _, rev := range revs {
					if rev.ObsDate.Equal(old[i].Date) {
						old[i].Value, r.Revised = rev.Old*v.Factor, true
						break
					}
				}
			}
			if r.Revised {
				first = v.factValues(f, old)
			}
		}
		r.Fact = []FactValue{}
		for _, c := range f.codes() {
			u := factUnit(v, c.code)
			r.Fact = append(r.Fact, FactValue{Code: c.code, Current: qty(cur[c.code], u), Previous: qty(prev[c.code], u), PreviousFirst: qty(first[c.code], u)})
		}
		chg := v.V[transform.Diff]
		if v.Kind == kindRate {
			chg = v.V[transform.D] // in basis points
		}
		r.LevelChange = qty(chg, v.Kind.changeUnit())
		rows = append(rows, row{at, r})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at.Before(rows[j].at) })
	out := make([]ReleaseRow, len(rows))
	for i, r := range rows {
		out[i] = r.r
	}
	return out, nil
}

// factNames are the cell prefixes of the transforms in section 1.
var factNames = map[string]string{transform.QoQA: "QoQa", transform.MoM: "MoM", transform.YoY: "YoY"}

func factPart(r ReleaseRow, code string, q Quantity) string {
	x := q.val()
	var s string
	switch code {
	case transform.Level:
		s = r.Kind.level(x)
		if r.SAAR && x.OK {
			s += " в год (SAAR)"
		}
	case transform.Diff:
		s = r.Kind.change(x, true)
	default:
		s = pct1(x)
	}
	if n := factNames[code]; n != "" {
		s = n + " " + s
	}
	return s
}

// factCell joins the cells of one state of the fact: "MoM 0.3% · YoY 2.9%",
// "+118 тыс.", a level.
func factCell(r ReleaseRow, pick func(FactValue) Quantity) string {
	var parts []string
	for _, f := range r.Fact {
		parts = append(parts, factPart(r, f.Code, pick(f)))
	}
	return strings.Join(parts, " · ")
}

func factCurrentCell(r ReleaseRow) string {
	return factCell(r, func(f FactValue) Quantity { return f.Current })
}

func factPreviousCell(r ReleaseRow) string {
	if !r.HasPrevious {
		return dash
	}
	return factCell(r, func(f FactValue) Quantity { return f.Previous })
}

// factRevisionCell is "first known → now", if that shows in the cells.
func factRevisionCell(r ReleaseRow) string {
	if !r.HasPrevious || !r.Revised {
		return dash
	}
	o, n := factCell(r, func(f FactValue) Quantity { return f.PreviousFirst }), factPreviousCell(r)
	if o == n {
		return dash
	}
	return o + " → " + n
}

// factChangeCell is the change of the fact between two releases, from the
// shown (rounded) values: "MoM +0.1 п.п. · YoY 0.0 п.п.".
func factChangeCell(r ReleaseRow) string {
	if len(r.Fact) == 1 && r.Fact[0].Code == transform.Level {
		return r.Kind.change(r.LevelChange.val(), true)
	}
	if !r.HasPrevious {
		return dash
	}
	var parts []string
	for _, f := range r.Fact {
		a, b := f.Current.val(), f.Previous.val()
		s := dash
		if a.OK && b.OK {
			switch f.Code {
			case transform.Diff:
				s = r.Kind.change(some(a.V-b.V), true)
			default:
				round := func(x float64) float64 { return math.Round(x*10) / 10 }
				s = pp(some(round(a.V)-round(b.V))) + " п.п."
			}
		}
		if n := factNames[f.Code]; n != "" {
			s = n + " " + s
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

// releaseWhen is the date column of section 1.
func releaseWhen(r ReleaseRow) string {
	if r.Timed {
		return r.At.Format("2006-01-02 15:04")
	}
	return r.At.Format(time.DateOnly)
}

// fact is what section 1 shows of a release: the transform the release is
// read by (inflation MoM and YoY, GDP QoQa, payrolls the change), not the
// level; levels stay for claims, sentiment, rates.
type fact int

const (
	factLevel  fact = iota
	factDiff        // change, in the series' units (payrolls, thous.)
	factQoQA        // quarterly series
	factMoMYoY      // monthly price indices and wages
	factMoM         // other monthly series with MoM (PCE)
	factYoY
)

func factOf(v view) fact {
	switch {
	case v.has(transform.Level) || saarSeries[v.ID]:
		return factLevel
	case v.has(transform.Diff):
		return factDiff
	case v.Freq == "q":
		return factQoQA
	case v.Freq == "m" && (v.Kind == kindIndex || v.Kind == kindUSD):
		return factMoMYoY
	case v.has(transform.MoM):
		return factMoM
	case v.has(transform.YoY):
		return factYoY
	}
	return factLevel
}

// codes are the transforms written for f, with their cell prefix.
func (f fact) codes() []struct{ code, name string } {
	type c = struct{ code, name string }
	switch f {
	case factDiff:
		return []c{{transform.Diff, ""}}
	case factQoQA:
		return []c{{transform.QoQA, "QoQa"}}
	case factMoMYoY:
		return []c{{transform.MoM, "MoM"}, {transform.YoY, "YoY"}}
	case factMoM:
		return []c{{transform.MoM, "MoM"}}
	case factYoY:
		return []c{{transform.YoY, "YoY"}}
	}
	return []c{{transform.Level, ""}}
}

// factValues computes f at the last of obs (values as shown, after Factor).
func (v view) factValues(f fact, obs []model.Observation) map[string]transform.Value {
	out := map[string]transform.Value{}
	if len(obs) == 0 {
		return out
	}
	var codes []string
	for _, c := range f.codes() {
		codes = append(codes, c.code)
	}
	res, err := transform.Compute(transform.Input{Obs: obs, Freq: v.Freq}, codes)
	if err != nil {
		return out // the codes are known ones
	}
	return res.Values
}

// StaleSeries is a stale series of the last section, with its next release
// in the calendar (empty without one).
type StaleSeries struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	LastObs     Day    `json:"last_obs"`
	Freq        string `json:"freq"`
	NextRelease string `json:"next_release"`
	NextDate    Day    `json:"next_date"`
}

// SourceFailure is a source failing at the export.
type SourceFailure struct {
	Key   string    `json:"key"`
	Since time.Time `json:"since"`
	Error string    `json:"error"`
}

// MissingSection is the last section of a file: only data absent from the
// database at the export is listed; polling timeouts stay in the logs and
// `macroctl status` (a release that never came shows as a stale series).
type MissingSection struct {
	Stale    []StaleSeries   `json:"stale"`
	Failures []SourceFailure `json:"failures"`
}

// missingSection builds the last section: stale series of vs and failing
// sources.
func (w *week) missingSection(ctx context.Context, vs []view, sources []string) (MissingSection, error) {
	upcoming, err := w.releasesBetween(ctx, w.to, w.to.AddDate(0, 0, 30))
	if err != nil {
		return MissingSection{}, err
	}
	m := MissingSection{Stale: []StaleSeries{}}
	for _, v := range vs {
		if !v.stale() {
			continue
		}
		s := StaleSeries{ID: v.ID, Label: v.Label, LastObs: Day{v.Last}, Freq: v.Freq}
		for _, e := range upcoming {
			if slices.Contains(e.Related.Series, v.ID) && e.Status != model.StatusRescheduled {
				s.NextRelease, s.NextDate = e.Name, Day{e.ScheduledAt.In(w.et)}
				break
			}
		}
		m.Stale = append(m.Stale, s)
	}
	m.Failures = w.failures(sources)
	return m, nil
}

// excerptChars bounds an official text in the weekly files (examples/).
const excerptChars = 3000

var docTypeNames = map[string]string{
	"fomc_statement": "FOMC statement", "minutes": "FOMC minutes", "sep": "SEP", "speech": "Речь",
	"testimony": "Testimony", "beige_book": "Beige Book",
	"borrowing_estimates": "Financing Estimates", "refunding": "Refunding Statement",
	"implementation_note": "Implementation Note", "press_conference": "Пресс-конференция FOMC",
	"ecb_decision": "Решение ЕЦБ", "ecb_statement": "Заявление ЕЦБ", "ecb_press_conference": "Пресс-конференция ЕЦБ (Q&A)",
	"ecb_accounts": "Accounts ЕЦБ", "ecb_speech": "Речь", "ecb_interview": "Интервью",
}

// DocumentItem is one official text of a file, its excerpt bounded.
type DocumentItem struct {
	Published Day    `json:"published"`
	DocType   string `json:"doc_type"`
	Source    string `json:"source"`
	Author    string `json:"author"`
	Title     string `json:"title"`
	Excerpt   string `json:"excerpt"`
	URL       string `json:"url"`
}

func (w *week) documentItem(d model.Document) DocumentItem {
	return DocumentItem{Published: Day{d.PublishedAt.In(w.et)}, DocType: d.DocType, Source: d.Source, Author: d.Author, Title: d.Title,
		Excerpt: excerpt(d.Body, excerptChars), URL: d.URL}
}

// documentHeading is "2026-10-21 — Речь — Powell — «Title»".
func documentHeading(d DocumentItem) string {
	parts := []string{d.Published.Format(time.DateOnly)}
	if t := docTypeNames[d.DocType]; t != "" {
		parts = append(parts, t)
	}
	if d.Author != "" && d.DocType != "beige_book" && d.Source != "treasury" {
		parts = append(parts, d.Author)
	}
	if d.Title != "" && d.DocType != "beige_book" {
		parts = append(parts, "«"+d.Title+"»")
	}
	return strings.Join(parts, " — ")
}
