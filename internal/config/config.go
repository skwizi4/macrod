// Package config loads config.yaml. It covers the core settings, FRED and
// derived series (phase 1), Fed texts, the release calendar and the export
// directory (phase 2), Yahoo, Treasury and CFTC (phase 3), Telegram, RSS
// feeds and classification (phase 4), EDGAR and the earnings calendar
// (phase 5); other sections are ignored until their phase.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"

	"macro-collector/internal/transform"
)

// EnvDataDir overrides data_dir from the config file (a --data-dir flag
// overrides both).
const EnvDataDir = "MACRO_DATA_DIR"

// DefaultDataDir is used when neither the flag, the env nor the config file
// sets data_dir. Relative paths are resolved against the config file's
// directory (D17: no absolute paths in the repo).
const DefaultDataDir = "./data"

// Config is the parsed and validated config.yaml.
type Config struct {
	Timezone      string            `yaml:"timezone"`
	DataDir       string            `yaml:"data_dir"`
	DBPath        string            `yaml:"db_path"`
	HistoryStart  string            `yaml:"history_start"`
	Metrics       Metrics           `yaml:"metrics"`
	FRED          FRED              `yaml:"fred"`
	Derived       []Derived         `yaml:"derived"`
	Export        Export            `yaml:"export"`
	FedTexts      FedTexts          `yaml:"fed_texts"`
	ECBTexts      ECBTexts          `yaml:"ecb_texts"`
	Calendar      Calendar          `yaml:"calendar"`
	Yahoo         Yahoo             `yaml:"yahoo"`
	Treasury      Treasury          `yaml:"treasury"`
	CFTC          CFTC              `yaml:"cftc"`
	Posts         Posts             `yaml:"posts"`
	Telegram      Telegram          `yaml:"telegram"`
	RSSFeeds      []RSSFeed         `yaml:"rss_feeds"`
	Classify      Classify          `yaml:"classify"`
	News          News              `yaml:"news"`
	EDGAR         EDGAR             `yaml:"edgar"`
	Sentiment     Sentiment         `yaml:"sentiment"`
	Dashboard     DashboardConfig   `yaml:"dashboard"`
	Cboe          Cboe              `yaml:"cboe"`
	Prediction    PredictionMarkets `yaml:"prediction_markets"`
	Earnings      EarningsCalendar  `yaml:"earnings_calendar"`
	Git           Git               `yaml:"git"`
	Site          Site              `yaml:"site"`
	Notify        Notify            `yaml:"notify"`
	Webhook       Webhook           `yaml:"webhook"`
	Backup        Backup            `yaml:"backup"`
	ECB           NumericSource     `yaml:"ecb"`
	Bundesbank    NumericSource     `yaml:"bundesbank"`
	Eurostat      NumericSource     `yaml:"eurostat"`
	STOXX         NumericSource     `yaml:"stoxx"`
	ZEW           NumericSource     `yaml:"zew"`
	Manual        Manual            `yaml:"manual"`
	Finanzagentur Finanzagentur     `yaml:"finanzagentur"`
	Issuers       Issuers           `yaml:"issuers"`
	Forecast      Forecast          `yaml:"forecast"`
	// ReleaseCalendars are the calendars of publishers known as dates
	// (Eurostat, ECB statistics, ZEW, EC survey; phase 11).
	ReleaseCalendars []ReleaseCalendar `yaml:"release_calendars"`
	TemplatesDir     string            `yaml:"templates_dir"` // overrides templates by name; relative to the config file
	Regions          []Region          `yaml:"-"`             // section regions, read strictly (regions.go)

	// Resolved values, filled by Load.
	Dir      string         `yaml:"-"` // directory of the config file; relative repo files resolve against it
	Location *time.Location `yaml:"-"`
	History  time.Time      `yaml:"-"`

	tagRegion map[string]string // series tag → region id
}

// Metrics holds z-score window settings.
type Metrics struct {
	ZScoreWindowDailyWeekly      string `yaml:"zscore_window_daily_weekly"`
	ZScoreWindowMonthlyQuarterly string `yaml:"zscore_window_monthly_quarterly"`

	// Parsed windows, filled by Load.
	ZDailyWeekly      transform.Window `yaml:"-"`
	ZMonthlyQuarterly transform.Window `yaml:"-"`
}

