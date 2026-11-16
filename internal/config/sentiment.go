package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"macro-collector/internal/model"
)

// Sentiment configures a news/sentiment.md (FIXES_W44 batch F, D16): the
// top-level block, or a region's own one under its sentiment report
// (phase 11). Absent fields of the instruments default to the USA file.
type Sentiment struct {
	CorrDays        int              `yaml:"corr_days"`        // F2: window of the regime correlation, trading days
	RegimeThreshold float64          `yaml:"regime_threshold"` // F2: |r| above it names the regime
	Ratios          []SentimentRatio `yaml:"ratios"`           // F4: breadth and rotation
	Reaction        Reaction         `yaml:"reaction"`         // F1: price reaction to events
	// DailyMoves are the instruments of "Крупнейшие дневные движения
	// недели" (G4b): daily closes of yahoo.symbols, duration as in Reaction;
	// or a stored series (Series): a rate moves in b.p.
	DailyMoves []ReactionSymbol `yaml:"daily_moves"`

	// Sections of the file, in its order; default: all (SentimentSections).
	Sections []string `yaml:"sections"`
	// Regime is the F2 pair: an equity index (yahoo symbol) and a rate
	// (series name); default ^NDX and DGS10.
	Regime RegimePair `yaml:"regime"`
	// Volatility are the rows of section 4 by series name; default VIX,
	// VIX3M, SKEW, MOVE. VolRatio is the ratio row under them.
	Volatility []string `yaml:"volatility"`
	VolRatio   VolRatio `yaml:"vol_ratio"`
	// PutCall adds the Cboe put/call table to section 4 when cboe is on
	// (default yes).
	PutCall *bool `yaml:"put_call"`
	cboe    bool
}

// PutCallOn reports whether section 4 has the Cboe put/call table.
func (s *Sentiment) PutCallOn() bool { return s.cboe && (s.PutCall == nil || *s.PutCall) }

// Sections of a sentiment file.
const (
	SentReaction    = "reaction"
	SentMoves       = "moves"
	SentRegime      = "regime"
	SentVolatility  = "volatility"
	SentBreadth     = "breadth"
	SentPositioning = "positioning"
	SentPredictions = "predictions"
)

// SentimentSections are all the sections, in the order of the USA file.
var SentimentSections = []string{SentReaction, SentMoves, SentRegime, SentVolatility, SentBreadth, SentPositioning, SentPredictions}

// Has reports whether the file has the section.
func (s *Sentiment) Has(section string) bool { return slices.Contains(s.Sections, section) }

// RegimePair is the equity index and the rate of the F2 correlation.
type RegimePair struct {
	Equity string `yaml:"equity"` // yahoo symbol
	Rates  string `yaml:"rates"`  // series name, changes in b.p.
}

// VolRatio is a ratio of two volatility series; Inversion flags it above 1
// (VIX over VIX3M: near-term fear above the 3-month one).
type VolRatio struct {
	Num       string `yaml:"num"`
	Den       string `yaml:"den"`
	Label     string `yaml:"label"`
	Inversion *bool  `yaml:"inversion"`
}

// Inverts reports whether the ratio has the inversion flag (default yes).
func (v VolRatio) Inverts() bool { return v.Inversion == nil || *v.Inversion }

// Reaction configures F1: one-minute bars around high-importance events.
type Reaction struct {
	Symbols    []ReactionSymbol `yaml:"symbols"`
	Window     time.Duration    `yaml:"window"`      // first reaction: 30m
	KeepDays   int              `yaml:"keep_days"`   // bars older than this are deleted
	FetchDelay time.Duration    `yaml:"fetch_delay"` // after the close the reaction runs to
	CheckEvery time.Duration    `yaml:"check_every"` // daemon: look for events with bars due
	Events     []ReactionEvent  `yaml:"events"`
	Earnings   bool             `yaml:"earnings"` // company reports of the calendar too
	// Close is the market close the full reaction runs to, HH:MM in
	// Timezone; default 16:00 America/New_York.
	Close    string `yaml:"close"`
	Timezone string `yaml:"timezone"`
	Region   string `yaml:"-"` // the region with this sentiment report, filled by Load

	CloseAt  ClockTime      `yaml:"-"` // resolved Close
	Location *time.Location `yaml:"-"` // resolved Timezone
}

