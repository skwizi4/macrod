package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/logging"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/esef"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/ircal"
	"macro-collector/internal/sources/irpress"
	"macro-collector/internal/sources/irxlsx"
	"macro-collector/internal/store"
)

// IssuersSource is the job and fetch command name of EU Micro issuers.
const IssuersSource = "issuers"

// IssuerFactsState is the fetch_state key of an issuer's annual facts; the
// press releases use their documents source (ir:<ticker>).
func IssuerFactsState(ticker string) string { return "issuer_facts:" + ticker }

// IssuerQuarterlyState is the fetch_state key of an issuer's quarterly
// Excel.
func IssuerQuarterlyState(ticker string) string { return "issuer_quarterly:" + ticker }

// IssuerCalendarState is the fetch_state key of an issuer's financial
// calendar page (phase 14c).
func IssuerCalendarState(ticker string) string { return "issuer_calendar:" + ticker }

// IssuerClients read the issuers' facts and press releases.
type IssuerClients struct {
	// Web fetches the quarterly Excel pages and files.
	Web interface {
		Get(ctx context.Context, url string) ([]byte, error)
	}
	EDGAR interface {
		FactsIn(ctx context.Context, cik, taxonomy string, concepts []string) ([]model.CompanyFact, error)
	}
	ESEF interface {
		Filings(ctx context.Context, lei string) ([]esef.Filing, error)
		Facts(ctx context.Context, f esef.Filing, taxonomy string, concepts []string) ([]model.CompanyFact, error)
	}
	Press interface {
		Fetch(ctx context.Context, ticker, region string, p config.IssuerPress, since time.Time, known func(string) bool) ([]model.Document, error)
	}
}

// NewIssuerClients returns the clients of the config, or nil when issuers
// are disabled. EDGAR facts use the edgar section's address and contact.
func NewIssuerClients(cfg *config.Config, root *slog.Logger) *IssuerClients {
	is := cfg.Issuers
	if !is.Enabled {
		return nil
	}
	out := &IssuerClients{}
	log := logging.For(root, IssuersSource)
	web := httpx.New(httpx.WithLogger(log), httpx.WithUserAgent(is.UserAgent), httpx.WithInterval(is.Interval))
	out.Press = irpress.New(web, log)
	out.Web = web
	out.ESEF = esef.New(is.ESEFURL, web, logging.For(root, esef.Source))
	if cfg.EDGAR.DataURL != "" {
		elog := logging.For(root, edgar.Source)
		h := httpx.New(httpx.WithLogger(elog), httpx.WithUserAgent(cfg.EDGAR.UserAgent),
			httpx.WithInterval(edgar.Interval(max(cfg.EDGAR.RequestsPerSecond, 1))))
		out.EDGAR = edgar.New(cfg.EDGAR.DataURL, cfg.EDGAR.ArchivesURL, h, elog)
	}
	return out
}

// IssuerResult is one issuer's load.
type IssuerResult struct {
	Ticker   string
	Facts    int // facts inserted or changed
	Releases int // releases stored (new or edited)
	Quarters int // quarterly facts inserted or changed
	Dates    int // calendar report dates added or moved
	Err      error
}

