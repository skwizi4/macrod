package export

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/guidance"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

// File is one exported file, its path relative to the exchange directory.
type File struct {
	Path        string
	Body        string
	ArchiveOnly bool   // written only under archive/<iso_week>/ (uncategorized.md, news_filtered.md)
	Report      string // JSON of a report model: the report's name (template and schema)
}

// Weekly writes the weekly export of a region (ТЗ §12).
type Weekly struct {
	Store  *store.Store
	Config *config.Config
	Region *config.Region // nil: the first region of the config
	Sink   EventSink
	Log    *slog.Logger
	Now    func() time.Time // defaults to time.Now
}

// WeeklyResult describes a weekly export.
type WeeklyResult struct {
	ISOWeek  string
	From, To time.Time
	Files    []File   // rendered files (relative paths)
	Written  []string // absolute paths written, archive included; empty on a dry run
	Previous []string // previous_report.md copied, relative paths
	Prompts  []string // instructions.md written, relative paths
	// NoPrompt are the agents (<region>/<agent>) without a prompt file: their
	// instructions.md is removed, the export goes on.
	NoPrompt []string
	// Forecast is the forecast log after this export (phase 13); nil when it
	// is off, the region has no forecast agent or it failed (ForecastErr).
	Forecast    *ForecastResult
	ForecastErr string
}

// week is the state of one rendering.
type week struct {
	st       *store.Store
	cfg      *config.Config
	loc, et  *time.Location
	from, to time.Time        // period instants
	asOf     time.Time        // calendar day of the period end (Bratislava), UTC midnight
	guidance []guidance.Value // capex guidance in force as of asOf (Micro, phase 14b)
	iso      string
	now      time.Time
	states   map[string]store.FetchState
	cache    map[ref]view
	log      *slog.Logger
	// releases is the calendar around the period end (loaded once, see
	// releaseDay).
	releases *ReleaseIndex
	// jsonFiles are the JSON files of the reports rendered so far.
	jsonFiles []File
	tmpl      *Templates
	region    *config.Region
	agent     *config.Agent  // of the report being built
	rep       *config.Report // the report being built
}

// path is the file of the report being built.
func (w *week) path() string { return w.region.Path(w.agent, w.rep.File) }

// title is "<report title> — <week>".
func (w *week) title() string { return w.rep.Title + " — " + w.iso }

// tables are the configured series of a table of the report being built.
func (w *week) table(name string) []string { return w.rep.Tables[name] }

// sourcesWithTexts are the sources of a report followed by the agent's text
// sources.
func (w *week) sourcesWithTexts(sources ...string) []string {
	return append(sources, w.agent.Texts...)
}

func (w *week) logWarn(msg string, args ...any) { w.log.Warn(msg, args...) }