// FRED lists the FRED series to collect.
type FRED struct {
	Series []Series `yaml:"series"`
}

// Series is one numeric series from a source.
type Series struct {
	ID          string   `yaml:"id"`
	Agent       string   `yaml:"agent"`
	Label       string   `yaml:"label"`
	Transforms  []string `yaml:"transforms"`
	Hidden      bool     `yaml:"hidden"`
	FallbackFor string   `yaml:"fallback_for"`
	FlagIfGt    *float64 `yaml:"flag_if_gt"`
	Quality     string   `yaml:"quality"` // official (default), proxy, manual
}

// Derived is a series computed by Go from other series.
type Derived struct {
	Name       string   `yaml:"name"`
	Label      string   `yaml:"label"` // name in the files; empty: the built-in one, then Name
	Formula    string   `yaml:"formula"`
	Agent      string   `yaml:"agent"`
	Transforms []string `yaml:"transforms"`
	Unit       string   `yaml:"unit"`
	FlagIfGt   *float64 `yaml:"flag_if_gt"`
	Note       string   `yaml:"note"`
	Quality    string   `yaml:"quality"` // official (default), proxy, manual
	// ShortHistory: z needs transform.MinZShortHistory stored observations
	// (spec 17.5), for spreads built from series with a short history.
	ShortHistory bool `yaml:"short_history"`
}

// Export configures the exported files (§12).
type Export struct {
	ExchangeDir  string `yaml:"exchange_dir"`
	ReportsDir   string `yaml:"reports_dir"`    // agents' saved reports → previous_report.md
	WeeklyCron   string `yaml:"weekly_cron"`    // config timezone
	MaxFileChars int    `yaml:"max_file_chars"` // §12.6
	// PromptsDir holds the agents' prompts <region>/<agent>.md, copied into
	// the agent folders as instructions.md (§21.5); relative to the config
	// file, default DefaultPromptsDir.
	PromptsDir string `yaml:"prompts_dir"`
}

// DefaultPromptsDir is export.prompts_dir when the config does not set it.
const DefaultPromptsDir = "prompts"

// FedTexts configures federalreserve.gov texts and the FOMC calendar.
type FedTexts struct {
	BaseURL      string        `yaml:"base_url"`
	Feeds        []Feed        `yaml:"feeds"`
	FOMCCalendar string        `yaml:"fomc_calendar"`
	BeigeBook    string        `yaml:"beige_book"`
	PollInterval time.Duration `yaml:"poll_interval"`
	Agent        string        `yaml:"agent"`
}

// Feed is one RSS feed.
type Feed struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// Calendar configures the release calendar.
type Calendar struct {
	HorizonDays        int            `yaml:"horizon_days"`
	RefreshCron        string         `yaml:"refresh_cron"`
	ReleaseTimezone    string         `yaml:"release_timezone"`
	DefaultReleaseTime string         `yaml:"default_release_time"`
	ReleaseTimes       map[int]string `yaml:"release_times"`      // FRED release_id → "HH:MM"
	ReleaseImportance  map[int]string `yaml:"release_importance"` // FRED release_id → high | medium | low

	// Resolved values, filled by Load.
	ReleaseLocation *time.Location    `yaml:"-"`
	DefaultTime     ClockTime         `yaml:"-"`
	Times           map[int]ClockTime `yaml:"-"`
}

// ClockTime is a wall-clock time of day.
type ClockTime struct{ Hour, Minute int }

// On returns the instant at the clock time on date's calendar day in loc.
func (c ClockTime) On(date time.Time, loc *time.Location) time.Time {
	y, m, d := date.Date()
	return time.Date(y, m, d, c.Hour, c.Minute, 0, 0, loc)
}

// ReleaseTime is the publication time of a FRED release: its configured time
// or the default.
func (c *Calendar) ReleaseTime(releaseID int) ClockTime {
	if t, ok := c.Times[releaseID]; ok {
		return t
	}
	return c.DefaultTime
}

// Importance levels of calendar events.
var Importances = []string{"high", "medium", "low"}

// Importance is the importance of a FRED release: configured or "low".
func (c *Calendar) Importance(releaseID int) string {
	if v, ok := c.ReleaseImportance[releaseID]; ok {
		return v
	}
	return "low"
}

