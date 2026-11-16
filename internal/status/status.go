// Package status builds the `macroctl status` report: per series the last
// observation, last successful fetch, last error and staleness.
package status

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/classify"
	"macro-collector/internal/config"
	"macro-collector/internal/derived"
	"macro-collector/internal/export"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/cftc"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// Row is one configured series.
type Row struct {
	Source    string
	ID        string
	Frequency string
	LastObs   time.Time // zero if no observations
	LastOK    time.Time // zero if never fetched successfully
	Error     string
	ErrorAt   time.Time
	Stale     bool
}

// Fetched reports whether the series has ever been stored.
func (r Row) Fetched() bool { return !r.LastObs.IsZero() }

// OK reports whether the series has data, no error and is not stale.
func (r Row) OK() bool { return r.Fetched() && r.Error == "" && !r.Stale }

// Report is the whole status.
type Report struct {
	DBPath        string
	DBBytes       int64
	SchemaVersion int
	Rows          []Row

	// Calendar and texts (phase 2).
	Calendars []State              // fetch_state rows "calendar:*"
	Texts     []State              // fetch_state rows "fed:*"
	Documents map[string]int       // Fed documents by doc_type
	ECBDocs   map[string]int       // ECB documents by doc_type (phase 11)
	Upcoming  []model.ReleaseEvent // next UpcomingDays, without rescheduled events
	Timeouts  []model.ReleaseEvent // timed out in the last TimeoutDays
	Polling   int                  // events being polled now

	// Treasury, Yahoo, CFTC and exports (phase 3).
	Datasets        []State // fetch_state rows "treasury:*", "cftc" and "finanzagentur"
	AuctionsHeld    int     // auctions with results in the last AuctionDays
	AuctionsPending int     // auctions in the last AuctionDays still without results
	Exports         []RegionExports

	// Phase 4: Telegram channels and RSS feeds.
	PostSources   []PostSource
	Classify      store.ClassifyCounts // posts published in the last PostDays
	GeminiToday   int                  // Gemini requests of the current Pacific day
	GeminiLimit   int
	GeminiEnabled bool

	// Phase 5: EDGAR companies.
	Companies []Company
}

// Company is the EDGAR state of one company.
type Company struct {
	State
	Reports    int
	LastReport model.CompanyReport // zero if none
	Facts      int
	LastFiled  time.Time // newest 10-Q/10-K date among the stored facts
}

// AuctionDays is the window of the auction counts.
const AuctionDays = 7

// State is a fetch_state row of a non-series source.
type State struct {
	Source  string
	LastOK  time.Time // zero if never succeeded
	Error   string
	ErrorAt time.Time
}

// Windows of the calendar part of the report.
const (
	UpcomingDays = 7
	TimeoutDays  = 7
	PostDays     = 7
)

// SourceSummary aggregates rows of one source.
type SourceSummary struct {
	Source                       string
	Series, Errors, Stale, Never int
	LastOK                       time.Time // latest successful fetch of any series
}

// Summaries groups rows by source, in first-seen order.
func (r Report) Summaries() []SourceSummary {
	var out []SourceSummary
	idx := map[string]int{}
	for _, row := range r.Rows {
		i, ok := idx[row.Source]
		if !ok {
			i = len(out)
			idx[row.Source] = i
			out = append(out, SourceSummary{Source: row.Source})
		}
		s := &out[i]
		s.Series++
		if row.Error != "" {
			s.Errors++
		}
		if row.Stale {
			s.Stale++
		}
		if !row.Fetched() {
			s.Never++
		}
		if row.LastOK.After(s.LastOK) {
			s.LastOK = row.LastOK
		}
	}
	return out
}

// RegionExports are the last exports of a region, zero if none.
type RegionExports struct {
	Region            string
	Weekly, FOMC, ECB time.Time
}

