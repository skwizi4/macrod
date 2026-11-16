package export

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/store"
	"macro-collector/internal/textdiff"
)

// KindEventECB is the export of an ECB decision day (exports.kind).
const KindEventECB = "event_ecb"

// ecbStateKey remembers the last exported decision, so a decision day is
// exported once (ТЗ §12.7).
const ecbStateKey = "export:" + KindEventECB

// ecbLookback bounds the search for the previous meeting (6 weeks apart).
const ecbLookback = 120 * 24 * time.Hour

// ECBDocument is a decision release or a statement introduction, its time
// in the ECB time zone.
type ECBDocument struct {
	Published time.Time `json:"published"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Body      string    `json:"body"`
}

// ECBDecision is what a decision release says about the key rates; Parsed
// false means the wording was not understood and no number is shown.
type ECBDecision struct {
	Date          Day     `json:"date"`
	Parsed        bool    `json:"parsed"`
	Action        string  `json:"action"`
	MoveBP        int     `json:"move_bp"`
	DFR           float64 `json:"dfr"`
	MRO           float64 `json:"mro"`
	MLF           float64 `json:"mlf"`
	EffectiveFrom Day     `json:"effective_from"`
	URL           string  `json:"url"`
}

// ECBNotice opens the file in a week without a meeting.
type ECBNotice struct {
	LastMeeting Day `json:"last_meeting"`
	WeeksAgo    int `json:"weeks_ago"`
	Next        Day `json:"next"`
}

// ECBMeetingDoc is another document of the meeting (the press conference
// Q&A), its time in the ECB time zone.
type ECBMeetingDoc struct {
	DocType   string    `json:"doc_type"`
	Title     string    `json:"title"`
	Published time.Time `json:"published"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
}

// ECBDiffReport is ecb_diff.md: the decision against the previous one, the
// diff of the decision releases and of the statement introductions (null
// without a previous document or without the statement), and the Q&A.
type ECBDiffReport struct {
	Header            Header          `json:"header"`
	Notice            *ECBNotice      `json:"notice"`
	Decision          ECBDocument     `json:"decision"`
	PreviousDecision  *ECBDocument    `json:"previous_decision"`
	Decisions         []ECBDecision   `json:"decisions"`
	DecisionDiff      []DiffEntry     `json:"decision_diff"`
	Statement         *ECBDocument    `json:"statement"`
	PreviousStatement *ECBDocument    `json:"previous_statement"`
	StatementDiff     []DiffEntry     `json:"statement_diff"`
	StatementWait     string          `json:"statement_wait"` // "15:30": when the export stops waiting for the statement
	Documents         []ECBMeetingDoc `json:"documents"`
}

// ECBMeeting is a decision and what belongs to it, as stored.
type ECBMeeting struct {
	Decision, PreviousDecision   model.Document
	Statement, PreviousStatement model.Document // zero when not stored
	QA                           []model.Document
}

// LoadECBMeeting collects the documents of decision's meeting and the
// previous one stored before until.
func LoadECBMeeting(ctx context.Context, st *store.Store, decision model.Document, until time.Time, loc *time.Location) (ECBMeeting, error) {
	docs, err := st.DocumentsBetween(ctx, ecbtexts.Source, decision.PublishedAt.Add(-ecbLookback), until)
	if err != nil {
		return ECBMeeting{}, err
	}
	m := ECBMeeting{Decision: decision}
	sameDay := func(a, b time.Time) bool {
		return a.In(loc).Format(time.DateOnly) == b.In(loc).Format(time.DateOnly)
	}
	for _, d := range docs {
		switch {
		case d.DocType == ecbtexts.DocDecision && d.PublishedAt.Before(decision.PublishedAt) && !sameDay(d.PublishedAt, decision.PublishedAt):
			m.PreviousDecision = d // oldest first: the last one wins
		case d.DocType == ecbtexts.DocStatement && sameDay(d.PublishedAt, decision.PublishedAt):
			m.Statement = d
		}
	}
	for _, d := range docs {
		switch {
		case d.DocType == ecbtexts.DocStatement && m.PreviousDecision.ExternalID != "" && sameDay(d.PublishedAt, m.PreviousDecision.PublishedAt):
			m.PreviousStatement = d
		case d.DocType == ecbtexts.DocPressConference && m.Statement.ExternalID != "" && d.ExternalID == m.Statement.ExternalID+ecbtexts.QASuffix:
			m.QA = append(m.QA, d)
		}
	}
	return m, nil
}

