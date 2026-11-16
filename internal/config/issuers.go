package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Issuers configures the companies outside the quarterly EDGAR flow (phase
// 11, ТЗ §18.7): EU Micro. Prices for all of them (Yahoo); annual numbers
// only where they are filed as structured data (SAP: EDGAR company facts of
// its 20-F; Airbus: ESEF on filings.xbrl.org); report dates by hand; press
// releases from the issuer's RSS feed or HTML list, for their text alone.
type Issuers struct {
	Enabled  bool   `yaml:"enabled"`
	PollCron string `yaml:"poll_cron"`
	// RequestInterval spaces requests to filings.xbrl.org and the issuer
	// sites (filings.xbrl.org answers 429 to several requests in a row).
	RequestInterval string `yaml:"request_interval"`
	ESEFURL         string `yaml:"esef_url"`   // https://filings.xbrl.org
	UserAgent       string `yaml:"user_agent"` // for the issuer sites and ESEF
	// PressHistoryDays bounds the first load of a press feed.
	PressHistoryDays int `yaml:"press_history_days"`
	// Paragraphs are RE2 patterns of the paragraphs taken from a release:
	// outlook, guidance, expectations (ТЗ §19.4). Quotes are taken too.
	Paragraphs []string `yaml:"paragraphs"`
	// ReleaseChars caps the paragraphs of one release in the file;
	// FullReleases is how many releases of a company in a week get them, the
	// others are one line.
	ReleaseChars int      `yaml:"release_chars"`
	FullReleases int      `yaml:"full_releases"`
	StaleMonths  int      `yaml:"stale_months"` // annual numbers older than this are flagged
	Companies    []Issuer `yaml:"companies"`

	// Resolved values, filled by Load.
	Interval   time.Duration  `yaml:"-"`
	Paragraph  *regexp.Regexp `yaml:"-"`
	tickerSeen map[string]bool
}

// Issuer is one company of EU Micro.
type Issuer struct {
	Ticker     string           `yaml:"ticker"` // SAP, SIE, ...
	Name       string           `yaml:"name"`
	Symbol     string           `yaml:"symbol"`   // Yahoo: SAP.DE
	Currency   string           `yaml:"currency"` // EUR; default USD
	Importance string           `yaml:"importance"`
	Reports    []IssuerDate     `yaml:"report_dates"`
	Facts      *IssuerFacts     `yaml:"facts"`
	Press      *IssuerPress     `yaml:"press"`
	Quarterly  *IssuerQuarterly `yaml:"quarterly"`
	Calendar   *IssuerCalendar  `yaml:"calendar"` // report dates from the issuer's page (phase 14c)
	Note       string           `yaml:"note"`     // shown with the company in the missing section
}

// IssuerDate is a report date entered by hand from the issuer's financial
// calendar. Session: pre_market (default; German issuers report before
// Xetra opens) or after_close. Estimate marks a date not confirmed by the
// issuer (a secondary calendar).
type IssuerDate struct {
	Date     string `yaml:"date"`
	Session  string `yaml:"session"`
	Period   string `yaml:"period"` // "Q3 2026", "FY 2026"
	Estimate bool   `yaml:"estimate"`
	Source   string `yaml:"source"`

	Day time.Time `yaml:"-"` // resolved by Load
}

// Issuer facts sources.
const (
	FactsEDGAR = "edgar"
	FactsESEF  = "esef"
)

// IssuerFacts is where an issuer's annual numbers come from: the EDGAR
// company facts of a foreign private issuer (taxonomy ifrs-full, form 20-F)
// or the ESEF annual reports of filings.xbrl.org (LEI).
type IssuerFacts struct {
	Source   string         `yaml:"source"`
	CIK      string         `yaml:"cik"`
	Taxonomy string         `yaml:"taxonomy"` // edgar: ifrs-full
	Forms    []string       `yaml:"forms"`    // edgar: 20-F
	LEI      string         `yaml:"lei"`
	Concepts IssuerConcepts `yaml:"concepts"`
}

// IssuerConcepts are the XBRL tags of the annual table, in priority order
// per metric. A tag without a prefix is in the issuer's taxonomy
// (ifrs-full); an extension tag keeps its prefix (airbus:...). Shares are
// the weighted average shares for the market value change; without them it
// is "—".
type IssuerConcepts struct {
	Revenue           []string `yaml:"revenue"`
	OperatingIncome   []string `yaml:"operating_income"`
	NetIncome         []string `yaml:"net_income"`
	EPS               []string `yaml:"eps"`
	OperatingCashFlow []string `yaml:"operating_cash_flow"`
	Capex             []string `yaml:"capex"`
	Shares            []string `yaml:"shares"`
}

// Tags returns every tag, to read from the facts.
func (c IssuerConcepts) Tags() []string {
	var out []string
	for _, t := range slices.Concat(c.Revenue, c.OperatingIncome, c.NetIncome, c.EPS, c.OperatingCashFlow, c.Capex, c.Shares) {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// Quarterly fields: the only numbers read from an issuer's quarterly Excel
// (phase 11, step 18; DECISIONS D15): they fill the quarterly table of EU
// Micro and nothing else.
const (
	QuarterRevenue   = "revenue"
	QuarterOperating = "operating_income"
	QuarterNet       = "net_income"
	QuarterEPS       = "eps"
)

// QuarterFields are the fields in table order.
var QuarterFields = []string{QuarterRevenue, QuarterOperating, QuarterNet, QuarterEPS}

// IssuerQuarterly is the issuer's quarterly Excel (Siemens «Financial
// Results in Excel», Deutsche Telekom «Backup»). Files are the links of
// Page matching one of Links: RE2 with named groups year (2 or 4 digits)
// and q, the fiscal quarter the file reports. A table is found by its
// Anchor (the first text of consecutive rows, any sheet), a row by its
// label (one of Labels, after the Section row if set), a column by its
// period header (quarter and fiscal year, the header texts above it with
// a group label carried right; Group: the first line of the group label).
// FiscalYearEnd is the month the fiscal year ends (Siemens 9).
type IssuerQuarterly struct {
	// Source: page (default) — the files linked from Page are downloaded;
	// file — the user sends the file (bot or `macroctl issuers file`;
	// phase 14c, D29): no Page, Links optional (patterns of the sent file
	// name; without a match the quarter is given with the file).
	Source        string           `yaml:"source"`
	Hint          string           `yaml:"hint"` // file: where the user downloads it, shown in the bot's reminder
	Page          string           `yaml:"page"`
	Links         []string         `yaml:"links"`
	FiscalYearEnd int              `yaml:"fiscal_year_end"`
	MaxFiles      int              `yaml:"max_files"`       // newest files read per run
	RevenueLabel  string           `yaml:"revenue_label"`   // the company's name of the revenue line
	OperatingName string           `yaml:"operating_label"` // the company's own operating profit: EBIT, Profit Industrial Business
	Tables        []QuarterlyTable `yaml:"tables"`

	LinkRes []*regexp.Regexp `yaml:"-"` // resolved by Load
}

// QuarterlyTable is one table of the file. Scale turns its numbers into
// currency units (millions: 1000000); EPS rows are per share (scale 1).
type QuarterlyTable struct {
	Anchor []string                `yaml:"anchor"`
	Group  string                  `yaml:"group"`
	Scale  float64                 `yaml:"scale"`
	Rows   map[string]QuarterlyRow `yaml:"rows"`
}

// QuarterlyRow finds a row by its label.
type QuarterlyRow struct {
	Labels  []string `yaml:"labels"`
	Section string   `yaml:"section"`
}

// QuarterEnd is the last day of the fiscal quarter q of fiscal year fy.
func (q *IssuerQuarterly) QuarterEnd(fy, quarter int) time.Time {
	m := q.FiscalYearEnd - 3*(4-quarter)
	firstNext := time.Date(fy, time.Month(m)+1, 1, 0, 0, 0, 0, time.UTC)
	return firstNext.AddDate(0, 0, -1)
}

// QuarterLabel is "Q3 2026", or "Q3 FY2026" for a fiscal year that is not
// the calendar year.
func (q *IssuerQuarterly) QuarterLabel(fy, quarter int) string {
	if q.FiscalYearEnd == 12 {
		return fmt.Sprintf("Q%d %d", quarter, fy)
	}
	return fmt.Sprintf("Q%d FY%d", quarter, fy)
}

// Quarterly sources.
const (
	QuarterlyPage = "page"
	QuarterlyFile = "file"
)

// FromFile reports whether the user sends the quarterly file.
func (q *IssuerQuarterly) FromFile() bool { return q.Source == QuarterlyFile }

func (q *IssuerQuarterly) validate(where string) error {
	var errs []error
	switch q.Source {
	case "", QuarterlyPage:
		q.Source = QuarterlyPage
		errs = append(errs, checkURL(where+" page", q.Page))
	case QuarterlyFile:
		if q.Page != "" {
			errs = append(errs, fmt.Errorf("%s: page is not used with source %s", where, QuarterlyFile))
		}
	default:
		errs = append(errs, fmt.Errorf("%s: source %q: want %s or %s", where, q.Source, QuarterlyPage, QuarterlyFile))
	}
	if q.FiscalYearEnd == 0 {
		q.FiscalYearEnd = 12
	}
	if q.FiscalYearEnd < 1 || q.FiscalYearEnd > 12 {
		errs = append(errs, fmt.Errorf("%s: fiscal_year_end %d: want a month 1–12", where, q.FiscalYearEnd))
	}
	if q.MaxFiles <= 0 {
		q.MaxFiles = 4
	}
	if q.OperatingName == "" || q.RevenueLabel == "" {
		errs = append(errs, fmt.Errorf("%s: revenue_label and operating_label are required (the company's names, shown in the file)", where))
	}
	q.LinkRes = nil
	for _, l := range q.Links {
		re, err := regexp.Compile(l)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: links %q: %w", where, l, err))
			continue
		}
		names := re.SubexpNames()
		if !slices.Contains(names, "year") || !slices.Contains(names, "q") {
			errs = append(errs, fmt.Errorf("%s: links %q: want named groups year and q", where, l))
		}
		q.LinkRes = append(q.LinkRes, re)
	}
	if len(q.Links) == 0 && !q.FromFile() {
		errs = append(errs, fmt.Errorf("%s: links is empty", where))
	}
	seen := map[string]bool{}
	for i := range q.Tables {
		t := &q.Tables[i]
		tw := fmt.Sprintf("%s tables[%d]", where, i)
		if len(t.Anchor) == 0 || len(t.Rows) == 0 {
			errs = append(errs, fmt.Errorf("%s: anchor and rows are required", tw))
		}
		if t.Scale == 0 {
			t.Scale = 1e6
		}
		for f, r := range t.Rows {
			if !slices.Contains(QuarterFields, f) {
				errs = append(errs, fmt.Errorf("%s: row %q: want one of %s", tw, f, strings.Join(QuarterFields, ", ")))
			}
			if seen[f] {
				errs = append(errs, fmt.Errorf("%s: row %q is in two tables", tw, f))
			}
			seen[f] = true
			if len(r.Labels) == 0 {
				errs = append(errs, fmt.Errorf("%s: row %q: labels are required", tw, f))
			}
		}
	}
	for _, f := range []string{QuarterRevenue, QuarterOperating, QuarterNet} {
		if !seen[f] {
			errs = append(errs, fmt.Errorf("%s: no table has the row %s", where, f))
		}
	}
	return errors.Join(errs...)
}

// Press release feed kinds.
const (
	PressRSS  = "rss"
	PressHTML = "html"
)

// IssuerPress is the issuer's press release list. RSS: the feed items (the
// body from the item, else the page). HTML: the items of the list page
// (Item: CSS selector of an entry; its first link matching LinkPattern, the
// title from the link's title or text, the date from time[datetime]); the
// body is the Body selector of the release page. TitleWords keep only
// releases whose title has one of the words (SAP's feed is mostly
// marketing); Exclude drops titles matching any pattern.
type IssuerPress struct {
	Kind        string   `yaml:"kind"`
	URL         string   `yaml:"url"`
	Item        string   `yaml:"item"`
	LinkPattern string   `yaml:"link_pattern"`
	Body        string   `yaml:"body"`
	TitleWords  []string `yaml:"title_words"`
	Exclude     []string `yaml:"exclude"`

	// Resolved values, filled by Load.
	Link     *regexp.Regexp `yaml:"-"`
	Title    *regexp.Regexp `yaml:"-"`
	Excluded *regexp.Regexp `yaml:"-"`
}

// Sessions of an issuer report date.
const (
	SessionPreMarket  = "pre_market"
	SessionAfterClose = "after_close"
)

var (
	issuerTickerRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,5}$`)
	leiRe          = regexp.MustCompile(`^[A-Z0-9]{20}$`)
	issuerTagRe    = regexp.MustCompile(`^(?:[a-z][a-z0-9-]*:)?[A-Z][A-Za-z0-9]+$`)
)

// Issuer returns the configured issuer with the ticker.
func (s *Issuers) Issuer(ticker string) (Issuer, bool) {
	if !s.Enabled {
		return Issuer{}, false
	}
	for _, c := range s.Companies {
		if c.Ticker == ticker {
			return c, true
		}
	}
	return Issuer{}, false
}

// CurrencyOf is the reporting currency, USD by default.
func (i Issuer) CurrencyOf() string {
	if i.Currency == "" {
		return "USD"
	}
	return i.Currency
}

func (s *Issuers) validate() error {
	if !s.Enabled {
		return nil
	}
	var errs []error
	if _, err := cron.ParseStandard(s.PollCron); err != nil {
		errs = append(errs, fmt.Errorf("issuers.poll_cron %q: %w", s.PollCron, err))
	}
	var err error
	if s.Interval, err = time.ParseDuration(s.RequestInterval); err != nil || s.Interval <= 0 {
		errs = append(errs, fmt.Errorf("issuers.request_interval %q: want a positive duration", s.RequestInterval))
	}
	if s.UserAgent == "" {
		errs = append(errs, errors.New("issuers.user_agent is empty"))
	}
	if s.PressHistoryDays <= 0 || s.ReleaseChars <= 0 || s.FullReleases < 0 || s.StaleMonths <= 0 {
		errs = append(errs, fmt.Errorf("issuers: press_history_days %d, release_chars %d, full_releases %d, stale_months %d: want positive values",
			s.PressHistoryDays, s.ReleaseChars, s.FullReleases, s.StaleMonths))
	}
	if s.Paragraph, err = compileAny("issuers.paragraphs", s.Paragraphs); err != nil {
		errs = append(errs, err)
	} else if s.Paragraph == nil {
		errs = append(errs, errors.New("issuers.paragraphs is empty"))
	}
	esef := false
	s.tickerSeen = map[string]bool{}
	for i := range s.Companies {
		c := &s.Companies[i]
		where := fmt.Sprintf("issuers.companies[%d] %s", i, c.Ticker)
		if !issuerTickerRe.MatchString(c.Ticker) {
			errs = append(errs, fmt.Errorf("%s: ticker must be uppercase letters and digits", where))
		}
		if s.tickerSeen[c.Ticker] {
			errs = append(errs, fmt.Errorf("%s: duplicate ticker", where))
		}
		s.tickerSeen[c.Ticker] = true
		if c.Name == "" || c.Symbol == "" {
			errs = append(errs, fmt.Errorf("%s: name and symbol are required", where))
		}
		if c.Currency != "" && !slices.Contains([]string{"EUR", "USD"}, c.Currency) {
			errs = append(errs, fmt.Errorf("%s: currency %q: want EUR or USD", where, c.Currency))
		}
		if c.Importance == "" {
			c.Importance = "medium"
		}
		if !slices.Contains(Importances, c.Importance) {
			errs = append(errs, fmt.Errorf("%s: importance %q: want one of %s", where, c.Importance, strings.Join(Importances, ", ")))
		}
		for j := range c.Reports {
			d := &c.Reports[j]
			if d.Day, err = time.Parse(time.DateOnly, d.Date); err != nil {
				errs = append(errs, fmt.Errorf("%s: report_dates[%d] %q: want YYYY-MM-DD", where, j, d.Date))
			}
			if d.Session == "" {
				d.Session = SessionPreMarket
			}
			if d.Session != SessionPreMarket && d.Session != SessionAfterClose {
				errs = append(errs, fmt.Errorf("%s: report_dates[%d] session %q: want %s or %s", where, j, d.Session, SessionPreMarket, SessionAfterClose))
			}
			if d.Source == "" {
				errs = append(errs, fmt.Errorf("%s: report_dates[%d]: source is required", where, j))
			}
		}
		slices.SortFunc(c.Reports, func(a, b IssuerDate) int { return a.Day.Compare(b.Day) })
		if f := c.Facts; f != nil {
			errs = append(errs, f.validate(where+" facts"))
			esef = esef || f.Source == FactsESEF
		}
		if p := c.Press; p != nil {
			errs = append(errs, p.validate(where+" press"))
		}
		if q := c.Quarterly; q != nil {
			errs = append(errs, q.validate(where+" quarterly"))
		}
		if cal := c.Calendar; cal != nil {
			errs = append(errs, cal.validate(where+" calendar"))
		}
	}
	if esef {
		errs = append(errs, checkURL("issuers.esef_url", s.ESEFURL))
	}
	return errors.Join(errs...)
}

func (f *IssuerFacts) validate(where string) error {
	var errs []error
	switch f.Source {
	case FactsEDGAR:
		if !cikRe.MatchString(f.CIK) {
			errs = append(errs, fmt.Errorf("%s: cik %q: want 10 digits with leading zeros", where, f.CIK))
		}
		if f.Taxonomy == "" || len(f.Forms) == 0 {
			errs = append(errs, fmt.Errorf("%s: taxonomy and forms are required for %s", where, FactsEDGAR))
		}
	case FactsESEF:
		if !leiRe.MatchString(f.LEI) {
			errs = append(errs, fmt.Errorf("%s: lei %q: want 20 characters", where, f.LEI))
		}
		if f.Taxonomy == "" {
			f.Taxonomy = "ifrs-full"
		}
	default:
		errs = append(errs, fmt.Errorf("%s: source %q: want %s or %s", where, f.Source, FactsEDGAR, FactsESEF))
	}
	c := f.Concepts
	for name, tags := range map[string][]string{"revenue": c.Revenue, "operating_income": c.OperatingIncome, "net_income": c.NetIncome,
		"eps": c.EPS, "operating_cash_flow": c.OperatingCashFlow, "capex": c.Capex, "shares": c.Shares} {
		if len(tags) == 0 && name != "shares" {
			errs = append(errs, fmt.Errorf("%s.concepts.%s is empty", where, name))
		}
		for _, t := range tags {
			if !issuerTagRe.MatchString(t) {
				errs = append(errs, fmt.Errorf("%s.concepts.%s: bad XBRL tag %q", where, name, t))
			}
			if f.Source == FactsEDGAR && strings.Contains(t, ":") {
				errs = append(errs, fmt.Errorf("%s.concepts.%s: %q: EDGAR company facts hold one taxonomy, give the tag without a prefix", where, name, t))
			}
		}
	}
	return errors.Join(errs...)
}

func (p *IssuerPress) validate(where string) error {
	var errs []error
	errs = append(errs, checkURL(where+" url", p.URL))
	var err error
	switch p.Kind {
	case PressRSS:
	case PressHTML:
		if p.Item == "" || p.LinkPattern == "" || p.Body == "" {
			errs = append(errs, fmt.Errorf("%s: item, link_pattern and body are required for %s", where, PressHTML))
		}
		if p.Link, err = regexp.Compile(p.LinkPattern); err != nil {
			errs = append(errs, fmt.Errorf("%s: link_pattern: %w", where, err))
		}
	default:
		errs = append(errs, fmt.Errorf("%s: kind %q: want %s or %s", where, p.Kind, PressRSS, PressHTML))
	}
	words := make([]string, len(p.TitleWords))
	for i, w := range p.TitleWords {
		words[i] = `\b` + regexp.QuoteMeta(w) + `\b`
	}
	if p.Title, err = compileAny(where+" title_words", words); err != nil {
		errs = append(errs, err)
	}
	if p.Excluded, err = compileAny(where+" exclude", p.Exclude); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// compileAny joins RE2 patterns into one case-insensitive alternative; nil
// for none.
func compileAny(where string, patterns []string) (*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	for _, p := range patterns {
		if _, err := regexp.Compile(p); err != nil {
			return nil, fmt.Errorf("%s: pattern %q: %w", where, p, err)
		}
	}
	return regexp.MustCompile(`(?i)(?:` + strings.Join(patterns, `|`) + `)`), nil
}
