package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// EDGAR configures SEC EDGAR (USA Micro, phase 5). Numbers come only from
// the XBRL company facts JSON; 10-K/10-Q documents are never downloaded.
// The only HTML read is the 8-K press release (Ex-99.1), for its text.
type EDGAR struct {
	Enabled           bool         `yaml:"enabled"`
	UserAgent         string       `yaml:"user_agent"`   // SEC requires a contact in it
	DataURL           string       `yaml:"data_url"`     // https://data.sec.gov
	ArchivesURL       string       `yaml:"archives_url"` // https://www.sec.gov/Archives/edgar/data
	RequestsPerSecond int          `yaml:"requests_per_second"`
	FormTypes         []string     `yaml:"form_types"`
	HistoryStart      string       `yaml:"history_start"` // filings and facts loaded from this date
	Concepts          Concepts     `yaml:"concepts"`      // default XBRL tags
	Companies         []Company    `yaml:"companies"`
	CapexCompanies    []string     `yaml:"capex_companies"`
	Transcripts       Transcripts  `yaml:"transcripts"`
	OneOff            OneOff       `yaml:"one_off"` // ⚠ one-off items (ТЗ §19.1)
	MacroSignals      MacroSignals `yaml:"macro_signals"`
	// Segments by ticker, read from the XBRL instance of each 10-Q/10-K
	// (phase 5d, ТЗ §20, DECISIONS D15).
	Segments map[string]Segments `yaml:"segments"`
	// CapexGuidance is the full-year capex guidance of companies whose
	// guidance is defined as our capex (ТЗ §19.3a): entered by hand from the
	// official call or 8-K, never parsed from text.
	CapexGuidance []CapexGuidance `yaml:"capex_guidance"`
	// GuidanceTickers are searched for capex guidance candidates in new
	// call transcripts and earnings 8-Ks (phase 14b, ТЗ §21.6 п. 3): only
	// companies whose guidance is defined as our capex (§19.3a).
	GuidanceTickers []string `yaml:"guidance_tickers"`

	History time.Time `yaml:"-"` // resolved by Load
}

// Transcripts configures official earnings call transcripts on investor
// relations sites (phase 5b, LOOKUP R1–R3): only companies that give their
// guidance on the call alone and publish the transcript as HTML. Text only:
// no numbers are taken from it.
type Transcripts struct {
	Enabled bool `yaml:"enabled"`
	// RequestInterval spaces requests to the sites (robots.txt Crawl-delay
	// is 10 s); RetryEvery limits tries per report while the transcript is
	// not up; WindowDays after the 8-K the tries stop.
	RequestInterval string             `yaml:"request_interval"`
	RetryEvery      string             `yaml:"retry_every"`
	WindowDays      int                `yaml:"window_days"`
	Companies       []TranscriptSource `yaml:"companies"`

	// Resolved values, filled by Load.
	Interval time.Duration `yaml:"-"`
	Retry    time.Duration `yaml:"-"`
}

// TranscriptSource is where one company publishes its transcripts: an event
// page whose URL follows the fiscal quarter (Page, with {fy} and {q}), or a
// Q4 IR feed (Feed, with {fy}) that lists the event page of the report
// "<Ordinal> Quarter <fy>".
type TranscriptSource struct {
	Ticker string `yaml:"ticker"`
	Page   string `yaml:"page"`
	Feed   string `yaml:"feed"`
}

// Transcript returns the transcript source of the ticker.
func (t *Transcripts) Transcript(ticker string) (TranscriptSource, bool) {
	if !t.Enabled {
		return TranscriptSource{}, false
	}
	for _, s := range t.Companies {
		if s.Ticker == ticker {
			return s, true
		}
	}
	return TranscriptSource{}, false
}

