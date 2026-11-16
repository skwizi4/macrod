// Package finanzagentur reads the Bund auction results of the German
// Finance Agency (spec 6.1; LOOKUP 2026-11-11): emissionsergebnisse_aktuell
// _en.xlsx (the current year) and emissionshistorie_en.xlsx (since 1999),
// one row per issue auctioned.
//
// The files are made by hand. Their header spans several rows with words
// split across them ("Bid-to-" / "Cover-" / "Ratio5", trailing digits are
// footnotes), so the columns are found by their joined headers; a file
// without a required column, or with a row that fails a check, gives an
// error and no auctions.
package finanzagentur

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/xlsx"
)

// Source is the source name (auctions.source, fetch_state prefix).
const Source = "finanzagentur"

// SourceAuction is the releases.source of the planned auctions.
const SourceAuction = "bund_auction"

// CloseAt is the close of bidding (ClosingTime) on day in loc, in UTC.
func CloseAt(day time.Time, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 11, 30, 0, 0, loc).UTC()
}

// ClosingTime is the end of the bidding period, Europe/Berlin (LOOKUP:
// "from 8:00 a.m. to 11:30 a.m. CET").
const ClosingTime = "11:30"

// Column keys: the header of a column with everything but letters dropped,
// lower case ("Bid-to-" "Cover-" "Ratio5" → bidtocoverratio).
const (
	colNo        = "no"
	colDate      = "date"
	colISIN      = "isin"
	colBond      = "bond"
	colCoupon    = "coupon"
	colMaturity  = "maturity"
	colSegment   = "maturitysegment"
	colVolume    = "issuancevolumem"
	colType      = "type"
	colProcess   = "issuanceprocess"
	colBids      = "bidsm"
	colAllotted  = "allottedvolumesm"
	colYield     = "averageyield"
	colRetention = "retentionquotem"
	colBTC       = "bidtocoverratio"
)

var required = []string{colDate, colISIN, colBond, colSegment, colVolume, colType, colProcess, colBids, colAllotted, colYield, colBTC}

// Issue processes: auctions are Auc and M-A; OH (taps into own holdings)
// and Syn (syndicates) have no bids and are skipped.
const (
	ProcessAuction    = "Auc"
	ProcessMultiISIN  = "M-A"
	processOwnHolding = "OH"
	processSyndicate  = "Syn"
)

// Client downloads the workbooks.
type Client struct {
	http *httpx.Client
	log  *slog.Logger
}

// New returns a client.
func New(h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, log: log}
}

// Results downloads and parses a results workbook.
func (c *Client) Results(ctx context.Context, url string, today time.Time) ([]model.Auction, error) {
	data, err := c.http.Get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("finanzagentur %s: %w", url, err)
	}
	as, err := ParseResults(data, today)
	if err != nil {
		return nil, fmt.Errorf("finanzagentur %s: %w", url, err)
	}
	return as, nil
}

// Get downloads a file (the auction calendar page and plan).
func (c *Client) Get(ctx context.Context, url string) ([]byte, error) {
	data, err := c.http.Get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("finanzagentur %s: %w", url, err)
	}
	return data, nil
}

// ParseResults reads the auctions of a results workbook (first sheet).
// today bounds the dates (a date after it is an error).
func ParseResults(data []byte, today time.Time) ([]model.Auction, error) {
	wb, err := xlsx.Open(data)
	if err != nil {
		return nil, err
	}
	rows, err := wb.Rows("")
	if err != nil {
		return nil, err
	}
	cols, first, err := header(rows)
	if err != nil {
		return nil, err
	}
	cell := func(r []string, key string) string {
		i, ok := cols[key]
		if !ok || i >= len(r) {
			return ""
		}
		return strings.TrimSpace(r[i])
	}
	var out []model.Auction
	ended := false
	for n := first; n < len(rows) && !ended; n++ {
		r := rows[n]
		if no := cell(r, colNo); strings.HasPrefix(no, "Sum") {
			ended = true
			continue
		}
		if cell(r, colISIN) == "" {
			if d := cell(r, colDate); d != "" {
				if _, err := xlsx.Serial(d); err != nil {
					ended = true // the notes under the table ("Sources: …")
				}
			}
			continue // blank rows
		}
		process := cell(r, colProcess)
		if process == processOwnHolding || process == processSyndicate {
			continue
		}
		a, err := auction(r, cell, today)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", n+1, err)
		}
		out = append(out, a)
	}
	if !ended {
		return nil, fmt.Errorf("no %q row or notes after the auctions: file truncated or changed", "Sum:")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no auctions in the file")
	}
	return out, nil
}

