package export

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/store"
	"macro-collector/internal/textdiff"
)

// Export kinds (exports.kind).
const (
	KindWeekly    = "weekly"
	KindEventFOMC = "event_fomc"
)

// fomcStateKey remembers the last exported statement, so a statement is
// exported once whichever path (FOMC poller or RSS job) stored it.
const fomcStateKey = "export:" + KindEventFOMC

// Exported describes a finished export for the sink.
type Exported struct {
	Kind, Region, ISOWeek string
	Files                 []string // absolute paths
}

// EventSink is told about every event export: git push and notification
// (§12.7–12.9) plug in here in phase 6.
type EventSink interface {
	Exported(ctx context.Context, e Exported) error
}

// LogSink only logs; it stands in until git sync and notifications exist.
type LogSink struct{ Log *slog.Logger }

// Exported implements EventSink.
func (s LogSink) Exported(_ context.Context, e Exported) error {
	s.Log.Info("event export ready (git push and notification: phase 6)",
		"kind", e.Kind, "region", e.Region, "iso_week", e.ISOWeek, "files", strings.Join(e.Files, ", "))
	return nil
}

// Events writes event exports.
type Events struct {
	Store       *store.Store
	Config      *config.Config
	ExchangeDir string
	Location    *time.Location // Europe/Bratislava: period and generated_at
	ReleaseLoc  *time.Location // America/New_York: statement times
	Sink        EventSink
	Now         func() time.Time // defaults to time.Now
}

// Exported reports whether statement was already exported.
func (x *Events) Exported(ctx context.Context, statement model.Document) (bool, error) {
	st, ok, err := x.Store.FetchStateOf(ctx, fomcStateKey)
	return ok && st.Cursor == statement.ExternalID, err
}

// FOMC writes usa/monetary/fomc_diff.md comparing statement with previous
// (zero Document if there is none), copies it to archive/<iso_week>/,
// records the export and tells the sink. The files are replaced
// atomically.
func (x *Events) FOMC(ctx context.Context, statement, previous model.Document) (Exported, error) {
	owner, ok := x.Config.Owner(config.ReportFOMCDiff)
	if !ok {
		return Exported{}, nil // no region exports it
	}
	path := owner.Region.Path(owner.Agent, owner.Report.File)
	now := x.now()
	week := ISOWeek(statement.PublishedAt, x.Location)
	from, err := x.periodStart(ctx, statement.PublishedAt, owner.Region.ID)
	if err != nil {
		return Exported{}, err
	}
	docs, err := meetingDocs(ctx, x.Store, statement, now.Add(time.Second), x.ReleaseLoc)
	if err != nil {
		return Exported{}, err
	}
	tmpl, err := LoadTemplates(x.Config)
	if err != nil {
		return Exported{}, err
	}
	md, js, err := reportFiles(tmpl, owner.Report.TemplateName(), path, config.ReportFOMCDiff, fomcReport(owner, statement, previous, docs, week, from, now, x.Location, x.ReleaseLoc, nil), false)
	if err != nil {
		return Exported{}, err
	}
	e := Exported{Kind: KindEventFOMC, Region: owner.Region.ID, ISOWeek: week}
	for _, f := range []File{md, js} {
		for _, p := range []string{filepath.Join(x.ExchangeDir, f.Path), filepath.Join(x.ExchangeDir, "archive", week, f.Path)} {
			if err := writeAtomic(p, f.Body); err != nil {
				return Exported{}, err
			}
			e.Files = append(e.Files, p)
		}
	}
	if err := x.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := store.InsertExport(tx, week, KindEventFOMC, owner.Region.ID, now); err != nil {
			return err
		}
		return store.SetFetchOK(tx, fomcStateKey, statement.ExternalID, now)
	}); err != nil {
		return Exported{}, fmt.Errorf("export fomc: %w", err)
	}
	if x.Sink != nil {
		if err := x.Sink.Exported(ctx, e); err != nil {
			return e, fmt.Errorf("export fomc sink: %w", err)
		}
	}
	return e, nil
}