// Yahoo configures the Yahoo chart source (unofficial and non-critical, D7).
type Yahoo struct {
	Endpoint        string          `yaml:"endpoint"` // chart URL with {symbol}
	Symbols         []YahooSymbol   `yaml:"symbols"`
	FedFundsFutures FedFundsFutures `yaml:"fed_funds_futures"`

	TagRegion map[string]string `yaml:"-"` // series tag → region id, filled by Load
}

// YahooSymbol is one Yahoo series.
type YahooSymbol struct {
	Symbol     string   `yaml:"symbol"`
	Agent      string   `yaml:"agent"`
	Label      string   `yaml:"label"`
	Transforms []string `yaml:"transforms"`
	Quality    string   `yaml:"quality"` // official (default), proxy, manual
	// Interval "1h" reads the history as hourly bars and keeps the last
	// value of each day: the Euronext MTS yields have a single bar at the
	// daily interval (spec 6.1). Empty: daily bars.
	Interval string `yaml:"interval"`
	// ExchangeTZ is the time zone of the symbol's exchange, which decides
	// the day of its last closed bar; empty: calendar.release_timezone.
	ExchangeTZ string `yaml:"exchange_tz"`
	// Units, when set, replace the quote currency in series_meta and decide
	// how the reports write the values (a yield: "Percent per Annum").
	Units string `yaml:"units"`
	// ShortHistory marks a series whose history is too short for the usual
	// z-score rule (spec 17.5): z needs transform.MinZShortHistory stored
	// observations.
	ShortHistory bool `yaml:"short_history"`

	Exchange *time.Location `yaml:"-"` // parsed ExchangeTZ, nil if empty
}

// YahooUnits are the values of a symbol's units key.
var YahooUnits = []string{UnitsRate, UnitsFX, UnitsEURPerMWh}

// Units of Yahoo quotes that are not a price in dollars or an index level.
const (
	UnitsFX        = "Exchange Rate" // four decimals
	UnitsEURPerMWh = "Euro per MWh"
)

// IntervalHourly is the interval value of symbols read by hourly bars.
const IntervalHourly = "1h"

// HourlySymbols are the symbols read by hourly bars.
func (y *Yahoo) HourlySymbols() []string {
	var out []string
	for _, s := range y.Symbols {
		if s.Interval == IntervalHourly {
			out = append(out, s.Symbol)
		}
	}
	return out
}

// Symbol finds a configured symbol.
func (y *Yahoo) Symbol(symbol string) (YahooSymbol, bool) {
	for _, s := range y.Symbols {
		if s.Symbol == symbol {
			return s, true
		}
	}
	return YahooSymbol{}, false
}

// ExchangeOf is the time zone of symbol's exchange: its exchange_tz, or def.
func (y *Yahoo) ExchangeOf(symbol string, def *time.Location) *time.Location {
	if s, ok := y.Symbol(symbol); ok && s.Exchange != nil {
		return s.Exchange
	}
	return def
}

// SymbolsFor returns the symbols whose agent belongs to region.
func (y *Yahoo) SymbolsFor(region string) []YahooSymbol {
	var out []YahooSymbol
	for _, s := range y.Symbols {
		if y.TagRegion[s.Agent] == region {
			out = append(out, s)
		}
	}
	return out
}

// FuturesIn reports whether the fed funds futures belong to region.
func (y *Yahoo) FuturesIn(region string) bool {
	return y.FedFundsFutures.SymbolFormat != "" && y.TagRegion[y.FedFundsFutures.Agent] == region
}

// FedFundsFutures configures the monthly 30-day fed funds contracts (ZQ)
// behind FED_PATH.
type FedFundsFutures struct {
	SymbolFormat string `yaml:"symbol_format"` // with {month} (CME month code) and {yy}
	MonthsAhead  int    `yaml:"months_ahead"`
	Agent        string `yaml:"agent"`
}

// monthCodes are the CME futures month codes, January first.
const monthCodes = "FGHJKMNQUVXZ"

// Symbol is the Yahoo symbol of the contract for month's calendar month,
// e.g. ZQX26.CBT for November 2026.
func (f FedFundsFutures) Symbol(month time.Time) string {
	return strings.NewReplacer(
		"{month}", string(monthCodes[month.Month()-1]),
		"{yy}", fmt.Sprintf("%02d", month.Year()%100),
	).Replace(f.SymbolFormat)
}

