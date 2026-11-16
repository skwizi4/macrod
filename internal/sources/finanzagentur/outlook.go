package finanzagentur

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/xlsx"
)

// Planned is one auction of the issuance outlook (spec 8.1; LOOKUP
// 2026-11-11: Issuance_outlook_<year>_Update_Q<n>_en.xlsx, one header row,
// dates as Excel numbers, the current quarter only).
type Planned struct {
	Day      time.Time // auction day, UTC midnight (Berlin calendar day)
	Security string    // Bubill | Schatz | Bobl | Bund | Green issue
	Term     string    // as planned: "10 years", "15/20/30 years"; "" for a green issue
	New      bool      // new issue (else a reopening)
	Volume   float64   // euros; 0 when the plan states none (green issue)
	ISIN     string    // "" when not fixed yet (multi-ISIN range, green issue)
}

// Name is the calendar name of the auction: "Bund 10 years".
func (p Planned) Name() string {
	return strings.TrimSpace(p.Security + " " + p.Term)
}

// Segment is the maturity segment of the results file for the planned term
// ("10 years" → "10 Y", "12 months" → "12 M"); "" for a range of terms or
// none, whose previous auction is not one segment.
func (p Planned) Segment() string {
	n, unit, ok := strings.Cut(p.Term, " ")
	if !ok {
		return ""
	}
	if _, err := strconv.Atoi(n); err != nil {
		return ""
	}
	switch unit {
	case "year", "years":
		return n + " Y"
	case "month", "months":
		return n + " M"
	}
	return ""
}

// Tenor is the tenor of the results file (auctions.original_term) of the
// planned term: "10Y", "12M"; "" when Segment is.
func (p Planned) Tenor() string { return strings.ReplaceAll(p.Segment(), " ", "") }

// Outlook column keys (letters of the header, lower case).
const (
	outDate     = "date"
	outSecurity = "security"
	outTerm     = "termtomaturityremainingterm"
	outType     = "type"
	outVolume   = "volumeinmn"
	outISIN     = "isin"
)

// ParseOutlook reads the auctions of an issuance outlook workbook.
func ParseOutlook(data []byte) ([]Planned, error) {
	wb, err := xlsx.Open(data)
	if err != nil {
		return nil, err
	}
	rows, err := wb.Rows("")
	if err != nil {
		return nil, err
	}
	top := -1
	cols := map[string]int{}
	for i, r := range rows {
		if slices.ContainsFunc(r, func(v string) bool { return strings.TrimSpace(v) == "ISIN" }) {
			top = i
			for j, h := range r {
				if k := key(h); k != "" {
					cols[k] = j
				}
			}
			break
		}
	}
	if top < 0 {
		return nil, fmt.Errorf("outlook: no header row with %q", "ISIN")
	}
	for _, k := range []string{outDate, outSecurity, outTerm, outType, outVolume, outISIN} {
		if _, ok := cols[k]; !ok {
			return nil, fmt.Errorf("outlook: column %q not found", k)
		}
	}
	cell := func(r []string, k string) string {
		if i := cols[k]; i < len(r) {
			return strings.TrimSpace(r[i])
		}
		return ""
	}
	var out []Planned
	for n := top + 1; n < len(rows); n++ {
		r := rows[n]
		d := cell(r, outDate)
		if d == "" {
			continue
		}
		day, err := xlsx.Serial(d)
		if err != nil {
			break // notes under the table
		}
		p := Planned{Day: day, Security: cell(r, outSecurity), Term: cell(r, outTerm), ISIN: cell(r, outISIN)}
		if p.Security == "" {
			return nil, fmt.Errorf("outlook row %d: no security", n+1)
		}
		// "Reopening", "Reopenings (multi-ISIN)" (Q3 2026), "New issue".
		switch t := cell(r, outType); {
		case strings.HasPrefix(t, "New issue"):
			p.New = true
		case t == "" || strings.HasPrefix(t, "Reopening"):
		default:
			return nil, fmt.Errorf("outlook row %d: type %q", n+1, t)
		}
		if v := cell(r, outVolume); v != "" {
			mn, err := strconv.ParseFloat(v, 64)
			if err != nil || mn <= 0 || mn > 100_000 {
				return nil, fmt.Errorf("outlook row %d: volume %q", n+1, v)
			}
			p.Volume = mn * 1e6
		}
		if p.ISIN != "" && (len(p.ISIN) != 12 || !strings.HasPrefix(p.ISIN, "DE")) {
			return nil, fmt.Errorf("outlook row %d: ISIN %q", n+1, p.ISIN)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("outlook: no auctions")
	}
	return out, nil
}

var outlookLinkRe = regexp.MustCompile(`href="([^"]*Issuance_outlook_(\d{4})_[^"]*?Q([1-4])_en\.xlsx)"`)

// OutlookLinks returns the outlook workbooks the issuance calendar page
// links, newest quarter first, resolved against the page URL. The file name
// changes every quarter; the two newest cover a quarter change.
func OutlookLinks(page []byte, pageURL string) ([]string, error) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("outlook links: %w", err)
	}
	type link struct {
		url  string
		year int
		q    int
	}
	var ls []link
	for _, m := range outlookLinkRe.FindAllSubmatch(page, -1) {
		ref, err := url.Parse(string(m[1]))
		if err != nil {
			continue
		}
		y, _ := strconv.Atoi(string(m[2]))
		q, _ := strconv.Atoi(string(m[3]))
		u := base.ResolveReference(ref).String()
		if !slices.ContainsFunc(ls, func(l link) bool { return l.url == u }) {
			ls = append(ls, link{u, y, q})
		}
	}
	if len(ls) == 0 {
		return nil, fmt.Errorf("outlook links: none on the issuance calendar page (layout changed?)")
	}
	slices.SortFunc(ls, func(a, b link) int { return cmp.Or(cmp.Compare(b.year, a.year), cmp.Compare(b.q, a.q)) })
	out := make([]string, 0, 2)
	for _, l := range ls[:min(2, len(ls))] {
		out = append(out, l.url)
	}
	return out, nil
}

// Outlook reads the issuance calendar page and the newest outlook files it
// links. The newer file is the plan from its first day on; the older one
// adds only the days before it (an update restates the rest of the year,
// with other ISINs).
func (c *Client) Outlook(ctx context.Context, pageURL string) ([]Planned, error) {
	page, err := c.Get(ctx, pageURL)
	if err != nil {
		return nil, err
	}
	links, err := OutlookLinks(page, pageURL)
	if err != nil {
		return nil, err
	}
	var plans [][]Planned // newest first
	for _, u := range links {
		data, err := c.Get(ctx, u)
		if err != nil {
			return nil, err
		}
		ps, err := ParseOutlook(data)
		if err != nil {
			return nil, fmt.Errorf("finanzagentur %s: %w", u, err)
		}
		plans = append(plans, ps)
	}
	return MergeOutlooks(plans...), nil
}

// MergeOutlooks joins plans given newest first: each older plan adds only
// the days before the first day of the newer ones.
func MergeOutlooks(plans ...[]Planned) []Planned {
	var out []Planned
	var start time.Time // first day of the newer plans
	for _, ps := range plans {
		first := time.Time{}
		for _, p := range ps {
			if !start.IsZero() && !p.Day.Before(start) {
				continue
			}
			out = append(out, p)
			if first.IsZero() || p.Day.Before(first) {
				first = p.Day
			}
		}
		if !first.IsZero() && (start.IsZero() || first.Before(start)) {
			start = first
		}
	}
	slices.SortStableFunc(out, func(a, b Planned) int { return a.Day.Compare(b.Day) })
	return out
}