// RunIssuers loads every issuer's annual facts (EDGAR 20-F or ESEF) and
// press releases. A failure is recorded in the fetch_state of that part
// and nothing of it is stored; the other issuers go on.
func RunIssuers(ctx context.Context, cfg *config.Config, st *store.Store, c *IssuerClients, root *slog.Logger, now time.Time) []IssuerResult {
	log := logging.For(root, IssuersSource)
	var out []IssuerResult
	for _, is := range cfg.Issuers.Companies {
		if ctx.Err() != nil {
			break
		}
		res := IssuerResult{Ticker: is.Ticker}
		if is.Facts != nil {
			n, err := issuerFacts(ctx, st, c, is, now)
			res.Facts, res.Err = n, err
			recordState(ctx, st, log, IssuerFactsState(is.Ticker), err, now.Format(time.DateOnly), now)
		}
		if is.Press != nil {
			region, _, _ := cfg.RegionOfCompany(is.Ticker)
			n, cursor, err := issuerPress(ctx, st, c, cfg.Issuers, is, region, now)
			res.Releases = n
			res.Err = errors.Join(res.Err, err)
			recordState(ctx, st, log, irpress.Source(is.Ticker), err, cursor, now)
		}
		if is.Quarterly != nil && !is.Quarterly.FromFile() {
			n, err := issuerQuarterly(ctx, st, c, is, now)
			res.Quarters = n
			res.Err = errors.Join(res.Err, err)
			recordState(ctx, st, log, IssuerQuarterlyState(is.Ticker), err, now.Format(time.DateOnly), now)
		}
		if is.Calendar != nil {
			n, err := issuerCalendar(ctx, st, c, is, log, now)
			res.Dates = n
			res.Err = errors.Join(res.Err, err)
			recordState(ctx, st, log, IssuerCalendarState(is.Ticker), err, now.Format(time.DateOnly), now)
		}
		if res.Err != nil {
			log.Warn("issuer load failed", "ticker", is.Ticker, "err", res.Err)
		}
		out = append(out, res)
	}
	return out
}

// recordState writes the fetch state of one part: its error, or success
// with cursor.
func recordState(ctx context.Context, st *store.Store, log *slog.Logger, key string, err error, cursor string, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	if werr := st.Write(ctx, func(tx *sql.Tx) error {
		if err != nil {
			return store.SetFetchError(tx, key, err, now)
		}
		return store.SetFetchOK(tx, key, cursor, now)
	}); werr != nil {
		log.Error("record fetch state", "key", key, "err", werr)
	}
}

// issuerFacts loads the annual facts: EDGAR company facts of the issuer's
// forms, or the ESEF reports not stored yet.
func issuerFacts(ctx context.Context, st *store.Store, c *IssuerClients, is config.Issuer, now time.Time) (int, error) {
	f := is.Facts
	tags := f.Concepts.Tags()
	var facts []model.CompanyFact
	switch f.Source {
	case config.FactsEDGAR:
		if c.EDGAR == nil {
			return 0, fmt.Errorf("issuer %s: facts from EDGAR need edgar.data_url and edgar.user_agent", is.Ticker)
		}
		all, err := c.EDGAR.FactsIn(ctx, f.CIK, f.Taxonomy, tags)
		if err != nil {
			return 0, fmt.Errorf("issuer %s: %w", is.Ticker, err)
		}
		facts = slices.DeleteFunc(all, func(x model.CompanyFact) bool { return !slices.Contains(f.Forms, x.Form) })
	case config.FactsESEF:
		filings, err := c.ESEF.Filings(ctx, f.LEI)
		if err != nil {
			return 0, fmt.Errorf("issuer %s: %w", is.Ticker, err)
		}
		for _, fl := range filings {
			stored, err := st.HasFactsFrom(ctx, is.Ticker, fl.ID)
			if err != nil {
				return 0, err
			}
			if stored {
				continue
			}
			fs, err := c.ESEF.Facts(ctx, fl, f.Taxonomy, tags)
			if err != nil {
				return 0, fmt.Errorf("issuer %s: %w", is.Ticker, err)
			}
			facts = append(facts, fs...)
		}
	}
	for i := range facts {
		facts[i].Ticker = is.Ticker
	}
	n := 0
	err := st.Write(ctx, func(tx *sql.Tx) (err error) {
		n, err = store.UpsertCompanyFacts(tx, is.Ticker, facts, now)
		return err
	})
	return n, err
}

