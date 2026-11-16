package ecbtexts

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/store"
)

var fixtureDir = filepath.Join("..", "..", "..", "testdata", "ecb_texts")

const (
	decisionOct  = "press/pr/date/2026/html/ecb.mp261029~9a493adf17.en.html"
	decisionSep  = "press/pr/date/2026/html/ecb.mp260910~314e508016.en.html"
	statementOct = "press/press_conference/monetary-policy-statement/2026/html/ecb.is261029~5c5ffe99e5.en.html"
	accountsOct  = "press/accounts/2026/html/ecb.mg261008~a10153d090.en.html"
	mopoList     = "/press/govcdec/mopo/{year}/html/index_include.en.html"
	accountsList = "/press/accounts/{year}/html/index_include.en.html"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// site serves testdata/ecb_texts by URL path; overrides replace pages.
func site(t *testing.T, overrides map[string][]byte) (*Site, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if b, ok := overrides[p]; ok {
			w.Write(b)
			return
		}
		b, err := os.ReadFile(filepath.Join(fixtureDir, filepath.FromSlash(p)))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	s := &Site{
		Client:   New(httpx.WithInterval(0), httpx.WithRetries(0, 0)),
		BaseURL:  srv.URL,
		Location: berlin(t),
		Times:    map[string]time.Duration{DocDecision: 14*time.Hour + 15*time.Minute, DocStatement: 14*time.Hour + 45*time.Minute, DocAccounts: 13*time.Hour + 30*time.Minute},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return s, srv
}

func TestDocTypeOf(t *testing.T) {
	for p, want := range map[string]string{
		"//press/key/date/2026/html/ecb.sp.261102~70003226e9.en.html": DocSpeech,
		"/press/key/date/2026/html/ecb.sp261110_1~9725b7aa07.en.html": DocSpeech,
		"/press/inter/date/2026/html/ecb.in261107~e7b86fc4ce.en.html": DocInterview,
		"/" + decisionOct:  DocDecision,
		"/" + statementOct: DocStatement,
		"/" + accountsOct:  DocAccounts,
		"/press/pr/date/2026/html/ecb.pr261106~356f2fc73a.en.html":                                    "",
		"/press/govcdec/otherdec/2026/html/ecb.gc261030~e40f864093.en.html":                           "",
		"/press/pr/date/2026/html/ecb.mp261029~9a493adf17.de.html":                                    "",
		"/press/press_conference/monetary-policy-statement/shared/pdf/ecb.ds260910~fbf0ab9b8d.en.pdf": "",
	} {
		if got := DocTypeOf(p); got != want {
			t.Errorf("DocTypeOf(%s) = %q, want %q", p, got, want)
		}
	}
	if got := ExternalID("//press/pr/date/2026/html/ecb.mp261029~9a493adf17.en.html"); got != "ecb.mp261029~9a493adf17" {
		t.Errorf("ExternalID = %q", got)
	}
}

func TestParseStatementSplitsQA(t *testing.T) {
	p, err := parsePage(readFixture(t, statementOct))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Intro, "Good afternoon, the Vice-President and I welcome you") {
		t.Errorf("intro starts %q", p.Intro[:80])
	}
	if !strings.HasSuffix(p.Intro, "We are now ready to take your questions.") {
		t.Errorf("intro ends %q", p.Intro[len(p.Intro)-80:])
	}
	if strings.Contains(p.Intro, "* * *") || strings.Contains(p.Intro, "Jump to the transcript") {
		t.Error("intro keeps the separator or the jump link")
	}
	if !strings.HasPrefix(p.QA, "The first one, on monetary policy") {
		t.Errorf("Q&A starts %q", p.QA[:80])
	}
	if strings.Contains(p.Intro, p.QA[:60]) {
		t.Error("Q&A text in the intro")
	}
}

// withoutQA is the statement page as it is at 14:45: nothing after the
// #qa anchor yet.
func withoutQA(t *testing.T) []byte {
	t.Helper()
	s := string(readFixture(t, statementOct))
	i := strings.Index(s, `<a id="qa"`)
	j := i + strings.Index(s[i:], "</p>") + len("</p>")
	k := strings.Index(s, `<div class="footnotes"`)
	if i < 0 || k < j {
		t.Fatal("statement fixture layout changed")
	}
	return []byte(s[:j] + "</div>" + s[k:])
}

func TestStatementIntroStableWhenQAAppears(t *testing.T) {
	early, _ := site(t, map[string][]byte{statementOct: withoutQA(t)})
	late, _ := site(t, nil)
	ctx := context.Background()
	a, err := early.Fetch(ctx, "/"+statementOct, time.Time{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := late.Fetch(ctx, "/"+statementOct, time.Time{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(b) != 2 {
		t.Fatalf("documents: early %d, late %d; want 1 and 2", len(a), len(b))
	}
	if store.ContentHash(a[0].Body) != store.ContentHash(b[0].Body) || a[0].ExternalID != b[0].ExternalID {
		t.Error("the introduction changed when the Q&A was added")
	}
	qa := b[1]
	if qa.DocType != DocPressConference || qa.ExternalID != "ecb.is261029~5c5ffe99e5#qa" || !strings.HasSuffix(qa.URL, "#qa") {
		t.Errorf("Q&A document %+v", qa)
	}
	if want := time.Date(2026, 10, 29, 13, 45, 0, 0, time.UTC); !a[0].PublishedAt.Equal(want) {
		t.Errorf("statement published %v, want %v (14:45 CET)", a[0].PublishedAt, want)
	}
	if a[0].Title != "Monetary policy statement, 29 October 2026" {
		t.Errorf("title %q", a[0].Title)
	}
}

func TestParseDecision(t *testing.T) {
	body := func(name string) string {
		p, err := parsePage(readFixture(t, name))
		if err != nil {
			t.Fatal(err)
		}
		return p.Body
	}
	oct := ParseDecision(body(decisionOct))
	if want := (Decision{Parsed: true, Action: ActionMaintain, DFR: 2.50, MRO: 2.65, MLF: 2.90}); oct != want {
		t.Errorf("29.10: %+v, want %+v", oct, want)
	}
	sep := ParseDecision(body(decisionSep))
	want := Decision{Parsed: true, Action: ActionRaise, MoveBP: 25, DFR: 2.50, MRO: 2.65, MLF: 2.90,
		EffectiveFrom: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)}
	if sep != want {
		t.Errorf("10.09: %+v, want %+v", sep, want)
	}
	for name, text := range map[string]string{
		"unknown wording": "The Governing Council today decided to adjust the corridor. The deposit facility rate will be 2.75%.",
		"action only":     "The Governing Council today decided to lower the three key ECB interest rates by 25 basis points.",
		"rates only":      "The interest rates on the deposit facility, the main refinancing operations and the marginal lending facility will remain unchanged at 2.50%, 2.65% and 2.90% respectively.",
	} {
		if d := ParseDecision(text); d.Parsed {
			t.Errorf("%s: parsed as %+v", name, d)
		}
	}
	dfr := ParseDecision("The Governing Council today decided to lower the deposit facility rate – the rate through which it steers the monetary policy stance – by 25 basis points. " +
		"Accordingly, the interest rates on the deposit facility, the main refinancing operations and the marginal lending facility will be decreased to 3.00%, 3.15% and 3.40% respectively, with effect from 18 December 2024.")
	if !dfr.Parsed || dfr.Action != ActionLower || dfr.MoveBP != 25 || dfr.DFR != 3.00 {
		t.Errorf("deposit facility wording: %+v", dfr)
	}
}

func TestParseCalendar(t *testing.T) {
	days, err := ParseCalendar(readFixture(t, "press/calendars/mgcgc/html/index.en.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 17 || days[0].Format(time.DateOnly) != "2026-12-17" {
		t.Fatalf("days = %d, first %v; want 17 from 2026-12-17", len(days), days[0])
	}
	if slices.ContainsFunc(days, func(d time.Time) bool { return d.Format(time.DateOnly) == "2026-12-16" }) {
		t.Error("day 1 of the meeting taken as a decision day")
	}
	// Once a year the meeting is held outside Frankfurt.
	away := []byte(`<dl><dt>10/09/2026</dt><dd>Governing Council of the ECB: monetary policy meeting in Berlin (Day 2), followed by press conference<br></dd>
<dt>25/11/2026</dt><dd>Governing Council of the ECB: non-monetary policy meeting (in Frankfurt)<br></dd>
<dt>11/06/2027</dt><dd>Governing Council of the ECB: monetary policy meeting in Athens (Day 2), followed by press conference</dd></dl>`)
	days, err = ParseCalendar(away)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range days {
		got = append(got, d.Format(time.DateOnly))
	}
	if !slices.Equal(got, []string{"2026-09-10", "2027-06-11"}) {
		t.Errorf("away meetings = %v", got)
	}
	if _, err := ParseCalendar([]byte("<html><body>maintenance</body></html>")); err == nil {
		t.Error("a page without meetings must fail")
	}
}

func TestEvents(t *testing.T) {
	loc := berlin(t)
	days := []time.Time{time.Date(2026, 12, 17, 0, 0, 0, 0, time.UTC), time.Date(2027, 6, 10, 0, 0, 0, 0, time.UTC)}
	evs := Events(days, loc, 14*time.Hour+15*time.Minute, 14*time.Hour+45*time.Minute,
		time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC))
	want := []string{"ECB decision 2026-12-17T13:15:00Z", "ECB press conference 2026-12-17T13:45:00Z",
		"ECB decision 2027-06-10T12:15:00Z", "ECB press conference 2027-06-10T12:45:00Z"}
	var got []string
	for _, e := range evs {
		got = append(got, e.Name+" "+e.ScheduledAt.Format(time.RFC3339))
		if e.Source != SourceMeeting || e.Related.Meeting == "" {
			t.Errorf("event %+v", e)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestFeed(t *testing.T) {
	s, _ := site(t, nil)
	f := &Feed{Site: s, URL: s.BaseURL + "/rss/press.html"}
	docs, cursor, err := f.FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	byType := map[string][]model.Document{}
	for _, d := range docs {
		byType[d.DocType] = append(byType[d.DocType], d)
	}
	for typ, n := range map[string]int{DocSpeech: 1, DocInterview: 1, DocDecision: 1, DocStatement: 1, DocPressConference: 1} {
		if len(byType[typ]) != n {
			t.Errorf("%s: %d documents, want %d", typ, len(byType[typ]), n)
		}
	}
	sp := byType[DocSpeech][0]
	if sp.Author != "Philip R. Lane" || sp.Title != "The science and practice of monetary policy communication: the ECB experience" {
		t.Errorf("speech author %q title %q", sp.Author, sp.Title)
	}
	if in := byType[DocInterview][0]; in.Author != "Christine Lagarde" {
		t.Errorf("interview author %q", in.Author)
	}
	if d := byType[DocDecision][0]; !d.PublishedAt.Equal(time.Date(2026, 10, 29, 13, 15, 0, 0, time.UTC)) {
		t.Errorf("decision published %v, want the feed time 14:15 CET", d.PublishedAt)
	}
	ids := strings.Fields(cursor)
	// Kept and stored, and the press releases that are not kept.
	for _, id := range []string{"ecb.sp261110_1~9725b7aa07", "ecb.mp261029~9a493adf17", "ecb.pr261106~356f2fc73a", "ecb.gc261030~e40f864093"} {
		if !slices.Contains(ids, id) {
			t.Errorf("cursor lacks %s", id)
		}
	}
	// Pages that failed (no fixture) are retried next time.
	if slices.Contains(ids, "ecb.sp261110~9422461bad") {
		t.Error("a failed item is in the cursor")
	}
	again, _, err := f.FetchDocuments(context.Background(), cursor)
	if err != nil || len(again) != 0 {
		t.Errorf("second run: %d documents, err %v", len(again), err)
	}
}

func TestList(t *testing.T) {
	s, _ := site(t, nil)
	now := func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	l := &List{Site: s, DocType: DocDecision, URL: mopoList, Now: now}
	docs, cursor, err := l.FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("decisions = %d, want 2 (the recorded pages)", len(docs))
	}
	for _, d := range docs {
		if d.DocType != DocDecision || d.PublishedAt.In(s.Location).Format("15:04") != "14:15" {
			t.Errorf("decision %s %s at %v", d.ExternalID, d.DocType, d.PublishedAt)
		}
	}
	if strings.Contains(cursor, "ecb.mp260723") {
		t.Error("a failed page is in the cursor")
	}

	links, err := ParseList(readFixture(t, "press/accounts/2026/html/index_include.en.html"), DocAccounts)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 7 || !strings.Contains(links[0], "ecb.mg261008") {
		t.Errorf("accounts links = %v", links)
	}
	acc := &List{Site: s, DocType: DocAccounts, URL: accountsList, Now: now}
	docs, _, err = acc.FetchDocuments(context.Background(), "")
	if err != nil || len(docs) != 1 {
		t.Fatalf("accounts: %d documents, err %v", len(docs), err)
	}
	if d := docs[0]; d.Title != "Account of the monetary policy meeting: Meeting of 9-10 September 2026" ||
		!d.PublishedAt.Equal(time.Date(2026, 10, 8, 11, 30, 0, 0, time.UTC)) {
		t.Errorf("accounts %q at %v", d.Title, d.PublishedAt)
	}
	if _, err := ParseList([]byte(`<dl><dt isoDate="2026-10-29">x</dt><dd><a href="/other.html">x</a></dd></dl>`), DocDecision); err == nil {
		t.Error("a list without links of the type must fail")
	}
}

func TestPressConferencesRereads(t *testing.T) {
	s, _ := site(t, nil)
	stored := model.Document{Source: Source, ExternalID: "ecb.is261029~5c5ffe99e5", DocType: DocStatement,
		URL: s.BaseURL + "/" + statementOct, PublishedAt: time.Date(2026, 10, 29, 13, 45, 0, 0, time.UTC)}
	p := &PressConferences{Site: s, Pending: func(context.Context) ([]model.Document, error) {
		return []model.Document{stored}, nil
	}}
	docs, _, err := p.FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[1].DocType != DocPressConference || !docs[0].PublishedAt.Equal(stored.PublishedAt) {
		t.Errorf("re-read: %d documents", len(docs))
	}
}
