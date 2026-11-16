package collect

import (
	"context"
	"log/slog"
	"math"
	"time"

	"macro-collector/internal/calendar"
	"macro-collector/internal/config"
	"macro-collector/internal/logging"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/store"
)

// qaFor is how long a statement page is re-read for its Q&A.
const qaFor = 21 * 24 * time.Hour

// NewECBClient returns the ecb.europa.eu client, or nil when ecb_texts is
// disabled.
func NewECBClient(cfg *config.Config, root *slog.Logger) *ecbtexts.Client {
	if !cfg.ECBTexts.Enabled {
		return nil
	}
	return ecbtexts.New(httpx.WithLogger(logging.For(root, ecbtexts.Source)))
}

// ECBSite is the page reader of the config.
func ECBSite(cfg *config.Config, client *ecbtexts.Client, root *slog.Logger) *ecbtexts.Site {
	e := &cfg.ECBTexts
	return &ecbtexts.Site{Client: client, BaseURL: e.BaseURL, Location: e.Location, Log: logging.For(root, ecbtexts.Source),
		Times: map[string]time.Duration{
			ecbtexts.DocDecision:  e.Times.DecisionAt.Since(),
			ecbtexts.DocStatement: e.Times.PressConferenceAt.Since(),
			ecbtexts.DocAccounts:  e.Times.AccountsAt.Since(),
		}}
}

// ECBSources are the feed, the yearly lists (decisions, statements,
// accounts) and the Q&A re-reader; nil when ecb_texts is disabled.
type ECBSources struct {
	Feed       *ecbtexts.Feed
	Decisions  *ecbtexts.List
	Statements *ecbtexts.List
	Accounts   *ecbtexts.List
	QA         *ecbtexts.PressConferences
}

// All lists the sources in polling order.
func (s *ECBSources) All() []sources.TextSource {
	if s == nil {
		return nil
	}
	return []sources.TextSource{s.Feed, s.Decisions, s.Statements, s.Accounts, s.QA}
}

// NewECBSources builds the ECB text sources; nil when disabled or without
// a client.
func NewECBSources(cfg *config.Config, client *ecbtexts.Client, st *store.Store, root *slog.Logger) *ECBSources {
	if !cfg.ECBTexts.Enabled || client == nil {
		return nil
	}
	e := &cfg.ECBTexts
	site := ECBSite(cfg, client, root)
	list := func(docType, u string) *ecbtexts.List { return &ecbtexts.List{Site: site, DocType: docType, URL: u} }
	return &ECBSources{
		Feed:       &ecbtexts.Feed{Site: site, URL: e.RSS},
		Decisions:  list(ecbtexts.DocDecision, e.Lists.Decisions),
		Statements: list(ecbtexts.DocStatement, e.Lists.Statements),
		Accounts:   list(ecbtexts.DocAccounts, e.Lists.Accounts),
		QA: &ecbtexts.PressConferences{Site: site, Pending: func(ctx context.Context) ([]model.Document, error) {
			return pendingQA(ctx, st, time.Now())
		}},
	}
}

// pendingQA are the statements of the last qaFor without a stored Q&A.
func pendingQA(ctx context.Context, st *store.Store, now time.Time) ([]model.Document, error) {
	sts, err := st.LatestDocuments(ctx, ecbtexts.Source, ecbtexts.DocStatement, 3)
	if err != nil {
		return nil, err
	}
	var out []model.Document
	for _, d := range sts {
		if now.Sub(d.PublishedAt) > qaFor {
			continue
		}
		if _, ok, err := st.Document(ctx, ecbtexts.Source, d.ExternalID+ecbtexts.QASuffix); err != nil {
			return nil, err
		} else if !ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// RefreshECBCalendar syncs the Governing Council meetings a year ahead
// (decision and press conference events); the region is that of the
// ecb_texts agent.
func RefreshECBCalendar(ctx context.Context, cfg *config.Config, st *store.Store, client *ecbtexts.Client, root *slog.Logger) (calendar.SourceReport, error) {
	e := &cfg.ECBTexts
	long := cfg.Calendar
	long.HorizonDays = MeetingsHorizonDays
	src := &calendar.Source{
		Source: &ecbtexts.Calendar{Client: client, URL: e.MeetingCalendar, Location: e.Location,
			Decision: e.Times.DecisionAt.Since(), PressConference: e.Times.PressConferenceAt.Since()},
		Keys: ecbtexts.Keys, Region: cfg.RegionOfTag(e.Agent), Store: st, Calendar: &long, Log: logging.For(root, "calendar"),
	}
	return src.Refresh(ctx)
}

// CheckECBDecisions compares the deposit facility rate of recent parsed
// decisions, once in force, with the stored series of ecb_texts.check_series
// and logs a mismatch. Without the series (another preset, not fetched yet)
// the check is skipped; it never fails the job.
func CheckECBDecisions(ctx context.Context, cfg *config.Config, st *store.Store, now time.Time, log *slog.Logger) {
	name := cfg.ECBTexts.CheckSeries
	if name == "" {
		return
	}
	ser, ok := cfg.ResolveSeries(name)
	if !ok {
		log.Info("ecb decision check skipped: series not in the config", "series", name)
		return
	}
	decisions, err := st.LatestDocuments(ctx, ecbtexts.Source, ecbtexts.DocDecision, 3)
	if err != nil {
		log.Warn("ecb decision check", "err", err)
		return
	}
	for _, d := range decisions {
		if now.Sub(d.PublishedAt) > 30*24*time.Hour {
			continue
		}
		p := ecbtexts.ParseDecision(d.Body)
		if !p.Parsed {
			continue
		}
		from := p.EffectiveFrom
		if from.IsZero() {
			from = d.PublishedAt.AddDate(0, 0, 7)
		}
		if now.Before(from) {
			continue
		}
		obs, err := st.Observations(ctx, ser.Source, ser.ID, from)
		if err != nil {
			log.Warn("ecb decision check", "series", name, "err", err)
			continue
		}
		if len(obs) == 0 {
			log.Info("ecb decision check skipped: no observations in force yet", "series", name, "decision", d.ExternalID, "from", from.Format(time.DateOnly))
			continue
		}
		got := obs[len(obs)-1]
		if math.Abs(got.Value-p.DFR) > 0.001 {
			log.Warn("ecb decision does not match series", "decision", d.ExternalID, "parsed_dfr", p.DFR,
				"series", name, "value", got.Value, "date", got.Date.Format(time.DateOnly))
		}
	}
}
