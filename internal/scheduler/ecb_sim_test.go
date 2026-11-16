package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/store"
)

const ecbFixtures = "../../testdata/ecb_texts"

const (
	ecbDecOct = "/press/pr/date/2026/html/ecb.mp261029~9a493adf17.en.html"
	ecbStOct  = "/press/press_conference/monetary-policy-statement/2026/html/ecb.is261029~5c5ffe99e5.en.html"
	ecbDecSep = "/press/pr/date/2026/html/ecb.mp260910~314e508016.en.html"
	ecbStSep  = "/press/press_conference/monetary-policy-statement/2026/html/ecb.is260910~6a45359cfc.en.html"
)

// ecbSite serves recorded ecb.europa.eu pages on the virtual clock: the
// 29 October decision appears in the feed and the decisions list at
// decisionAt, the statement at statementAt (zero: never) without its Q&A,
// which is added at qaAt. Feed and list requests are counted.
type ecbSite struct {
	clock                         Clock
	decisionAt, statementAt, qaAt time.Time
	mu                            sync.Mutex
	rss, lists                    []time.Time
}

func (s *ecbSite) published(at time.Time) bool { return !at.IsZero() && !s.clock.Now().Before(at) }

func (s *ecbSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := s.clock.Now()
	p := r.URL.Path
	switch {
	case p == "/rss/press.html":
		s.mu.Lock()
		s.rss = append(s.rss, now)
		s.mu.Unlock()
		var items string
		if s.published(s.statementAt) {
			items += item("Christine Lagarde: Monetary policy statement (with Q&A)", ecbStOct, s.statementAt)
		}
		if s.published(s.decisionAt) {
			items += item("Monetary policy decisions", ecbDecOct, s.decisionAt)
		}
		fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?><rss version="2.0"><channel><title>ECB</title>%s</channel></rss>`, items)
		return
	case strings.HasSuffix(p, "index_include.en.html"):
		s.mu.Lock()
		s.lists = append(s.lists, now)
		s.mu.Unlock()
		var link string
		switch {
		case strings.Contains(p, "/govcdec/mopo/") && s.published(s.decisionAt):
			link = ecbDecOct
		case strings.Contains(p, "/monetary-policy-statement/") && s.published(s.statementAt):
			link = ecbStOct
		}
		if link != "" {
			fmt.Fprintf(w, `<dl><dt isoDate="2026-10-29">29 October 2026</dt><dd><div class="title"><a href="%s">x</a></div></dd></dl>`, link)
		} else {
			fmt.Fprint(w, `<dl></dl>`)
		}
		return
	case p == ecbDecOct && !s.published(s.decisionAt), p == ecbStOct && !s.published(s.statementAt):
		w.WriteHeader(http.StatusNotFound)
		return
	}
	b, err := os.ReadFile(filepath.Join(ecbFixtures, filepath.FromSlash(strings.TrimPrefix(p, "/"))))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if p == ecbStOct && !s.published(s.qaAt) {
		b = stripQA(b)
	}
	w.Write(b)
}

func item(title, link string, at time.Time) string {
	return fmt.Sprintf("<item><title>%s</title><link>https://www.ecb.europa.eu/%s</link><pubDate>%s</pubDate></item>",
		title, link, at.Format(time.RFC1123Z))
}

// stripQA is the statement page as it is during the press conference.
func stripQA(b []byte) []byte {
	s := string(b)
	i := strings.Index(s, `<a id="qa"`)
	j := i + strings.Index(s[i:], "</p>") + len("</p>")
	k := strings.Index(s, `<div class="footnotes"`)
	return []byte(s[:j] + "</div>" + s[k:])
}

type ecbSim struct {
	site     *ecbSite
	st       *store.Store
	exchange string
	sched    *Scheduler
	sink     *recordingSink
	alerts   []string
	clock    *fakeClock
	event    model.ReleaseEvent
}

// newECBSim prepares the 29 October 2026 decision day (14:15 CET) with the
// September meeting stored; the clock starts at 14:13:30.
func newECBSim(t *testing.T, statementAt, qaAt time.Time) *ecbSim {
	t.Helper()
	ctx := context.Background()
	berlin, bts := mustLoc(t, "Europe/Berlin"), mustLoc(t, "Europe/Bratislava")
	decision := time.Date(2026, 10, 29, 13, 15, 0, 0, time.UTC)
	clock := &fakeClock{now: decision.Add(-90 * time.Second)}
	site := &ecbSite{clock: clock, decisionAt: decision.Add(30 * time.Second), statementAt: statementAt, qaAt: qaAt}
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)

	sim := &ecbSim{site: site, exchange: t.TempDir(), sink: &recordingSink{}, clock: clock, st: openStore(t)}
	mode := config.ECBDayMode{Lead: time.Minute, RSSInterval: 20 * time.Second, ListInterval: time.Minute, FastFor: 75 * time.Minute,
		AlertDecisionAt: "14:25", StatementWait: "15:30", AlertDecision: config.ClockTime{Hour: 14, Minute: 25}, StatementBy: config.ClockTime{Hour: 15, Minute: 30}}
	cfg := &config.Config{
		Location: bts,
		Export:   config.Export{ExchangeDir: sim.exchange},
		FedTexts: config.FedTexts{BaseURL: srv.URL, PollInterval: 15 * time.Minute, Agent: "eu_monetary"},
		ECBTexts: config.ECBTexts{Enabled: true, BaseURL: srv.URL, RSS: srv.URL + "/rss/press.html", Location: berlin, Agent: "eu_monetary",
			PollInterval: 15 * time.Minute, DayMode: mode,
			Lists: config.ECBLists{Decisions: "/press/govcdec/mopo/{year}/html/index_include.en.html",
				Statements: "/press/press_conference/monetary-policy-statement/{year}/html/index_include.en.html",
				Accounts:   "/press/accounts/{year}/html/index_include.en.html"},
			Times: config.ECBTimes{DecisionAt: config.ClockTime{Hour: 14, Minute: 15}, PressConferenceAt: config.ClockTime{Hour: 14, Minute: 45},
				AccountsAt: config.ClockTime{Hour: 13, Minute: 30}}},
		Calendar: config.Calendar{ReleaseLocation: nyLoc(t), RefreshCron: "0 5 * * *"},
		Regions: []config.Region{{ID: "eu", Agents: []config.Agent{{ID: "monetary_macro", Folder: "monetary_macro", SeriesTags: []string{"eu_monetary"},
			Reports: []config.Report{{Kind: config.ReportECBDiff, File: "ecb_diff.md", Title: "EU Monetary & Macro"}}}}}},
	}
	ecb := ecbtexts.New(httpx.WithInterval(0), httpx.WithRetries(0, time.Millisecond))
	sim.sched = New(Deps{Config: cfg, Store: sim.st, FRED: fakeFull{&fakeFRED{}}, Fed: fedtexts.New(httpx.WithInterval(0)), ECB: ecb,
		Root: slog.New(slog.DiscardHandler), FetchCron: "0 6 * * *", Clock: clock, Sink: sim.sink,
		Alert: func(_ context.Context, text string) error { sim.alerts = append(sim.alerts, text); return nil }})
	sim.sched.Events.spawn = func(f func()) { f() }

	// The September meeting is stored: its decision and statement (with Q&A).
	site.decisionAt = time.Time{}
	sept := &ecbtexts.Site{Client: ecb, BaseURL: srv.URL, Location: berlin, Log: slog.New(slog.DiscardHandler),
		Times: map[string]time.Duration{ecbtexts.DocDecision: 14*time.Hour + 15*time.Minute, ecbtexts.DocStatement: 14*time.Hour + 45*time.Minute}}
	var docs []model.Document
	for _, p := range []string{ecbDecSep, ecbStSep} {
		ds, err := sept.Fetch(ctx, p, time.Time{}, "", "")
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, ds...)
	}
	site.decisionAt = decision.Add(30 * time.Second)
	sim.event = model.ReleaseEvent{Source: ecbtexts.SourceMeeting, Name: ecbtexts.NameDecision, ScheduledAt: decision,
		Region: "eu", Importance: "high", Related: model.Related{Meeting: "2026-10-29"}}
	if err := sim.st.Write(ctx, func(tx *sql.Tx) error {
		for _, d := range docs {
			if _, _, err := store.UpsertDocument(tx, d, decision.AddDate(0, 0, -40)); err != nil {
				return err
			}
		}
		_, err := store.SyncReleases(tx, ecbtexts.SourceMeeting, ecbtexts.NameDecision, decision.AddDate(0, 0, -1), decision.AddDate(0, 0, 1),
			[]model.ReleaseEvent{sim.event})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return sim
}

func (s *ecbSim) exports(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.st.ReadDB().QueryRow(`SELECT count(*) FROM exports WHERE kind = 'event_ecb'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (s *ecbSim) file(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.exchange, "eu", "monetary_macro", "ecb_diff.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func berlinClock(ts []time.Time) []string {
	loc, _ := time.LoadLocation("Europe/Berlin")
	var out []string
	for _, t := range ts {
		out = append(out, t.In(loc).Format("15:04:05"))
	}
	return out
}

// TestECBDaySimulation replays 29 October 2026: the decision appears at
// 14:15:30, the statement at 14:47 (the Q&A is added at 15:40). The feed is
// polled every 20 s from 14:14, the lists every minute; the day is exported
// once, with both comparisons, after the statement.
func TestECBDaySimulation(t *testing.T) {
	ctx := context.Background()
	decision := time.Date(2026, 10, 29, 13, 15, 0, 0, time.UTC)
	sim := newECBSim(t, decision.Add(32*time.Minute), decision.Add(85*time.Minute))
	sim.sched.Events.Dispatch(ctx)

	rss := berlinClock(sim.site.rss)
	if len(rss) < 3 || rss[0] != "14:14:00" || rss[1] != "14:14:20" || rss[len(rss)-1] != "14:47:00" {
		t.Errorf("feed polls %v: want every 20 s from 14:14:00 to 14:47:00", rss)
	}
	if n := len(sim.site.lists); n == 0 || n > 2*(len(rss)/3+1) {
		t.Errorf("list requests = %d for %d feed polls: want two lists about every third poll", n, len(rss))
	}
	evs, _ := sim.st.Releases(ctx, decision, decision.Add(time.Second))
	if len(evs) != 1 || evs[0].Status != model.StatusDone {
		t.Fatalf("event = %+v", evs)
	}
	if sim.exports(t) != 1 || len(sim.sink.got) != 1 || sim.sink.got[0].Kind != "event_ecb" || sim.sink.got[0].Region != "eu" {
		t.Fatalf("exports %d, sink %+v", sim.exports(t), sim.sink.got)
	}
	md := sim.file(t)
	for _, s := range []string{"kind: event_ecb", "decision: 2026-10-29 14:15 CET", "statement: 2026-10-29 14:47 CET",
		"| Решение | без изменений | повышение на 25 б.п. |", "## Решение: изменения текста", "## Заявление: изменения текста"} {
		if !strings.Contains(md, s) {
			t.Errorf("ecb_diff.md lacks %q", s)
		}
	}
	if strings.Contains(md, "вопросы и ответы") {
		t.Error("the Q&A is in the file before it was published")
	}
	if len(sim.alerts) != 0 {
		t.Errorf("alerts on a normal day: %v", sim.alerts)
	}

	// A repeat poll (restart, the RSS job) exports nothing more.
	h := sim.sched.Events.Routes[len(sim.sched.Events.Routes)-2].Handler
	if done, err := h.Poll(ctx, evs[0]); !done || err != nil {
		t.Fatalf("repeat poll: %v %v", done, err)
	}
	if sim.exports(t) != 1 || len(sim.sink.got) != 1 {
		t.Errorf("exports after a repeat poll = %d", sim.exports(t))
	}
	// The introduction stored at 14:47 is the one the Q&A page carries later.
	intro, _ := sim.st.LatestDocuments(ctx, ecbtexts.Source, ecbtexts.DocStatement, 1)
	if len(intro) != 1 || strings.Contains(intro[0].Body, "first one, on monetary policy") {
		t.Errorf("statement intro = %+v", intro)
	}
}

// No statement by 15:30: the export goes out with the decision alone.
func TestECBDayWithoutStatement(t *testing.T) {
	ctx := context.Background()
	sim := newECBSim(t, time.Time{}, time.Time{})
	sim.sched.Events.Dispatch(ctx)
	if sim.exports(t) != 1 {
		t.Fatalf("exports = %d", sim.exports(t))
	}
	if got := sim.sink.got[0]; len(got.Files) != 4 {
		t.Errorf("sink = %+v", got)
	}
	md := sim.file(t)
	if !strings.Contains(md, "statement: —") || !strings.Contains(md, "- Заявление: не получено к 15:30") {
		t.Errorf("ecb_diff.md:\n%s", md[:600])
	}
	if last := berlinClock(sim.site.rss); last[len(last)-1] != "15:30:00" {
		t.Errorf("last feed poll %s, want 15:30:00", last[len(last)-1])
	}
	if len(sim.alerts) != 0 {
		t.Errorf("alerts: %v", sim.alerts)
	}
}

// Nothing published: one alert at 14:25, one at 15:30, no export; a
// restarted daemon does not repeat them.
func TestECBDayNothingFound(t *testing.T) {
	ctx := context.Background()
	sim := newECBSim(t, time.Time{}, time.Time{})
	sim.site.decisionAt = time.Time{}
	sim.sched.Events.Dispatch(ctx) // polls until the window ends

	if sim.exports(t) != 0 || len(sim.sink.got) != 0 {
		t.Errorf("exports = %d", sim.exports(t))
	}
	if len(sim.alerts) != 2 || !strings.Contains(sim.alerts[0], "не найдено к 14:25") || !strings.Contains(sim.alerts[1], "не найдены к 15:30") ||
		!strings.Contains(sim.alerts[1], "macroctl export --region eu") {
		t.Fatalf("alerts = %q", sim.alerts)
	}
	evs, _ := sim.st.Releases(ctx, sim.event.ScheduledAt, sim.event.ScheduledAt.Add(time.Second))
	if len(evs) != 1 || evs[0].Status != model.StatusTimeout {
		t.Errorf("event = %+v", evs)
	}

	// After a restart (a new handler, same database) the alerts are not sent again.
	day := sim.sched.Events.Routes[len(sim.sched.Events.Routes)-2].Handler.(*ECBDay)
	again := &ECBDay{Texts: day.Texts, Feed: day.Feed, Decisions: day.Decisions, Statements: day.Statements, Store: day.Store,
		Export: day.Export, Config: day.Config, Alert: day.Alert, Clock: day.Clock, Log: day.Log}
	if done, err := again.Poll(ctx, sim.event); done || err != nil {
		t.Fatalf("poll after restart: %v %v", done, err)
	}
	if len(sim.alerts) != 2 {
		t.Errorf("alerts after restart = %q", sim.alerts)
	}
}

func TestECBPlan(t *testing.T) {
	p := ECBPlan{Mode: config.ECBDayMode{Lead: time.Minute, RSSInterval: 20 * time.Second, FastFor: 75 * time.Minute}}
	ev := model.ReleaseEvent{ScheduledAt: time.Date(2026, 12, 17, 13, 15, 0, 0, time.UTC)}
	if got := p.Start(ev); !got.Equal(ev.ScheduledAt.Add(-time.Minute)) {
		t.Errorf("start %v", got)
	}
	if next, ok := p.Next(ev, ev.ScheduledAt.Add(74*time.Minute)); !ok || next.Sub(ev.ScheduledAt) != 74*time.Minute+20*time.Second {
		t.Errorf("fast step %v %v", next, ok)
	}
	if next, ok := p.Next(ev, ev.ScheduledAt.Add(75*time.Minute)); !ok || next.Sub(ev.ScheduledAt) != 80*time.Minute {
		t.Errorf("standard step after the fast phase %v %v", next, ok)
	}
}