// issuerPress loads the releases published since the cursor (the newest
// stored) or, the first time, within press_history_days.
func issuerPress(ctx context.Context, st *store.Store, c *IssuerClients, sec config.Issuers, is config.Issuer, region string, now time.Time) (int, string, error) {
	source := irpress.Source(is.Ticker)
	since := now.AddDate(0, 0, -sec.PressHistoryDays)
	cursor := ""
	if fs, ok, err := st.FetchStateOf(ctx, source); err != nil {
		return 0, "", err
	} else if ok && fs.Cursor != "" {
		cursor = fs.Cursor
		if t, err := time.Parse(time.RFC3339, fs.Cursor); err == nil {
			since = t.AddDate(0, 0, -2) // an edited release comes back
		}
	}
	known := func(url string) bool {
		_, ok, err := st.Document(ctx, source, url)
		return err == nil && ok
	}
	docs, err := c.Press.Fetch(ctx, is.Ticker, region, *is.Press, since, known)
	if err != nil {
		return 0, cursor, err
	}
	n := 0
	err = st.Write(ctx, func(tx *sql.Tx) error {
		for _, d := range docs {
			d.FetchedAt = now
			_, outcome, err := store.UpsertDocument(tx, d, now)
			if err != nil {
				return err
			}
			if outcome != store.DocUnchanged {
				n++
			}
		}
		return nil
	})
	for _, d := range docs {
		if t := d.PublishedAt.Format(time.RFC3339); t > cursor {
			cursor = t
		}
	}
	return n, cursor, err
}

// issuerCalendar reads the report dates of the issuer's calendar page
// (phase 14c, D29): no matched event stores nothing and is an error.
func issuerCalendar(ctx context.Context, st *store.Store, c *IssuerClients, is config.Issuer, log *slog.Logger, now time.Time) (int, error) {
	cal := is.Calendar
	if c.Web == nil {
		return 0, fmt.Errorf("issuer %s: no web client", is.Ticker)
	}
	page, err := c.Web.Get(ctx, cal.URL)
	if err != nil {
		return 0, fmt.Errorf("issuer %s calendar: %w", is.Ticker, err)
	}
	events, err := ircal.Parse(page, *cal)
	if err != nil {
		return 0, fmt.Errorf("issuer %s: %w", is.Ticker, err)
	}
	ds := make([]store.IssuerReportDate, len(events))
	for i, e := range events {
		ds[i] = store.IssuerReportDate{Ticker: is.Ticker, Period: e.Period, Date: e.Date, Title: e.Title, URL: cal.URL}
	}
	var added int
	var moved []store.Moved
	err = st.Write(ctx, func(tx *sql.Tx) (err error) {
		added, moved, err = store.UpsertIssuerReportDates(tx, ds, now)
		return err
	})
	for _, m := range moved {
		log.Warn("issuer report date moved", "ticker", is.Ticker, "period", m.Period, "old", m.Old.Format(time.DateOnly), "new", m.New.Format(time.DateOnly))
	}
	return added + len(moved), err
}

// issuerQuarterly reads the quarterly Excel files linked from the issuer's
// page that are not stored yet (by file name). A file that fails a check —
// a table, row or period column not found exactly once, a value that is
// not a number — stores nothing and is reported; the other files go on. A
// later file with the same quarter is a revision: the export takes the
// latest filed.
func issuerQuarterly(ctx context.Context, st *store.Store, c *IssuerClients, is config.Issuer, now time.Time) (int, error) {
	q := is.Quarterly
	if c.Web == nil {
		return 0, fmt.Errorf("issuer %s: no web client", is.Ticker)
	}
	page, err := c.Web.Get(ctx, q.Page)
	if err != nil {
		return 0, fmt.Errorf("issuer %s quarterly page: %w", is.Ticker, err)
	}
	files := irxlsx.Links(page, q.LinkRes, q.MaxFiles)
	if len(files) == 0 {
		return 0, fmt.Errorf("issuer %s: no quarterly Excel linked from %s", is.Ticker, q.Page)
	}
	var errs []error
	var facts []model.CompanyFact
	for _, f := range files {
		stored, err := st.HasFactsFrom(ctx, is.Ticker, f.Name)
		if err != nil {
			return 0, err
		}
		if stored {
			continue
		}
		data, err := c.Web.Get(ctx, f.URL)
		if err != nil {
			errs = append(errs, fmt.Errorf("issuer %s %s: %w", is.Ticker, f.Name, err))
			continue
		}
		pairs, err := irxlsx.Parse(data, *q, f.FY, f.Q)
		if err != nil {
			errs = append(errs, fmt.Errorf("issuer %s %s: %w", is.Ticker, f.Name, err))
			continue
		}
		facts = append(facts, QuarterFacts(is, f, pairs, now)...)
	}
	n := 0
	if len(facts) > 0 {
		err = st.Write(ctx, func(tx *sql.Tx) (err error) {
			n, err = store.UpsertCompanyFacts(tx, is.Ticker, facts, now)
			return err
		})
		errs = append(errs, err)
	}
	return n, errors.Join(errs...)
}