// periodStart is §17.1 for event exports: the last weekly export, else the
// start of the export week.
func (x *Events) periodStart(ctx context.Context, at time.Time, region string) (time.Time, error) {
	last, ok, err := x.Store.LastExport(ctx, KindWeekly, region)
	if err != nil {
		return time.Time{}, err
	}
	if ok && last.Before(at) {
		return last, nil
	}
	return WeekEnd(at, x.Location).AddDate(0, 0, -7), nil
}

// FOMCDocument is a statement, its time in ET.
type FOMCDocument struct {
	Published time.Time `json:"published"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Body      string    `json:"body"`
}

// FOMCNotice opens the file in a week without a meeting; Next is zero when
// the next meeting is not in the calendar.
type FOMCNotice struct {
	LastMeeting Day `json:"last_meeting"`
	WeeksAgo    int `json:"weeks_ago"`
	Next        Day `json:"next"`
}

// FOMCDissent is a group of votes against the decision.
type FOMCDissent struct {
	Names     []string `json:"names"`
	Preferred string   `json:"preferred"`
	MoveBP    int      `json:"move_bp"`
}

// FOMCDecision is the decision read from a statement of Date (FIXES_W44 B5).
type FOMCDecision struct {
	Date     Day           `json:"date"`
	Action   string        `json:"action"`
	Range    string        `json:"range"`
	MoveBP   int           `json:"move_bp"`
	For      int           `json:"for"`
	Against  int           `json:"against"`
	Dissents []FOMCDissent `json:"dissents"`
}

// DiffEntry is one sentence of the comparison of two statements.
type DiffEntry struct {
	Kind string `json:"kind"`
	Old  string `json:"old"`
	New  string `json:"new"`
	Para int    `json:"para"`
}

// FOMCMeetingDoc is another document of the meeting, its time in ET.
type FOMCMeetingDoc struct {
	DocType   string    `json:"doc_type"`
	Title     string    `json:"title"`
	Published time.Time `json:"published"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
}

// FOMCDiffReport is usa/monetary/fomc_diff.md: the decision, the diff with
// the previous statement (null without one) and the meeting's other
// documents in full.
type FOMCDiffReport struct {
	Header    Header           `json:"header"`
	Notice    *FOMCNotice      `json:"notice"`
	Statement FOMCDocument     `json:"statement"`
	Previous  *FOMCDocument    `json:"previous"`
	Decisions []FOMCDecision   `json:"decisions"`
	Diff      []DiffEntry      `json:"diff"`
	Documents []FOMCMeetingDoc `json:"documents"`
}

var diffKinds = map[textdiff.Kind]string{textdiff.Equal: "equal", textdiff.Added: "added", textdiff.Removed: "removed", textdiff.Changed: "changed"}

func fomcDocument(d model.Document, releaseLoc *time.Location) FOMCDocument {
	return FOMCDocument{Published: d.PublishedAt.In(releaseLoc), Title: d.Title, URL: d.URL, Body: d.Body}
}

func fomcDecision(d model.Document, releaseLoc *time.Location) FOMCDecision {
	p := fedtexts.ParseDecision(d.Body)
	out := FOMCDecision{Date: Day{d.PublishedAt.In(releaseLoc)}, Action: p.Action, Range: p.Range, MoveBP: p.MoveBP, For: p.For, Against: p.Against, Dissents: []FOMCDissent{}}
	for _, x := range p.Dissents {
		out.Dissents = append(out.Dissents, FOMCDissent{Names: x.Names, Preferred: x.Preferred, MoveBP: x.MoveBP})
	}
	return out
}