// Treasury configures Treasury Fiscal Data and TreasuryDirect.
type Treasury struct {
	FiscalDataURL         string            `yaml:"fiscal_data_url"`
	ReleaseCalendarURL    string            `yaml:"release_calendar_url"`
	TreasuryDirectURL     string            `yaml:"treasurydirect_url"`
	Datasets              []TreasuryDataset `yaml:"datasets"`
	QuarterlyRefundingURL string            `yaml:"quarterly_refunding_url"`
}

// TreasuryDataset is one Treasury dataset to collect.
type TreasuryDataset struct {
	Name          string   `yaml:"name"`
	Agent         string   `yaml:"agent"`
	Enabled       bool     `yaml:"enabled"`
	SecurityTypes []string `yaml:"security_types"` // auctions only
}

// Treasury dataset names.
const (
	DatasetDTS              = "dts_operating_cash"
	DatasetPublicDebt       = "dts_public_debt" // DTS public debt transactions (net issuance)
	DatasetMTS              = "mts"
	DatasetAuctions         = "auctions"
	DatasetUpcomingAuctions = "upcoming_auctions"
	DatasetBuybacks         = "buybacks"
)

var treasuryDatasets = []string{DatasetDTS, DatasetPublicDebt, DatasetMTS, DatasetAuctions, DatasetUpcomingAuctions, DatasetBuybacks}

// auctionSecurityTypes are the security types TreasuryDirect accepts.
var auctionSecurityTypes = []string{"Bill", "Note", "Bond"}

// Dataset returns the named dataset if it is configured and enabled.
func (t *Treasury) Dataset(name string) (TreasuryDataset, bool) {
	for _, d := range t.Datasets {
		if d.Name == name {
			return d, d.Enabled
		}
	}
	return TreasuryDataset{}, false
}

// CFTC configures the Commitments of Traders source (TFF, futures only).
type CFTC struct {
	Enabled   bool           `yaml:"enabled"`
	Endpoint  string         `yaml:"endpoint"`
	CheckCron string         `yaml:"check_cron"` // config timezone
	Contracts []CFTCContract `yaml:"contracts"`

	TagRegion map[string]string `yaml:"-"` // series tag → region id, filled by Load
}

// CFTCContract is one COT contract, identified by its CFTC market code.
type CFTCContract struct {
	Name  string `yaml:"name"`
	Code  string `yaml:"code"`
	Agent string `yaml:"agent"`
}

// ContractsOfTags returns the contracts whose agent is one of tags: the
// contracts an agent shows (USA sentiment, EU markets).
func (c *CFTC) ContractsOfTags(tags []string) []CFTCContract {
	var out []CFTCContract
	for _, k := range c.Contracts {
		if slices.Contains(tags, k.Agent) {
			out = append(out, k)
		}
	}
	return out
}

// ContractsFor returns the contracts whose agent belongs to region.
func (c *CFTC) ContractsFor(region string) []CFTCContract {
	var out []CFTCContract
	for _, k := range c.Contracts {
		if c.TagRegion[k.Agent] == region {
			out = append(out, k)
		}
	}
	return out
}

func parseClock(s string) (ClockTime, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return ClockTime{}, fmt.Errorf("time %q: want HH:MM", s)
	}
	return ClockTime{Hour: t.Hour(), Minute: t.Minute()}, nil
}

// Load reads, expands and validates the config file at path.
func Load(path string) (*Config, error) {
	return LoadWithDataDir(path, "")
}

// LoadWithDataDir is Load with a data_dir override from a command-line flag.
// Priority: dataDir > env MACRO_DATA_DIR > config file > DefaultDataDir.
func LoadWithDataDir(path, dataDir string) (*Config, error) {
	return load(path, filepath.Dir(path), dataDir)
}

// LoadAt is Load for a file that is not in the directory its relative paths
// start from: a preset under presets/ is written for the repo root.
func LoadAt(path, dir string) (*Config, error) {
	return load(path, dir, "")
}

// LoadAtWithDataDir is LoadAt with a data_dir override, which wins over env
// MACRO_DATA_DIR: parallel tests give each config its own directory.
func LoadAtWithDataDir(path, dir, dataDir string) (*Config, error) {
	return load(path, dir, dataDir)
}

