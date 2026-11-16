package config

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"macro-collector/internal/transform"
)

// Report kinds (ТЗ §21.3): a kind is a Go builder, a default template and a
// JSON schema. A region is any combination of them.
const (
	ReportMonetary = "monetary"
	ReportFiscal   = "fiscal_liquidity"
	ReportBonds    = "fiscal_bonds" // government bonds of a region without the US liquidity data (phase 11)
	ReportMicro    = "micro"
	// ReportIssuers is the micro file of issuers outside the quarterly EDGAR
	// flow (EU Micro, phase 11): prices, report dates, annual numbers.
	ReportIssuers    = "micro_annual"
	ReportNews       = "news"
	ReportSentiment  = "sentiment"
	ReportMarkets    = "markets"
	ReportCalendar   = "calendar"
	ReportKeyMetrics = "key_metrics"
	ReportFOMCDiff   = "fomc_diff"
	ReportECBDiff    = "ecb_diff"
)

// reportTables are the tables each kind takes from the config; every one of
// them must be given (an empty one as []), so a misspelt or forgotten table
// cannot lose its rows silently.
var reportTables = map[string][]string{
	ReportMonetary:   {"growth", "sentiment", "labor", "inflation", "expectations", "fed", "energy"},
	ReportFiscal:     {"liquidity", "funding", "budget", "curve", "derived"},
	ReportBonds:      {"curve", "spreads", "monthly", "debt"},
	ReportMicro:      nil,
	ReportIssuers:    nil,
	ReportNews:       nil,
	ReportSentiment:  nil,
	ReportMarkets:    {"rows"},
	ReportCalendar:   nil,
	ReportKeyMetrics: nil,
	ReportFOMCDiff:   nil,
	ReportECBDiff:    nil,
}

// optionalTables are tables a kind may take but need not: a region that
// has the data adds them (Europe's money and credit, batch 11f), older
// configs stay valid.
var optionalTables = map[string][]string{
	ReportMonetary: {"credit"},
}

// Text sources an agent may read.
const (
	TextsFed      = "fed"
	TextsTreasury = "treasury"
	TextsECB      = "ecb_texts"
)

var textSources = []string{TextsFed, TextsTreasury, TextsECB}

// News topics a classifier tag can vote for (report news, tag_topics).
const (
	TopicFed      = "fed"
	TopicFiscal   = "fiscal"
	TopicPolitics = "politics"
	TopicEnergy   = "energy"
	TopicEarnings = "earnings"
	TopicEurope   = "europe"
)

var newsTopicNames = []string{TopicFed, TopicFiscal, TopicPolitics, TopicEnergy, TopicEarnings, TopicEurope}

// KeyRowFedPath is the id of the key metrics row showing the FOMC path
// priced by fed funds futures; it is not a stored series.
const KeyRowFedPath = "FED_PATH"

// TreasurySeries are the Treasury Fiscal Data series tables can show.
var TreasurySeries = []string{"TGA_DTS", "MTS_RECEIPTS", "MTS_OUTLAYS"}