// fomcReport is fomc_diff.md of a statement; notice, if any, comes first
// (the weekly refresh in a week without a meeting).
func fomcReport(owner config.ReportOwner, st, prev model.Document, docs []model.Document, week string, from, now time.Time, loc, releaseLoc *time.Location, notice *FOMCNotice) *FOMCDiffReport {
	m := &FOMCDiffReport{
		Header: Header{SchemaVersion: SchemaVersion, Region: owner.Region.ID, Agent: owner.Agent.ID, ISOWeek: week,
			Period: Period{From: from.In(loc), To: st.PublishedAt.In(loc), TZ: loc.String()}, GeneratedAt: now.In(loc), Kind: KindEventFOMC,
			SourcesOK: []string{}, SourcesFailed: []string{}, Title: owner.Report.Title + " — FOMC statement — " + st.PublishedAt.In(releaseLoc).Format(time.DateOnly)},
		Notice: notice, Statement: fomcDocument(st, releaseLoc), Decisions: []FOMCDecision{fomcDecision(st, releaseLoc)}, Documents: []FOMCMeetingDoc{},
	}
	if prev.ExternalID != "" {
		p := fomcDocument(prev, releaseLoc)
		m.Previous = &p
		m.Decisions = append(m.Decisions, fomcDecision(prev, releaseLoc))
		m.Diff = []DiffEntry{}
		for _, e := range textdiff.Compare(prev.Body, st.Body).Entries {
			m.Diff = append(m.Diff, DiffEntry{Kind: diffKinds[e.Kind], Old: e.Old, New: e.New, Para: e.Para})
		}
	}
	for _, d := range docs {
		m.Documents = append(m.Documents, FOMCMeetingDoc{DocType: d.DocType, Title: d.Title, Published: d.PublishedAt.In(releaseLoc), Body: d.Body, URL: d.URL})
	}
	return m
}

// diffMarkdown renders the comparison of two statements.
func diffMarkdown(entries []DiffEntry) string {
	d := textdiff.Diff{}
	for _, e := range entries {
		var k textdiff.Kind
		for kind, name := range diffKinds {
			if name == e.Kind {
				k = kind
			}
		}
		d.Entries = append(d.Entries, textdiff.Entry{Kind: k, Old: e.Old, New: e.New, Para: e.Para})
	}
	return d.Markdown()
}

func fomcETime(t time.Time) string { return t.Format("2006-01-02 15:04") + " ET" }

func fomcLink(d FOMCDocument) string {
	if d.URL == "" {
		return d.Title
	}
	return "[" + d.Title + "](" + d.URL + ")"
}

// fomcNotice writes the first line of the file in a week without a meeting.
func fomcNoticeLine(n FOMCNotice) string {
	next := "нет в календаре"
	if !n.Next.IsZero() {
		next = n.Next.Format(time.DateOnly)
	}
	return fmt.Sprintf("⚠ Последнее заседание: %s (%d нед. назад). Следующее: %s", n.LastMeeting.Format(time.DateOnly), n.WeeksAgo, next)
}

// meetingDocs are the implementation note and the press conference
// transcript of statement's meeting stored before until, note first.
func meetingDocs(ctx context.Context, st *store.Store, statement model.Document, until time.Time, releaseLoc *time.Location) ([]model.Document, error) {
	docs, err := st.DocumentsBetween(ctx, fedtexts.Source, statement.PublishedAt.Add(-time.Hour), until)
	if err != nil {
		return nil, err
	}
	stamp := statement.PublishedAt.In(releaseLoc).Format("20060102")
	var note, conf []model.Document
	for _, d := range docs {
		if !strings.Contains(d.ExternalID, stamp) {
			continue
		}
		switch d.DocType {
		case fedtexts.DocImplementationNote:
			note = append(note, d)
		case fedtexts.DocPressConference:
			conf = append(conf, d)
		}
	}
	return append(note, conf...), nil
}

// decisionVote is "10–2".
func decisionVote(d FOMCDecision) string {
	if d.For > 0 {
		return fmt.Sprintf("%d–%d", d.For, d.Against)
	}
	return dash
}

// decisionAgainst names the dissents and what they preferred.
func decisionAgainst(d FOMCDecision) string {
	var against []string
	for _, x := range d.Dissents {
		s := strings.Join(x.Names, ", ")
		switch x.Preferred {
		case "":
		case fedtexts.ActionMaintain:
			s += " — за сохранение ставки"
		default:
			s += " — за " + actionWords(x.Preferred, x.MoveBP)
		}
		against = append(against, s)
	}
	if len(against) == 0 {
		return dash
	}
	return strings.Join(against, "; ")
}