// IssuerQuarterlyFile stores the quarterly numbers of a file the user
// sent (quarterly source file: the bot or `macroctl issuers file`; phase
// 14c, D29), read like a downloaded one: a table, row or period column not
// found exactly once stores nothing. The result is recorded in the
// issuer's quarterly fetch_state. It returns the numbers read and the
// facts inserted or changed.
func IssuerQuarterlyFile(ctx context.Context, st *store.Store, is config.Issuer, f irxlsx.File, data []byte, now time.Time) (map[string]irxlsx.Pair, int, error) {
	if is.Quarterly == nil {
		return nil, 0, fmt.Errorf("issuer %s: no quarterly section in the config", is.Ticker)
	}
	pairs, err := irxlsx.Parse(data, *is.Quarterly, f.FY, f.Q)
	n := 0
	if err != nil {
		err = fmt.Errorf("issuer %s %s: %w", is.Ticker, f.Name, err)
	} else {
		err = st.Write(ctx, func(tx *sql.Tx) (err error) {
			n, err = store.UpsertCompanyFacts(tx, is.Ticker, QuarterFacts(is, f, pairs, now), now)
			return err
		})
	}
	if werr := st.Write(ctx, func(tx *sql.Tx) error {
		if err != nil {
			return store.SetFetchError(tx, IssuerQuarterlyState(is.Ticker), err, now)
		}
		return store.SetFetchOK(tx, IssuerQuarterlyState(is.Ticker), f.Name, now)
	}); werr != nil && err == nil {
		err = werr
	}
	if err != nil {
		return nil, 0, err
	}
	return pairs, n, nil
}

// QuarterFacts are the facts of one file: each field in the file's quarter
// and a year earlier, concept = the field name, form IR_XLSX.
func QuarterFacts(is config.Issuer, f irxlsx.File, pairs map[string]irxlsx.Pair, now time.Time) []model.CompanyFact {
	q := is.Quarterly
	cur := is.CurrencyOf()
	filed := model.Day(now)
	var out []model.CompanyFact
	for _, field := range config.QuarterFields {
		p, ok := pairs[field]
		if !ok {
			continue
		}
		unit := cur
		if field == config.QuarterEPS {
			unit = cur + "/shares"
		}
		for k, fy := range []int{f.FY, f.FY - 1} {
			end := q.QuarterEnd(fy, f.Q)
			start := time.Date(end.Year(), end.Month()-2, 1, 0, 0, 0, 0, time.UTC)
			v := p.Cur
			if k == 1 {
				v = p.Ago
			}
			out = append(out, model.CompanyFact{Ticker: is.Ticker, Concept: field, Unit: unit, Start: start, End: end, Value: v,
				Accession: f.Name, Form: irxlsx.Form, Filed: filed, FY: fy, FP: fmt.Sprintf("Q%d", f.Q)})
		}
	}
	return out
}