// Region is one export region (ТЗ §21.3): its id is the folder under
// exchange/ and reports/ and the region of its stored documents and events.
type Region struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`      // shown in messages: США, Европа; default: the id in upper case
	Index       string `yaml:"index"`      // the main equity index
	YahooPollAt string `yaml:"yahoo_poll"` // HH:MM, config timezone: after the region's close
	// Timezone is the region's own zone, the second time of the calendar
	// file and the day of its releases (spec 17.8: USA ET, Europe CET/CEST);
	// empty: calendar.release_timezone. TimezoneLabel names it in the files.
	Timezone      string  `yaml:"timezone"`
	TimezoneLabel string  `yaml:"timezone_label"`
	Agents        []Agent `yaml:"agents"`
	// Dashboard is optional: a region without it has the generic pages.
	Dashboard RegionDashboard `yaml:"dashboard"`

	YahooPoll ClockTime      `yaml:"-"` // filled by Load
	Location  *time.Location `yaml:"-"` // resolved Timezone
}

// TitleOf is the region's name in messages.
func (r *Region) TitleOf() string {
	if r.Title != "" {
		return r.Title
	}
	return strings.ToUpper(r.ID)
}

// RegionTitle is the name of the region with the id (the id in upper case
// for an unknown one).
func (c *Config) RegionTitle(id string) string {
	if r, ok := c.Region(id); ok {
		return r.TitleOf()
	}
	return strings.ToUpper(id)
}

// DefaultTimezoneLabel names calendar.release_timezone (America/New_York).
const DefaultTimezoneLabel = "ET"

// Agent is one consumer of the export: a folder of files.
type Agent struct {
	ID         string   `yaml:"id"` // header agent, reports/<region>/<week>/<id>.md
	Folder     string   `yaml:"folder"`
	SeriesTags []string `yaml:"series_tags"` // values of the agent field of its series
	Texts      []string `yaml:"texts"`       // official text sources
	Companies  []string `yaml:"companies"`   // tickers of edgar.companies
	Channels   []string `yaml:"channels"`    // handles and feed names; empty: every source of the region
	Reports    []Report `yaml:"reports"`
}

// Report is one exported file of an agent.
type Report struct {
	Kind     string              `yaml:"report"`
	File     string              `yaml:"file"`
	Title    string              `yaml:"title"`    // "<title> — <week>" in the file
	Template string              `yaml:"template"` // default: the kind
	Tables   map[string][]string `yaml:"tables"`

	Rows []KeyRow `yaml:"rows"` // key_metrics
	// SentimentRows adds the key rows of the US sentiment file after Rows
	// (key_metrics): VIX/VIX3M, equity put/call, RSP/SPY, P(recession, Kalshi).
	SentimentRows bool                    `yaml:"sentiment_rows"`
	Previous      map[int]ReleasePrevious `yaml:"previous"` // calendar: FRED release id → its series

	FilteredFile      string   `yaml:"filtered_file"`      // news, archive only
	UncategorizedFile string   `yaml:"uncategorized_file"` // news, archive only
	OwnTagPrefix      string   `yaml:"own_tag_prefix"`     // news: tags of this region
	OwnTagPrefixes    []string `yaml:"own_tag_prefixes"`   // news: or several (a shared file)
	// Shared makes the news file common to every region (phase 11, user
	// decision 2026-11-10): it reads the posts of all regions, not only its own.
	Shared         bool              `yaml:"shared"`
	OtherTagPrefix string            `yaml:"other_tag_prefix"` // news: tags of another region
	TagTopics      map[string]string `yaml:"tag_topics"`       // news: classifier tag → topic

	Sentiment *Sentiment `yaml:"sentiment"` // sentiment: the region's own block; nil: the top-level one
	// MaxChars is this file's size limit, over export.max_file_chars (0:
	// that one). ecb_diff of Europe has a higher one than fomc_diff: the
	// ECB statement and Q&A are longer (user decision 2026-11-13).
	MaxChars int `yaml:"max_chars"`
}

// FileLimit is the size limit of the report's file, 0 for none.
func (c *Config) FileLimit(rep *Report) int {
	if rep != nil && rep.MaxChars > 0 {
		return rep.MaxChars
	}
	return c.Export.MaxFileChars
}

// KeyRow is a row of a key metrics file.
type KeyRow struct {
	Label string `yaml:"label"`
	ID    string `yaml:"id"`
	Code  string `yaml:"code"` // shown transform; empty: the level
}

// ReleasePrevious is the series whose last value stands for a release in the
// calendar file, and the transform shown.
type ReleasePrevious struct {
	Series string `yaml:"series"`
	Code   string `yaml:"code"`
	Name   string `yaml:"name"`
}

// OwnPrefixes are the tag prefixes of the news file's own posts.
func (r *Report) OwnPrefixes() []string {
	out := slices.Clone(r.OwnTagPrefixes)
	if r.OwnTagPrefix != "" && !slices.Contains(out, r.OwnTagPrefix) {
		out = append(out, r.OwnTagPrefix)
	}
	return out
}

// TemplateName is the Markdown template of the report.
func (r *Report) TemplateName() string {
	if r.Template != "" {
		return r.Template
	}
	return r.Kind
}

// FolderName is the agent's folder under its region.
func (a *Agent) FolderName() string {
	if a.Folder != "" {
		return a.Folder
	}
	return a.ID
}

// Path is the file of an agent, relative to the exchange directory.
func (r *Region) Path(a *Agent, file string) string {
	return filepath.Join(r.ID, a.FolderName(), file)
}

// PromptPath is the prompt file of an agent: <prompts_dir>/<region>/<agent id>.md
// (§21.5), named by the agent id like its saved reports, not by its folder.
func (c *Config) PromptPath(r *Region, a *Agent) string {
	return filepath.Join(c.Export.PromptsDir, r.ID, a.ID+".md")
}

// Region returns the region with the id.
func (c *Config) Region(id string) (*Region, bool) {
	for i := range c.Regions {
		if c.Regions[i].ID == id {
			return &c.Regions[i], true
		}
	}
	return nil, false
}

// RegionIDs are the ids of the configured regions, in config order.
func (c *Config) RegionIDs() []string {
	out := make([]string, len(c.Regions))
	for i := range c.Regions {
		out[i] = c.Regions[i].ID
	}
	return out
}

// TreasuryRegion is the region of the Treasury documents and events: that of
// the first dataset.
func (c *Config) TreasuryRegion() string {
	if len(c.Treasury.Datasets) == 0 {
		return ""
	}
	return c.tagRegion[c.Treasury.Datasets[0].Agent]
}

// RegionOfTag is the region owning a series tag, "" if none does.
func (c *Config) RegionOfTag(tag string) string { return c.tagRegion[tag] }

// ReportOwner is where a report kind is exported.
type ReportOwner struct {
	Region *Region
	Agent  *Agent
	Report *Report
}

// Owner finds the first report of a kind; there is at most one per region
// for the event-driven kinds, and those belong to a single source.
func (c *Config) Owner(kind string) (ReportOwner, bool) {
	for i := range c.Regions {
		if o, ok := c.Regions[i].Owner(kind); ok {
			return o, true
		}
	}
	return ReportOwner{}, false
}

// Owners are the reports of a kind, the first of each region.
func (c *Config) Owners(kind string) []ReportOwner {
	var out []ReportOwner
	for i := range c.Regions {
		if o, ok := c.Regions[i].Owner(kind); ok {
			out = append(out, o)
		}
	}
	return out
}

// Owner finds the first report of a kind in the region.
func (r *Region) Owner(kind string) (ReportOwner, bool) {
	for i := range r.Agents {
		a := &r.Agents[i]
		for j := range a.Reports {
			if a.Reports[j].Kind == kind {
				return ReportOwner{Region: r, Agent: a, Report: &a.Reports[j]}, true
			}
		}
	}
	return ReportOwner{}, false
}

// RegionOfCompany is the region of the agent listing a ticker, and the tag
// its series carry (the first tag of that agent).
func (c *Config) RegionOfCompany(ticker string) (region, tag string, ok bool) {
	for i := range c.Regions {
		for j := range c.Regions[i].Agents {
			a := &c.Regions[i].Agents[j]
			if slices.Contains(a.Companies, ticker) && len(a.SeriesTags) > 0 {
				return c.Regions[i].ID, a.SeriesTags[0], true
			}
		}
	}
	return "", "", false
}

// parseRegions reads the regions section strictly: a misspelt field must not
// silently drop what it configures. The rest of the file holds drafts of
// later phases and is read leniently.
func (c *Config) parseRegions(raw []byte) error {
	var doc struct {
		Regions yaml.Node `yaml:"regions"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if doc.Regions.IsZero() {
		return errors.New(`section regions is missing: copy the regions block of presets/us-equities.yaml (see README, "Регионы и агенты")`)
	}
	section, err := yaml.Marshal(&doc.Regions)
	if err != nil {
		return fmt.Errorf("regions: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(section))
	dec.KnownFields(true)
	if err := dec.Decode(&c.Regions); err != nil {
		return fmt.Errorf("regions: %w", err)
	}
	return nil
}

var regionID = regexp.MustCompile(`^[a-z0-9_-]+$`)

// resolveRegions fills what the other sections' validation needs: the owner
// of every series tag. It runs before them.
func (c *Config) resolveRegions() error {
	var errs []error
	c.tagRegion = map[string]string{}
	if len(c.Regions) == 0 {
		errs = append(errs, errors.New("regions: at least one region is required"))
	}
	for i := range c.Regions {
		r := &c.Regions[i]
		for j := range r.Agents {
			for _, tag := range r.Agents[j].SeriesTags {
				where := fmt.Sprintf("regions[%d].agents[%d]", i, j)
				if tag == "" {
					errs = append(errs, fmt.Errorf("%s: empty series tag", where))
				} else if owner, dup := c.tagRegion[tag]; dup && owner != r.ID {
					errs = append(errs, fmt.Errorf("%s: series tag %q is already in region %s", where, tag, owner))
				}
				c.tagRegion[tag] = r.ID
			}
		}
	}
	c.Yahoo.TagRegion = c.tagRegion
	c.CFTC.TagRegion = c.tagRegion
	return errors.Join(errs...)
}

// validateRegions checks the regions against the sections they refer to.
func (c *Config) validateRegions() error {
	var errs []error
	series := c.knownSeries()
	regions := map[string]bool{}
	for i := range c.Regions {
		r := &c.Regions[i]
		where := fmt.Sprintf("regions[%d]", i)
		if !regionID.MatchString(r.ID) {
			errs = append(errs, fmt.Errorf("%s: id %q: want lowercase letters, digits, _ or -", where, r.ID))
		}
		if regions[r.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id %q", where, r.ID))
		}
		regions[r.ID] = true
		if r.ID == allRegions {
			errs = append(errs, fmt.Errorf("%s: id %q is reserved", where, r.ID))
		}
		if r.Index != "" && !series[r.Index] {
			errs = append(errs, fmt.Errorf("%s: index %q is not a configured series", where, r.Index))
		}
		var err error
		if r.YahooPoll, err = parseClock(r.YahooPollAt); err != nil {
			errs = append(errs, fmt.Errorf("%s.yahoo_poll: %w", where, err))
		}
		switch {
		case r.Timezone == "":
			r.Location = c.Calendar.ReleaseLocation
			if r.TimezoneLabel == "" {
				r.TimezoneLabel = DefaultTimezoneLabel
			}
		default:
			if r.Location, err = time.LoadLocation(r.Timezone); err != nil {
				errs = append(errs, fmt.Errorf("%s.timezone %q: invalid", where, r.Timezone))
			}
			if r.TimezoneLabel == "" {
				errs = append(errs, fmt.Errorf("%s: timezone_label is required with timezone", where))
			}
		}
		if len(r.Agents) == 0 {
			errs = append(errs, fmt.Errorf("%s: no agents", where))
		}
		agents, folders := map[string]bool{}, map[string]bool{}
		for j := range r.Agents {
			a := &r.Agents[j]
			awhere := fmt.Sprintf("%s.agents[%d]", where, j)
			if !regionID.MatchString(a.ID) {
				errs = append(errs, fmt.Errorf("%s: id %q: want lowercase letters, digits, _ or -", awhere, a.ID))
			}
			if agents[a.ID] {
				errs = append(errs, fmt.Errorf("%s: duplicate id %q", awhere, a.ID))
			}
			agents[a.ID] = true
			if folders[a.FolderName()] {
				errs = append(errs, fmt.Errorf("%s: duplicate folder %q", awhere, a.FolderName()))
			}
			folders[a.FolderName()] = true
			errs = append(errs, c.validateAgent(awhere, r, a, series))
		}
	}
	return errors.Join(errs...)
}

func (c *Config) validateAgent(where string, r *Region, a *Agent, series map[string]bool) error {
	var errs []error
	for _, t := range a.Texts {
		if !slices.Contains(textSources, t) {
			errs = append(errs, fmt.Errorf("%s: unknown text source %q (want one of %s)", where, t, strings.Join(textSources, ", ")))
		}
	}
	issuers := slices.ContainsFunc(a.Reports, func(r Report) bool { return r.Kind == ReportIssuers })
	for _, t := range a.Companies {
		if issuers {
			if _, ok := c.Issuers.Issuer(t); !ok {
				errs = append(errs, fmt.Errorf("%s: company %q is not in issuers.companies (or issuers is disabled)", where, t))
			}
		} else if _, ok := c.EDGAR.Company(t); !ok {
			errs = append(errs, fmt.Errorf("%s: company %q is not in edgar.companies", where, t))
		}
	}
	sources := c.postSourceRegions()
	for _, name := range a.Channels {
		switch region, ok := sources[name]; {
		case !ok:
			errs = append(errs, fmt.Errorf("%s: channel %q is not in telegram.channels or rss_feeds", where, name))
		case region != r.ID:
			errs = append(errs, fmt.Errorf("%s: channel %q belongs to region %s", where, name, region))
		}
	}
	if len(a.Reports) == 0 {
		errs = append(errs, fmt.Errorf("%s: no reports", where))
	}
	files := map[string]bool{}
	for k := range a.Reports {
		rep := &a.Reports[k]
		rwhere := fmt.Sprintf("%s.reports[%d]", where, k)
		if rep.File == "" || rep.File != filepath.Base(rep.File) {
			errs = append(errs, fmt.Errorf("%s: file %q: want a file name", rwhere, rep.File))
		}
		if files[rep.File] {
			errs = append(errs, fmt.Errorf("%s: duplicate file %q", rwhere, rep.File))
		}
		files[rep.File] = true
		if rep.Title == "" {
			errs = append(errs, fmt.Errorf("%s: title is required", rwhere))
		}
		errs = append(errs, c.validateReport(rwhere, a, rep, series))
	}
	return errors.Join(errs...)
}

func (c *Config) validateReport(where string, a *Agent, rep *Report, series map[string]bool) error {
	want, ok := reportTables[rep.Kind]
	if !ok {
		return fmt.Errorf("%s: unknown report %q (want one of %s)", where, rep.Kind, strings.Join(reportKinds(), ", "))
	}
	var errs []error
	allowed := slices.Concat(want, optionalTables[rep.Kind])
	for _, name := range sortedKeys(rep.Tables) {
		if !slices.Contains(allowed, name) {
			errs = append(errs, fmt.Errorf("%s.tables: unknown table %q (want one of %s)", where, name, strings.Join(allowed, ", ")))
			continue
		}
		for _, id := range rep.Tables[name] {
			if !series[id] {
				errs = append(errs, fmt.Errorf("%s.tables.%s: %q is not a configured series", where, name, id))
			}
		}
	}
	for _, name := range want {
		if _, ok := rep.Tables[name]; !ok {
			errs = append(errs, fmt.Errorf("%s.tables: table %q is missing (an empty one is written as [])", where, name))
		}
	}
	if rep.Kind != ReportKeyMetrics && len(rep.Rows) > 0 {
		errs = append(errs, fmt.Errorf("%s: rows are for report %s", where, ReportKeyMetrics))
	}
	if rep.Kind == ReportKeyMetrics && len(rep.Rows) == 0 {
		errs = append(errs, fmt.Errorf("%s: rows are required", where))
	}
	for i, row := range rep.Rows {
		rwhere := fmt.Sprintf("%s.rows[%d]", where, i)
		if row.Label == "" {
			errs = append(errs, fmt.Errorf("%s: label is required", rwhere))
		}
		if row.ID != KeyRowFedPath && !series[row.ID] {
			errs = append(errs, fmt.Errorf("%s: %q is not a configured series", rwhere, row.ID))
		}
		if row.Code != "" && !slices.Contains(transform.Codes, row.Code) {
			errs = append(errs, fmt.Errorf("%s: unknown transform %q", rwhere, row.Code))
		}
	}
	if rep.Kind != ReportKeyMetrics && rep.SentimentRows {
		errs = append(errs, fmt.Errorf("%s: sentiment_rows is for report %s", where, ReportKeyMetrics))
	}
	if rep.Kind != ReportCalendar && len(rep.Previous) > 0 {
		errs = append(errs, fmt.Errorf("%s: previous is for report %s", where, ReportCalendar))
	}
	for _, id := range sortedKeys(rep.Previous) {
		p := rep.Previous[id]
		pwhere := fmt.Sprintf("%s.previous[%d]", where, id)
		if p.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", pwhere))
		}
		if !series[p.Series] {
			errs = append(errs, fmt.Errorf("%s: %q is not a configured series", pwhere, p.Series))
		}
		if !slices.Contains(transform.Codes, p.Code) {
			errs = append(errs, fmt.Errorf("%s: unknown transform %q", pwhere, p.Code))
		}
	}
	news := rep.FilteredFile != "" || rep.UncategorizedFile != "" || rep.OwnTagPrefix != "" || len(rep.OwnTagPrefixes) > 0 || rep.OtherTagPrefix != "" || len(rep.TagTopics) > 0 || rep.Shared
	if rep.Kind != ReportNews && news {
		errs = append(errs, fmt.Errorf("%s: filtered_file, uncategorized_file, shared, *_tag_prefix(es) and tag_topics are for report %s", where, ReportNews))
	}
	if rep.Kind == ReportNews {
		errs = append(errs, c.validateNewsReport(where, rep))
	}
	if rep.MaxChars < 0 {
		errs = append(errs, fmt.Errorf("%s: max_chars %d: want 0 (export.max_file_chars) or more", where, rep.MaxChars))
	}
	if rep.Kind != ReportSentiment && rep.Sentiment != nil {
		errs = append(errs, fmt.Errorf("%s: sentiment is for report %s", where, ReportSentiment))
	}
	if rep.Kind == ReportMicro && c.EDGAR.Enabled && len(a.Companies) == 0 {
		errs = append(errs, fmt.Errorf("%s: the agent needs companies", where))
	}
	if rep.Kind == ReportIssuers && c.Issuers.Enabled && len(a.Companies) == 0 {
		errs = append(errs, fmt.Errorf("%s: the agent needs companies", where))
	}
	if rep.Kind == ReportIssuers && slices.ContainsFunc(a.Reports, func(r Report) bool { return r.Kind == ReportMicro }) {
		errs = append(errs, fmt.Errorf("%s: an agent has %s or %s, not both (companies of one list)", where, ReportMicro, ReportIssuers))
	}
	return errors.Join(errs...)
}

