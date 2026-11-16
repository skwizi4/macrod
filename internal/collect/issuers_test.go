package collect

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/esef"
	"macro-collector/internal/sources/irpress"
	"macro-collector/internal/store"
)

type fakeIssuerClients struct {
	edgarCalls, esefFacts, pressCalls int
	pressErr                          error
	since                             time.Time
}

func (f *fakeIssuerClients) FactsIn(_ context.Context, cik, taxonomy string, _ []string) ([]model.CompanyFact, error) {
	f.edgarCalls++
	y := func(form string, v float64) model.CompanyFact {
		return model.CompanyFact{Concept: "Revenue", Unit: "EUR", Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			End: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), Value: v, Accession: form + "-1", Form: form,
			Filed: time.Date(2026, 2, 26, 0, 0, 0, 0, time.UTC), FY: 2025, FP: "FY"}
	}
	return []model.CompanyFact{y("20-F", 36.8e9), y("6-K", 1)}, nil
}

func (f *fakeIssuerClients) Filings(context.Context, string) ([]esef.Filing, error) {
	return []esef.Filing{{ID: "1", PeriodEnd: time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC), Added: time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)},
		{ID: "2", PeriodEnd: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), Added: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)}}, nil
}

func (f *fakeIssuerClients) Facts(_ context.Context, fl esef.Filing, _ string, _ []string) ([]model.CompanyFact, error) {
	f.esefFacts++
	return []model.CompanyFact{{Concept: "Revenue", Unit: "EUR", Start: fl.PeriodEnd.AddDate(-1, 0, 1), End: fl.PeriodEnd,
		Value: 70e9, Accession: fl.ID, Form: esef.Form, Filed: fl.Added, FY: fl.PeriodEnd.Year(), FP: "FY"}}, nil
}

func (f *fakeIssuerClients) Fetch(_ context.Context, ticker, region string, _ config.IssuerPress, since time.Time, _ func(string) bool) ([]model.Document, error) {
	f.pressCalls++
	f.since = since
	if f.pressErr != nil {
		return nil, f.pressErr
	}
	return []model.Document{{Source: irpress.Source(ticker), ExternalID: "https://x/1", DocType: irpress.DocType, Region: region,
		Title: "Q3 results", URL: "https://x/1", Body: "We expect growth.", PublishedAt: time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)}}, nil
}