// ECBDecisionOn is the stored decision published on day (ECB time zone).
func ECBDecisionOn(ctx context.Context, st *store.Store, day time.Time, loc *time.Location) (model.Document, bool, error) {
	return ecbDocOn(ctx, st, ecbtexts.DocDecision, day, loc)
}

// ECBStatementOn is the stored statement introduction published on day.
func ECBStatementOn(ctx context.Context, st *store.Store, day time.Time, loc *time.Location) (model.Document, bool, error) {
	return ecbDocOn(ctx, st, ecbtexts.DocStatement, day, loc)
}

func ecbDocOn(ctx context.Context, st *store.Store, docType string, day time.Time, loc *time.Location) (model.Document, bool, error) {
	y, mo, d := day.In(loc).Date()
	from := time.Date(y, mo, d, 0, 0, 0, 0, loc)
	docs, err := st.DocumentsBetween(ctx, ecbtexts.Source, from, from.AddDate(0, 0, 1))
	if err != nil {
		return model.Document{}, false, err
	}
	for i := len(docs) - 1; i >= 0; i-- {
		if docs[i].DocType == docType {
			return docs[i], true, nil
		}
	}
	return model.Document{}, false, nil
}

// ECBExported reports whether decision's day was already exported.
func (x *Events) ECBExported(ctx context.Context, decision model.Document) (bool, error) {
	st, ok, err := x.Store.FetchStateOf(ctx, ecbStateKey)
	return ok && st.Cursor == decision.ExternalID, err
}

// ECB writes ecb_diff.md of the region owning it for decision's meeting,
// copies it to archive/<iso_week>/, records the export together with the
// exported decision (one transaction: exactly one export per decision) and
// tells the sink.
func (x *Events) ECB(ctx context.Context, decision model.Document) (Exported, error) {
	owner, ok := x.Config.Owner(config.ReportECBDiff)
	if !ok {
		return Exported{}, nil
	}
	loc := x.Config.ECBTexts.Location
	now := x.now()
	m, err := LoadECBMeeting(ctx, x.Store, decision, now.Add(time.Second), loc)
	if err != nil {
		return Exported{}, err
	}
	week := ISOWeek(decision.PublishedAt, x.Location)
	from, err := x.periodStart(ctx, decision.PublishedAt, owner.Region.ID)
	if err != nil {
		return Exported{}, err
	}
	tmpl, err := LoadTemplates(x.Config)
	if err != nil {
		return Exported{}, err
	}
	path := owner.Region.Path(owner.Agent, owner.Report.File)
	rep := ecbReport(owner, m, x.Config.ECBTexts.DayMode.StatementWait, week, from, now, x.Location, loc, nil)
	md, js, err := reportFiles(tmpl, owner.Report.TemplateName(), path, config.ReportECBDiff, rep, false)
	if err != nil {
		return Exported{}, err
	}
	e := Exported{Kind: KindEventECB, Region: owner.Region.ID, ISOWeek: week}
	for _, f := range []File{md, js} {
		for _, p := range []string{filepath.Join(x.ExchangeDir, f.Path), filepath.Join(x.ExchangeDir, "archive", week, f.Path)} {
			if err := writeAtomic(p, f.Body); err != nil {
				return Exported{}, err
			}
			e.Files = append(e.Files, p)
		}
	}
	if err := x.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := store.InsertExport(tx, week, KindEventECB, owner.Region.ID, now); err != nil {
			return err
		}
		return store.SetFetchOK(tx, ecbStateKey, decision.ExternalID, now)
	}); err != nil {
		return Exported{}, fmt.Errorf("export ecb: %w", err)
	}
	if x.Sink != nil {
		if err := x.Sink.Exported(ctx, e); err != nil {
			return e, fmt.Errorf("export ecb sink: %w", err)
		}
	}
	return e, nil
}

