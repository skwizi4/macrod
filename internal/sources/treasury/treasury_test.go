package treasury

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/httpx"
)

var testdata = filepath.Join("..", "..", "..", "testdata", "treasury")

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// server serves recorded responses. files maps "kind:key" to a fixture:
// fiscal:<endpoint>, direct:<path>:<type>, calendar, refunding, page:<id>.
type server struct {
	t     *testing.T
	files map[string]string
	mu    sync.Mutex
	reqs  []string
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, r.URL.RequestURI())
	s.mu.Unlock()
	var key string
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/fiscal/"):
		key = "fiscal:" + strings.TrimPrefix(p, "/fiscal/")
	case strings.HasPrefix(p, "/ta/"):
		key = "direct:" + strings.TrimPrefix(p, "/ta/") + ":" + r.URL.Query().Get("type")
	case p == "/calendar":
		key = "calendar"
	case p == "/refunding":
		key = "refunding"
	case strings.HasPrefix(p, "/news/press-releases/"):
		key = "page:" + strings.TrimPrefix(p, "/news/press-releases/")
	}
	name, ok := s.files[key]
	if !ok && strings.HasPrefix(key, "direct:search:") {
		w.Write([]byte("[]")) // no auctions of that type in the range
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write(fixture(s.t, name))
}

func newClient(t *testing.T, files map[string]string, opts ...Option) (*Client, *server) {
	t.Helper()
	s := &server{t: t, files: files}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cfg := config.Treasury{
		FiscalDataURL:         srv.URL + "/fiscal",
		ReleaseCalendarURL:    srv.URL + "/calendar",
		TreasuryDirectURL:     srv.URL + "/ta",
		QuarterlyRefundingURL: srv.URL + "/refunding",
	}
	h := httpx.New(httpx.WithInterval(0), httpx.WithRetries(0, 0))
	return New(cfg, h, opts...), s
}

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

var et = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return l
}()

func values(obs []model.Observation) map[string]float64 {
	m := make(map[string]float64, len(obs))
	for _, o := range obs {
		m[o.Date.Format(time.DateOnly)] = o.Value
	}
	return m
}

// The TGA moves between rows and columns twice (LOOKUP T2).
func TestFetchTGAAcrossFormatChanges(t *testing.T) {
	for _, tt := range []struct {
		file string
		want map[string]float64
	}{
		{"dts_2021_10.json", map[string]float64{
			"2021-09-28": 172920, "2021-09-30": 215160, // Federal Reserve Account, closing column
			"2021-10-01": 132452, "2021-10-05": 99385, // Treasury General Account (TGA), closing column
		}},
		{"dts_2022_04.json", map[string]float64{
			"2022-04-15": 578473,                       // last day of the old layout
			"2022-04-18": 841253, "2022-04-19": 893351, // "Closing Balance" row, value in open_today_bal
		}},
		{"dts_2026_10.json", map[string]float64{"2026-10-21": 945153, "2026-10-22": 961759}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			c, s := newClient(t, map[string]string{"fiscal:" + endpointDTS: tt.file})
			obs, err := c.FetchTGA(context.Background(), day("2021-01-01"))
			if err != nil {
				t.Fatal(err)
			}
			got := values(obs)
			for d, w := range tt.want {
				if got[d] != w {
					t.Errorf("%s = %v, want %v", d, got[d], w)
				}
			}
			for i := 1; i < len(obs); i++ {
				if !obs[i].Date.After(obs[i-1].Date) {
					t.Fatalf("not ascending at %d", i)
				}
			}
			if q := s.reqs[0]; !strings.Contains(q, "filter=record_date%3Agte%3A2021-01-01") || !strings.Contains(q, "page%5Bsize%5D=10000") {
				t.Errorf("request = %s", q)
			}
		})
	}
}

// Fiscal Data paging is followed until total-pages.
func TestFiscalPaging(t *testing.T) {
	all := fixture(t, "dts_2026_10.json")
	var full struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(all, &full); err != nil {
		t.Fatal(err)
	}
	const size = 7
	pages := (len(full.Data) + size - 1) / size
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("page[number]"))
		lo, hi := (n-1)*size, min(n*size, len(full.Data))
		b, _ := json.Marshal(map[string]any{"data": full.Data[lo:hi], "meta": map[string]any{"total-pages": pages}})
		w.Write(b)
	}))
	defer srv.Close()
	c := New(config.Treasury{FiscalDataURL: srv.URL}, httpx.New(httpx.WithInterval(0)), WithPageSize(size))
	rows, err := c.fiscal(context.Background(), endpointDTS, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(full.Data) || pages < 3 {
		t.Errorf("rows = %d of %d over %d pages", len(rows), len(full.Data), pages)
	}
}