// Concepts are us-gaap XBRL tags per metric, in priority order: for each
// period the first tag that has a fact is used (companies switch tags).
// Tags per company: LOOKUP E3 (2026-10-25) and T1–T5 (2026-10-30).
type Concepts struct {
	Revenue []string `yaml:"revenue"`
	EPS     []string `yaml:"eps"`
	Capex   []string `yaml:"capex"`

	// Phase 5b (ТЗ §18).
	OperatingIncome []string `yaml:"operating_income"`
	NetIncome       []string `yaml:"net_income"`
	GrossProfit     []string `yaml:"gross_profit"`
	// CostOfRevenue gives a computed gross profit (revenue − cost) where the
	// company files none; set only for companies whose cost of revenue is
	// one line (META, GOOGL; decision 2026-10-30).
	CostOfRevenue        []string   `yaml:"cost_of_revenue"`
	DA                   Components `yaml:"da"` // depreciation and amortization, cash flow statement
	OperatingCashFlow    []string   `yaml:"operating_cash_flow"`
	Buybacks             []string   `yaml:"buybacks"`
	Dividends            []string   `yaml:"dividends"`
	Cash                 []string   `yaml:"cash"`
	ShortTermInvestments []string   `yaml:"short_term_investments"`
	// Debt: long-term debt with its current part plus commercial paper,
	// without leases. The first component is required, the others count as
	// 0 on a balance sheet date without them.
	Debt          Components `yaml:"debt"`
	DilutedShares []string   `yaml:"diluted_shares"`

	// Phase 5c (ТЗ §19): other income = pretax income − operating income.
	PretaxIncome []string `yaml:"pretax_income"`
	IncomeTax    []string `yaml:"income_tax"`
	RPO          []string `yaml:"rpo"` // remaining performance obligations, balance date
}

// OneOff configures the ⚠ flag (ТЗ §19.1, rule of 2026-10-31): the
// effective tax rate or the share of other income in pretax income moved by
// more than the p.p. thresholds against a year earlier, and that moves net
// income by more than MaterialityPct percent.
type OneOff struct {
	RateChangePP       float64 `yaml:"rate_change_pp"`
	OtherShareChangePP float64 `yaml:"other_share_change_pp"`
	MaterialityPct     float64 `yaml:"materiality_pct"`
}

// Segments are a company's reported segments (LOOKUP S2): facts on
// us-gaap:StatementBusinessSegmentsAxis for the members, with the revenue
// and profit tags. Gross: the profit is gross profit (TSLA reports no
// segment operating income), its margin is shown as "вал.".
type Segments struct {
	Revenue string          `yaml:"revenue"`
	Profit  string          `yaml:"profit"`
	Gross   bool            `yaml:"gross"`
	Members []SegmentMember `yaml:"members"`
}

// CapexGuidance is one company's full-year capex guidance range, billions
// of dollars.
type CapexGuidance struct {
	Ticker string  `yaml:"ticker"`
	Year   int     `yaml:"year"`
	Low    float64 `yaml:"low"`
	High   float64 `yaml:"high"`
	Source string  `yaml:"source"` // where it was taken from, shown in the export
	// Given is the date of the call or 8-K; an export of an earlier week
	// does not show it (no hindsight).
	Given string `yaml:"given"`

	GivenAt time.Time `yaml:"-"` // resolved by Load
}

// SegmentMember is a segment: the member's local name and its label.
type SegmentMember struct {
	Member string `yaml:"member"` // AmazonWebServicesSegmentMember (no prefix)
	Label  string `yaml:"label"`  // AWS
}

// MacroSignals configures the sentences on macro topics picked from the
// week's press releases and call transcripts (phase 5c, ТЗ §19.4). Patterns
// are RE2 regular expressions, matched case-insensitively. Text only: no
// numbers are taken from the sentences.
type MacroSignals struct {
	Enabled bool          `yaml:"enabled"`
	Topics  []SignalTopic `yaml:"topics"`
	Exclude []string      `yaml:"exclude"` // a sentence matching any is dropped
	// Strong phrases (exceed supply, delayed, tariff...) go first; in the
	// Q&A of a call only sentences with a number or a strong phrase count.
	Strong          []string `yaml:"strong"`
	MinChars        int      `yaml:"min_chars"`
	MaxChars        int      `yaml:"max_chars"`
	PerCompany      int      `yaml:"per_company"`
	PerCompanyTopic int      `yaml:"per_company_topic"`
	Max             int      `yaml:"max"` // per section

	// Resolved values, filled by Load.
	Excluded *regexp.Regexp `yaml:"-"`
	Strongs  *regexp.Regexp `yaml:"-"`
}

// SignalTopic is one macro topic: a sentence belongs to the first topic
// whose patterns match it.
type SignalTopic struct {
	Name     string   `yaml:"name"`
	Patterns []string `yaml:"patterns"`

	Match *regexp.Regexp `yaml:"-"` // resolved by Load
}