func (x *Weekly) log() *slog.Logger {
	if x.Log != nil {
		return x.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (x *Weekly) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Weekly) newWeek(ctx context.Context, end time.Time) (*week, error) {
	loc := x.Config.Location
	end = end.In(loc)
	states, err := x.Store.FetchStates(ctx)
	if err != nil {
		return nil, err
	}
	tmpl, err := LoadTemplates(x.Config)
	if err != nil {
		return nil, err
	}
	region := x.Region
	if region == nil {
		region = &x.Config.Regions[0]
	}
	// The region's own zone: the second time of the calendar, the day of
	// its releases (stale) and of its events.
	et := region.Location
	if et == nil {
		et = x.Config.Calendar.ReleaseLocation
	}
	y, m, d := end.Date()
	return &week{
		st: x.Store, cfg: x.Config, loc: loc, et: et,
		from: end.AddDate(0, 0, -7), to: end, asOf: time.Date(y, m, d, 0, 0, 0, 0, time.UTC),
		iso: ISOWeek(end.Add(-time.Minute), loc), now: x.now(), states: states, cache: map[ref]view{},
		log: x.log(), tmpl: tmpl, region: region,
	}, nil
}

// selfLimited are the kinds that fit the size limit themselves (§12.6): the
// other files hold only tables and official texts, which are not cut.
var selfLimited = map[string]bool{config.ReportNews: true, config.ReportMicro: true}

// builders make the reports of a week: the Markdown, the JSON goes to
// week.jsonFiles. News also makes archive files, see newsFiles.
var builders = map[string]func(*week, context.Context) (string, error){
	config.ReportMonetary:   (*week).monetary,
	config.ReportFiscal:     (*week).fiscal,
	config.ReportBonds:      (*week).bonds,
	config.ReportMicro:      (*week).micro,
	config.ReportIssuers:    (*week).issuersMicro,
	config.ReportSentiment:  (*week).sentiment,
	config.ReportMarkets:    (*week).markets,
	config.ReportCalendar:   (*week).calendar,
	config.ReportKeyMetrics: (*week).keyMetrics,
}

// Render builds the files of the region for the period ending at end, in
// the order of the config; the event file and the archive-only files last.
func (x *Weekly) Render(ctx context.Context, end time.Time) ([]File, *week, error) {
	w, err := x.newWeek(ctx, end)
	if err != nil {
		return nil, nil, err
	}
	var files, archive []File
	for i := range w.region.Agents {
		w.agent = &w.region.Agents[i]
		for j := range w.agent.Reports {
			w.rep = &w.agent.Reports[j]
			path := w.path()
			switch {
			case w.rep.Kind == config.ReportFOMCDiff, w.rep.Kind == config.ReportECBDiff:
				continue // refreshed last
			case w.rep.Kind == config.ReportMicro && !x.Config.EDGAR.Enabled, w.rep.Kind == config.ReportIssuers && !x.Config.Issuers.Enabled:
				continue
			case w.rep.Kind == config.ReportNews:
				f, a, err := w.newsFiles(ctx)
				if err != nil {
					return nil, nil, fmt.Errorf("export %s: %w", path, err)
				}
				files, archive = append(files, f), append(archive, a...)
				continue
			}
			body, err := builders[w.rep.Kind](w, ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("export %s: %w", path, err)
			}
			if !selfLimited[w.rep.Kind] {
				x.warnSize(w.rep, path, body)
			}
			files = append(files, File{Path: path, Body: body})
		}
	}
	if o, ok := w.region.Owner(config.ReportFOMCDiff); ok {
		w.agent, w.rep = o.Agent, o.Report
		if f, ok, err := w.fomcRefresh(ctx); err != nil {
			return nil, nil, fmt.Errorf("export %s: %w", w.path(), err)
		} else if ok {
			x.warnSize(w.rep, f.Path, f.Body)
			files = append(files, f)
		}
	}
	if o, ok := w.region.Owner(config.ReportECBDiff); ok {
		w.agent, w.rep = o.Agent, o.Report
		if f, ok, err := w.ecbRefresh(ctx); err != nil {
			return nil, nil, fmt.Errorf("export %s: %w", w.path(), err)
		} else if ok {
			x.warnSize(w.rep, f.Path, f.Body)
			files = append(files, f)
		}
	}
	files = append(files, archive...)
	return append(files, w.jsonFiles...), w, nil
}

// warnSize logs a file over its limit (report max_chars, else
// export.max_file_chars). Official texts are never cut: the warning is the
// signal to raise the limit or shorten the template.
func (x *Weekly) warnSize(rep *config.Report, path, body string) {
	limit := x.Config.FileLimit(rep)
	if n := len([]rune(body)); limit > 0 && n > limit && x.Log != nil {
		x.Log.Warn("export file over the size limit", "file", path, "chars", n, "limit", limit)
	}
}

// newsFiles renders the news file and its archive-only companions.
func (w *week) newsFiles(ctx context.Context) (File, []File, error) {
	news, filtered, uncat, err := w.news(ctx)
	if err != nil {
		return File{}, nil, err
	}
	return File{Path: w.path(), Body: news}, []File{{Path: w.rep.UncategorizedFile, Body: uncat, ArchiveOnly: true},
		{Path: w.rep.FilteredFile, Body: filtered, ArchiveOnly: true}}, nil
}

// Run renders and, unless dryRun, writes the files and their archive copy,
// copies last week's reports as previous_report.md, records the export and
// tells the sink.
func (x *Weekly) Run(ctx context.Context, end time.Time, dryRun bool) (WeeklyResult, error) {
	files, w, err := x.Render(ctx, end)
	if err != nil {
		return WeeklyResult{}, err
	}
	res := WeeklyResult{ISOWeek: w.iso, From: w.from, To: w.to, Files: files}
	if dryRun {
		return res, nil
	}
	exch := x.Config.Export.ExchangeDir
	for _, f := range files {
		paths := []string{filepath.Join(exch, f.Path), filepath.Join(exch, "archive", w.iso, f.Path)}
		if f.ArchiveOnly {
			paths = paths[1:]
		}
		for _, p := range paths {
			if err := writeAtomic(p, f.Body); err != nil {
				return res, err
			}
			res.Written = append(res.Written, p)
		}
	}
	prevWeek := ISOWeek(w.from.Add(-time.Minute), w.loc)
	for i := range w.region.Agents {
		a := &w.region.Agents[i]
		rel := w.region.Path(a, "previous_report.md")
		dst := filepath.Join(exch, rel)
		src := filepath.Join(x.Config.Export.ReportsDir, w.region.ID, prevWeek, a.ID+".md")
		b, err := os.ReadFile(src)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// No report saved last week: drop an older copy, it would
			// pass for last week's.
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return res, fmt.Errorf("export remove %s: %w", dst, err)
			}
		case err != nil:
			return res, fmt.Errorf("export read %s: %w", src, err)
		default:
			if err := writeAtomic(dst, string(b)); err != nil {
				return res, err
			}
			res.Written = append(res.Written, dst)
			res.Previous = append(res.Previous, rel)
		}
	}
	x.writeForecasts(ctx, w, &res)
	if err := x.writePrompts(w, &res); err != nil {
		return res, err
	}
	if err := x.Store.Write(ctx, func(tx *sql.Tx) error {
		return store.InsertExport(tx, w.iso, KindWeekly, w.region.ID, w.now)
	}); err != nil {
		return res, fmt.Errorf("export weekly: %w", err)
	}
	if x.Sink != nil {
		if err := x.Sink.Exported(ctx, Exported{Kind: KindWeekly, Region: w.region.ID, ISOWeek: w.iso, Files: res.Written}); err != nil {
			return res, fmt.Errorf("export weekly sink: %w", err)
		}
	}
	return res, nil
}