func TestFetchMTSLatestStatementWins(t *testing.T) {
	c, _ := newClient(t, map[string]string{"fiscal:" + endpointMTS: "mts_2026_08_09.json"})
	got, err := c.FetchMTS(context.Background(), day("2026-08-31"))
	if err != nil {
		t.Fatal(err)
	}
	def := values(got[SeriesMTSDeficit])
	for d, w := range map[string]float64{
		"2026-08-01": 166778812054.25,  // August as revised in the September statement
		"2026-09-01": 26900347102.02,   // only in the September statement
		"2025-09-01": -197937721277.68, // FY 2025 September: a surplus
		"2025-10-01": 284332743998.37,  // FY 2026 starts in October 2025
	} {
		if def[d] != w {
			t.Errorf("deficit %s = %v, want %v", d, def[d], w)
		}
	}
	rc, out := values(got[SeriesMTSReceipts]), values(got[SeriesMTSOutlays])
	if rc["2026-09-01"] != 557874519238.24 || out["2026-09-01"] != 584774866340.26 {
		t.Errorf("September receipts %v, outlays %v", rc["2026-09-01"], out["2026-09-01"])
	}
	// 24 months: FY 2025 and FY 2026.
	for id, obs := range got {
		if len(obs) != 24 {
			t.Errorf("%s: %d months, want 24", id, len(obs))
		}
	}
}

func find(as []model.Auction, cusip string) model.Auction {
	for _, a := range as {
		if a.CUSIP == cusip {
			return a
		}
	}
	return model.Auction{}
}

func near(a model.Num, want float64) bool { return a.OK && math.Abs(a.V-want) < 1e-6 }

func TestFetchAuctionsBondsAndReopenings(t *testing.T) {
	c, s := newClient(t, map[string]string{"direct:search:Bond": "ta_search_bond_2025_2026.json"})
	as, err := c.FetchAuctions(context.Background(), []string{"Bond"}, day("2025-10-01"), day("2026-10-23"))
	if err != nil {
		t.Fatal(err)
	}
	if len(as) == 0 {
		t.Fatal("no auctions")
	}
	// Requests go by issue date, extended past the range end.
	if q := s.reqs[len(s.reqs)-1]; !strings.Contains(q, "endDate=2026-11-13") {
		t.Errorf("last request = %s", q)
	}
	a := as[len(as)-1]
	if a.AuctionDate.Format(time.DateOnly) != "2026-10-21" || a.Term != "19-Year 10-Month" || a.Tenor != "20-Year" ||
		a.Kind != model.KindBond || !a.Reopening || a.ClosingTime != "13:00" || a.Security() != "20-Year Bond" {
		t.Errorf("last auction = %+v", a)
	}
	if !near(a.HighYield, 5.469) || !near(a.BidToCover, 2.73) || a.HighRate.OK {
		t.Errorf("yields = %+v / btc %+v / rate %+v", a.HighYield, a.BidToCover, a.HighRate)
	}
	if !a.IndirectPct.OK || math.Abs(a.IndirectPct.V+a.DirectPct.V+a.DealerPct.V-100) > 1e-9 {
		t.Errorf("shares = %v %v %v", a.IndirectPct, a.DirectPct, a.DealerPct)
	}
	for i := 1; i < len(as); i++ {
		if as[i].AuctionDate.Before(as[i-1].AuctionDate) {
			t.Fatalf("not sorted at %d", i)
		}
	}
	tenors := map[string]bool{}
	for _, a := range as {
		tenors[a.Tenor] = true
	}
	if len(tenors) != 2 || !tenors["20-Year"] || !tenors["30-Year"] {
		t.Errorf("tenors = %v", tenors)
	}
}

