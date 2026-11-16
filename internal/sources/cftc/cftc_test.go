package cftc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/derived"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/transform"
)

func TestFetchRecorded(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "cftc", FixtureFile))
	if err != nil {
		t.Fatal(err)
	}
	var where string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		where = r.URL.Query().Get("$where")
		w.Write(body)
	}))
	defer srv.Close()
	c := New(srv.URL, httpx.New(httpx.WithInterval(0)), nil)
	got, err := c.Fetch(context.Background(), FixtureCodes, FixtureSince)
	if err != nil {
		t.Fatal(err)
	}
	if where != "cftc_contract_market_code in ('209742','043602') AND report_date_as_yyyy_mm_dd >= '2025-10-01'" {
		t.Errorf("$where = %s", where)
	}
	if len(got) != 10 {
		t.Fatalf("series = %d", len(got))
	}
	nq := got[SeriesID("209742", FieldLevLong)]
	if len(nq) != 55 || nq[len(nq)-1].Date.Format(time.DateOnly) != "2026-10-20" || nq[len(nq)-1].Value != 51749 {
		t.Fatalf("NQ lev long = %d, last %+v", len(nq), nq[len(nq)-1])
	}
	// Leveraged Funds net on 2026-10-20: 51 749 − 50 799 = +950; 10Y:
	// 567 553 − 2 674 777 = −2 107 224 (testdata, LOOKUP C2).
	net := derived.Net(nq, got[SeriesID("209742", FieldLevShort)])
	if v := net[len(net)-1].Value; v != 950 {
		t.Errorf("NQ net = %v", v)
	}
	ty := derived.Net(got[SeriesID("043602", FieldLevLong)], got[SeriesID("043602", FieldLevShort)])
	if v := ty[len(ty)-1].Value; v != -2107224 {
		t.Errorf("10Y net = %v", v)
	}
	if oi := got[SeriesID("043602", FieldOpenInterest)]; oi[len(oi)-1].Value != 5868295 {
		t.Errorf("10Y OI = %v", oi[len(oi)-1])
	}
	// A year of weekly nets is enough for the 52-week z-score (≥ 20 obs).
	win, _ := transform.ParseWindow("52w")
	res, err := transform.Compute(transform.Input{Obs: ty, Freq: "w", AsOf: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		ZWindowDailyWeekly: win}, []string{transform.Z, transform.D})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Values[transform.Z].OK || !res.Values[transform.D].OK {
		t.Errorf("z / d = %+v", res.Values)
	}
}

func TestFetchPages(t *testing.T) {
	var offsets []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off, _ := strconv.Atoi(r.URL.Query().Get("$offset"))
		offsets = append(offsets, off)
		var rows []string
		for i := off; i < min(off+2, 5); i++ {
			d := time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 7*i).Format("2006-01-02")
			rows = append(rows, `{"report_date_as_yyyy_mm_dd":"`+d+`T00:00:00.000","cftc_contract_market_code":"209742","open_interest_all":"`+strconv.Itoa(100+i)+`"}`)
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	}))
	defer srv.Close()
	c := New(srv.URL, httpx.New(httpx.WithInterval(0)), nil)
	c.pageSize = 2
	got, err := c.Fetch(context.Background(), []string{"209742"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	oi := got[SeriesID("209742", FieldOpenInterest)]
	if len(oi) != 5 || oi[4].Value != 104 || len(offsets) != 3 || offsets[2] != 4 {
		t.Errorf("oi = %v, offsets %v", oi, offsets)
	}
	// Fields Socrata leaves out are absent, not zero.
	if _, ok := got[SeriesID("209742", FieldLevLong)]; ok {
		t.Error("missing field stored")
	}
}

func TestQueryQuotesCodes(t *testing.T) {
	c := New("https://cftc.example/resource/x.json", nil, nil)
	u, err := url.Parse(c.Query([]string{"20974+", "o'x"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 10))
	if err != nil {
		t.Fatal(err)
	}
	if w := u.Query().Get("$where"); !strings.Contains(w, "('20974+','o''x')") || !strings.HasSuffix(w, "<= '2026-02-01'") {
		t.Errorf("$where = %s", w)
	}
	if u.Query().Get("$offset") != "10" || u.Query().Get("$limit") != "5000" {
		t.Errorf("query = %s", u.RawQuery)
	}
}