// ReactionSymbol is one instrument of the reaction table; Duration turns a
// note future's price change into an approximate yield change (b.p.).
// Series, instead of Symbol, is a stored daily series with no minute bars
// (Bund 10Y): the reaction is its change on the close day, the 30-minute
// move stays empty.
type ReactionSymbol struct {
	Symbol   string  `yaml:"symbol"`
	Series   string  `yaml:"series"`
	Label    string  `yaml:"label"`
	Duration float64 `yaml:"duration"`
}

// ReactionEvent is a calendar event (releases.name) with the words that
// name it in surprise lines of posts (FIXES_W44 E2). Region: the event of
// another region (US CPI for Europe); default the file's region.
type ReactionEvent struct {
	Name    string   `yaml:"name"`
	Label   string   `yaml:"label"`
	Aliases []string `yaml:"aliases"`
	Region  string   `yaml:"region"`
}

// BarSymbols are the instruments read from one-minute bars.
func (r Reaction) BarSymbols() []ReactionSymbol {
	var out []ReactionSymbol
	for _, s := range r.Symbols {
		if s.Series == "" {
			out = append(out, s)
		}
	}
	return out
}

// Cboe configures the daily put/call ratios (FIXES_W44 F3).
type Cboe struct {
	Enabled     bool   `yaml:"enabled"`
	Endpoint    string `yaml:"endpoint"`     // daily file URL with {date}
	PollCron    string `yaml:"poll_cron"`    // config timezone
	HistoryDays int    `yaml:"history_days"` // backfill depth
	Agent       string `yaml:"agent"`
}

// PredictionMarkets configures Kalshi and Polymarket prices (FIXES_W44 F6).
type PredictionMarkets struct {
	Enabled     bool              `yaml:"enabled"`
	Kalshi      string            `yaml:"kalshi_endpoint"`  // trade API v2 base URL
	Gamma       string            `yaml:"polymarket_gamma"` // Polymarket Gamma base URL
	CLOB        string            `yaml:"polymarket_clob"`  // Polymarket CLOB base URL (price history)
	PollCron    string            `yaml:"poll_cron"`        // config timezone
	HistoryDays int               `yaml:"history_days"`     // first load of a market
	Agent       string            `yaml:"agent"`
	Topics      []PredictionTopic `yaml:"topics"`
}

// PredictionTopic is one question asked on both platforms.
type PredictionTopic struct {
	Name             string   `yaml:"name"` // series prefix: "<name>|<market>"
	Label            string   `yaml:"label"`
	KalshiSeries     string   `yaml:"kalshi_series"`
	KalshiEvent      string   `yaml:"kalshi_event"`      // optional: one event of the series
	KalshiNearest    bool     `yaml:"kalshi_nearest"`    // only the event closing first (next FOMC)
	PolymarketSlugs  []string `yaml:"polymarket_slugs"`  // events by slug
	PolymarketSearch string   `yaml:"polymarket_search"` // or: the active event with this title prefix ending first
}

// SentimentRatio is one Yahoo price ratio (RSP/SPY).
type SentimentRatio struct {
	Num   string `yaml:"num"`
	Den   string `yaml:"den"`
	Label string `yaml:"label"`
}

// Defaults of an absent sentiment block: the USA file.
const (
	defaultCorrDays        = 20
	defaultRegimeThreshold = 0.3
	defaultReactionClose   = "16:00"
	defaultReactionZone    = "America/New_York"
)

var (
	defaultRegime     = RegimePair{Equity: "^NDX", Rates: "DGS10"}
	defaultVolatility = []string{"^VIX", "^VIX3M", "^SKEW", "^MOVE"}
	defaultVolRatio   = VolRatio{Num: "^VIX", Den: "^VIX3M", Label: "VIX/VIX3M"}
)

// SentimentOf is the configuration of a sentiment report: its own block,
// else the top-level one.
func (c *Config) SentimentOf(rep *Report) *Sentiment {
	if rep != nil && rep.Sentiment != nil {
		return rep.Sentiment
	}
	return &c.Sentiment
}