func TestFetchAuctionsBillsAndFiltering(t *testing.T) {
	c, _ := newClient(t, map[string]string{"direct:search:Bill": "ta_search_bill_2026_10.json"})
	// The fixture holds bills issued 13–15 Oct, auctioned 7–13 Oct.
	as, err := c.FetchAuctions(context.Background(), []string{"Bill"}, day("2026-10-08"), day("2026-10-13"))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range as {
		if d := a.AuctionDate.Format(time.DateOnly); d < "2026-10-08" || d > "2026-10-13" {
			t.Errorf("auction %s outside the range", d)
		}
	}
	six := find(as, "912797SU2")
	// A 6-week bill reopening a 52-week bill still groups as 6-Week.
	if six.Tenor != "6-Week" || six.Kind != model.KindBill || !near(six.HighRate, 4.055) || six.HighYield.OK ||
		six.Security() != "6-Week Bill" {
		t.Errorf("6-week bill = %+v", six)
	}
	if !near(six.IndirectPct, 59768443600/91733588600.0*100) {
		t.Errorf("indirect = %+v", six.IndirectPct)
	}
}

// Notes include TIPS and FRNs only through their own TA_WS types; a TIPS
// bond comes from both the Bond and TIPS queries and is kept once.
func TestFetchAuctionsTIPS(t *testing.T) {
	c, s := newClient(t, map[string]string{"direct:search:TIPS": "ta_search_tips_2026.json"})
	as, err := c.FetchAuctions(context.Background(), []string{"Note", "Bond"}, day("2026-07-01"), day("2026-10-23"))
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, q := range s.reqs {
		if i := strings.Index(q, "type="); i > 0 {
			if typ := q[i+5:]; !slices.Contains(types, typ) {
				types = append(types, typ)
			}
		}
	}
	slices.Sort(types)
	if !slices.Equal(types, []string{"Bond", "FRN", "Note", "TIPS"}) {
		t.Errorf("queried types = %v", types)
	}
	tips := find(as, "91282CRR4")
	if tips.Kind != model.KindTIPS || tips.SecurityType != model.KindNote || tips.Security() != "5-Year TIPS" ||
		!near(tips.HighYield, 2.367) || !near(tips.BidToCover, 2.41) {
		t.Errorf("5-year TIPS = %+v", tips)
	}
	seen := map[string]int{}
	for _, a := range as {
		seen[a.CUSIP+a.AuctionDate.Format(time.DateOnly)]++
		if a.Kind != model.KindTIPS {
			t.Errorf("unexpected kind %s", a.Kind)
		}
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times", k, n)
		}
	}
	// With only bills configured, the TIPS are not returned.
	if as, _ := c.FetchAuctions(context.Background(), []string{"Bill"}, day("2026-07-01"), day("2026-10-23")); len(as) != 0 {
		t.Errorf("bills only: %d auctions", len(as))
	}
}

func TestFetchAuctionsWithoutBidderShares(t *testing.T) {
	c, _ := newClient(t, map[string]string{"direct:search:Note": "ta_search_note_2000_02.json"})
	as, err := c.FetchAuctions(context.Background(), []string{"Note"}, day("2000-01-01"), day("2000-02-29"))
	if err != nil {
		t.Fatal(err)
	}
	a := find(as, "9128276A5")
	if a.Tenor != "2-Year" || !near(a.HighYield, 6.59) || !near(a.BidToCover, 2.66) || a.IndirectPct.OK || a.ClosingTime != "" {
		t.Errorf("2000 note = %+v", a)
	}
}