func load(path, dir, dataDir string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("config parse %s: %w", path, err)
	}
	if err := c.parseRegions(raw); err != nil {
		return nil, fmt.Errorf("config parse %s: %w", path, err)
	}
	if v := os.Getenv(EnvDataDir); v != "" {
		c.DataDir = v
	}
	if dataDir != "" {
		c.DataDir = dataDir
	}
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir
	}
	if !filepath.IsAbs(c.DataDir) {
		abs, err := filepath.Abs(filepath.Join(dir, c.DataDir))
		if err != nil {
			return nil, fmt.Errorf("config data_dir %s: %w", c.DataDir, err)
		}
		c.DataDir = abs
	}
	c.Dir = dir
	c.DBPath = c.expand(c.DBPath)
	c.Export.ExchangeDir = c.expand(c.Export.ExchangeDir)
	c.Export.ReportsDir = c.expand(c.Export.ReportsDir)
	if c.Export.PromptsDir == "" {
		c.Export.PromptsDir = DefaultPromptsDir
	}
	if !filepath.IsAbs(c.Export.PromptsDir) {
		c.Export.PromptsDir = filepath.Join(dir, c.Export.PromptsDir)
	}
	if c.TemplatesDir != "" && !filepath.IsAbs(c.TemplatesDir) {
		c.TemplatesDir = filepath.Join(dir, c.TemplatesDir)
	}
	regionsErr := c.resolveRegions()
	if err := errors.Join(regionsErr, c.validate(), c.validatePosts(dir), c.validateSentiment(), c.validateDelivery(), c.validateSite(), c.validateRegions(), c.validateDashboard(), c.validateForecast()); err != nil {
		return nil, fmt.Errorf("config validate %s: %w", path, err)
	}
	return &c, nil
}

// LogDir is where log files are written.
func (c *Config) LogDir() string {
	return filepath.Join(c.DataDir, "logs")
}

// expand substitutes ${data_dir}; other ${...} references are left as is.
func (c *Config) expand(s string) string {
	return os.Expand(s, func(key string) string {
		if key == "data_dir" {
			return c.DataDir
		}
		return "${" + key + "}"
	})
}

func (c *Config) validate() error {
	var errs []error
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir is empty"))
	}
	if c.DBPath == "" {
		errs = append(errs, errors.New("db_path is empty"))
	}
	if c.Export.ExchangeDir == "" {
		errs = append(errs, errors.New("export.exchange_dir is empty"))
	}
	if c.Export.ReportsDir == "" {
		errs = append(errs, errors.New("export.reports_dir is empty"))
	}
	if _, err := cron.ParseStandard(c.Export.WeeklyCron); err != nil {
		errs = append(errs, fmt.Errorf("export.weekly_cron %q: %w", c.Export.WeeklyCron, err))
	}
	if c.Export.MaxFileChars < 0 {
		errs = append(errs, errors.New("export.max_file_chars must not be negative"))
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil || c.Timezone == "" {
		errs = append(errs, fmt.Errorf("timezone %q: invalid", c.Timezone))
	}
	c.Location = loc
	c.History, err = time.Parse(time.DateOnly, c.HistoryStart)
	if err != nil {
		errs = append(errs, fmt.Errorf("history_start %q: %w", c.HistoryStart, err))
	}
	if c.Metrics.ZDailyWeekly, err = transform.ParseWindow(c.Metrics.ZScoreWindowDailyWeekly); err != nil {
		errs = append(errs, fmt.Errorf("metrics.zscore_window_daily_weekly: %w", err))
	}
	if c.Metrics.ZMonthlyQuarterly, err = transform.ParseWindow(c.Metrics.ZScoreWindowMonthlyQuarterly); err != nil {
		errs = append(errs, fmt.Errorf("metrics.zscore_window_monthly_quarterly: %w", err))
	}

	seen := make(map[string]bool)
	for i, s := range c.FRED.Series {
		where := fmt.Sprintf("fred.series[%d] %s", i, s.ID)
		if s.ID == "" {
			errs = append(errs, fmt.Errorf("fred.series[%d]: empty id", i))
		}
		if seen[s.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id", where))
		}
		seen[s.ID] = true
		errs = append(errs, c.checkAgent(where, s.Agent), checkTransforms(where, s.Transforms))
	}
	for i, d := range c.Derived {
		where := fmt.Sprintf("derived[%d] %s", i, d.Name)
		if d.Name == "" {
			errs = append(errs, fmt.Errorf("derived[%d]: empty name", i))
		}
		errs = append(errs, c.checkAgent(where, d.Agent), checkTransforms(where, d.Transforms))
	}
	errs = append(errs, c.FedTexts.validate(c), c.ECBTexts.validate(c), c.Calendar.validate(), c.Yahoo.validate(c), c.Treasury.validate(c), c.CFTC.validate(c),
		c.EDGAR.validate(), c.Earnings.validate(), c.validateSources(), c.Finanzagentur.validate(), c.validateFinanzagenturAgent(), c.Issuers.validate(), c.validateSeries(), c.validateDerived(), c.validateReleaseCalendars())
	return errors.Join(errs...)
}