// writePrompts copies each agent's prompt into its folder and the archive as
// instructions.md (§21.5). A missing prompt is a warning and removes the old
// copy, which would pass for the current one; a malformed prompt fails.
func (x *Weekly) writePrompts(w *week, res *WeeklyResult) error {
	exch := x.Config.Export.ExchangeDir
	for i := range w.region.Agents {
		a := &w.region.Agents[i]
		rel := w.region.Path(a, InstructionsFile)
		name := w.region.ID + "/" + a.ID
		p, err := LoadPrompt(x.Config.PromptPath(w.region, a), name)
		if errors.Is(err, os.ErrNotExist) {
			x.log().Warn("no agent prompt, instructions.md removed", "agent", name, "path", x.Config.PromptPath(w.region, a))
			res.NoPrompt = append(res.NoPrompt, name)
			if err := os.Remove(filepath.Join(exch, rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("export remove %s: %w", rel, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("export: %w", err)
		}
		for _, dst := range []string{filepath.Join(exch, rel), filepath.Join(exch, "archive", w.iso, rel)} {
			if err := writeAtomic(dst, p.Render()); err != nil {
				return err
			}
			res.Written = append(res.Written, dst)
		}
		res.Prompts = append(res.Prompts, rel)
	}
	return nil
}

// newHeader is the header of a weekly report of the period [from, to).
func (w *week) newHeader(file, title string, sources, stale []string, from, to time.Time) Header {
	ok, failed := w.sourceStatus(sources)
	return Header{
		SchemaVersion: SchemaVersion, Region: w.region.ID, Agent: w.agent.ID, File: file, ISOWeek: w.iso,
		Period:      Period{From: from.In(w.loc), To: to.In(w.loc), TZ: w.loc.String()},
		GeneratedAt: w.now.In(w.loc), Kind: KindWeekly, SourcesOK: ok, SourcesFailed: failed, StaleSeries: stale, Title: title,
	}
}

// report renders a report model: its Markdown is returned, its JSON kept for
// the end of the rendering.
func (w *week) report(path, report string, model any) (string, error) {
	md, js, err := reportFiles(w.tmpl, w.templateOf(report), path, report, model, false)
	if err != nil {
		return "", err
	}
	w.jsonFiles = append(w.jsonFiles, js)
	return md.Body, nil
}

// render is the Markdown of a report without its JSON: for sizing a file.
func (w *week) render(report string, model any) (string, error) {
	return w.tmpl.render(report, w.templateOf(report), model)
}

// templateOf is the template of a report kind: the configured one for the
// report being built, the built-in one for its companions.
func (w *week) templateOf(report string) string {
	if w.rep != nil && w.rep.Kind == report {
		return w.rep.TemplateName()
	}
	return report
}

// archiveReport is report for a file written only under archive/<iso_week>/.
func (w *week) archiveReport(path, report string, model any) (string, error) {
	md, js, err := reportFiles(w.tmpl, w.templateOf(report), path, report, model, true)
	if err != nil {
		return "", err
	}
	w.jsonFiles = append(w.jsonFiles, js)
	return md.Body, nil
}

// sourceAlso are the other fetch_state keys a source of the front matter
// stands for: the calendar's Treasury rows show past auctions too, so a
// failed auctions fetch fails "treasury" there.
var sourceAlso = map[string][]string{"calendar:treasury": {"treasury:auctions"}}

// sourceStatus splits sources by whether any of their fetch_state rows
// carries an error now (a success clears it). A source name matches the
// key itself and keys "<name>:…"; "calendar:<name>" is used for the
// calendar sources fred, fed and treasury when prefixed so.
func (w *week) sourceStatus(sources []string) (ok, failed []string) {
	ok, failed = []string{}, []string{}
	for _, s := range sources {
		bad := false
		for _, name := range append([]string{s}, sourceAlso[s]...) {
			for key, st := range w.states {
				if (key == name || strings.HasPrefix(key, name+":")) && st.LastError != "" {
					bad = true
					break
				}
			}
		}
		name := strings.TrimPrefix(s, "calendar:")
		if bad {
			failed = append(failed, name)
		} else {
			ok = append(ok, name)
		}
	}
	return ok, failed
}

// failures lists the failing fetch_state keys of sources, for the last
// section.
func (w *week) failures(sources []string) []SourceFailure {
	out := []SourceFailure{}
	for key, st := range w.states {
		for _, s := range sources {
			if (key == s || strings.HasPrefix(key, s+":")) && st.LastError != "" {
				out = append(out, SourceFailure{Key: key, Since: st.LastErrorAt.In(w.loc), Error: oneLine(st.LastError)})
				break
			}
		}
	}
	// As the lines read: "fred:A: …" sorts after "fred:A1: …".
	slices.SortFunc(out, func(a, b SourceFailure) int { return strings.Compare(a.Key+":", b.Key+":") })
	return out
}

// oneLine is an error text on one line, in full: the cause is at its end
// (after the URL).
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// staleIDs are the ids of stale views.
func staleIDs(vs ...[]view) []string {
	out := []string{}
	for _, l := range vs {
		for _, v := range l {
			if v.stale() && !slices.Contains(out, v.ID) {
				out = append(out, v.ID)
			}
		}
	}
	return out
}

// releasePollWindow is how long an event is polled (scheduler §8.2): a
// release younger than that may still be on its way.
const releasePollWindow = 24 * time.Hour

// ReleaseIndex maps series to their calendar releases around a moment, for
// the staleness rule of §17.7.
type ReleaseIndex struct {
	last, next map[string]time.Time // days at midnight in the release timezone
}

// LoadReleaseIndex reads the calendar around to; keep, if set, narrows the
// events (to a region, in an export).
func LoadReleaseIndex(ctx context.Context, st *store.Store, to time.Time, et *time.Location, keep func(model.ReleaseEvent) bool) (ReleaseIndex, error) {
	cut := to.Add(-releasePollWindow + time.Second)
	between := func(a, b time.Time) ([]model.ReleaseEvent, error) {
		evs, err := st.Releases(ctx, a, b)
		if err != nil || keep == nil {
			return evs, err
		}
		return slices.DeleteFunc(evs, func(e model.ReleaseEvent) bool { return !keep(e) }), nil
	}
	evs, err := between(to.AddDate(-1, 0, 0), cut)
	if err != nil {
		return ReleaseIndex{}, err
	}
	ahead, err := between(cut, to.AddDate(1, 0, 0))
	if err != nil {
		return ReleaseIndex{}, err
	}
	freqs, err := st.SeriesFrequencies(ctx, "fred")
	if err != nil {
		return ReleaseIndex{}, err
	}
	skip := func(e model.ReleaseEvent) bool {
		return e.Status == model.StatusRescheduled || weekendDaily(e, freqs, et)
	}
	ix := ReleaseIndex{last: map[string]time.Time{}, next: map[string]time.Time{}}
	for _, e := range ahead { // oldest first: the earliest wins
		if skip(e) {
			continue
		}
		y, m, d := e.ScheduledAt.In(et).Date()
		for _, s := range e.Related.Series {
			if _, seen := ix.next[s]; !seen {
				ix.next[s] = time.Date(y, m, d, 0, 0, 0, 0, et)
			}
		}
	}
	for _, e := range evs { // oldest first: the latest wins
		if skip(e) {
			continue
		}
		y, m, d := e.ScheduledAt.In(et).Date()
		for _, s := range e.Related.Series {
			ix.last[s] = time.Date(y, m, d, 0, 0, 0, 0, et)
		}
	}
	return ix, nil
}

// AddConfigDays adds the publication days a config lists for series that
// no calendar source knows (manual input: Ifo). As with calendar events, a
// day counts as the latest release once its poll window has passed at to.
func (ix *ReleaseIndex) AddConfigDays(days map[string][]time.Time, to time.Time, et *time.Location) {
	cut := to.Add(-releasePollWindow + time.Second)
	for id, list := range days { // each list is ascending
		for _, d := range list {
			at := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, et)
			if at.Before(cut) {
				ix.last[id] = at
			} else if _, seen := ix.next[id]; !seen {
				ix.next[id] = at
			}
		}
	}
}

// Of is the day of the latest release of series id past its poll window (ok
// is false without one) and the day of its next release, zero without one.
// With neither, stale follows the fixed table of §17.7.
func (ix ReleaseIndex) Of(id string) (day time.Time, ok bool, next time.Time) {
	day, ok = ix.last[id]
	return day, ok, ix.next[id]
}

// releaseDay is ReleaseIndex.Of for the region and period end of the week.
func (w *week) releaseDay(ctx context.Context, id string) (day time.Time, ok bool, next time.Time, err error) {
	if w.releases == nil {
		ix, err := LoadReleaseIndex(ctx, w.st, w.to, w.et, func(e model.ReleaseEvent) bool { return e.Region == w.region.ID })
		if err != nil {
			return time.Time{}, false, time.Time{}, err
		}
		days, err := ManualReleaseDays(ctx, w.cfg, w.st)
		if err != nil {
			return time.Time{}, false, time.Time{}, err
		}
		ix.AddConfigDays(days, w.to, w.et)
		w.releases = &ix
	}
	day, ok, next = w.releases.Of(id)
	return day, ok, next, nil
}

// weekendDaily: a FRED release dated on a Saturday or Sunday (ET) whose
// series are all daily, such as ICE BofA Indices on 2026-10-31: no data
// comes out on that day (Friday's value is posted on Monday), so it is not
// an expected release.
func weekendDaily(e model.ReleaseEvent, freqs map[string]string, et *time.Location) bool {
	if e.Source != "fred_release" || len(e.Related.Series) == 0 {
		return false
	}
	if d := e.ScheduledAt.In(et).Weekday(); d != time.Saturday && d != time.Sunday {
		return false
	}
	for _, id := range e.Related.Series {
		if freqs[id] != "d" {
			return false
		}
	}
	return true
}

// releasesBetween are the stored events of the region in [from, to).
func (w *week) releasesBetween(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	evs, err := w.st.Releases(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(evs, func(e model.ReleaseEvent) bool { return e.Region != w.region.ID }), nil
}

// inNews reports whether a post belongs to the news of the region: its
// sources (every region's with a shared news file), narrowed by the
// channels of the agent if it lists any.
func (w *week) inNews(p store.WeekPost) bool {
	o, ok := w.region.Owner(config.ReportNews)
	if p.Region != w.region.ID && !(ok && o.Report.Shared) {
		return false
	}
	if ok && len(o.Agent.Channels) > 0 {
		_, name, _ := strings.Cut(p.Source, ":")
		return slices.Contains(o.Agent.Channels, name)
	}
	return true
}

// newsConfig is the configured news report of the region, zero without one.
func (w *week) newsConfig() *config.Report {
	if o, ok := w.region.Owner(config.ReportNews); ok {
		return o.Report
	}
	return &config.Report{}
}

// excerpt cuts a text at maxRunes on a paragraph or sentence boundary.
func excerpt(body string, maxRunes int) string {
	r := []rune(strings.TrimSpace(body))
	if len(r) <= maxRunes {
		return string(r)
	}
	cut := string(r[:maxRunes])
	if i := strings.LastIndex(cut, "\n\n"); i > maxRunes/2 {
		cut = cut[:i]
	} else if i := strings.LastIndex(cut, ". "); i > maxRunes/2 {
		cut = cut[:i+1]
	}
	return strings.TrimSpace(cut) + " […]"
}