func TestAuctionEventsFromUpcoming(t *testing.T) {
	c, _ := newClient(t, map[string]string{"direct:upcoming:": "ta_upcoming.json"})
	as, err := c.FetchUpcoming(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	evs := AuctionEvents(as, et)
	byName := map[string][]model.ReleaseEvent{}
	for _, e := range evs {
		byName[e.Name] = append(byName[e.Name], e)
	}
	for name, want := range map[string]struct{ at, importance string }{
		"2-Year Note":  {"2026-10-26T17:00:00Z", "medium"}, // 13:00 EDT
		"7-Year Note":  {"2026-10-29T17:00:00Z", "medium"},
		"2-Year FRN":   {"2026-10-28T15:30:00Z", "low"},
		"52-Week Bill": {"2026-10-27T15:30:00Z", "low"},
	} {
		e := byName[name]
		if len(e) != 1 || e[0].ScheduledAt.Format(time.RFC3339) != want.at || e[0].Importance != want.importance ||
			e[0].Source != SourceAuction || e[0].Related.CUSIP == "" {
			t.Errorf("%s = %+v, want %+v", name, e, want)
		}
	}
	// Not yet announced: no close time, default 11:30 ET for bills; after
	// the DST switch 11:30 EST is 16:30 UTC.
	var nov []string
	for _, e := range byName["13-Week Bill"] {
		nov = append(nov, e.ScheduledAt.Format(time.RFC3339))
	}
	if !slices.Contains(nov, "2026-11-02T16:30:00Z") {
		t.Errorf("13-week bills = %v", nov)
	}
	if got := AuctionImportance(model.Auction{Kind: model.KindNote, Tenor: "10-Year"}); got != "high" {
		t.Errorf("10-year importance = %s", got)
	}
}

func TestReleaseCalendar(t *testing.T) {
	c, _ := newClient(t, map[string]string{"calendar": "release_calendar.json"})
	evs, err := c.FetchReleaseCalendar(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var dts, mts []string
	for _, e := range evs {
		switch e.Name {
		case NameDTS:
			dts = append(dts, e.ScheduledAt.Format(time.RFC3339))
		case NameMTS:
			mts = append(mts, e.ScheduledAt.Format(time.RFC3339))
			if e.Importance != "medium" || e.Related.Dataset != "mts" {
				t.Errorf("mts event = %+v", e)
			}
		default:
			t.Errorf("unexpected event %s", e.Name)
		}
	}
	// 16:00 ET in both summer and winter time; MTS 14:00 ET.
	for _, w := range []string{"2026-10-26T20:00:00Z", "2026-11-02T21:00:00Z"} {
		if !slices.Contains(dts, w) {
			t.Errorf("dts missing %s", w)
		}
	}
	if !slices.Contains(mts, "2026-11-12T19:00:00Z") {
		t.Errorf("mts = %v", mts)
	}
}

func TestParseRefundingPage(t *testing.T) {
	secs, err := ParseRefundingPage(fixture(t, "refunding.html"), "https://home.treasury.gov/x", et)
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 3 {
		t.Fatalf("sections = %d", len(secs))
	}
	f := func(t time.Time) string { return t.Format("2006-01-02 15:04 MST") }
	for i, want := range []struct{ released, next, firstDoc string }{
		{"2026-08-03 15:00 EDT", "2026-11-02 15:00 EST", "Financing Estimates: 2026 - 3rd Quarter"},
		{"2026-08-05 08:30 EDT", "2026-11-04 08:30 EST", "Policy Statement: 2026 - 3rd Quarter"},
		{"2026-10-16 12:00 EDT", "2027-01-15 12:00 EST", "Primary Dealer Meeting Agenda: 2026 - 4th Quarter"},
	} {
		s := secs[i]
		if f(s.ReleasedAt) != want.released || f(s.Next) != want.next || len(s.Docs) == 0 || s.Docs[0].Title != want.firstDoc {
			t.Errorf("section %d = %s / %s / %+v", i+1, f(s.ReleasedAt), f(s.Next), s.Docs)
		}
	}
	if u := secs[0].Docs[0].URL; u != "https://home.treasury.gov/news/press-releases/sb0584" {
		t.Errorf("link = %s", u)
	}
	evs := RefundingEvents(secs)
	var got []string
	for _, e := range evs {
		got = append(got, e.Name+" "+e.ScheduledAt.Format(time.RFC3339))
	}
	want := []string{
		NameFinancingEstimates + " 2026-08-03T19:00:00Z", NameFinancingEstimates + " 2026-11-02T20:00:00Z",
		NameRefundingStatement + " 2026-08-05T12:30:00Z", NameRefundingStatement + " 2026-11-04T13:30:00Z",
	}
	if !slices.Equal(got, want) {
		t.Errorf("events = %v", got)
	}
}

// The page as of 2026-11-11 (after the Q4 refunding).
func TestParseRefundingPage202611(t *testing.T) {
	secs, err := ParseRefundingPage(fixture(t, "refunding_2026_11.html"), "https://home.treasury.gov/x", et)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range RefundingEvents(secs) {
		got = append(got, e.Name+" "+e.ScheduledAt.Format(time.RFC3339))
	}
	want := []string{
		NameFinancingEstimates + " 2026-11-02T20:00:00Z", NameFinancingEstimates + " 2027-02-01T20:00:00Z",
		NameRefundingStatement + " 2026-11-04T13:30:00Z", NameRefundingStatement + " 2027-02-03T13:30:00Z",
	}
	if len(secs) != 3 || !slices.Equal(got, want) {
		t.Errorf("sections = %d, events = %v", len(secs), got)
	}
	if u := secs[0].Docs[0].URL; u != "https://home.treasury.gov/news/press-releases/sb0675" {
		t.Errorf("link = %s", u)
	}
}

// Page prose mentioning "documents released" (seen live on 2026-11-11, a
// pointer to past refundings) is not a release block: it is skipped, not an
// error, wherever it sits.
func TestParseRefundingPageProse(t *testing.T) {
	const prose = `<p>Looking for documents released as part of past Treasury Quarterly Refundings? ` +
		`You can find them in the <a href=/quarterly-refunding-archives>Quarterly Refunding Archives</a>.</p>`
	page := string(fixture(t, "refunding_2026_11.html"))
	body := `field--name-field-page-body field--type-text-long field--label-hidden field__item">`
	if !strings.Contains(page, body) || !strings.Contains(page, "<h3>DOCUMENTS RELEASED at 12:00 PM") {
		t.Fatal("fixture layout changed")
	}
	for name, p := range map[string]string{
		"top":    strings.Replace(page, body, body+prose, 1),
		"middle": strings.Replace(page, "<h3>DOCUMENTS RELEASED at 12:00 PM", prose+"<h3>DOCUMENTS RELEASED at 12:00 PM", 1),
	} {
		secs, err := ParseRefundingPage([]byte(p), "https://home.treasury.gov/x", et)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(secs) != 3 || len(RefundingEvents(secs)) != 4 {
			t.Errorf("%s: sections = %+v", name, secs)
		}
	}
	// Only prose: the page changed, an error.
	only := `<html><body><div class="field--name-field-page-body">` + prose + `</div></body></html>`
	if _, err := ParseRefundingPage([]byte(only), "https://home.treasury.gov/x", et); err == nil ||
		!strings.Contains(err.Error(), "no section with a release time") {
		t.Errorf("err = %v", err)
	}
}

// A failing refunding page leaves the auctions and publications listed and
// reports a PartialError on the refunding series.
func TestCalendarRefundingDown(t *testing.T) {
	c, _ := newClient(t, map[string]string{"direct:upcoming:": "ta_upcoming.json", "calendar": "release_calendar.json"})
	cal := &Calendar{Client: c, ET: et, Now: func() time.Time { return day("2000-01-03") }}
	evs, err := cal.Upcoming(context.Background(), day("2026-01-01"), day("2028-01-01"))
	var partial *sources.PartialError
	if !errors.As(err, &partial) || !slices.Equal(partial.Keys, RefundingKeys) {
		t.Fatalf("err = %v", err)
	}
	var auctions, pubs int
	for _, e := range evs {
		switch {
		case e.Source == SourceAuction:
			auctions++
		case e.Name == NameDTS || e.Name == NameMTS:
			pubs++
		default:
			t.Errorf("unexpected event %+v", e)
		}
	}
	if auctions == 0 || pubs == 0 {
		t.Errorf("auctions = %d, publications = %d", auctions, pubs)
	}
}

func TestRefundingDocuments(t *testing.T) {
	c, s := newClient(t, map[string]string{"refunding": "refunding.html", "page:sb0584": "sb0584.html", "page:sb0590": "sb0590.html"})
	src := &Refunding{Client: c, ET: et}
	docs, cursor, err := src.FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || cursor != "sb0584 sb0590" {
		t.Fatalf("docs = %d, cursor %q", len(docs), cursor)
	}
	est, st := docs[0], docs[1]
	if est.DocType != DocBorrowingEstimates || est.Source != Source || est.ExternalID != "sb0584" ||
		est.PublishedAt.Format(time.RFC3339) != "2026-08-03T19:00:00Z" || !strings.Contains(est.Body, "privately-held net marketable borrowing") {
		t.Errorf("estimates = %+v", est)
	}
	if st.DocType != DocRefunding || !strings.Contains(st.Body, "refund approximately $96.3 billion") ||
		strings.Contains(st.Body, "Skip Navigation") {
		t.Errorf("statement = %s / %.200s", st.DocType, st.Body)
	}
	// Known ids are not fetched again.
	before := len(s.reqs)
	docs, cursor2, err := src.FetchDocuments(context.Background(), cursor)
	if err != nil || len(docs) != 0 || cursor2 != cursor || len(s.reqs) != before+1 {
		t.Errorf("second fetch: %d docs, cursor %q, %d requests, err %v", len(docs), cursor2, len(s.reqs)-before, err)
	}
}

func TestFetchBuybacks(t *testing.T) {
	c, _ := newClient(t, map[string]string{"fiscal:" + endpointBuybacks: "buybacks_2026_10.json"})
	bs, err := c.FetchBuybacks(context.Background(), day("2026-10-01"))
	if err != nil {
		t.Fatal(err)
	}
	var tips model.Buyback
	for _, b := range bs {
		if b.OperationDate.Format(time.DateOnly) == "2026-10-21" {
			tips = b
		}
	}
	if tips.StartTime != "13:40" || tips.OperationType != "Liquidity Support" || tips.SecurityType != "TIPS" ||
		tips.MaturityBucket != "1Y to 10Y" || !near(tips.ParOffered, 4751000000) || !near(tips.ParAccepted, 750000000) {
		t.Errorf("tips buyback = %+v", tips)
	}
}

func TestNumberAndClock(t *testing.T) {
	for in, want := range map[string]string{"": "-", "null": "-", "N/A": "-", "1,234.5": "1234.5", "-3": "-3"} {
		v, ok, err := number(in)
		got := "-"
		if ok {
			got = strconv.FormatFloat(v, 'f', -1, 64)
		}
		if err != nil || got != want {
			t.Errorf("number(%q) = %s, %v", in, got, err)
		}
	}
	if _, _, err := number("abc"); err == nil {
		t.Error("number(abc): want error")
	}
	for in, want := range map[string]string{"01:00 PM": "13:00", "11:30 AM": "11:30", "": ""} {
		if got, err := clock(in); err != nil || got != want {
			t.Errorf("clock(%q) = %q, %v", in, got, err)
		}
	}
}

// Only marketable bills, notes and bonds count: on 2026-10-29 the
// Government Account Series alone issued 803 bn and redeemed 801 bn.
func TestFetchPublicDebtMarketableOnly(t *testing.T) {
	c, _ := newClient(t, map[string]string{"fiscal:" + endpointPublicDebt: "dts_public_debt_2026_10.json"})
	got, err := c.FetchPublicDebt(context.Background(), day("2026-10-26"))
	if err != nil {
		t.Fatal(err)
	}
	at := func(id, d string) float64 {
		for _, o := range got[id] {
			if o.Date.Equal(day(d)) {
				return o.Value
			}
		}
		t.Fatalf("%s %s missing", id, d)
		return 0
	}
	for _, tt := range []struct {
		id, date string
		want     float64
	}{
		{SeriesBillsIssued, "2026-10-29", 354996}, // Regular 354 996 + CMB 0
		{SeriesBillsRedeemed, "2026-10-29", 315295},
		{SeriesCouponsIssued, "2026-10-29", 0},      // TIPS increment 217 left out
		{SeriesCouponsRedeemed, "2026-10-28", 6000}, // the 10-27 buyback settled
		{SeriesBillsIssued, "2026-10-28", -12},
	} {
		if v := at(tt.id, tt.date); v != tt.want {
			t.Errorf("%s %s = %v, want %v", tt.id, tt.date, v, tt.want)
		}
	}
	if len(got) != 4 || len(got[SeriesBillsIssued]) != 4 {
		t.Errorf("series %d, bill days %d", len(got), len(got[SeriesBillsIssued]))
	}

	// Nonmarketable rows alone give nothing.
	var nonmarketable []row
	for _, r := range fixtureRows(t, "dts_public_debt_2026_10.json") {
		if r["security_market"] != "Marketable" {
			nonmarketable = append(nonmarketable, r)
		}
	}
	if len(nonmarketable) == 0 {
		t.Fatal("fixture has no nonmarketable rows")
	}
	out, err := parsePublicDebt(nonmarketable)
	if err != nil || len(out) != 0 {
		t.Errorf("nonmarketable rows counted: %v %v", out, err)
	}
}

// fixtureRows decodes a recorded Fiscal Data page into rows.
func fixtureRows(t *testing.T, name string) []row {
	t.Helper()
	var p fiscalPage
	if err := json.Unmarshal(fixture(t, name), &p); err != nil {
		t.Fatal(err)
	}
	out := make([]row, 0, len(p.Data))
	for _, d := range p.Data {
		r := make(row, len(d))
		for k, v := range d {
			r[k] = str(v)
		}
		out = append(out, r)
	}
	return out
}