func (y *Yahoo) validate(cfg *Config) error {
	var errs []error
	if !strings.Contains(y.Endpoint, "{symbol}") {
		errs = append(errs, fmt.Errorf("yahoo.endpoint %q: must contain {symbol}", y.Endpoint))
	}
	seen := make(map[string]bool)
	for i := range y.Symbols {
		s := &y.Symbols[i]
		where := fmt.Sprintf("yahoo.symbols[%d] %s", i, s.Symbol)
		if s.Interval != "" && s.Interval != IntervalHourly {
			errs = append(errs, fmt.Errorf("%s: interval %q: want %s or nothing", where, s.Interval, IntervalHourly))
		}
		if s.Units != "" && !slices.Contains(YahooUnits, s.Units) {
			errs = append(errs, fmt.Errorf("%s: units %q: want one of %s", where, s.Units, strings.Join(YahooUnits, ", ")))
		}
		if s.ExchangeTZ != "" {
			loc, err := time.LoadLocation(s.ExchangeTZ)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: exchange_tz %q: invalid", where, s.ExchangeTZ))
			}
			s.Exchange = loc
		}
		if s.Symbol == "" {
			errs = append(errs, fmt.Errorf("yahoo.symbols[%d]: empty symbol", i))
		}
		if seen[s.Symbol] {
			errs = append(errs, fmt.Errorf("%s: duplicate symbol", where))
		}
		seen[s.Symbol] = true
		errs = append(errs, cfg.checkAgent(where, s.Agent), checkTransforms(where, s.Transforms))
	}
	f := y.FedFundsFutures
	if f == (FedFundsFutures{}) {
		return errors.Join(errs...) // optional
	}
	if !strings.Contains(f.SymbolFormat, "{month}") || !strings.Contains(f.SymbolFormat, "{yy}") {
		errs = append(errs, fmt.Errorf("yahoo.fed_funds_futures.symbol_format %q: must contain {month} and {yy}", f.SymbolFormat))
	}
	if f.MonthsAhead <= 0 {
		errs = append(errs, errors.New("yahoo.fed_funds_futures.months_ahead must be positive"))
	}
	errs = append(errs, cfg.checkAgent("yahoo.fed_funds_futures", f.Agent))
	return errors.Join(errs...)
}

func (t *Treasury) validate(cfg *Config) error {
	var errs []error
	for name, u := range map[string]string{
		"fiscal_data_url":         t.FiscalDataURL,
		"release_calendar_url":    t.ReleaseCalendarURL,
		"treasurydirect_url":      t.TreasuryDirectURL,
		"quarterly_refunding_url": t.QuarterlyRefundingURL,
	} {
		if !strings.HasPrefix(u, "https://") {
			errs = append(errs, fmt.Errorf("treasury.%s %q: want an https URL", name, u))
		}
	}
	seen := make(map[string]bool)
	for i, d := range t.Datasets {
		where := fmt.Sprintf("treasury.datasets[%d] %s", i, d.Name)
		if !slices.Contains(treasuryDatasets, d.Name) {
			errs = append(errs, fmt.Errorf("%s: unknown dataset (want one of %s)", where, strings.Join(treasuryDatasets, ", ")))
		}
		if seen[d.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate dataset", where))
		}
		seen[d.Name] = true
		for _, st := range d.SecurityTypes {
			if !slices.Contains(auctionSecurityTypes, st) {
				errs = append(errs, fmt.Errorf("%s: unknown security type %q", where, st))
			}
		}
		errs = append(errs, cfg.checkAgent(where, d.Agent))
	}
	return errors.Join(errs...)
}

