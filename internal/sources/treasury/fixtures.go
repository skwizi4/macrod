package treasury

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// Fixture is one recorded response in testdata/treasury. Tests serve these
// files instead of calling Treasury; `macroctl record-fixtures --source
// treasury` refreshes them.
type Fixture struct {
	File  string // file name in testdata/treasury
	Base  string // fiscal | direct | calendar | refunding | page
	Path  string // endpoint (fiscal, direct) or absolute URL (page)
	Query url.Values
}

func fiscalRange(field, from, to string) url.Values {
	return url.Values{
		"filter":       {field + ":gte:" + from + "," + field + ":lte:" + to},
		"sort":         {field},
		"page[number]": {"1"},
		"page[size]":   {strconv.Itoa(defaultPageSize)},
	}
}

func searchRange(typ, from, to string) url.Values {
	return url.Values{"type": {typ}, "startDate": {from}, "endDate": {to}}
}

// Fixtures are pinned to fixed date ranges.
var Fixtures = []Fixture{
	// DTS: both changes of the TGA rows (LOOKUP T2) and a recent week.
	{"dts_2021_10.json", "fiscal", endpointDTS, fiscalRange("record_date", "2021-09-28", "2021-10-05")},
	{"dts_2022_04.json", "fiscal", endpointDTS, fiscalRange("record_date", "2022-04-13", "2022-04-19")},
	{"dts_2026_10.json", "fiscal", endpointDTS, fiscalRange("record_date", "2026-10-15", "2026-10-22")},
	// Public debt transactions, marketable and nonmarketable rows (FIXES_W44 C4).
	{"dts_public_debt_2026_10.json", "fiscal", endpointPublicDebt, fiscalRange("record_date", "2026-10-26", "2026-10-30")},
	// MTS: two statements (September repeats August with revisions).
	{"mts_2026_08_09.json", "fiscal", endpointMTS, fiscalRange("record_date", "2026-08-31", "2026-09-30")},
	{"buybacks_2026_10.json", "fiscal", endpointBuybacks, fiscalRange("operation_date", "2026-10-01", "2026-10-23")},
	{"release_calendar.json", "calendar", "", nil},
	// TA_WS: week W43 coupons and bills, a year of bonds (6-auction
	// averages), an auction from 2000 without bidder shares, upcoming.
	{"ta_search_note_2026_10.json", "direct", "search", searchRange("Note", "2026-10-01", "2026-10-23")},
	{"ta_search_bond_2025_2026.json", "direct", "search", searchRange("Bond", "2025-10-01", "2026-10-23")},
	{"ta_search_bill_2026_10.json", "direct", "search", searchRange("Bill", "2026-10-13", "2026-10-15")},
	{"ta_search_note_2000_02.json", "direct", "search", searchRange("Note", "2000-02-01", "2000-02-29")},
	// TIPS are a TA_WS type of their own (5-year note 2026-10-22, 30-year bond).
	{"ta_search_tips_2026.json", "direct", "search", searchRange("TIPS", "2026-07-01", "2026-11-14")},
	{"ta_upcoming.json", "direct", "upcoming", nil},
	{"refunding.html", "refunding", "", nil},
	{"sb0584.html", "page", "https://home.treasury.gov/news/press-releases/sb0584", nil},
	{"sb0590.html", "page", "https://home.treasury.gov/news/press-releases/sb0590", nil},
}

func (c *Client) fixtureURL(f Fixture) string {
	switch f.Base {
	case "fiscal":
		return c.fiscalURL + "/" + f.Path + "?" + f.Query.Encode()
	case "direct":
		q := url.Values{"format": {"json"}}
		for k, v := range f.Query {
			q[k] = v
		}
		return c.directURL + "/" + f.Path + "?" + q.Encode()
	case "calendar":
		return c.calendarURL
	case "refunding":
		return c.refundingURL
	}
	return f.Path
}

// RecordFixtures fetches every fixture from the live sources into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("treasury record mkdir %s: %w", dir, err)
	}
	for _, f := range Fixtures {
		body, err := c.http.Get(ctx, c.fixtureURL(f))
		if err != nil {
			return fmt.Errorf("treasury record %s: %w", f.File, err)
		}
		path := filepath.Join(dir, f.File)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return fmt.Errorf("treasury record %s: %w", f.File, err)
		}
		c.log.Info("fixture recorded", "file", path, "bytes", len(body))
	}
	return nil
}