// Facts of the issuer's forms only; ESEF reports already stored are not
// read again; a press failure lands in fetch_state and the cursor stays.
func TestRunIssuers(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	press := &config.IssuerPress{Kind: config.PressRSS, URL: "https://x/feed"}
	cfg := &config.Config{Issuers: config.Issuers{Enabled: true, PressHistoryDays: 30, Companies: []config.Issuer{
		{Ticker: "SAP", Facts: &config.IssuerFacts{Source: config.FactsEDGAR, CIK: "0001000184", Taxonomy: "ifrs-full", Forms: []string{"20-F"},
			Concepts: config.IssuerConcepts{Revenue: []string{"Revenue"}}}, Press: press},
		{Ticker: "AIR", Facts: &config.IssuerFacts{Source: config.FactsESEF, LEI: "MINO79WLOO247M1IL051", Taxonomy: "ifrs-full",
			Concepts: config.IssuerConcepts{Revenue: []string{"Revenue"}}}},
	}}}
	f := &fakeIssuerClients{}
	c := &IssuerClients{EDGAR: f, ESEF: f, Press: f}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	res := RunIssuers(ctx, cfg, st, c, slog.New(slog.DiscardHandler), now)
	if len(res) != 2 || res[0].Err != nil || res[1].Err != nil || res[0].Facts != 1 || res[0].Releases != 1 || res[1].Facts != 2 {
		t.Fatalf("first run %+v", res)
	}
	facts, err := st.CompanyFacts(ctx, "SAP")
	if err != nil || len(facts) != 1 || facts[0].Form != "20-F" {
		t.Errorf("SAP facts %+v, %v", facts, err)
	}
	if !f.since.Equal(now.AddDate(0, 0, -30)) {
		t.Errorf("first press load since %s", f.since)
	}

	f.pressErr = errors.New("403")
	res = RunIssuers(ctx, cfg, st, c, slog.New(slog.DiscardHandler), now.Add(time.Hour))
	if f.esefFacts != 2 {
		t.Errorf("ESEF reports read %d times, want 2 (stored ones skipped)", f.esefFacts)
	}
	if res[0].Err == nil {
		t.Error("press error not reported")
	}
	if !f.since.Equal(time.Date(2026, 11, 8, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("second press load since %s, want the cursor less 2 days", f.since)
	}
	fs, ok, err := st.FetchStateOf(ctx, irpress.Source("SAP"))
	if err != nil || !ok || fs.LastError == "" || fs.Cursor != "2026-11-10T07:00:00Z" {
		t.Errorf("press fetch state %+v %v %v", fs, ok, err)
	}
	if fs, ok, _ := st.FetchStateOf(ctx, IssuerFactsState("SAP")); !ok || fs.LastError != "" {
		t.Errorf("facts fetch state %+v", fs)
	}
}

type webPages map[string][]byte

func (w webPages) Get(_ context.Context, url string) ([]byte, error) {
	if b, ok := w[url]; ok {
		return b, nil
	}
	return nil, errors.New("404 " + url)
}

// Quarterly Excel (step 18): a good file is stored as IR_XLSX facts of the
// quarter and a year earlier; a file failing a check stores nothing and its
// error lands in fetch_state; a stored file is not read again.
func TestIssuerQuarterly(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	good, err := os.ReadFile("../../testdata/irxlsx/siemens-2026-q3.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := os.ReadFile("../../testdata/irxlsx/siemens-2026-q2.xlsx") // linked as Q1: its columns are Q2
	if err != nil {
		t.Fatal(err)
	}
	q := &config.IssuerQuarterly{Page: "https://ir/page", FiscalYearEnd: 9, MaxFiles: 4, RevenueLabel: "Revenue", OperatingName: "P",
		Links: []string{`^(?P<year>\d{4})-q(?P<q>[1-4])\.xlsx$`},
		Tables: []config.QuarterlyTable{{Anchor: []string{"Consolidated Statements of Income"}, Scale: 1e6,
			Rows: map[string]config.QuarterlyRow{config.QuarterRevenue: {Labels: []string{"Revenue"}}}}}}
	q.LinkRes = []*regexp.Regexp{regexp.MustCompile(q.Links[0])}
	cfg := &config.Config{Issuers: config.Issuers{Enabled: true, Companies: []config.Issuer{{Ticker: "SIE", Currency: "EUR", Quarterly: q}}}}
	web := webPages{"https://ir/page": []byte(`<a href="https://ir/f/2026-q3.xlsx">Q3</a> <a href="https://ir/f/2026-q1.xlsx">Q1</a>`),
		"https://ir/f/2026-q3.xlsx": good, "https://ir/f/2026-q1.xlsx": bad}
	c := &IssuerClients{Web: web}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	res := RunIssuers(ctx, cfg, st, c, slog.New(slog.DiscardHandler), now)
	if res[0].Quarters != 2 || res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "2026-q1.xlsx") {
		t.Fatalf("result %+v", res[0])
	}
	facts, err := st.CompanyFacts(ctx, "SIE")
	if err != nil || len(facts) != 2 {
		t.Fatalf("facts %+v %v", facts, err)
	}
	for _, f := range facts {
		if f.Form != "IR_XLSX" || f.Concept != config.QuarterRevenue || f.Accession != "2026-q3.xlsx" {
			t.Errorf("fact %+v", f)
		}
	}
	if f := facts[1]; f.End.Format(time.DateOnly) != "2026-06-30" || f.Start.Format(time.DateOnly) != "2026-04-01" || f.Value != 20794e6 {
		t.Errorf("Q3 FY2026 %+v", f)
	}
	if fs, ok, _ := st.FetchStateOf(ctx, IssuerQuarterlyState("SIE")); !ok || !strings.Contains(fs.LastError, "Q1") {
		t.Errorf("fetch state %+v", fs)
	}
	delete(web, "https://ir/f/2026-q3.xlsx") // stored: not requested again
	res = RunIssuers(ctx, cfg, st, c, slog.New(slog.DiscardHandler), now.Add(time.Hour))
	if res[0].Quarters != 0 || strings.Contains(res[0].Err.Error(), "404") {
		t.Errorf("second run %+v", res[0])
	}
}

// Report dates of the calendar page are stored; a page without a matching
// event is an error in fetch_state and stores nothing.
func TestIssuerCalendar(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	page, err := os.ReadFile("../../testdata/ircal/siemens-energy-2026-11-14.html")
	if err != nil {
		t.Fatal(err)
	}
	cal := &config.IssuerCalendar{URL: "https://ir/cal", Kind: config.CalendarText, Slash: "dmy", Lines: 4,
		Events: []config.CalendarMatch{{Match: `^Q(?P<q>[1-4]) FY(?P<year>\d{4})$`, Period: "Q{q} FY{year}"}}}
	cal.Events[0].Re = regexp.MustCompile(cal.Events[0].Match)
	cfg := &config.Config{Issuers: config.Issuers{Enabled: true, Companies: []config.Issuer{{Ticker: "ENR", Calendar: cal}}}}
	web := webPages{"https://ir/cal": page}
	now := time.Date(2026, 11, 14, 8, 30, 0, 0, time.UTC)
	res := RunIssuers(ctx, cfg, st, &IssuerClients{Web: web}, slog.New(slog.DiscardHandler), now)
	if res[0].Err != nil || res[0].Dates != 4 {
		t.Fatalf("result %+v", res[0])
	}
	ds, _ := st.IssuerReportDates(ctx, "ENR")
	if len(ds) != 4 || ds[0].Period != "Q1 FY2027" || ds[0].Date.Format(time.DateOnly) != "2027-02-11" {
		t.Fatalf("dates %+v", ds)
	}
	web["https://ir/cal"] = []byte("<html><body>new layout</body></html>")
	res = RunIssuers(ctx, cfg, st, &IssuerClients{Web: web}, slog.New(slog.DiscardHandler), now.Add(time.Hour))
	if res[0].Err == nil || res[0].Dates != 0 {
		t.Fatalf("changed layout %+v", res[0])
	}
	if fs, ok, _ := st.FetchStateOf(ctx, IssuerCalendarState("ENR")); !ok || !strings.Contains(fs.LastError, "no report event") {
		t.Errorf("fetch state %+v", fs)
	}
	if ds, _ := st.IssuerReportDates(ctx, "ENR"); len(ds) != 4 {
		t.Errorf("dates after a failed read %+v", ds)
	}
}