func (c *CFTC) validate(cfg *Config) error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if !strings.HasPrefix(c.Endpoint, "https://") {
		errs = append(errs, fmt.Errorf("cftc.endpoint %q: want an https URL", c.Endpoint))
	}
	if _, err := cron.ParseStandard(c.CheckCron); err != nil {
		errs = append(errs, fmt.Errorf("cftc.check_cron %q: %w", c.CheckCron, err))
	}
	seen := make(map[string]bool)
	for i, k := range c.Contracts {
		where := fmt.Sprintf("cftc.contracts[%d] %s", i, k.Name)
		if k.Name == "" || k.Code == "" {
			errs = append(errs, fmt.Errorf("cftc.contracts[%d]: name and code are required", i))
		}
		if seen[k.Code] {
			errs = append(errs, fmt.Errorf("%s: duplicate code %s", where, k.Code))
		}
		seen[k.Code] = true
		errs = append(errs, cfg.checkAgent(where, k.Agent))
	}
	return errors.Join(errs...)
}

func (f *FedTexts) validate(cfg *Config) error {
	var errs []error
	if f.BaseURL == "" {
		errs = append(errs, errors.New("fed_texts.base_url is empty"))
	}
	if len(f.Feeds) == 0 {
		errs = append(errs, errors.New("fed_texts.feeds is empty"))
	}
	seen := make(map[string]bool)
	for i, fd := range f.Feeds {
		if fd.Name == "" || fd.URL == "" {
			errs = append(errs, fmt.Errorf("fed_texts.feeds[%d]: name and url are required", i))
		}
		if seen[fd.Name] {
			errs = append(errs, fmt.Errorf("fed_texts.feeds[%d] %s: duplicate name", i, fd.Name))
		}
		seen[fd.Name] = true
	}
	if f.FOMCCalendar == "" {
		errs = append(errs, errors.New("fed_texts.fomc_calendar is empty"))
	}
	if f.BeigeBook == "" {
		errs = append(errs, errors.New("fed_texts.beige_book is empty"))
	}
	if f.PollInterval <= 0 {
		errs = append(errs, errors.New("fed_texts.poll_interval must be positive"))
	}
	errs = append(errs, cfg.checkAgent("fed_texts", f.Agent))
	return errors.Join(errs...)
}

func (c *Calendar) validate() error {
	var errs []error
	if c.HorizonDays <= 0 {
		errs = append(errs, errors.New("calendar.horizon_days must be positive"))
	}
	if _, err := cron.ParseStandard(c.RefreshCron); err != nil {
		errs = append(errs, fmt.Errorf("calendar.refresh_cron %q: %w", c.RefreshCron, err))
	}
	loc, err := time.LoadLocation(c.ReleaseTimezone)
	if err != nil || c.ReleaseTimezone == "" {
		errs = append(errs, fmt.Errorf("calendar.release_timezone %q: invalid", c.ReleaseTimezone))
	}
	c.ReleaseLocation = loc
	if c.DefaultTime, err = parseClock(c.DefaultReleaseTime); err != nil {
		errs = append(errs, fmt.Errorf("calendar.default_release_time: %w", err))
	}
	c.Times = make(map[int]ClockTime, len(c.ReleaseTimes))
	for id, v := range c.ReleaseTimes {
		t, err := parseClock(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("calendar.release_times[%d]: %w", id, err))
			continue
		}
		c.Times[id] = t
	}
	for id, v := range c.ReleaseImportance {
		if !slices.Contains(Importances, v) {
			errs = append(errs, fmt.Errorf("calendar.release_importance[%d] %q: want one of %s", id, v, strings.Join(Importances, ", ")))
		}
	}
	return errors.Join(errs...)
}

// checkAgent checks a series tag against the tags of the regions' agents.
func (c *Config) checkAgent(where, agent string) error {
	if _, ok := c.tagRegion[agent]; !ok {
		return fmt.Errorf("%s: unknown agent %q (want one of %s)", where, agent, strings.Join(sortedKeys(c.tagRegion), ", "))
	}
	return nil
}

func checkTransforms(where string, ts []string) error {
	var errs []error
	for _, t := range ts {
		if !slices.Contains(transform.Codes, t) {
			errs = append(errs, fmt.Errorf("%s: unknown transform %q", where, t))
		}
	}
	return errors.Join(errs...)
}
