// Package esef reads ESEF annual reports from filings.xbrl.org (LOOKUP
// 2026-11-11): the filing index of an entity (JSON:API,
// /api/entities/<LEI>/filings) and the xBRL-JSON of a report (json_url).
// Only facts without dimensions over a whole year are taken: the
// consolidated annual totals. Numbers only, no text.
package esef

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (fetch_state prefix).
const Source = "esef"

// Form is company_facts.form of an ESEF fact.
const Form = "ESEF"

// Client reads filings.xbrl.org. The httpx client spaces the requests (the
// site answers 429 to several in a row) and retries a 429.
type Client struct {
	http *httpx.Client
	base string
	log  *slog.Logger
}

// New returns a client of base (https://filings.xbrl.org).
func New(base string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, base: strings.TrimRight(base, "/"), log: log}
}

// Filing is one annual report in the index.
type Filing struct {
	ID        string
	PeriodEnd time.Time
	Added     time.Time // date_added: when the site took it in
	JSONURL   string    // relative to the site
}

// Filings returns the entity's reports, one per period end (the latest
// added), oldest first.
func (c *Client) Filings(ctx context.Context, lei string) ([]Filing, error) {
	body, err := c.http.Get(ctx, fmt.Sprintf("%s/api/entities/%s/filings", c.base, lei))
	if err != nil {
		return nil, fmt.Errorf("esef filings %s: %w", lei, err)
	}
	fs, err := ParseFilings(body)
	if err != nil {
		return nil, fmt.Errorf("esef filings %s: %w", lei, err)
	}
	return fs, nil
}

// ParseFilings decodes the filing index.
func ParseFilings(body []byte) ([]Filing, error) {
	var raw struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				PeriodEnd string `json:"period_end"`
				DateAdded string `json:"date_added"`
				JSONURL   string `json:"json_url"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	byEnd := map[time.Time]Filing{}
	for _, d := range raw.Data {
		a := d.Attributes
		end, err := time.Parse(time.DateOnly, a.PeriodEnd)
		if err != nil {
			return nil, fmt.Errorf("filing %s period_end %q: %w", d.ID, a.PeriodEnd, err)
		}
		if len(a.DateAdded) < 10 {
			return nil, fmt.Errorf("filing %s date_added %q", d.ID, a.DateAdded)
		}
		added, err := time.Parse(time.DateOnly, a.DateAdded[:10])
		if err != nil {
			return nil, fmt.Errorf("filing %s date_added %q: %w", d.ID, a.DateAdded, err)
		}
		if a.JSONURL == "" {
			continue // not converted yet
		}
		f := Filing{ID: d.ID, PeriodEnd: end, Added: added, JSONURL: a.JSONURL}
		if old, ok := byEnd[end]; !ok || f.Added.After(old.Added) {
			byEnd[end] = f
		}
	}
	var out []Filing
	for _, f := range byEnd {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b Filing) int { return a.PeriodEnd.Compare(b.PeriodEnd) })
	return out, nil
}

// Facts reads the report's annual facts of the concepts. taxonomy is the
// prefix of a concept given without one (ifrs-full).
func (c *Client) Facts(ctx context.Context, f Filing, taxonomy string, concepts []string) ([]model.CompanyFact, error) {
	body, err := c.http.Get(ctx, c.base+f.JSONURL)
	if err != nil {
		return nil, fmt.Errorf("esef report %s: %w", f.ID, err)
	}
	facts, err := ParseFacts(body, f, taxonomy, concepts)
	if err != nil {
		return nil, fmt.Errorf("esef report %s: %w", f.ID, err)
	}
	return facts, nil
}

// ParseFacts decodes the xBRL-JSON report: facts of the concepts without
// dimensions other than concept, entity, period and unit, over a period of
// 350–380 days. The period end in xBRL-JSON is exclusive
// (2025-01-01T00:00:00/2026-01-01T00:00:00 is the year 2025). Units:
// iso4217:EUR → EUR, iso4217:EUR/xbrli:shares → EUR/shares, xbrli:shares →
// shares. A fact repeated in several statements is kept once. The stored
// concept is the configured name.
func ParseFacts(body []byte, f Filing, taxonomy string, concepts []string) ([]model.CompanyFact, error) {
	var raw struct {
		Facts map[string]struct {
			Value      json.RawMessage   `json:"value"`
			Dimensions map[string]string `json:"dimensions"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	want := map[string]string{} // qualified name → configured name
	for _, c := range concepts {
		q := c
		if !strings.Contains(c, ":") {
			q = taxonomy + ":" + c
		}
		want[q] = c
	}
	seen := map[string]bool{}
	var out []model.CompanyFact
	for _, id := range sortedKeys(raw.Facts) {
		fact := raw.Facts[id]
		dims := fact.Dimensions
		name, ok := want[dims["concept"]]
		if !ok {
			continue
		}
		if slices.ContainsFunc(sortedKeys(dims), func(k string) bool {
			return k != "concept" && k != "entity" && k != "period" && k != "unit"
		}) {
			continue
		}
		start, end, ok := period(dims["period"])
		if !ok {
			continue
		}
		if days := end.Sub(start).Hours() / 24; days < 350 || days > 380 {
			continue
		}
		// Canonical values are strings ("73420000000.0"); a number is
		// accepted too, null and text are not numbers.
		s := strings.Trim(string(fact.Value), `"`)
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		unit := unitOf(dims["unit"])
		key := name + unit + start.String() + end.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, model.CompanyFact{Concept: name, Unit: unit, Start: start, End: end, Value: v,
			Accession: f.ID, Form: Form, Filed: f.Added, FY: f.PeriodEnd.Year(), FP: "FY"})
	}
	slices.SortFunc(out, func(a, b model.CompanyFact) int {
		return strings.Compare(a.Concept+a.Unit+a.End.Format(time.DateOnly), b.Concept+b.Unit+b.End.Format(time.DateOnly))
	})
	return out, nil
}

// period parses "2025-01-01T00:00:00/2026-01-01T00:00:00" into the first
// and the last day.
func period(p string) (start, end time.Time, ok bool) {
	a, b, found := strings.Cut(p, "/")
	if !found {
		return start, end, false
	}
	var err1, err2 error
	start, err1 = time.Parse("2006-01-02T15:04:05", a)
	end, err2 = time.Parse("2006-01-02T15:04:05", b)
	if err1 != nil || err2 != nil {
		return start, end, false
	}
	return start, end.AddDate(0, 0, -1), true
}

// unitOf maps an xBRL-JSON unit to the company facts style.
func unitOf(u string) string {
	r := strings.NewReplacer("iso4217:", "", "xbrli:", "")
	return r.Replace(u)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
