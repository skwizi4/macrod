package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/store"
)

// releaseDay is midnight (in loc) of the day an event is scheduled: data
// counts as published for the event if it appeared on that day or later.
func releaseDay(ev model.ReleaseEvent, loc *time.Location) time.Time {
	y, m, d := ev.ScheduledAt.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// FREDRelease polls the series of a FRED release. A series has arrived
// when its last_updated is on the release day or later; the event is done
// when all its series have.
type FREDRelease struct {
	Config *config.Config
	Store  *store.Store
	Client ingest.FREDClient
	Root   *slog.Logger
}

// Poll implements Handler.
func (h *FREDRelease) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	pending, err := h.pending(ctx, ev)
	if err != nil || len(pending) == 0 {
		return err == nil, err
	}
	sum := collect.RunFREDSeries(ctx, h.Config, h.Store, h.Client, h.Root, pending)
	var pollErr error
	if n := sum.Failures(); n > 0 {
		pollErr = fmt.Errorf("%d of %d series failed: %v", n, len(pending), sum.FRED.Failed()[0].Err)
	}
	if pending, err = h.pending(ctx, ev); err != nil {
		return false, err
	}
	return len(pending) == 0, pollErr
}

// Expire implements Expirer: a release with some series updated is done.
func (h *FREDRelease) Expire(ctx context.Context, ev model.ReleaseEvent) string {
	pending, err := h.pending(ctx, ev)
	if err == nil && len(pending) < len(ev.Related.Series) {
		return model.StatusDone
	}
	return model.StatusTimeout
}

// pending lists the series of ev that have not arrived. FRED sometimes
// posts a release the evening before its calendar date (Debt-to-GDP
// 2026-10-29 was updated 10-28 19:22 ET), so non-daily series count from
// midnight of the day before; a daily series was updated the day before
// by the previous release and must be updated on the day itself.
func (h *FREDRelease) pending(ctx context.Context, ev model.ReleaseEvent) ([]string, error) {
	day := releaseDay(ev, h.Config.Calendar.ReleaseLocation)
	var out []string
	for _, id := range ev.Related.Series {
		lu, ok, err := ingest.FREDLastUpdated(ctx, h.Store, id)
		if err != nil {
			return nil, err
		}
		from := day
		meta, known, err := h.Store.Meta(ctx, "fred", id)
		if err != nil {
			return nil, err
		}
		if known && meta.Frequency != "" && meta.Frequency != "d" {
			from = day.AddDate(0, 0, -1)
		}
		if !ok || lu.Before(from) {
			out = append(out, id)
		}
	}
	return out, nil
}

// FedDocument polls a Fed text source until a document of DocType
// published on the event day or later is stored.
type FedDocument struct {
	Texts    *ingest.Texts
	Source   sources.TextSource
	Store    *store.Store
	DocType  string
	Location *time.Location
}

// Poll implements Handler.
func (h *FedDocument) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	if ok, err := h.have(ctx, ev); ok || err != nil {
		return ok, err
	}
	res := h.Texts.Run(ctx, h.Source)
	ok, err := h.have(ctx, ev)
	if err == nil {
		err = res.Err
	}
	return ok, err
}

// Expire implements Expirer: done if the document is stored anyway.
func (h *FedDocument) Expire(ctx context.Context, ev model.ReleaseEvent) string {
	return statusOf(h.have(ctx, ev))
}

// statusOf maps a has-the-data check to done or timeout.
func statusOf(ok bool, err error) string {
	if err == nil && ok {
		return model.StatusDone
	}
	return model.StatusTimeout
}

func (h *FedDocument) have(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	docs, err := h.Store.LatestDocuments(ctx, fedtexts.Source, h.DocType, 1)
	if err != nil || len(docs) == 0 {
		return false, err
	}
	return !docs[0].PublishedAt.Before(releaseDay(ev, h.Location)), nil
}