func ecbDocument(d model.Document, loc *time.Location) *ECBDocument {
	if d.ExternalID == "" {
		return nil
	}
	return &ECBDocument{Published: d.PublishedAt.In(loc), Title: d.Title, URL: d.URL, Body: d.Body}
}

func ecbDecision(d model.Document, loc *time.Location) ECBDecision {
	p := ecbtexts.ParseDecision(d.Body)
	out := ECBDecision{Date: Day{d.PublishedAt.In(loc)}, Parsed: p.Parsed, URL: d.URL}
	if p.Parsed {
		out.Action, out.MoveBP, out.DFR, out.MRO, out.MLF = p.Action, p.MoveBP, p.DFR, p.MRO, p.MLF
		if !p.EffectiveFrom.IsZero() {
			out.EffectiveFrom = Day{p.EffectiveFrom}
		}
	}
	return out
}

func diffEntries(old, new string) []DiffEntry {
	out := []DiffEntry{}
	for _, e := range textdiff.Compare(old, new).Entries {
		out = append(out, DiffEntry{Kind: diffKinds[e.Kind], Old: e.Old, New: e.New, Para: e.Para})
	}
	return out
}

// ecbReport is ecb_diff.md of a meeting; notice, if any, comes first (the
// weekly refresh in a week without a meeting).
func ecbReport(owner config.ReportOwner, m ECBMeeting, statementWait, week string, from, now time.Time, loc, ecbLoc *time.Location, notice *ECBNotice) *ECBDiffReport {
	day := m.Decision.PublishedAt.In(ecbLoc).Format(time.DateOnly)
	r := &ECBDiffReport{
		Header: Header{SchemaVersion: SchemaVersion, Region: owner.Region.ID, Agent: owner.Agent.ID, ISOWeek: week,
			Period: Period{From: from.In(loc), To: m.Decision.PublishedAt.In(loc), TZ: loc.String()}, GeneratedAt: now.In(loc), Kind: KindEventECB,
			SourcesOK: []string{ecbtexts.Source}, SourcesFailed: []string{}, Title: owner.Report.Title + " — ECB decision — " + day},
		Notice: notice, Decision: *ecbDocument(m.Decision, ecbLoc), Decisions: []ECBDecision{ecbDecision(m.Decision, ecbLoc)},
		PreviousDecision: ecbDocument(m.PreviousDecision, ecbLoc), Statement: ecbDocument(m.Statement, ecbLoc),
		PreviousStatement: ecbDocument(m.PreviousStatement, ecbLoc), StatementWait: statementWait, Documents: []ECBMeetingDoc{},
	}
	if r.PreviousDecision != nil {
		r.Decisions = append(r.Decisions, ecbDecision(m.PreviousDecision, ecbLoc))
		r.DecisionDiff = diffEntries(m.PreviousDecision.Body, m.Decision.Body)
	}
	if r.Statement != nil && r.PreviousStatement != nil {
		r.StatementDiff = diffEntries(m.PreviousStatement.Body, m.Statement.Body)
	}
	for _, d := range m.QA {
		r.Documents = append(r.Documents, ECBMeetingDoc{DocType: d.DocType, Title: d.Title, Published: d.PublishedAt.In(ecbLoc), Body: d.Body, URL: d.URL})
	}
	return r
}

