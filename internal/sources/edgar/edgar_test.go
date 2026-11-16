package edgar

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

var update = flag.Bool("update", false, "rewrite testdata/edgar/golden/*.txt")

const fixtures = "../../../testdata/edgar"

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestParseSubmissions(t *testing.T) {
	s, err := ParseSubmissions(read(t, "submissions_MSFT.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.CIK != "0000789019" || s.FiscalYearEnd != "0630" || s.Name == "" {
		t.Errorf("header = %q %q %q", s.CIK, s.FiscalYearEnd, s.Name)
	}
	var found bool
	for _, f := range s.Filings {
		if f.Accession != "0001193125-26-323632" {
			continue
		}
		found = true
		if f.Form != "8-K" || !f.HasItem(ItemEarnings) || !f.HasItem("9.01") || f.HasItem("1.01") {
			t.Errorf("8-K = %+v", f)
		}
		if got := f.AcceptedAt.Format(time.RFC3339); got != "2026-07-29T20:04:53Z" {
			t.Errorf("accepted = %s", got)
		}
		if !f.FilingDate.Equal(day("2026-07-29")) || f.PrimaryDocument != "msft-20260729.htm" {
			t.Errorf("filing date / document = %s %s", f.FilingDate, f.PrimaryDocument)
		}
	}
	if !found {
		t.Fatal("MSFT FQ4 FY26 earnings 8-K not found")
	}
}

// Tesla files deliveries under item 2.02 as well: both 8-Ks of October 2026
// are 2.02 candidates; telling them apart is the collector's job (by the
// earnings date), not the parser's.
func TestTeslaDeliveriesAreItem202(t *testing.T) {
	s, err := ParseSubmissions(read(t, "submissions_TSLA.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range s.Filings {
		if f.Form == "8-K" && f.HasItem(ItemEarnings) && !f.FilingDate.Before(day("2026-10-01")) {
			got = append(got, f.Accession)
		}
	}
	want := "0001628280-26-066738 0001628280-26-064366"
	if strings.Join(got, " ") != want {
		t.Errorf("2.02 8-Ks since 2026-10-01 = %v, want %s", got, want)
	}
}

func TestParseCompanyFacts(t *testing.T) {
	concepts := []string{"PaymentsToAcquirePropertyPlantAndEquipment", "EarningsPerShareDiluted", "NoSuchConcept"}
	facts, err := ParseCompanyFacts(read(t, "companyfacts_MSFT.json"), concepts)
	if err != nil {
		t.Fatal(err)
	}
	byConcept := map[string]int{}
	var fy26 *model.CompanyFact
	for i, f := range facts {
		byConcept[f.Concept+" "+f.Unit]++
		if f.Concept == concepts[0] && f.Start.Equal(day("2025-07-01")) && f.End.Equal(day("2026-06-30")) {
			fy26 = &facts[i]
		}
	}
	if byConcept["PaymentsToAcquirePropertyPlantAndEquipment USD"] == 0 || byConcept["EarningsPerShareDiluted USD/shares"] == 0 {
		t.Errorf("facts per concept = %v", byConcept)
	}
	if byConcept["NoSuchConcept USD"] != 0 {
		t.Error("unknown concept produced facts")
	}
	if fy26 == nil {
		t.Fatal("FY2026 capex fact missing")
	}
	if fy26.Value != 115_948_000_000 || fy26.Form != "10-K" || fy26.FP != "FY" || fy26.Accession != "0001193125-26-323660" || fy26.Frame != "CY2026" {
		t.Errorf("FY2026 capex = %+v", *fy26)
	}
}

// AMZN puts trailing twelve months into its 10-Qs: a 364-day fact filed in
// a 10-Q is not a fiscal year (LOOKUP E3).
func TestCompanyFactsKeepTTM(t *testing.T) {
	facts, err := ParseCompanyFacts(read(t, "companyfacts_AMZN.json"), []string{"PaymentsToAcquireProductiveAssets"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range facts {
		if f.Start.Equal(day("2025-07-01")) && f.End.Equal(day("2026-06-30")) {
			if f.Form != "10-Q" || f.Value != 173_028_000_000 {
				t.Errorf("TTM fact = %+v", f)
			}
			return
		}
	}
	t.Error("AMZN TTM capex to 2026-06-30 missing")
}

var releases = []struct {
	ticker, accn, file string
	outlook            bool
	quotes             int
}{
	{"NVDA", "0001045810-26-000073", "q2fy27pr.htm", true, 1},
	{"MSFT", "0001193125-26-323632", "msft-ex99_1.htm", false, 2},
	{"AAPL", "0000320193-26-000018", "a8-kex991q3202606272026.htm", false, 2},
	{"AMZN", "0001018724-26-000024", "amzn-20260630xex991.htm", true, 1},
	{"META", "0001628280-26-050596", "meta-06302026xexhibit991.htm", true, 1},
	{"META", "0001628280-26-066700", "meta-09302026xexhibit991.htm", true, 1},
	{"GOOGL", "0001652044-26-000066", "googexhibit991q22026.htm", false, 1},
	{"AVGO", "0001730168-26-000076", "avgo-08022026x8kxex99.htm", true, 2},
	{"TSLA", "0001628280-26-066738", "exhibit9914.htm", true, 0},
	{"TSLA", "0001628280-26-064366", "exhibit991111111.htm", false, 0}, // deliveries, not earnings
}

func TestExhibitFile(t *testing.T) {
	for _, r := range releases {
		got, ok := ExhibitFile(read(t, r.ticker+"_"+r.accn+"-index-headers.html"), "EX-99.1")
		if !ok || got != r.file {
			t.Errorf("%s %s: EX-99.1 = %q, %v; want %s", r.ticker, r.accn, got, ok, r.file)
		}
	}
	if _, ok := ExhibitFile(read(t, "MSFT_0001193125-26-323632-index-headers.html"), "EX-99.2"); ok {
		t.Error("MSFT has no EX-99.2")
	}
}

// The golden files hold the title, Outlook and quotes extracted from each
// recorded press release: what the USA Micro export shows.
func TestReleaseExtractionGolden(t *testing.T) {
	for _, r := range releases {
		t.Run(r.ticker+"_"+r.accn, func(t *testing.T) {
			text, err := ReleaseText(read(t, r.ticker+"_"+r.accn+"_"+r.file))
			if err != nil {
				t.Fatal(err)
			}
			if len(text) < 1000 {
				t.Fatalf("text has %d bytes", len(text))
			}
			for _, bad := range []string{"<", "&nbsp;", "&#"} {
				if strings.Contains(text, bad) {
					t.Errorf("text contains %q", bad)
				}
			}
			outlook, quotes := Outlook(text), Quotes(text)
			if (len(outlook) > 0) != r.outlook {
				t.Errorf("outlook = %d paragraphs, want present = %v", len(outlook), r.outlook)
			}
			if len(quotes) != r.quotes {
				t.Errorf("quotes = %d, want %d", len(quotes), r.quotes)
			}
			var b strings.Builder
			b.WriteString("# Title\n" + Title(text) + "\n\n# Outlook\n" + strings.Join(outlook, "\n\n") + "\n\n# Quotes\n" + strings.Join(quotes, "\n\n") + "\n")
			golden := filepath.Join(fixtures, "golden", r.ticker+"_"+r.accn+".txt")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if b.String() != string(want) {
				t.Errorf("extraction differs from %s (go test ./internal/sources/edgar -run Golden -update)\ngot:\n%s", golden, b.String())
			}
		})
	}
}

func TestOutlookSkipsSentencesOutsideSections(t *testing.T) {
	text := "Microsoft Announces Results\n\nSeveral items impacted results compared to our forward-looking guidance provided on April 29, 2026.\n\n" +
		"Microsoft will provide forward-looking guidance on its earnings conference call."
	if got := Outlook(text); got != nil {
		t.Errorf("outlook = %q, want none", got)
	}
}

func TestQuoteAcrossParagraphs(t *testing.T) {
	text := "Alex Example, CEO of Example, said: “Our models are driving growth.\n\nWe are investing for the long term.”\n\nOther paragraph."
	got := Quotes(text)
	if len(got) != 1 || !strings.HasSuffix(got[0], "long term.”") {
		t.Errorf("quotes = %q", got)
	}
}

// fixtureServer serves testdata/edgar under the data.sec.gov and archive
// paths the client requests.
func fixtureServer(t *testing.T, requireUA string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("User-Agent") != requireUA {
			http.Error(w, "Your Request Originates from an Undeclared Automated Tool", http.StatusForbidden)
			return
		}
		var name string
		switch p := r.URL.Path; {
		case p == "/submissions/CIK0000789019.json":
			name = "submissions_MSFT.json"
		case p == "/api/xbrl/companyfacts/CIK0000789019.json":
			name = "companyfacts_MSFT.json"
		case p == "/archives/789019/000119312526323632/0001193125-26-323632-index-headers.html":
			name = "MSFT_0001193125-26-323632-index-headers.html"
		case p == "/archives/789019/000119312526323632/msft-ex99_1.htm":
			name = "MSFT_0001193125-26-323632_msft-ex99_1.htm"
		default:
			http.NotFound(w, r)
			return
		}
		w.Write(read(t, name))
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestClient(t *testing.T) {
	const ua = "macro-collector test@example.com"
	srv, paths := fixtureServer(t, ua)
	c := New(srv.URL, srv.URL+"/archives/", httpx.New(httpx.WithUserAgent(ua), httpx.WithInterval(0), httpx.WithRetries(0, 0)), nil)
	ctx := context.Background()

	s, err := c.Submissions(ctx, "0000789019")
	if err != nil || len(s.Filings) == 0 {
		t.Fatalf("submissions: %d filings, %v", len(s.Filings), err)
	}
	facts, err := c.Facts(ctx, "0000789019", []string{"EarningsPerShareDiluted"})
	if err != nil || len(facts) == 0 {
		t.Fatalf("facts: %d, %v", len(facts), err)
	}
	pr, err := c.PressRelease(ctx, "0000789019", "0001193125-26-323632")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Filename != "msft-ex99_1.htm" || !strings.HasSuffix(pr.URL, "/archives/789019/000119312526323632/msft-ex99_1.htm") || len(Quotes(pr.Text)) != 2 {
		t.Errorf("press release = %s %s, %d quotes", pr.Filename, pr.URL, len(Quotes(pr.Text)))
	}
	// Only JSON APIs and the 8-K exhibit: no 10-K/10-Q document is fetched.
	for _, p := range *paths {
		if strings.Contains(p, "10-k") || strings.Contains(p, "msft-2026063") {
			t.Errorf("unexpected request %s", p)
		}
	}
}

func TestClientMissingContact(t *testing.T) {
	srv, _ := fixtureServer(t, "macro-collector test@example.com")
	c := New(srv.URL, srv.URL+"/archives", httpx.New(httpx.WithUserAgent("curl/8"), httpx.WithInterval(0), httpx.WithRetries(0, 0)), nil)
	_, err := c.Submissions(context.Background(), "0000789019")
	if err == nil || !strings.Contains(err.Error(), "status 403") || !strings.Contains(err.Error(), "edgar.user_agent") {
		t.Errorf("err = %v", err)
	}
}

func TestInterval(t *testing.T) {
	if got := Interval(8); got != 125*time.Millisecond {
		t.Errorf("Interval(8) = %v", got)
	}
}