// Components are XBRL tag lists summed into one metric (AVGO D&A =
// depreciation + amortization of intangibles; debt of several lines). Each
// component is a priority list like a Concepts metric. In YAML a plain list
// is one component: [A, B]; several are a list of lists: [[A], [B, C]].
type Components [][]string

// UnmarshalYAML accepts a plain tag list or a list of tag lists.
func (c *Components) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: want a list of XBRL tags or a list of such lists", n.Line)
	}
	if len(n.Content) > 0 && n.Content[0].Kind == yaml.SequenceNode {
		var lists [][]string
		if err := n.Decode(&lists); err != nil {
			return err
		}
		*c = lists
		return nil
	}
	var one []string
	if err := n.Decode(&one); err != nil {
		return err
	}
	*c = nil
	if len(one) > 0 {
		*c = Components{one}
	}
	return nil
}

// Tags returns every tag of every component.
func (c Components) Tags() []string { return slices.Concat(c...) }

// Tags returns every configured tag, to read from company facts.
func (c Concepts) Tags() []string {
	var out []string
	for _, m := range c.metrics() {
		for _, t := range m.tags {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	return out
}

type conceptMetric struct {
	name     string
	tags     []string
	optional bool // may be empty in edgar.concepts
}

func (c Concepts) metrics() []conceptMetric {
	return []conceptMetric{
		{"revenue", c.Revenue, false}, {"eps", c.EPS, false}, {"capex", c.Capex, false},
		{"operating_income", c.OperatingIncome, false}, {"net_income", c.NetIncome, false},
		{"gross_profit", c.GrossProfit, false}, {"cost_of_revenue", c.CostOfRevenue, true},
		{"da", c.DA.Tags(), false}, {"operating_cash_flow", c.OperatingCashFlow, false},
		{"buybacks", c.Buybacks, false}, {"dividends", c.Dividends, false},
		{"cash", c.Cash, false}, {"short_term_investments", c.ShortTermInvestments, false},
		{"debt", c.Debt.Tags(), false}, {"diluted_shares", c.DilutedShares, false},
		{"pretax_income", c.PretaxIncome, false}, {"income_tax", c.IncomeTax, false}, {"rpo", c.RPO, false},
	}
}

// NotPaid values: metrics a company does not pay at all (decision
// 2026-10-30), shown as 0 instead of a missing value.
const (
	NotPaidBuybacks  = "buybacks"
	NotPaidDividends = "dividends"
)

// Company is one US company followed by USA Micro.
type Company struct {
	Ticker     string   `yaml:"ticker"`
	CIK        string   `yaml:"cik"`        // 10 digits with leading zeros
	Importance string   `yaml:"importance"` // of its earnings in the calendar; default medium
	Concepts   Concepts `yaml:"concepts"`   // per-metric overrides of edgar.concepts
	// NotPaid lists buybacks/dividends the company does not pay (AMZN,
	// TSLA): 0, not a missing value. A tag missing without it stays "—".
	NotPaid []string `yaml:"not_paid"`

	Resolved Concepts `yaml:"-"` // defaults with overrides applied, filled by Load
}

// Company returns the configured company with the ticker.
func (e *EDGAR) Company(ticker string) (Company, bool) {
	for _, c := range e.Companies {
		if c.Ticker == ticker {
			return c, true
		}
	}
	return Company{}, false
}

// EarningsCalendar configures future earnings dates (Nasdaq API; Yahoo needs
// a crumb, LOOKUP E5). Times are in calendar.release_timezone.
type EarningsCalendar struct {
	Enabled        bool   `yaml:"enabled"`
	Endpoint       string `yaml:"endpoint"`         // with {ticker}
	AfterCloseTime string `yaml:"after_close_time"` // expected 8-K time of an after-close report
	PreMarketTime  string `yaml:"pre_market_time"`
	UnknownTime    string `yaml:"unknown_time"` // date known, time not

	// Resolved values, filled by Load.
	AfterClose ClockTime `yaml:"-"`
	PreMarket  ClockTime `yaml:"-"`
	Unknown    ClockTime `yaml:"-"`
}

// edgarFormTypes are the filings EDGAR collects: earnings 8-Ks and the
// 10-Q/10-K that trigger a company facts refresh.
var edgarFormTypes = []string{"8-K", "10-Q", "10-K"}

var (
	tickerRe  = regexp.MustCompile(`^[A-Z][A-Z.]{0,5}$`)
	cikRe     = regexp.MustCompile(`^\d{10}$`)
	conceptRe = regexp.MustCompile(`^[A-Z][A-Za-z0-9]+$`)
)

func (e *EDGAR) validate() error {
	if !e.Enabled {
		return nil
	}
	var errs []error
	if !strings.Contains(e.UserAgent, "@") || strings.Contains(e.UserAgent, "[[") {
		errs = append(errs, fmt.Errorf("edgar.user_agent %q: SEC requires a contact email in it", e.UserAgent))
	}
	errs = append(errs, checkURL("edgar.data_url", e.DataURL), checkURL("edgar.archives_url", e.ArchivesURL))
	if e.RequestsPerSecond <= 0 || e.RequestsPerSecond > 10 {
		errs = append(errs, fmt.Errorf("edgar.requests_per_second %d: want 1–10 (SEC fair access limit)", e.RequestsPerSecond))
	}
	if len(e.FormTypes) == 0 {
		errs = append(errs, errors.New("edgar.form_types is empty"))
	}
	for _, f := range e.FormTypes {
		if !slices.Contains(edgarFormTypes, f) {
			errs = append(errs, fmt.Errorf("edgar.form_types: unknown form %q (want one of %s)", f, strings.Join(edgarFormTypes, ", ")))
		}
	}
	var err error
	if e.History, err = time.Parse(time.DateOnly, e.HistoryStart); err != nil {
		errs = append(errs, fmt.Errorf("edgar.history_start %q: %w", e.HistoryStart, err))
	}
	errs = append(errs, e.Concepts.check("edgar.concepts", true))

	seen := make(map[string]bool)
	for i := range e.Companies {
		c := &e.Companies[i]
		where := fmt.Sprintf("edgar.companies[%d] %s", i, c.Ticker)
		if !tickerRe.MatchString(c.Ticker) {
			errs = append(errs, fmt.Errorf("%s: ticker must be uppercase letters", where))
		}
		if seen[c.Ticker] {
			errs = append(errs, fmt.Errorf("%s: duplicate ticker", where))
		}
		seen[c.Ticker] = true
		if !cikRe.MatchString(c.CIK) {
			errs = append(errs, fmt.Errorf("%s: cik %q: want 10 digits with leading zeros (sec.gov/files/company_tickers.json)", where, c.CIK))
		}
		if c.Importance == "" {
			c.Importance = "medium"
		}
		if !slices.Contains(Importances, c.Importance) {
			errs = append(errs, fmt.Errorf("%s: importance %q: want one of %s", where, c.Importance, strings.Join(Importances, ", ")))
		}
		errs = append(errs, c.Concepts.check(where+" concepts", false))
		for _, m := range c.NotPaid {
			if m != NotPaidBuybacks && m != NotPaidDividends {
				errs = append(errs, fmt.Errorf("%s: not_paid %q: want %s or %s", where, m, NotPaidBuybacks, NotPaidDividends))
			}
		}
		c.Resolved = e.Concepts.override(c.Concepts)
	}
	for _, t := range e.CapexCompanies {
		if !seen[t] {
			errs = append(errs, fmt.Errorf("edgar.capex_companies: %s is not in edgar.companies", t))
		}
	}
	errs = append(errs, e.Transcripts.validate(seen))
	if o := e.OneOff; o.RateChangePP <= 0 || o.OtherShareChangePP <= 0 || o.MaterialityPct <= 0 {
		errs = append(errs, fmt.Errorf("edgar.one_off %+v: want every threshold > 0", o))
	}
	errs = append(errs, e.MacroSignals.validate())
	for i, g := range e.CapexGuidance {
		where := fmt.Sprintf("edgar.capex_guidance[%d] %s", i, g.Ticker)
		if !slices.Contains(e.CapexCompanies, g.Ticker) {
			errs = append(errs, fmt.Errorf("%s: not in edgar.capex_companies", where))
		}
		var err error
		if e.CapexGuidance[i].GivenAt, err = time.Parse(time.DateOnly, g.Given); err != nil {
			errs = append(errs, fmt.Errorf("%s: given %q: want YYYY-MM-DD", where, g.Given))
		}
		if g.Year < 2000 || g.Low <= 0 || g.High < g.Low || g.Source == "" {
			errs = append(errs, fmt.Errorf("%s: year %d, low %v, high %v, source %q: want a year, 0 < low ≤ high and a source", where, g.Year, g.Low, g.High, g.Source))
		}
	}
	for _, t := range e.GuidanceTickers {
		if !slices.Contains(e.CapexCompanies, t) {
			errs = append(errs, fmt.Errorf("edgar.guidance_tickers: %s is not in edgar.capex_companies", t))
		}
	}
	for t, sg := range e.Segments {
		where := "edgar.segments." + t
		if !seen[t] {
			errs = append(errs, fmt.Errorf("%s: not in edgar.companies", where))
		}
		if !conceptRe.MatchString(sg.Revenue) || !conceptRe.MatchString(sg.Profit) {
			errs = append(errs, fmt.Errorf("%s: revenue %q, profit %q: want us-gaap tags", where, sg.Revenue, sg.Profit))
		}
		if len(sg.Members) == 0 {
			errs = append(errs, fmt.Errorf("%s: no members", where))
		}
		for _, m := range sg.Members {
			if !conceptRe.MatchString(m.Member) || strings.Contains(m.Member, ":") || m.Label == "" {
				errs = append(errs, fmt.Errorf("%s: member %q (label %q): want a local name without prefix and a label", where, m.Member, m.Label))
			}
		}
	}
	return errors.Join(errs...)
}

func (m *MacroSignals) validate() error {
	if !m.Enabled {
		return nil
	}
	var errs []error
	compile := func(where string, patterns []string) *regexp.Regexp {
		if len(patterns) == 0 {
			errs = append(errs, fmt.Errorf("%s: no patterns", where))
			return nil
		}
		for _, p := range patterns {
			if _, err := regexp.Compile(p); err != nil {
				errs = append(errs, fmt.Errorf("%s: pattern %q: %w", where, p, err))
				return nil
			}
		}
		return regexp.MustCompile(`(?i)(?:` + strings.Join(patterns, `|`) + `)`)
	}
	if len(m.Topics) == 0 {
		errs = append(errs, errors.New("edgar.macro_signals.topics is empty"))
	}
	for i := range m.Topics {
		t := &m.Topics[i]
		if t.Name == "" {
			errs = append(errs, fmt.Errorf("edgar.macro_signals.topics[%d]: no name", i))
		}
		t.Match = compile(fmt.Sprintf("edgar.macro_signals.topics[%d] %s", i, t.Name), t.Patterns)
	}
	if len(m.Exclude) > 0 {
		m.Excluded = compile("edgar.macro_signals.exclude", m.Exclude)
	}
	m.Strongs = compile("edgar.macro_signals.strong", m.Strong)
	if m.MinChars < 0 || m.MaxChars <= m.MinChars {
		errs = append(errs, fmt.Errorf("edgar.macro_signals: min_chars %d, max_chars %d: want 0 ≤ min < max", m.MinChars, m.MaxChars))
	}
	if m.PerCompanyTopic <= 0 || m.PerCompany < m.PerCompanyTopic || m.Max <= 0 {
		errs = append(errs, fmt.Errorf("edgar.macro_signals: per_company_topic %d, per_company %d, max %d: want 0 < per_company_topic ≤ per_company, max > 0",
			m.PerCompanyTopic, m.PerCompany, m.Max))
	}
	return errors.Join(errs...)
}

func (t *Transcripts) validate(companies map[string]bool) error {
	if !t.Enabled {
		return nil
	}
	var errs []error
	var err error
	if t.Interval, err = time.ParseDuration(t.RequestInterval); err != nil || t.Interval < 10*time.Second {
		errs = append(errs, fmt.Errorf("edgar.transcripts.request_interval %q: want a duration of at least 10s (robots.txt Crawl-delay)", t.RequestInterval))
	}
	if t.Retry, err = time.ParseDuration(t.RetryEvery); err != nil || t.Retry <= 0 {
		errs = append(errs, fmt.Errorf("edgar.transcripts.retry_every %q: want a positive duration", t.RetryEvery))
	}
	if t.WindowDays <= 0 {
		errs = append(errs, fmt.Errorf("edgar.transcripts.window_days %d: want > 0", t.WindowDays))
	}
	for i, s := range t.Companies {
		where := fmt.Sprintf("edgar.transcripts.companies[%d] %s", i, s.Ticker)
		if !companies[s.Ticker] {
			errs = append(errs, fmt.Errorf("%s: not in edgar.companies", where))
		}
		switch {
		case (s.Page == "") == (s.Feed == ""):
			errs = append(errs, fmt.Errorf("%s: set one of page or feed", where))
		case s.Page != "" && (!strings.Contains(s.Page, "{fy}") || !strings.Contains(s.Page, "{q}")):
			errs = append(errs, fmt.Errorf("%s: page %q must contain {fy} and {q}", where, s.Page))
		case s.Page != "":
			errs = append(errs, checkURL(where+" page", s.Page))
		case !strings.Contains(s.Feed, "{fy}"):
			errs = append(errs, fmt.Errorf("%s: feed %q must contain {fy}", where, s.Feed))
		default:
			errs = append(errs, checkURL(where+" feed", s.Feed))
		}
	}
	return errors.Join(errs...)
}

func (c Concepts) check(where string, required bool) error {
	var errs []error
	for _, m := range c.metrics() {
		if required && !m.optional && len(m.tags) == 0 {
			errs = append(errs, fmt.Errorf("%s.%s is empty", where, m.name))
		}
		for _, t := range m.tags {
			if !conceptRe.MatchString(t) {
				errs = append(errs, fmt.Errorf("%s.%s: bad XBRL tag %q (us-gaap name, e.g. Revenues)", where, m.name, t))
			}
		}
	}
	for name, cs := range map[string]Components{"da": c.DA, "debt": c.Debt} {
		if slices.ContainsFunc(cs, func(tags []string) bool { return len(tags) == 0 }) {
			errs = append(errs, fmt.Errorf("%s.%s: empty component", where, name))
		}
	}
	return errors.Join(errs...)
}

// override returns c with each non-empty metric of o replacing c's.
func (c Concepts) override(o Concepts) Concepts {
	pick := func(def, over []string) []string {
		if len(over) > 0 {
			return over
		}
		return def
	}
	pickC := func(def, over Components) Components {
		if len(over) > 0 {
			return over
		}
		return def
	}
	return Concepts{
		Revenue: pick(c.Revenue, o.Revenue), EPS: pick(c.EPS, o.EPS), Capex: pick(c.Capex, o.Capex),
		OperatingIncome: pick(c.OperatingIncome, o.OperatingIncome), NetIncome: pick(c.NetIncome, o.NetIncome),
		GrossProfit: pick(c.GrossProfit, o.GrossProfit), CostOfRevenue: pick(c.CostOfRevenue, o.CostOfRevenue),
		DA: pickC(c.DA, o.DA), OperatingCashFlow: pick(c.OperatingCashFlow, o.OperatingCashFlow),
		Buybacks: pick(c.Buybacks, o.Buybacks), Dividends: pick(c.Dividends, o.Dividends),
		Cash: pick(c.Cash, o.Cash), ShortTermInvestments: pick(c.ShortTermInvestments, o.ShortTermInvestments),
		Debt: pickC(c.Debt, o.Debt), DilutedShares: pick(c.DilutedShares, o.DilutedShares),
		PretaxIncome: pick(c.PretaxIncome, o.PretaxIncome), IncomeTax: pick(c.IncomeTax, o.IncomeTax), RPO: pick(c.RPO, o.RPO),
	}
}

func (e *EarningsCalendar) validate() error {
	if !e.Enabled {
		return nil
	}
	var errs []error
	if !strings.Contains(e.Endpoint, "{ticker}") {
		errs = append(errs, fmt.Errorf("earnings_calendar.endpoint %q: must contain {ticker}", e.Endpoint))
	} else {
		errs = append(errs, checkURL("earnings_calendar.endpoint", e.Endpoint))
	}
	var err error
	if e.AfterClose, err = parseClock(e.AfterCloseTime); err != nil {
		errs = append(errs, fmt.Errorf("earnings_calendar.after_close_time: %w", err))
	}
	if e.PreMarket, err = parseClock(e.PreMarketTime); err != nil {
		errs = append(errs, fmt.Errorf("earnings_calendar.pre_market_time: %w", err))
	}
	if e.Unknown, err = parseClock(e.UnknownTime); err != nil {
		errs = append(errs, fmt.Errorf("earnings_calendar.unknown_time: %w", err))
	}
	return errors.Join(errs...)
}
