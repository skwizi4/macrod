package bot

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/notify"
)

// A Siemens file stands in for the Allianz one: the path is the same
// (quarterly source file, irxlsx).
func fileConfig() *config.Config {
	q := &config.IssuerQuarterly{Source: config.QuarterlyFile, FiscalYearEnd: 9, RevenueLabel: "Revenue", OperatingName: "Profit Industrial Business",
		Links: []string{`^(?P<year>\d{4})-q(?P<q>[1-4])-Financial-Results-Excel\.xlsx$`},
		Tables: []config.QuarterlyTable{{Anchor: []string{"Consolidated Statements of Income"}, Scale: 1e6,
			Rows: map[string]config.QuarterlyRow{config.QuarterRevenue: {Labels: []string{"Revenue"}}}}}}
	q.LinkRes = []*regexp.Regexp{regexp.MustCompile(q.Links[0])}
	return &config.Config{Issuers: config.Issuers{Enabled: true, Companies: []config.Issuer{
		{Ticker: "SIE", Name: "Siemens", Currency: "EUR", Quarterly: q},
		{Ticker: "SAP", Name: "SAP"},
	}}}
}

func TestIssuerFile(t *testing.T) {
	ctx := context.Background()
	q3, err := os.ReadFile("../../testdata/irxlsx/siemens-2026-q3.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTG{files: map[string][]byte{"F3": q3}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	h := &IssuerFile{Config: fileConfig(), Store: testStore(t), TG: notify.New(srv.URL, "1:t", "42"), Log: discard(),
		Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }}
	doc := func(name, caption string, size int64) *notify.Message {
		return &notify.Message{ID: 5, ChatID: "42", Text: caption, Document: &notify.Document{FileID: "F3", Name: name, Size: size}}
	}
	for _, tt := range []struct {
		msg  *notify.Message
		ok   bool
		want string
	}{
		{&notify.Message{Text: "ALV Q3 2026"}, false, ""},
		{doc("fs.pdf", "", 10), true, "нужен файл .xlsx"},
		{doc("fs.xlsx", "SAP Q3 2026", 10), true, "у SAP нет секции quarterly"},
		{doc("fs.xlsx", "", 10), true, "квартал не виден из имени fs.xlsx"},
		{doc("big.xlsx", "", 30<<20), true, "больше 20 МБ"},
		{doc("fs.xlsx", "Q2 2026", 10), true, "Ничего не записано"}, // the Q3 file has no Q2 columns
		{doc("2026-q3-Financial-Results-Excel.xlsx", "", 10), true, "✅ Siemens Q3 2026 (2026-q3-Financial-Results-Excel.xlsx), изменено фактов: 2\nRevenue: 20.79 млрд EUR"},
		{doc("siemens.xlsx", "sie q3 2026", 10), true, "✅ Siemens Q3 2026 (siemens.xlsx), изменено фактов: 2"}, // same numbers, another file name: its own facts
	} {
		reply, ok := h.Handle(ctx, tt.msg)
		if ok != tt.ok || !strings.Contains(reply, tt.want) {
			t.Errorf("%+v: %v %q, want %q", tt.msg.Document, ok, reply, tt.want)
		}
	}
	facts, _ := h.Store.CompanyFacts(ctx, "SIE")
	if len(facts) != 4 {
		t.Fatalf("facts %+v", facts)
	}
	if st, ok, _ := h.Store.FetchStateOf(ctx, collect.IssuerQuarterlyState("SIE")); !ok || st.LastError != "" || st.Cursor != "siemens.xlsx" {
		t.Fatalf("fetch state %+v", st)
	}
}

func TestReportQuarter(t *testing.T) {
	for in, want := range map[string]string{"Q3 2026": "2026 3", "Q4 FY2026": "2026 4", "FY 2026": "2026 4", "H1 2026": "no"} {
		fy, q, ok := reportQuarter(in)
		got := "no"
		if ok {
			got = fmt.Sprint(fy, " ", q)
		}
		if got != want {
			t.Errorf("reportQuarter(%q) = %s, want %s", in, got, want)
		}
	}
}

// From the report day the file is asked for once a day until it is stored.
func TestIssuerFileRemind(t *testing.T) {
	ctx := context.Background()
	q3, err := os.ReadFile("../../testdata/irxlsx/siemens-2026-q3.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	cfg := fileConfig()
	cfg.Location = time.UTC
	sie := &cfg.Issuers.Companies[0]
	sie.Quarterly.Hint = "siemens.com → Investor Relations → Financial results"
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	sie.Reports = []config.IssuerDate{{Day: day("2026-08-06"), Period: "Q3 FY2026"}, {Day: day("2026-11-12"), Period: "Q4 FY2026"}}
	fake := &fakeTG{files: map[string][]byte{"F3": q3}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	now := day("2026-08-05").Add(8 * time.Hour)
	h := &IssuerFile{Config: cfg, Store: testStore(t), TG: notify.New(srv.URL, "1:t", "42"), Log: discard(), Now: func() time.Time { return now }}
	for _, step := range []struct{ now, want string }{
		{"2026-08-05", "[]"},    // before the report
		{"2026-08-06", "[SIE]"}, // report day
		{"2026-08-06", "[]"},    // once a day
		{"2026-08-07", "[SIE]"},
	} {
		now = day(step.now).Add(8 * time.Hour)
		got, err := h.Remind(ctx)
		if err != nil || fmt.Sprint(got) != step.want {
			t.Fatalf("%s: %v %v, want %s", step.now, got, err, step.want)
		}
	}
	calls, _ := fake.snapshot()
	if len(calls) != 2 || !strings.Contains(calls[0], `Siemens: отчёт за Q3 2026 вышел 2026-08-06`) ||
		!strings.Contains(calls[0], `напишите:\nSIE Q3 2026\nШаблон подписи: \u003cТИКЕР\u003e Q\u003cквартал\u003e \u003cгод\u003e`) ||
		!strings.Contains(calls[0], "Где взять: siemens.com") || !strings.Contains(calls[0], `macroctl issuers file SIE \u003cфайл.xlsx\u003e --period \"Q3 2026\"`) ||
		!strings.Contains(calls[0], "до 2026-08-27") {
		t.Fatalf("calls %v", calls)
	}
	// The file arrives: no more reminders for Q3.
	if _, ok := h.Handle(ctx, &notify.Message{ID: 1, ChatID: "42", Text: "SIE Q3 2026", Document: &notify.Document{FileID: "F3", Name: "q3.xlsx", Size: 10}}); !ok {
		t.Fatal("file not taken")
	}
	now = day("2026-08-10").Add(8 * time.Hour)
	if got, _ := h.Remind(ctx); len(got) != 0 {
		t.Fatalf("after the file: %v", got)
	}
	now = day("2026-09-01").Add(8 * time.Hour) // window over
	if got, _ := h.Remind(ctx); len(got) != 0 {
		t.Fatalf("after the window: %v", got)
	}
}
