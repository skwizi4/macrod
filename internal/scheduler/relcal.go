package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/store"
)

// SeriesRelease polls the sources of the series a release calendar event
// publishes (Eurostat, ZEW, EC survey: spec 8.1, phase 11). A series has
// arrived when a row of it changed on the day before the release or later
// (a monthly or quarterly series is not touched between its releases); the
// event is done when all have. An event without series is done at once:
// it is in the calendar only.
type SeriesRelease struct {
	Config  *config.Config
	Store   *store.Store
	Numeric []collect.NumericRunner
	Root    *slog.Logger
}

// Poll implements Handler.
func (h *SeriesRelease) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	pending, err := h.pending(ctx, ev)
	if err != nil || len(pending) == 0 {
		return err == nil, err
	}
	var sources []string
	for _, s := range pending {
		if !slices.Contains(sources, s.Source) {
			sources = append(sources, s.Source)
		}
	}
	var errs []error
	for _, src := range sources {
		i := slices.IndexFunc(h.Numeric, func(r collect.NumericRunner) bool { return r.Name() == src })
		if i < 0 {
			errs = append(errs, fmt.Errorf("%s: no loader for source %s", ev.Name, src))
			continue
		}
		// Only the source's own failures: a derived series that cannot be
		// computed yet is not this release's.
		sum := collect.RunNumeric(ctx, h.Config, h.Store, h.Numeric[i], h.Root, ingest.Incremental)
		if f := sum.FRED.Failed(); len(f) > 0 {
			errs = append(errs, fmt.Errorf("%s: %d series of %s failed: %w", ev.Name, len(f), src, f[0].Err))
		}
	}
	if pending, err = h.pending(ctx, ev); err != nil {
		return false, err
	}
	return len(pending) == 0, errors.Join(errs...)
}

// Expire implements Expirer: a release with some series updated is done.
func (h *SeriesRelease) Expire(ctx context.Context, ev model.ReleaseEvent) string {
	pending, err := h.pending(ctx, ev)
	if err == nil && (len(ev.Related.Series) == 0 || len(pending) < len(ev.Related.Series)) {
		return model.StatusDone
	}
	return model.StatusTimeout
}

func (h *SeriesRelease) pending(ctx context.Context, ev model.ReleaseEvent) ([]config.SeriesDef, error) {
	from := releaseDay(ev, h.location(ev)).AddDate(0, 0, -1)
	var out []config.SeriesDef
	for _, id := range ev.Related.Series {
		def, ok := h.Config.ResolveSeries(id)
		if !ok {
			continue // dropped from the config since the event was stored
		}
		t, ok, err := h.Store.LastChange(ctx, def.Source, def.ID)
		if err != nil {
			return nil, err
		}
		if !ok || t.Before(from) {
			out = append(out, def)
		}
	}
	return out, nil
}

// location is the timezone of the event's calendar.
func (h *SeriesRelease) location(ev model.ReleaseEvent) *time.Location {
	for _, c := range h.Config.ReleaseCalendars {
		if c.Source() == ev.Source && c.Location != nil {
			return c.Location
		}
	}
	return time.UTC
}

// BundAuction polls the results workbook of the Finanzagentur until the
// planned auction is in it: the ISIN on the day, or, for a range of terms
// without one, any auction of the security on the day.
type BundAuction struct {
	Config *config.Config
	Store  *store.Store
	Client *finanzagentur.Client
	Root   *slog.Logger
	Now    func() time.Time // nil: time.Now
}

// Poll implements Handler.
func (h *BundAuction) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	if ok, err := h.held(ctx, ev); err != nil || ok {
		return ok, err
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	res := collect.RunBundAuctions(ctx, h.Config, h.Store, h.Client, h.Root, ingest.Incremental, now())
	ok, err := h.held(ctx, ev)
	return ok, errors.Join(res.Err, err)
}

func (h *BundAuction) held(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	day := releaseDay(ev, h.Config.Finanzagentur.Location)
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	as, err := h.Store.AuctionsOf(ctx, finanzagentur.Source, d, d)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(as, func(a model.Auction) bool {
		if ev.Related.CUSIP != "" {
			return a.CUSIP == ev.Related.CUSIP
		}
		return a.Kind == ev.Related.Security
	}), nil
}
