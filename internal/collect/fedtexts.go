package collect

import (
	"context"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/store"
)

// FedTextSources are the configured RSS feeds, the Beige Book and the
// documents of recent FOMC meetings (implementation note, press conference
// transcript), whose meeting days come from the calendar in st.
func FedTextSources(cfg *config.Config, client *fedtexts.Client, st *store.Store, root *slog.Logger) []sources.TextSource {
	log := logging.For(root, "fed")
	var out []sources.TextSource
	for _, f := range cfg.FedTexts.Feeds {
		out = append(out, &fedtexts.Feed{
			Client: client, FeedName: f.Name, URL: f.URL, BaseURL: cfg.FedTexts.BaseURL,
			Location: cfg.Calendar.ReleaseLocation, Log: log,
		})
	}
	out = append(out, &fedtexts.BeigeBook{Client: client, URL: cfg.FedTexts.BeigeBook, Location: cfg.Calendar.ReleaseLocation})
	return append(out, &fedtexts.MeetingDocs{Client: client, BaseURL: cfg.FedTexts.BaseURL, Location: cfg.Calendar.ReleaseLocation,
		Meetings: func(ctx context.Context, since time.Time) ([]time.Time, error) {
			return pastFOMCDecisions(ctx, st, since)
		}})
}

// pastFOMCDecisions are the FOMC decision times in the calendar from since to now.
func pastFOMCDecisions(ctx context.Context, st *store.Store, since time.Time) ([]time.Time, error) {
	evs, err := st.Releases(ctx, since, time.Now())
	if err != nil {
		return nil, err
	}
	var out []time.Time
	for _, e := range evs {
		if e.Source == fedtexts.SourceFOMC && e.Name == fedtexts.NameDecision && e.Status != model.StatusRescheduled {
			out = append(out, e.ScheduledAt)
		}
	}
	return out, nil
}

// NewTexts is the document loader for official texts: a document takes the
// region of the agent its source is configured for.
func NewTexts(cfg *config.Config, st *store.Store, root *slog.Logger) *ingest.Texts {
	return &ingest.Texts{Store: st, Log: logging.For(root, "fed"), WarnRevised: []string{ecbtexts.DocStatement}, RegionOf: func(d model.Document) string {
		switch d.Source {
		case treasury.Source:
			return cfg.TreasuryRegion()
		case ecbtexts.Source:
			return cfg.RegionOfTag(cfg.ECBTexts.Agent)
		}
		return cfg.RegionOfTag(cfg.FedTexts.Agent)
	}}
}

// RunFedTexts polls srcs in order (any text sources: the ECB ones too); one failing does not stop the others.
func RunFedTexts(ctx context.Context, t *ingest.Texts, srcs []sources.TextSource) []ingest.TextResult {
	var out []ingest.TextResult
	for _, src := range srcs {
		if ctx.Err() != nil {
			break
		}
		out = append(out, t.Run(ctx, src))
	}
	return out
}