func (c *Config) validateNewsReport(where string, rep *Report) error {
	var errs []error
	for name, file := range map[string]string{"filtered_file": rep.FilteredFile, "uncategorized_file": rep.UncategorizedFile} {
		if file == "" || file != filepath.Base(file) {
			errs = append(errs, fmt.Errorf("%s: %s %q: want a file name", where, name, file))
		}
	}
	if len(rep.OwnPrefixes()) == 0 {
		errs = append(errs, fmt.Errorf("%s: own_tag_prefix or own_tag_prefixes is required", where))
	}
	for _, tag := range sortedKeys(rep.TagTopics) {
		if !slices.Contains(c.Classify.Tags, tag) {
			errs = append(errs, fmt.Errorf("%s.tag_topics: unknown tag %q (want one of %s)", where, tag, strings.Join(c.Classify.Tags, ", ")))
		}
		if topic := rep.TagTopics[tag]; !slices.Contains(newsTopicNames, topic) {
			errs = append(errs, fmt.Errorf("%s.tag_topics.%s: unknown topic %q (want one of %s)", where, tag, topic, strings.Join(newsTopicNames, ", ")))
		}
	}
	return errors.Join(errs...)
}

// knownSeries are the names a table can give a series: its id and its
// full source/id reference.
func (c *Config) knownSeries() map[string]bool {
	out := map[string]bool{}
	for _, s := range c.Series() {
		out[s.ID] = true
		out[s.Ref()] = true
	}
	return out
}

// postSourceRegions maps Telegram handles and RSS feed names to their region.
func (c *Config) postSourceRegions() map[string]string {
	out := map[string]string{}
	for _, ch := range c.Telegram.Channels {
		out[ch.Handle] = ch.Region
	}
	for _, f := range c.RSSFeeds {
		out[f.Name] = f.Region
	}
	return out
}

func reportKinds() []string {
	return sortedKeys(reportTables)
}

func sortedKeys[K int | string, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// allRegions selects every region on the command line.
const allRegions = "all"

// SelectRegions resolves a --region value: an id or "all", in any case.
func (c *Config) SelectRegions(value string) ([]*Region, error) {
	value = strings.ToLower(value)
	if value == allRegions {
		out := make([]*Region, len(c.Regions))
		for i := range c.Regions {
			out[i] = &c.Regions[i]
		}
		return out, nil
	}
	if r, ok := c.Region(value); ok {
		return []*Region{r}, nil
	}
	return nil, fmt.Errorf("--region must be %s or %s, got %q", strings.Join(c.RegionIDs(), ", "), allRegions, value)
}