// header finds the header block (from the row holding "ISIN" to the row
// before the first dated row), joins it per column and returns the column
// of each key and the index of the first data row.
func header(rows [][]string) (map[string]int, int, error) {
	top := -1
	for i, r := range rows {
		for _, v := range r {
			if strings.TrimSpace(v) == "ISIN" {
				top = i
				break
			}
		}
		if top >= 0 {
			break
		}
	}
	if top < 0 {
		return nil, 0, fmt.Errorf("no header row with %q", "ISIN")
	}
	first := -1
	for i := top + 1; i < len(rows) && i < top+10; i++ {
		if len(rows[i]) > 1 {
			if _, err := xlsx.Serial(rows[i][1]); err == nil {
				first = i
				break
			}
		}
	}
	if first < 0 {
		return nil, 0, fmt.Errorf("no dated row under the header")
	}
	joined := map[int]string{}
	for i := top; i < first; i++ {
		for j, v := range rows[i] {
			joined[j] += v
		}
	}
	cols := map[string]int{}
	for j, h := range joined {
		k := key(h)
		if k == "" {
			continue
		}
		if prev, dup := cols[k]; dup {
			return nil, 0, fmt.Errorf("header %q in columns %d and %d", k, prev+1, j+1)
		}
		cols[k] = j
	}
	var missing []string
	for _, k := range required {
		if _, ok := cols[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, 0, fmt.Errorf("columns not found: %s", strings.Join(missing, ", "))
	}
	return cols, first, nil
}

// key keeps the letters of a header, lower case.
func key(h string) string {
	var b strings.Builder
	for _, r := range h {
		if r < unicode.MaxASCII && unicode.IsLetter(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func auction(r []string, cell func([]string, string) string, today time.Time) (model.Auction, error) {
	day, err := xlsx.Serial(cell(r, colDate))
	if err != nil {
		return model.Auction{}, err
	}
	if day.Year() < 1999 || day.After(today.AddDate(0, 0, 1)) {
		return model.Auction{}, fmt.Errorf("date %s out of range", day.Format(time.DateOnly))
	}
	isin := cell(r, colISIN)
	if len(isin) != 12 || !strings.HasPrefix(isin, "DE") {
		return model.Auction{}, fmt.Errorf("ISIN %q", isin)
	}
	bond, segment := cell(r, colBond), cell(r, colSegment)
	if bond == "" || segment == "" {
		return model.Auction{}, fmt.Errorf("%s: no bond type or maturity segment", isin)
	}
	process := cell(r, colProcess)
	if process != ProcessAuction && process != ProcessMultiISIN {
		return model.Auction{}, fmt.Errorf("%s: issuance process %q", isin, process)
	}
	typ := cell(r, colType)
	if typ != "N" && typ != "R" {
		return model.Auction{}, fmt.Errorf("%s: type %q (want N or R)", isin, typ)
	}
	a := model.Auction{
		Source: Source, AuctionDate: day, SecurityType: bond, Kind: bond,
		Term: segment, Tenor: strings.ReplaceAll(segment, " ", ""),
		CUSIP: isin, Reopening: typ == "R", ClosingTime: ClosingTime, Process: process,
	}
	num := func(k string, lo, hi, scale float64) (model.Num, error) {
		s := cell(r, k)
		if s == "" {
			return model.Num{}, nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v < lo || v > hi {
			return model.Num{}, fmt.Errorf("%s: %s = %q out of range [%g, %g]", isin, k, s, lo, hi)
		}
		return model.Num{V: round(v*scale, 6), OK: true}, nil
	}
	const million = 1e6
	for _, f := range []struct {
		key        string
		dst        *model.Num
		lo, hi, sc float64
		need       bool
	}{
		{colVolume, &a.OfferingAmt, 0, 100_000, million, true},
		{colBids, &a.BidsAmt, 0, 1_000_000, million, true},
		{colAllotted, &a.TotalAccepted, 0, 100_000, million, true},
		{colRetention, &a.Retention, 0, 100_000, million, false},
		{colYield, &a.AvgYield, -5, 20, 1, true},
		{colBTC, &a.BidToCover, 0, 50, 1, true},
		{colCoupon, &a.Coupon, 0, 0.2, 100, false},
	} {
		v, err := num(f.key, f.lo, f.hi, f.sc)
		if err != nil {
			return model.Auction{}, err
		}
		if f.need && !v.OK {
			return model.Auction{}, fmt.Errorf("%s: no %s", isin, f.key)
		}
		*f.dst = v
	}
	if m := cell(r, colMaturity); m != "" {
		if a.MaturityDate, err = xlsx.Serial(m); err != nil {
			return model.Auction{}, fmt.Errorf("%s: maturity: %w", isin, err)
		}
	}
	return a, nil
}

func round(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}