// Build collects the status of every configured FRED series and every
// implemented derived series as of now.
func Build(ctx context.Context, cfg *config.Config, st *store.Store, now time.Time) (Report, error) {
	rep := Report{DBPath: st.Path()}
	var err error
	if rep.DBBytes, err = st.Size(); err != nil {
		return Report{}, err
	}
	if rep.SchemaVersion, err = st.SchemaVersion(ctx); err != nil {
		return Report{}, err
	}
	overviews, err := st.SeriesOverviews(ctx, now)
	if err != nil {
		return Report{}, err
	}
	meta := make(map[string]store.SeriesOverview, len(overviews))
	for _, o := range overviews {
		meta[o.Source+":"+o.SeriesID] = o
	}
	states, err := st.FetchStates(ctx)
	if err != nil {
		return Report{}, err
	}

	// stateKey is the fetch_state row of a series: "<source>:<id>" unless
	// the source fetches several series at once (Treasury datasets, CFTC).
	add := func(source, id, stateKey string) {
		key := source + ":" + id
		if stateKey == "" {
			stateKey = key
		}
		row := Row{Source: source, ID: id}
		if m, ok := meta[key]; ok {
			row.Frequency, row.LastObs = m.Frequency, m.LastObs
		}
		if s, ok := states[stateKey]; ok {
			if s.Cursor != "" {
				row.LastOK = s.UpdatedAt
			}
			row.Error, row.ErrorAt = s.LastError, s.LastErrorAt
		}
		rep.Rows = append(rep.Rows, row)
	}
	for _, s := range cfg.FRED.Series {
		add("fred", s.ID, "")
	}
	for _, region := range cfg.RegionIDs() {
		for _, y := range ingest.YahooSymbols(&cfg.Yahoo, region, now) {
			add(yahoo.Source, y.Symbol, "")
		}
	}
	for name, ids := range map[string][]string{
		config.DatasetDTS: {treasury.SeriesTGA},
		config.DatasetMTS: {treasury.SeriesMTSReceipts, treasury.SeriesMTSOutlays, treasury.SeriesMTSDeficit},
	} {
		if _, ok := cfg.Treasury.Dataset(name); ok {
			for _, id := range ids {
				add(treasury.Source, id, treasury.Source+":"+name)
			}
		}
	}
	if cfg.CFTC.Enabled {
		for _, region := range cfg.RegionIDs() {
			for _, k := range cfg.CFTC.ContractsFor(region) {
				add(cftc.Source, cftc.SeriesID(k.Code, cftc.FieldOpenInterest), cftc.Source)
			}
		}
	}
	for _, src := range cfg.NumericSources() {
		if !src.Enabled {
			continue
		}
		for _, sr := range src.Series {
			add(src.Name, sr.ID, "")
		}
	}
	for _, sr := range cfg.Manual.Series {
		add(config.SourceManual, sr.ID, "")
	}
	if err := rep.addPhase3(ctx, cfg, st, states, now); err != nil {
		return Report{}, err
	}
	if err := rep.addCalendar(ctx, st, states, now); err != nil {
		return Report{}, err
	}
	if err := rep.addCompanies(ctx, cfg, st, states); err != nil {
		return Report{}, err
	}
	if err := rep.addPosts(ctx, cfg, st, states, now); err != nil {
		return Report{}, err
	}
	for _, d := range cfg.Derived {
		if _, ok := derived.Formulas[d.Name]; ok {
			add(derived.Source, d.Name, "")
		}
	}
	if err := rep.markStale(ctx, cfg, st, now); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// markStale applies §17.7 the way an export does: a series with a calendar
// is stale when its latest release brought nothing, whatever the age of its
// last observation; the fixed table is for series without a calendar.
func (rep *Report) markStale(ctx context.Context, cfg *config.Config, st *store.Store, now time.Time) error {
	et := cfg.Calendar.ReleaseLocation
	if et == nil { // set by config validation
		et = time.UTC
	}
	releases, err := export.LoadReleaseIndex(ctx, st, now, et, nil)
	if err != nil {
		return err
	}
	days, err := export.ManualReleaseDays(ctx, cfg, st)
	if err != nil {
		return err
	}
	releases.AddConfigDays(days, now, et)
	for i := range rep.Rows {
		row := &rep.Rows[i]
		if !row.Fetched() {
			continue
		}
		in := transform.FlagInput{Freq: row.Frequency, Last: row.LastObs, AsOf: now}
		day, ok, next := releases.Of(row.ID)
		if ok {
			if in.Seen, err = lastSeen(ctx, st, *row, now); err != nil {
				return err
			}
			in.ReleaseDay = day
		}
		in.NextRelease = next
		row.Stale = slices.Contains(transform.Flags(in), transform.FlagStale)
	}
	return nil
}

// lastSeen is when the latest value of the row became known. Empty values
// (a daily series on a holiday) are skipped, so it looks back a year.
func lastSeen(ctx context.Context, st *store.Store, row Row, now time.Time) (time.Time, error) {
	known, err := st.ObservationsAsOf(ctx, row.Source, row.ID, row.LastObs.AddDate(-1, 0, 0), now)
	if err != nil {
		return time.Time{}, err
	}
	for i := len(known) - 1; i >= 0; i-- {
		if !known[i].Date.After(now) {
			return known[i].Seen, nil
		}
	}
	return time.Time{}, nil
}

func (rep *Report) addPhase3(ctx context.Context, cfg *config.Config, st *store.Store, states map[string]store.FetchState, now time.Time) error {
	for _, k := range slices.Sorted(maps.Keys(states)) {
		if strings.HasPrefix(k, treasury.Source+":") || k == cftc.Source || k == finanzagentur.Source {
			s := states[k]
			row := State{Source: k, Error: s.LastError, ErrorAt: s.LastErrorAt}
			if s.Cursor != "" {
				row.LastOK = s.UpdatedAt
			}
			rep.Datasets = append(rep.Datasets, row)
		}
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	as, err := st.Auctions(ctx, today.AddDate(0, 0, -AuctionDays), today)
	if err != nil {
		return err
	}
	for _, a := range as {
		if a.HasResults() {
			rep.AuctionsHeld++
		} else if a.AuctionDate.Before(today) {
			rep.AuctionsPending++
		}
	}
	for _, region := range cfg.RegionIDs() {
		x := RegionExports{Region: region}
		if x.Weekly, _, err = st.LastExport(ctx, export.KindWeekly, region); err != nil {
			return err
		}
		if x.FOMC, _, err = st.LastExport(ctx, export.KindEventFOMC, region); err != nil {
			return err
		}
		if x.ECB, _, err = st.LastExport(ctx, export.KindEventECB, region); err != nil {
			return err
		}
		rep.Exports = append(rep.Exports, x)
	}
	return nil
}

func (rep *Report) addCalendar(ctx context.Context, st *store.Store, states map[string]store.FetchState, now time.Time) error {
	for _, k := range slices.Sorted(maps.Keys(states)) {
		s := states[k]
		row := State{Source: k, Error: s.LastError, ErrorAt: s.LastErrorAt}
		if s.Cursor != "" {
			row.LastOK = s.UpdatedAt
		}
		switch {
		case strings.HasPrefix(k, "calendar:"):
			rep.Calendars = append(rep.Calendars, row)
		case strings.HasPrefix(k, fedtexts.Source+":"), strings.HasPrefix(k, ecbtexts.Source+":"):
			rep.Texts = append(rep.Texts, row)
		}
	}
	var err error
	if rep.Documents, err = st.DocumentCounts(ctx, fedtexts.Source); err != nil {
		return err
	}
	if rep.ECBDocs, err = st.DocumentCounts(ctx, ecbtexts.Source); err != nil {
		return err
	}
	evs, err := st.Releases(ctx, now.AddDate(0, 0, -TimeoutDays), now.AddDate(0, 0, UpcomingDays))
	if err != nil {
		return err
	}
	for _, e := range evs {
		switch {
		case e.Status == model.StatusTimeout:
			rep.Timeouts = append(rep.Timeouts, e)
		case e.Status == model.StatusPolling:
			rep.Polling++
		}
		if !e.ScheduledAt.Before(now) && e.Status != model.StatusRescheduled {
			rep.Upcoming = append(rep.Upcoming, e)
		}
	}
	return nil
}

// PostSource is a configured channel or feed with its fetch state and posts.
type PostSource struct {
	State
	Posts, Recent int // stored; published in the last PostDays
	LastPost      time.Time
}

func (rep *Report) addPosts(ctx context.Context, cfg *config.Config, st *store.Store, states map[string]store.FetchState, now time.Time) error {
	since := now.AddDate(0, 0, -PostDays)
	counts, err := st.PostSources(ctx, since)
	if err != nil {
		return err
	}
	byName := make(map[string]store.PostSourceStats)
	for _, c := range counts {
		byName[c.Source] = c
	}
	var names []string
	if cfg.Telegram.Enabled {
		for _, ch := range cfg.Telegram.Channels {
			names = append(names, "telegram:"+ch.Handle)
		}
	}
	for _, f := range cfg.RSSFeeds {
		names = append(names, "rss:"+f.Name)
	}
	for _, n := range names {
		p := PostSource{State: State{Source: n}}
		if s, ok := states[n]; ok {
			p.Error, p.ErrorAt = s.LastError, s.LastErrorAt
			p.LastOK = s.UpdatedAt
		}
		c := byName[n]
		p.Posts, p.Recent, p.LastPost = c.Posts, c.Recent, c.LastPost
		rep.PostSources = append(rep.PostSources, p)
	}
	if rep.Classify, err = st.PostClassifyCounts(ctx, since); err != nil {
		return err
	}
	rep.GeminiEnabled, rep.GeminiLimit = cfg.Classify.Gemini.Enabled, cfg.Classify.Gemini.DailyRequestLimit
	rep.GeminiToday, err = classify.GeminiRequestsToday(ctx, st, now)
	return err
}

func (rep *Report) addCompanies(ctx context.Context, cfg *config.Config, st *store.Store, states map[string]store.FetchState) error {
	if !cfg.EDGAR.Enabled {
		return nil
	}
	stats, err := st.CompanyFactStats(ctx)
	if err != nil {
		return err
	}
	for _, c := range cfg.EDGAR.Companies {
		row := Company{State: State{Source: "edgar:" + c.Ticker}}
		if s, ok := states[row.Source]; ok {
			row.Error, row.ErrorAt = s.LastError, s.LastErrorAt
			if s.LastError == "" {
				row.LastOK = s.UpdatedAt
			}
		}
		reports, err := st.CompanyReportsOf(ctx, c.Ticker)
		if err != nil {
			return err
		}
		row.Reports = len(reports)
		if len(reports) > 0 {
			row.LastReport = reports[0]
		}
		row.Facts, row.LastFiled = stats[c.Ticker].Facts, stats[c.Ticker].LastFiled
		rep.Companies = append(rep.Companies, row)
	}
	return nil
}