// Sentiments are the configurations of the sentiment reports, one per
// report (the top-level block once, for the first report without its own).
func (c *Config) Sentiments() []*Sentiment {
	var out []*Sentiment
	for _, o := range c.Owners(ReportSentiment) {
		if s := c.SentimentOf(o.Report); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func (c *Config) validateSentiment() error {
	var errs []error
	top := false
	for _, o := range c.Owners(ReportSentiment) {
		where := fmt.Sprintf("regions.%s.%s.sentiment", o.Region.ID, o.Agent.ID)
		s := o.Report.Sentiment
		if s == nil {
			if top {
				errs = append(errs, fmt.Errorf("%s: a second sentiment report needs its own sentiment block", where))
				continue
			}
			top, s, where = true, &c.Sentiment, "sentiment"
		}
		s.Reaction.Region = o.Region.ID
		errs = append(errs, c.checkSentiment(where, s, true))
	}
	if !top {
		errs = append(errs, c.checkSentiment("sentiment", &c.Sentiment, false))
	}
	errs = append(errs, c.checkSentimentSources())
	return errors.Join(errs...)
}

// checkSentiment fills the defaults of a sentiment block and checks it;
// the instruments only when a report prints them (owned).
func (c *Config) checkSentiment(where string, s *Sentiment, owned bool) error {
	s.cboe = c.Cboe.Enabled
	if s.CorrDays == 0 {
		s.CorrDays = defaultCorrDays
	}
	if s.RegimeThreshold == 0 {
		s.RegimeThreshold = defaultRegimeThreshold
	}
	if len(s.Sections) == 0 {
		s.Sections = slices.Clone(SentimentSections)
	}
	if s.Regime == (RegimePair{}) {
		s.Regime = defaultRegime
	}
	if len(s.Volatility) == 0 {
		s.Volatility = slices.Clone(defaultVolatility)
	}
	if s.VolRatio.Num == "" && s.VolRatio.Den == "" {
		s.VolRatio = defaultVolRatio
	}
	r := &s.Reaction
	if r.Close == "" {
		r.Close = defaultReactionClose
	}
	if r.Timezone == "" {
		r.Timezone = defaultReactionZone
	}
	var errs []error
	for _, sec := range s.Sections {
		if !slices.Contains(SentimentSections, sec) {
			errs = append(errs, fmt.Errorf("%s.sections: unknown %q (want %s)", where, sec, strings.Join(SentimentSections, ", ")))
		}
	}
	var err error
	if r.CloseAt, err = parseClock(r.Close); err != nil {
		errs = append(errs, fmt.Errorf("%s.reaction.close: %w", where, err))
	}
	if r.Location, err = time.LoadLocation(r.Timezone); err != nil {
		errs = append(errs, fmt.Errorf("%s.reaction.timezone %q: %w", where, r.Timezone, err))
	}
	known := map[string]bool{}
	for _, y := range c.Yahoo.Symbols {
		known[y.Symbol] = true
	}
	series := func(name string) bool { _, ok := c.ResolveSeries(name); return ok }
	if len(r.Symbols) > 0 {
		if r.Window <= 0 || r.KeepDays <= 0 || r.CheckEvery <= 0 || r.FetchDelay < 0 {
			errs = append(errs, fmt.Errorf("%s.reaction: window, keep_days and check_every must be > 0, fetch_delay ≥ 0", where))
		}
		if len(r.BarSymbols()) == 0 {
			errs = append(errs, fmt.Errorf("%s.reaction.symbols: at least one symbol with minute bars", where))
		}
		for i, e := range r.Events {
			if e.Name == "" || e.Label == "" {
				errs = append(errs, fmt.Errorf("%s.reaction.events[%d]: name and label are required", where, i))
			}
			// A region missing from the config is no error: a preset of one
			// region keeps the events of another, they never occur.
		}
		for i, m := range r.Symbols {
			if (m.Symbol == "") == (m.Series == "") || m.Series != "" && !series(m.Series) {
				errs = append(errs, fmt.Errorf("%s.reaction.symbols[%d]: want symbol or a configured series", where, i))
			}
		}
	}
	if s.CorrDays < 3 {
		errs = append(errs, fmt.Errorf("%s.corr_days %d: want at least 3", where, s.CorrDays))
	}
	if s.RegimeThreshold <= 0 || s.RegimeThreshold >= 1 {
		errs = append(errs, fmt.Errorf("%s.regime_threshold %g: want 0 < x < 1", where, s.RegimeThreshold))
	}
	for i, r := range s.Ratios {
		if !known[r.Num] || !known[r.Den] {
			errs = append(errs, fmt.Errorf("%s.ratios[%d] %s/%s: both must be yahoo.symbols", where, i, r.Num, r.Den))
		}
		if r.Label == "" {
			errs = append(errs, fmt.Errorf("%s.ratios[%d]: empty label", where, i))
		}
	}
	for i, m := range s.DailyMoves {
		okSym := m.Symbol != "" && m.Series == "" && known[m.Symbol]
		okSer := m.Series != "" && m.Symbol == "" && series(m.Series)
		if !(okSym || okSer) || m.Label == "" || m.Duration < 0 {
			errs = append(errs, fmt.Errorf("%s.daily_moves[%d] %s%s: a yahoo.symbols symbol or a configured series with a label, duration ≥ 0", where, i, m.Symbol, m.Series))
		}
	}
	// The instruments are checked only where a file prints them: a config
	// without a sentiment report keeps the USA defaults unused.
	if !owned {
		return errors.Join(errs...)
	}
	if s.Has(SentRegime) && (!known[s.Regime.Equity] || !series(s.Regime.Rates)) {
		errs = append(errs, fmt.Errorf("%s.regime: equity %q must be a yahoo symbol, rates %q a configured series", where, s.Regime.Equity, s.Regime.Rates))
	}
	if s.Has(SentVolatility) {
		for _, v := range append(slices.Clone(s.Volatility), s.VolRatio.Num, s.VolRatio.Den) {
			if !series(v) {
				errs = append(errs, fmt.Errorf("%s.volatility: %q is not a configured series", where, v))
			}
		}
		if s.VolRatio.Label == "" {
			errs = append(errs, fmt.Errorf("%s.vol_ratio: label is required", where))
		}
	}
	return errors.Join(errs...)
}

// checkSentimentSources checks the sources sentiment files read.
func (c *Config) checkSentimentSources() error {
	var errs []error
	if p := c.Prediction; p.Enabled {
		if _, err := cron.ParseStandard(p.PollCron); err != nil {
			errs = append(errs, fmt.Errorf("prediction_markets.poll_cron %q: %w", p.PollCron, err))
		}
		if p.Kalshi == "" || p.Gamma == "" || p.CLOB == "" {
			errs = append(errs, errors.New("prediction_markets: kalshi_endpoint, polymarket_gamma and polymarket_clob are required"))
		}
		if p.HistoryDays <= 0 {
			errs = append(errs, fmt.Errorf("prediction_markets.history_days %d: want > 0", p.HistoryDays))
		}
		if err := c.checkAgent("prediction_markets", p.Agent); err != nil {
			errs = append(errs, err)
		}
		seen := map[string]bool{}
		for i, t := range p.Topics {
			if t.Name == "" || strings.Contains(t.Name, "|") || seen[t.Name] {
				errs = append(errs, fmt.Errorf("prediction_markets.topics[%d] %q: want a unique name without |", i, t.Name))
			}
			seen[t.Name] = true
			if t.KalshiSeries == "" && len(t.PolymarketSlugs) == 0 && t.PolymarketSearch == "" {
				errs = append(errs, fmt.Errorf("prediction_markets.topics[%d] %s: no market", i, t.Name))
			}
		}
	}
	if c.Cboe.Enabled {
		if !strings.Contains(c.Cboe.Endpoint, "{date}") {
			errs = append(errs, fmt.Errorf("cboe.endpoint %q: want {date}", c.Cboe.Endpoint))
		}
		if _, err := cron.ParseStandard(c.Cboe.PollCron); err != nil {
			errs = append(errs, fmt.Errorf("cboe.poll_cron %q: %w", c.Cboe.PollCron, err))
		}
		if c.Cboe.HistoryDays <= 0 {
			errs = append(errs, fmt.Errorf("cboe.history_days %d: want > 0", c.Cboe.HistoryDays))
		}
		if err := c.checkAgent("cboe", c.Cboe.Agent); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Match reports whether a calendar event gets a reaction row, with its
// label and the surprise-line words that name it.
func (r Reaction) Match(e model.ReleaseEvent) (label string, aliases []string, ok bool) {
	if e.Region == r.Region && r.Earnings && e.Related.Ticker != "" {
		return e.Related.Ticker + " отчёт", []string{"$" + e.Related.Ticker}, true
	}
	for _, ev := range r.Events {
		region := ev.Region
		if region == "" {
			region = r.Region
		}
		if ev.Name == e.Name && e.Region == region {
			return ev.Label, ev.Aliases, true
		}
	}
	return "", nil, false
}
