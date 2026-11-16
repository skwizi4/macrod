// Package edgar reads SEC EDGAR (LOOKUP E1–E5, 2026-10-25): the submissions
// and XBRL company facts JSON APIs on data.sec.gov, and the 8-K earnings
// press release (Ex-99.1) from the filing archive.
//
// All numbers come from company facts. 10-K and 10-Q documents are never
// downloaded: a new 10-Q/10-K in submissions only means the facts must be
// read again. The press release is read for its text alone (Outlook and
// management quotes), never for numbers.
package edgar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (documents.source, fetch_state prefix).
const Source = "edgar"

// ItemEarnings is the 8-K item "Results of Operations and Financial
// Condition". Not every 2.02 filing is an earnings release: Tesla files its
// quarterly deliveries under 2.02 too (LOOKUP E2).
const ItemEarnings = "2.02"

// Client reads EDGAR. The httpx client must carry the configured User-Agent
// (SEC answers 403 without a contact) and space requests within the SEC
// limit of 10 per second.
type Client struct {
	http        *httpx.Client
	dataURL     string // https://data.sec.gov
	archivesURL string // https://www.sec.gov/Archives/edgar/data
	log         *slog.Logger
}

// New returns a client.
func New(dataURL, archivesURL string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		http:        h,
		dataURL:     strings.TrimRight(dataURL, "/"),
		archivesURL: strings.TrimRight(archivesURL, "/"),
		log:         log,
	}
}

// Interval is the request spacing for perSecond requests a second.
func Interval(perSecond int) time.Duration {
	return time.Second / time.Duration(perSecond)
}

// Filing is one entry of submissions filings.recent.
type Filing struct {
	Accession       string    // 0001193125-26-323632
	Form            string    // 8-K, 10-Q, 10-K, 4, ...
	FilingDate      time.Time // date only
	ReportDate      time.Time // period end for 10-Q/10-K, event date for 8-K; zero if absent
	AcceptedAt      time.Time // UTC
	Items           []string  // 8-K items: 2.02, 9.01, ...
	PrimaryDocument string
}

// HasItem reports whether the filing lists the 8-K item.
func (f Filing) HasItem(item string) bool { return slices.Contains(f.Items, item) }

// Submissions is the part of the submissions JSON the collector uses.
type Submissions struct {
	CIK           string // 10 digits
	Name          string
	Tickers       []string
	FiscalYearEnd string   // MMDD, e.g. 0630
	Filings       []Filing // newest first, as SEC lists them
}

// Submissions returns the company's recent filings (at least a year, up to
// ~1000 filings; older archives in filings.files are not read).
func (c *Client) Submissions(ctx context.Context, cik string) (Submissions, error) {
	body, err := c.get(ctx, fmt.Sprintf("%s/submissions/CIK%s.json", c.dataURL, cik))
	if err != nil {
		return Submissions{}, fmt.Errorf("edgar submissions %s: %w", cik, err)
	}
	s, err := ParseSubmissions(body)
	if err != nil {
		return Submissions{}, fmt.Errorf("edgar submissions %s: %w", cik, err)
	}
	return s, nil
}