// ecbTime is "2026-10-29 14:15 CET".
func ecbTime(t time.Time) string { return t.Format("2006-01-02 15:04 MST") }

func ecbLink(d ECBDocument) string {
	if d.URL == "" {
		return d.Title
	}
	return "[" + d.Title + "](" + d.URL + ")"
}

func ecbNoticeLine(n ECBNotice) string {
	return fomcNoticeLine(FOMCNotice(n))
}

// ecbDiffMarkdown is diffMarkdown one heading level down: the file holds
// two comparisons, each under its own "##" heading.
func ecbDiffMarkdown(entries []DiffEntry) string {
	return strings.ReplaceAll(diffMarkdown(entries), "\n## ", "\n### ")
}

// ecbRate is "2.50%".
func ecbRate(v float64) string { return fmt.Sprintf("%.2f%%", v) }

// ecbDecisionCell: "повышение на 25 б.п.", "без изменений", "не распознано".
func ecbDecisionCell(d ECBDecision) string {
	if !d.Parsed {
		return "не распознано — см. текст решения"
	}
	switch d.Action {
	case ecbtexts.ActionMaintain:
		return "без изменений"
	case ecbtexts.ActionRaise:
		return fmt.Sprintf("повышение на %d б.п.", d.MoveBP)
	case ecbtexts.ActionLower:
		return fmt.Sprintf("снижение на %d б.п.", d.MoveBP)
	}
	return dash
}

// ecbRateCell is a rate, or the "not parsed" mark.
func ecbRateCell(d ECBDecision, v float64) string {
	if !d.Parsed {
		return "не распознано"
	}
	return ecbRate(v)
}

// ecbRefresh re-renders ecb_diff.md at a weekly export from the latest
// decision, with the statement and Q&A that came after the event export. In
// a week without a decision it opens with how old the last meeting is and
// when the next one is. ok is false without any decision stored.
func (w *week) ecbRefresh(ctx context.Context) (f File, ok bool, err error) {
	loc := w.cfg.ECBTexts.Location
	if loc == nil {
		return File{}, false, nil
	}
	docs, err := w.st.DocumentsBetween(ctx, ecbtexts.Source, w.to.AddDate(-1, 0, 0), w.to)
	if err != nil {
		return File{}, false, err
	}
	var decision model.Document
	for _, d := range docs {
		if d.DocType == ecbtexts.DocDecision {
			decision = d
		}
	}
	if decision.ExternalID == "" {
		return File{}, false, nil
	}
	var nextDay Day
	evs, err := w.releasesBetween(ctx, w.to, w.to.AddDate(0, 0, 120))
	if err != nil {
		return File{}, false, err
	}
	for _, e := range evs {
		if e.Source == ecbtexts.SourceMeeting && e.Name == ecbtexts.NameDecision && e.Status != model.StatusRescheduled {
			nextDay = Day{e.ScheduledAt.In(loc)}
			break
		}
	}
	var notice *ECBNotice
	if decision.PublishedAt.Before(w.from) {
		notice = &ECBNotice{LastMeeting: Day{decision.PublishedAt.In(loc)}, WeeksAgo: int(w.to.Sub(decision.PublishedAt).Hours() / 24 / 7), Next: nextDay}
	}
	m, err := LoadECBMeeting(ctx, w.st, decision, w.to, loc)
	if err != nil {
		return File{}, false, err
	}
	week := ISOWeek(decision.PublishedAt, w.loc)
	from := WeekEnd(decision.PublishedAt, w.loc).AddDate(0, 0, -7)
	owner := config.ReportOwner{Region: w.region, Agent: w.agent, Report: w.rep}
	body, err := w.report(w.path(), config.ReportECBDiff, ecbReport(owner, m, w.cfg.ECBTexts.DayMode.StatementWait, week, from, w.now, w.loc, loc, notice))
	if err != nil {
		return File{}, false, err
	}
	return File{Path: w.path(), Body: body}, true, nil
}
