package collect

import (
	"context"
	"errors"
	"fmt"
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

	"macro-collector/internal/config"
	"macro-collector/internal/guidance"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/notify"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/irtranscript"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

const edgarFixtures = "../../testdata/edgar"

var ciks = map[string]string{
	"NVDA": "0001045810", "MSFT": "0000789019", "AAPL": "0000320193", "AMZN": "0001018724",
	"META": "0001326801", "GOOGL": "0001652044", "AVGO": "0001730168", "TSLA": "0001318605",
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(edgarFixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func submissions(t *testing.T, ticker string) edgar.Submissions {
	t.Helper()
	s, err := edgar.ParseSubmissions(readFixture(t, "submissions_"+ticker+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ny(t *testing.T) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func dt(s string) time.Time {
	v, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return v
}

// Without a calendar, history is classified by the 10-Q/10-K that follows.
// The recorded earnings 8-Ks of Q2 2026 are found; Tesla's deliveries 8-Ks
// (item 2.02 too) are not.
func TestEarningsFilingsFixtures(t *testing.T) {
	want := map[string]struct{ accn, period string }{
		"NVDA": {"0001045810-26-000073", "2026-07-26"}, "MSFT": {"0001193125-26-323632", "2026-06-30"},
		"AAPL": {"0000320193-26-000018", "2026-06-27"}, "AMZN": {"0001018724-26-000024", "2026-06-30"},
		"META": {"0001628280-26-066700", "2026-09-30"}, "GOOGL": {"0001652044-26-000066", "2026-06-30"},
		"AVGO": {"0001730168-26-000076", "2026-08-02"}, "TSLA": {"0001628280-26-066738", "2026-09-30"},
	}
	for ticker, w := range want {
		got := EarningsFilings(submissions(t, ticker), dt("2024-07-01"), nil, ny(t))
		i := slices.IndexFunc(got, func(e EarningsFiling) bool { return e.Filing.Accession == w.accn })
		if i < 0 {
			t.Errorf("%s: %s not found among %d", ticker, w.accn, len(got))
			continue
		}
		if e := got[i]; e.Estimated || e.PeriodEnd.Format(time.DateOnly) != w.period {
			t.Errorf("%s: %+v, want period %s", ticker, e, w.period)
		}
		// About one report a quarter since July 2024.
		if n := len(got); n < 8 || n > 10 {
			t.Errorf("%s: %d earnings 8-Ks since 2024-07", ticker, n)
		}
	}
	for _, e := range EarningsFilings(submissions(t, "TSLA"), dt("2024-07-01"), nil, ny(t)) {
		if d := e.Filing.AcceptedAt.In(ny(t)).Day(); d <= 3 {
			t.Errorf("TSLA deliveries 8-K taken as earnings: %s %s", e.Filing.Accession, e.Filing.AcceptedAt)
		}
	}
}

// On the report evening the 8-K is there and the 10-Q is not: only the
// calendar tells it is earnings, and the period is estimated.
func TestEarningsFilingBeforeTenQ(t *testing.T) {
	s := submissions(t, "TSLA")
	cut := time.Date(2026, 10, 21, 21, 0, 0, 0, time.UTC) // 8-K 20:41Z, 10-Q next day
	s.Filings = slices.DeleteFunc(slices.Clone(s.Filings), func(f edgar.Filing) bool { return f.AcceptedAt.After(cut) })
	if got := EarningsFilings(s, dt("2026-07-01"), nil, ny(t)); slices.ContainsFunc(got, func(e EarningsFiling) bool {
		return e.Filing.Accession == "0001628280-26-066738"
	}) {
		t.Error("8-K without calendar or 10-Q classified as earnings")
	}
	got := EarningsFilings(s, dt("2026-07-01"), []time.Time{dt("2026-10-21")}, ny(t))
	i := slices.IndexFunc(got, func(e EarningsFiling) bool { return e.Filing.Accession == "0001628280-26-066738" })
	if i < 0 || !got[i].Estimated || got[i].PeriodEnd.Format(time.DateOnly) != "2026-09-30" {
		t.Fatalf("filings = %+v", got)
	}
	if slices.ContainsFunc(got, func(e EarningsFiling) bool { return e.Filing.Accession == "0001628280-26-064366" }) {
		t.Error("deliveries 8-K of 2026-10-02 taken as earnings")
	}
}

func TestNextQuarterEnd(t *testing.T) {
	for from, want := range map[string]string{"2026-06-30": "2026-09-30", "2026-09-30": "2026-12-31", "2026-12-31": "2027-03-31",
		"2026-06-27": "2026-09-26", "2026-07-26": "2026-10-25"} {
		if got := transform.NextQuarterEnd(dt(from)).Format(time.DateOnly); got != want {
			t.Errorf("%s → %s, want %s", from, got, want)
		}
	}
}

// fakeEDGAR serves the recorded fixtures and counts requests.
type fakeEDGAR struct {
	t                     *testing.T
	subs, facts, releases int
	instances             int
	failRelease           bool
	submissions           func(string) edgar.Submissions
}

func (f *fakeEDGAR) Submissions(_ context.Context, cik string) (edgar.Submissions, error) {
	f.subs++
	for ticker, c := range ciks {
		if c == cik {
			if f.submissions != nil {
				return f.submissions(ticker), nil
			}
			return submissions(f.t, ticker), nil
		}
	}
	return edgar.Submissions{}, errors.New("unknown cik " + cik)
}

func (f *fakeEDGAR) Facts(_ context.Context, cik string, concepts []string) ([]model.CompanyFact, error) {
	f.facts++
	for ticker, c := range ciks {
		if c == cik {
			return edgar.ParseCompanyFacts(readFixture(f.t, "companyfacts_"+ticker+".json"), concepts)
		}
	}
	return nil, errors.New("unknown cik " + cik)
}

func (f *fakeEDGAR) PressRelease(_ context.Context, cik, accn string) (edgar.PressRelease, error) {
	f.releases++
	if f.failRelease {
		return edgar.PressRelease{}, errors.New("edgar " + accn + " index: status 503")
	}
	matches, _ := filepath.Glob(filepath.Join(edgarFixtures, "*_"+accn+"_*.htm"))
	if len(matches) == 0 {
		// Older quarters are not recorded: an empty release keeps the test small.
		return edgar.PressRelease{Accession: accn, URL: "https://www.sec.gov/x/" + accn, Text: "Results release"}, nil
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return edgar.PressRelease{}, err
	}
	text, err := edgar.ReleaseText(b)
	return edgar.PressRelease{Accession: accn, URL: "https://www.sec.gov/x/" + filepath.Base(matches[0]), Text: text}, err
}

func edgarConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := earningsConfig(t)
	cfg.EDGAR.History = dt("2025-07-01")
	cfg.EDGAR.Companies = []config.Company{
		{Ticker: "MSFT", CIK: ciks["MSFT"], Resolved: config.Concepts{
			Revenue: []string{"RevenueFromContractWithCustomerExcludingAssessedTax"}, EPS: []string{"EarningsPerShareDiluted"},
			Capex: []string{"PaymentsToAcquirePropertyPlantAndEquipment"}}},
		{Ticker: "TSLA", CIK: ciks["TSLA"], Resolved: config.Concepts{
			Revenue: []string{"Revenues"}, EPS: []string{"EarningsPerShareDiluted"}, Capex: []string{"PaymentsToAcquirePropertyPlantAndEquipment"}}},
	}
	return cfg
}

func TestEDGARRun(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	client := &fakeEDGAR{t: t}
	e := &EDGAR{Client: client, Store: st, Config: edgarConfig(t), Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }}

	rep := e.Run(ctx, ingest.Backfill)
	if len(rep.Failed()) > 0 {
		t.Fatalf("failed: %+v", rep.Failed())
	}
	if client.subs != 2 || client.facts != 2 {
		t.Errorf("requests: submissions %d, facts %d", client.subs, client.facts)
	}
	for _, r := range rep.Results {
		if r.NewReports < 4 || !r.FactsCurrent || r.Facts == 0 || len(r.Reports) != r.NewReports {
			t.Errorf("%s: %+v", r.Ticker, r)
		}
	}
	tsla, ok, err := st.CompanyReport(ctx, "0001628280-26-066738")
	if !ok || err != nil || tsla.Session != "amc" || tsla.ReportDate.Format(time.DateOnly) != "2026-10-21" ||
		tsla.PeriodEnd.Format(time.DateOnly) != "2026-09-30" || tsla.DocumentID == 0 {
		t.Errorf("TSLA Q3 report = %+v, %v, %v", tsla, ok, err)
	}
	doc, ok, err := st.Document(ctx, edgar.Source, "0001193125-26-323632")
	if !ok || err != nil || doc.Author != "MSFT" || doc.DocType != "8-K" || len(edgar.Quotes(doc.Body)) != 2 {
		t.Errorf("MSFT press release = %+v, %v, %v", doc.Title, ok, err)
	}
	if facts, _ := st.CompanyFacts(ctx, "MSFT"); len(facts) == 0 {
		t.Error("MSFT facts not stored")
	}
	if fs, _, _ := st.FetchStateOf(ctx, "edgar:MSFT"); fs.LastError != "" || fs.Cursor == "" {
		t.Errorf("fetch state = %+v", fs)
	}

	// Incremental, nothing new: submissions only, no facts or releases.
	client.subs, client.facts, client.releases = 0, 0, 0
	rep = e.Run(ctx, ingest.Incremental)
	if client.subs != 2 || client.facts != 0 || client.releases != 0 {
		t.Errorf("second run requests: submissions %d, facts %d, releases %d", client.subs, client.facts, client.releases)
	}
	for _, r := range rep.Results {
		if r.NewReports != 0 || r.FactsRead || !r.FactsCurrent {
			t.Errorf("second run %s: %+v", r.Ticker, r)
		}
	}
}

// A failing press release does not lose the report: it is stored without
// the document and the release is fetched again on the next run.
func TestEDGARPressReleaseRetry(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	client := &fakeEDGAR{t: t, failRelease: true}
	cfg := edgarConfig(t)
	cfg.EDGAR.Companies = cfg.EDGAR.Companies[1:] // TSLA
	e := &EDGAR{Client: client, Store: st, Config: cfg, Log: slog.New(slog.DiscardHandler)}
	rep := e.Run(ctx, ingest.Incremental)
	if len(rep.Failed()) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	r, ok, _ := st.CompanyReport(ctx, "0001628280-26-066738")
	if !ok || r.DocumentID != 0 {
		t.Fatalf("report without release = %+v, %v", r, ok)
	}
	if fs, _, _ := st.FetchStateOf(ctx, "edgar:TSLA"); fs.LastError == "" {
		t.Error("release error not recorded")
	}
	client.failRelease = false
	if rep := e.Run(ctx, ingest.Incremental); len(rep.Failed()) != 0 {
		t.Fatalf("retry failed: %+v", rep.Failed())
	}
	if r, _, _ := st.CompanyReport(ctx, "0001628280-26-066738"); r.DocumentID == 0 {
		t.Error("release not stored on retry")
	}
	if fs, _, _ := st.FetchStateOf(ctx, "edgar:TSLA"); fs.LastError != "" {
		t.Errorf("error not cleared: %s", fs.LastError)
	}
}

// fakeTranscripts answers "not published" until ready, then a transcript.
type fakeTranscripts struct {
	calls  []string // "page fy q"
	ready  bool
	failed bool
	text   string // "" : a short outlook
}

func (f *fakeTranscripts) Fetch(_ context.Context, page, _ string, fy, q int) (irtranscript.Transcript, bool, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s %d %d", page, fy, q))
	switch {
	case f.failed:
		return irtranscript.Transcript{}, false, errors.New("transcript: status 503")
	case !f.ready:
		return irtranscript.Transcript{}, false, nil
	}
	text := f.text
	if text == "" {
		text = "Now, moving to the outlook.\n\nWe expect revenue to grow."
	}
	return irtranscript.Transcript{URL: irtranscript.PageURL(page, fy, q), Text: text}, true, nil
}

// MSFT's FY26 Q4 8-K (2026-07-29): the transcript is tried after the run,
// at most once per retry_every until it is up, then stored and not asked
// again; a failure goes to fetch_state["transcript:MSFT"] without failing
// the EDGAR run.
func TestEDGARTranscripts(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	cfg := edgarConfig(t)
	cfg.EDGAR.Companies = cfg.EDGAR.Companies[:1] // MSFT
	cfg.EDGAR.Transcripts = config.Transcripts{Enabled: true, WindowDays: 4, Retry: time.Hour,
		Companies: []config.TranscriptSource{{Ticker: "MSFT", Page: "https://ir.example/fy-{fy}/q{q}"}}}
	now := time.Date(2026, 7, 29, 22, 0, 0, 0, time.UTC)
	tr := &fakeTranscripts{}
	e := &EDGAR{Client: &fakeEDGAR{t: t}, Transcripts: tr, Store: st, Config: cfg, Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return now }}
	const accn = "0001193125-26-323632"

	run := func() {
		t.Helper()
		if rep := e.Run(ctx, ingest.Incremental); len(rep.Failed()) > 0 {
			t.Fatalf("failed: %+v", rep.Failed())
		}
	}
	run()
	if len(tr.calls) != 1 || tr.calls[0] != "https://ir.example/fy-{fy}/q{q} 2026 4" {
		t.Fatalf("calls = %q", tr.calls)
	}
	now = now.Add(30 * time.Minute) // within retry_every: not asked
	run()
	if len(tr.calls) != 1 {
		t.Errorf("asked again after 30 min: %q", tr.calls)
	}
	tr.failed = true
	now = now.Add(time.Hour)
	run()
	if fs, _, _ := st.FetchStateOf(ctx, "transcript:MSFT"); fs.LastError == "" {
		t.Error("transcript error not recorded")
	}
	tr.failed, tr.ready = false, true
	now = now.Add(time.Hour)
	run()
	doc, ok, err := st.Document(ctx, irtranscript.Source, accn)
	if !ok || err != nil || doc.URL != "https://ir.example/fy-2026/q4" || len(irtranscript.Outlook(doc.Body)) != 2 || doc.Author != "MSFT" {
		t.Fatalf("transcript = %+v, %v, %v", doc, ok, err)
	}
	if fs, _, _ := st.FetchStateOf(ctx, "transcript:MSFT"); fs.LastError != "" || fs.Cursor != accn {
		t.Errorf("fetch state = %+v", fs)
	}
	n := len(tr.calls)
	now = now.Add(2 * time.Hour) // stored: not asked
	run()
	now = now.Add(5 * 24 * time.Hour) // window over
	e.tried = nil
	run()
	if len(tr.calls) != n {
		t.Errorf("asked after storing or after the window: %q", tr.calls[n:])
	}
}

func (f *fakeEDGAR) InstanceFacts(_ context.Context, cik string, fl edgar.Filing, sel edgar.InstanceSelect) ([]model.CompanyFact, error) {
	f.instances++
	for t, c := range ciks {
		if c == cik {
			b, err := os.ReadFile(filepath.Join(edgarFixtures, "instance_"+t+"_"+fl.Accession+".xml"))
			if err != nil {
				return nil, nil
			}
			return edgar.ParseInstance(b, sel, fl)
		}
	}
	return nil, errors.New("unknown cik " + cik)
}

// Instances (phase 5d): backfill reads the newest five 10-Q/10-K instances
// and stores their segment facts and RPO total; an incremental run with
// no new filing reads none.
func TestEDGARInstances(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	cfg := edgarConfig(t)
	cfg.EDGAR.Companies = cfg.EDGAR.Companies[:1] // MSFT
	cfg.EDGAR.Segments = map[string]config.Segments{"MSFT": {Revenue: "RevenueFromContractWithCustomerExcludingAssessedTax",
		Profit: "OperatingIncomeLoss", Members: []config.SegmentMember{{Member: "IntelligentCloudMember", Label: "Intelligent Cloud"}}}}
	client := &fakeEDGAR{t: t}
	e := &EDGAR{Client: client, Store: st, Config: cfg, Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }}
	if rep := e.Run(ctx, ingest.Backfill); len(rep.Failed()) > 0 {
		t.Fatalf("failed: %+v", rep.Failed())
	}
	if client.instances != 5 {
		t.Errorf("instances read = %d, want 5", client.instances)
	}
	facts, err := st.InstanceFacts(ctx, "MSFT")
	if err != nil || !slices.ContainsFunc(facts, func(f model.CompanyFact) bool { return f.Member == "IntelligentCloudMember" }) ||
		!slices.ContainsFunc(facts, func(f model.CompanyFact) bool { return f.Member == "" && f.Concept == edgar.RPOConcept }) {
		t.Fatalf("instance facts = %d, %v", len(facts), err)
	}
	if fs, _, _ := st.FetchStateOf(ctx, "edgar-instance:MSFT"); fs.Cursor != "0001193125-26-323660" || fs.LastError != "" {
		t.Errorf("fetch state = %+v", fs)
	}
	client.instances = 0
	e.Run(ctx, ingest.Incremental)
	if client.instances != 0 {
		t.Errorf("incremental run read %d instances", client.instances)
	}
}

// A new transcript of a guidance ticker yields a pending candidate in the
// same write, sent to Telegram with buttons (phase 14b). The text is the
// recorded GOOGL Q3 call, served as MSFT's to reuse its filings.
func TestEDGARTranscriptGuidanceCandidate(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	cfg := edgarConfig(t)
	cfg.EDGAR.Companies = cfg.EDGAR.Companies[:1] // MSFT
	cfg.EDGAR.CapexCompanies = []string{"MSFT"}
	cfg.EDGAR.GuidanceTickers = []string{"MSFT"}
	cfg.EDGAR.Transcripts = config.Transcripts{Enabled: true, WindowDays: 4, Retry: time.Hour,
		Companies: []config.TranscriptSource{{Ticker: "MSFT", Page: "https://ir.example/fy-{fy}/q{q}"}}}
	page := readFixture(t, "../ir/googl_2026-Q3-Earnings-Call.html")
	text, err := irtranscript.Text(page)
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]+" "+string(b))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":9}}`))
	}))
	defer srv.Close()
	now := time.Date(2026, 7, 29, 22, 0, 0, 0, time.UTC)
	log := slog.New(slog.DiscardHandler)
	g := &guidance.Service{Store: st, Config: cfg, TG: notify.New(srv.URL, "1:t", "42"), Log: log}
	e := &EDGAR{Client: &fakeEDGAR{t: t}, Transcripts: &fakeTranscripts{ready: true, text: text}, Store: st, Config: cfg, Log: log,
		Now: func() time.Time { return now }, Guidance: g}
	if rep := e.Run(ctx, ingest.Incremental); len(rep.Failed()) > 0 {
		t.Fatalf("failed: %+v", rep.Failed())
	}
	cands, err := st.GuidanceCandidates(ctx)
	if err != nil || len(cands) != 1 {
		t.Fatalf("candidates = %+v, %v", cands, err)
	}
	c := cands[0]
	if c.Ticker != "MSFT" || c.Year != 2026 || c.Low != 195 || c.High != 205 || c.Status != store.GuidancePending ||
		c.DocSource != irtranscript.Source || c.DocRef != "0001193125-26-323632" || c.Given.Format(time.DateOnly) != "2026-07-29" || c.TGMessageID != 9 {
		t.Fatalf("candidate = %+v", c)
	}
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "sendMessage") || !strings.Contains(sent[0], `"callback_data":"g:a:1"`) {
		t.Fatalf("telegram = %q", sent)
	}
	if conf, _ := st.ConfirmedGuidance(ctx); len(conf) != 0 {
		t.Fatalf("a candidate became guidance without approval: %+v", conf)
	}
}