// decisionCell: "повышение на 25 б.п. → 3.75–4.00%", "без изменений, 3.75–4.00%".
func decisionCell(d FOMCDecision) string {
	if d.Action == "" {
		return dash + " (не разобрано)"
	}
	if d.Action == fedtexts.ActionMaintain {
		return strings.TrimSuffix("без изменений, "+d.Range, ", ")
	}
	s := actionWords(d.Action, d.MoveBP)
	if d.Range != "" {
		s += " → " + d.Range
	}
	return s
}

// actionWords names an action: "повышение на 25 б.п.", "снижение",
// "без изменений".
func actionWords(action string, moveBP int) string {
	var s string
	switch action {
	case fedtexts.ActionRaise:
		s = "повышение"
	case fedtexts.ActionLower:
		s = "снижение"
	case fedtexts.ActionMaintain:
		return "без изменений"
	default:
		return ""
	}
	if moveBP > 0 {
		s += fmt.Sprintf(" на %d б.п.", moveBP)
	}
	return s
}

// writeAtomic writes via a temp file in the same directory and renames it,
// so a reader never sees a half-written file.
func writeAtomic(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("export mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("export temp %s: %w", path, err)
	}
	tmp := f.Name()
	_, werr := f.WriteString(body)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		return fmt.Errorf("export write %s: %w", path, errors.Join(werr, cerr))
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("export chmod %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("export rename %s: %w", path, err)
	}
	return nil
}

func (x *Events) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

// fomcRefresh re-renders fomc_diff.md at a weekly export from the latest
// statement, with the meeting documents that came after the event export
// (the transcript comes days later). In a week without a statement it
// opens with how old the last meeting is and when the next one is
// (FIXES_W44 B5), so the file is not taken for this week's. ok is false
// without any statement in the database.
func (w *week) fomcRefresh(ctx context.Context) (f File, ok bool, err error) {
	docs, err := w.st.DocumentsBetween(ctx, fedtexts.Source, w.to.AddDate(-1, 0, 0), w.to)
	if err != nil {
		return File{}, false, err
	}
	var sts []model.Document
	for _, d := range docs {
		if d.DocType == fedtexts.DocStatement {
			sts = append(sts, d)
		}
	}
	if len(sts) == 0 {
		return File{}, false, nil
	}
	st, prev := sts[len(sts)-1], model.Document{}
	if len(sts) >= 2 {
		prev = sts[len(sts)-2]
	}
	var nextDay Day
	evs, err := w.releasesBetween(ctx, w.to, w.to.AddDate(0, 0, 120))
	if err != nil {
		return File{}, false, err
	}
	for _, e := range evs {
		if e.Source == fedtexts.SourceFOMC && e.Name == fedtexts.NameDecision && e.Status != model.StatusRescheduled {
			nextDay = Day{e.ScheduledAt.In(w.et)}
			break
		}
	}
	var notice *FOMCNotice
	if st.PublishedAt.Before(w.from) {
		notice = &FOMCNotice{LastMeeting: Day{st.PublishedAt.In(w.et)}, WeeksAgo: int(w.to.Sub(st.PublishedAt).Hours() / 24 / 7), Next: nextDay} // whole weeks
	}
	mdocs, err := meetingDocs(ctx, w.st, st, w.to, w.et)
	if err != nil {
		return File{}, false, err
	}
	stWeek := ISOWeek(st.PublishedAt, w.loc)
	from := WeekEnd(st.PublishedAt, w.loc).AddDate(0, 0, -7)
	owner := config.ReportOwner{Region: w.region, Agent: w.agent, Report: w.rep}
	body, err := w.report(w.path(), config.ReportFOMCDiff, fomcReport(owner, st, prev, mdocs, stWeek, from, w.now, w.loc, w.et, notice))
	if err != nil {
		return File{}, false, err
	}
	return File{Path: w.path(), Body: body}, true, nil
}