// ParseSubmissions decodes a submissions response. filings.recent is
// columnar: one array per field, aligned by index.
func ParseSubmissions(body []byte) (Submissions, error) {
	var raw struct {
		CIK           string   `json:"cik"`
		Name          string   `json:"name"`
		Tickers       []string `json:"tickers"`
		FiscalYearEnd string   `json:"fiscalYearEnd"`
		Filings       struct {
			Recent struct {
				Accession  []string `json:"accessionNumber"`
				Form       []string `json:"form"`
				FilingDate []string `json:"filingDate"`
				ReportDate []string `json:"reportDate"`
				Accepted   []string `json:"acceptanceDateTime"`
				Items      []string `json:"items"`
				Primary    []string `json:"primaryDocument"`
			} `json:"recent"`
		} `json:"filings"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Submissions{}, fmt.Errorf("decode: %w", err)
	}
	r := raw.Filings.Recent
	n := len(r.Accession)
	for name, l := range map[string]int{"form": len(r.Form), "filingDate": len(r.FilingDate), "reportDate": len(r.ReportDate),
		"acceptanceDateTime": len(r.Accepted), "items": len(r.Items), "primaryDocument": len(r.Primary)} {
		if l != n {
			return Submissions{}, fmt.Errorf("filings.recent.%s has %d entries, accessionNumber %d", name, l, n)
		}
	}
	s := Submissions{CIK: fmt.Sprintf("%010s", raw.CIK), Name: raw.Name, Tickers: raw.Tickers, FiscalYearEnd: raw.FiscalYearEnd}
	for i := range n {
		f := Filing{Accession: r.Accession[i], Form: r.Form[i], PrimaryDocument: r.Primary[i]}
		var err error
		if f.FilingDate, err = time.Parse(time.DateOnly, r.FilingDate[i]); err != nil {
			return Submissions{}, fmt.Errorf("filing %s filingDate %q: %w", f.Accession, r.FilingDate[i], err)
		}
		if r.ReportDate[i] != "" {
			if f.ReportDate, err = time.Parse(time.DateOnly, r.ReportDate[i]); err != nil {
				return Submissions{}, fmt.Errorf("filing %s reportDate %q: %w", f.Accession, r.ReportDate[i], err)
			}
		}
		// acceptanceDateTime is UTC despite older docs (checked in summer
		// and winter, LOOKUP E2).
		if f.AcceptedAt, err = time.Parse(time.RFC3339, r.Accepted[i]); err != nil {
			return Submissions{}, fmt.Errorf("filing %s acceptanceDateTime %q: %w", f.Accession, r.Accepted[i], err)
		}
		f.AcceptedAt = f.AcceptedAt.UTC()
		if r.Items[i] != "" {
			f.Items = strings.Split(r.Items[i], ",")
		}
		s.Filings = append(s.Filings, f)
	}
	return s, nil
}

// TaxonomyUSGAAP is the taxonomy of US filers' company facts.
const TaxonomyUSGAAP = "us-gaap"

// Facts returns the us-gaap facts of the concepts; Ticker is left empty.
func (c *Client) Facts(ctx context.Context, cik string, concepts []string) ([]model.CompanyFact, error) {
	return c.FactsIn(ctx, cik, TaxonomyUSGAAP, concepts)
}

// FactsIn returns the facts of the concepts in a taxonomy: ifrs-full for a
// foreign private issuer filing 20-F (SAP, phase 11).
func (c *Client) FactsIn(ctx context.Context, cik, taxonomy string, concepts []string) ([]model.CompanyFact, error) {
	body, err := c.get(ctx, fmt.Sprintf("%s/api/xbrl/companyfacts/CIK%s.json", c.dataURL, cik))
	if err != nil {
		return nil, fmt.Errorf("edgar companyfacts %s: %w", cik, err)
	}
	facts, err := ParseCompanyFactsIn(body, taxonomy, concepts)
	if err != nil {
		return nil, fmt.Errorf("edgar companyfacts %s: %w", cik, err)
	}
	return facts, nil
}

// ParseCompanyFacts decodes the us-gaap facts of the concepts only.
func ParseCompanyFacts(body []byte, concepts []string) ([]model.CompanyFact, error) {
	return ParseCompanyFactsIn(body, TaxonomyUSGAAP, concepts)
}

// ParseCompanyFactsIn decodes the facts of the concepts in the taxonomy
// only; other concepts (hundreds per company) stay undecoded raw JSON. A
// concept the company never used is simply absent.
func ParseCompanyFactsIn(body []byte, taxonomy string, concepts []string) ([]model.CompanyFact, error) {
	var raw struct {
		Facts map[string]map[string]json.RawMessage `json:"facts"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	gaap := raw.Facts[taxonomy]
	var out []model.CompanyFact
	for _, concept := range concepts {
		msg, ok := gaap[concept]
		if !ok {
			continue
		}
		var cf struct {
			Units map[string][]struct {
				Start string  `json:"start"`
				End   string  `json:"end"`
				Val   float64 `json:"val"`
				Accn  string  `json:"accn"`
				FY    int     `json:"fy"`
				FP    string  `json:"fp"`
				Form  string  `json:"form"`
				Filed string  `json:"filed"`
				Frame string  `json:"frame"`
			} `json:"units"`
		}
		if err := json.Unmarshal(msg, &cf); err != nil {
			return nil, fmt.Errorf("decode %s: %w", concept, err)
		}
		for unit, items := range cf.Units {
			for _, it := range items {
				f := model.CompanyFact{Concept: concept, Unit: unit, Value: it.Val, Accession: it.Accn, Form: it.Form,
					FY: it.FY, FP: it.FP, Frame: it.Frame}
				var err error
				if f.End, err = time.Parse(time.DateOnly, it.End); err != nil {
					return nil, fmt.Errorf("%s %s end %q: %w", concept, it.Accn, it.End, err)
				}
				if it.Start != "" {
					if f.Start, err = time.Parse(time.DateOnly, it.Start); err != nil {
						return nil, fmt.Errorf("%s %s start %q: %w", concept, it.Accn, it.Start, err)
					}
				}
				if f.Filed, err = time.Parse(time.DateOnly, it.Filed); err != nil {
					return nil, fmt.Errorf("%s %s filed %q: %w", concept, it.Accn, it.Filed, err)
				}
				out = append(out, f)
			}
		}
	}
	slices.SortFunc(out, func(a, b model.CompanyFact) int {
		return strings.Compare(a.Concept+a.Unit+a.End.Format(time.DateOnly)+a.Start.Format(time.DateOnly)+a.Accession,
			b.Concept+b.Unit+b.End.Format(time.DateOnly)+b.Start.Format(time.DateOnly)+b.Accession)
	})
	return out, nil
}

// get fetches url, turning SEC's 403 for a missing contact into a hint.
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	body, err := c.http.Get(ctx, url)
	var he *httpx.HTTPError
	if errors.As(err, &he) && he.Status == 403 {
		return nil, fmt.Errorf("%w (SEC refuses requests without a contact; check edgar.user_agent)", err)
	}
	return body, err
}
