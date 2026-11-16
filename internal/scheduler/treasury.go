package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/store"
)

// TreasuryRunner runs Treasury datasets one at a time: several auctions
// close at 11:30 together, and their pollers must not fetch in parallel.
type TreasuryRunner struct {
	Ingest *ingest.Treasury
	mu     sync.Mutex
}

// Dataset fetches one dataset incrementally.
func (r *TreasuryRunner) Dataset(ctx context.Context, name string) (ingest.DatasetResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.Ingest.RunDataset(ctx, ingest.Incremental, name)
	if !ok {
		return res, fmt.Errorf("treasury dataset %s is not enabled", name)
	}
	return res, res.Err
}

// All fetches every enabled dataset (the daily safety job).
func (r *TreasuryRunner) All(ctx context.Context) ingest.TreasuryReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Ingest.Run(ctx, ingest.Incremental)
}

// TreasuryDataset polls one dataset until Arrived reports the event's data.
type TreasuryDataset struct {
	Runner  *TreasuryRunner
	Dataset string
	Arrived func(ctx context.Context, ev model.ReleaseEvent) (bool, error)
	// Fresh, if set, decides the status when the window ends (see Expire).
	Fresh func(ctx context.Context, ev model.ReleaseEvent) (bool, error)
	// Also are enabled datasets of the same publication, fetched once the
	// event's data has arrived (DTS: public debt transactions).
	Also []string
}

// Poll implements Handler.
func (h *TreasuryDataset) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	if ok, err := h.Arrived(ctx, ev); ok || err != nil {
		return ok, err
	}
	_, fetchErr := h.Runner.Dataset(ctx, h.Dataset)
	ok, err := h.Arrived(ctx, ev)
	if err == nil {
		err = fetchErr
	}
	if ok {
		for _, name := range h.Also {
			if _, alsoErr := h.Runner.Dataset(ctx, name); alsoErr != nil && err == nil {
				err = alsoErr
			}
		}
	}
	return ok, err
}

// Expire implements Expirer: an event whose data arrived without this
// poller (a missed event, the daily safety job) is done; with Fresh set,
// one whose expected date never came (a holiday shifted it) is done if the
// data is fresh anyway.
func (h *TreasuryDataset) Expire(ctx context.Context, ev model.ReleaseEvent) string {
	if ok, err := h.Arrived(ctx, ev); err == nil && ok {
		return model.StatusDone
	}
	if h.Fresh != nil {
		if ok, err := h.Fresh(ctx, ev); err == nil && ok {
			return model.StatusDone
		}
	}
	return model.StatusTimeout
}

// prevWeekday is the weekday before day.
func prevWeekday(day time.Time) time.Time {
	d := day.AddDate(0, 0, -1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// dtsArrived: the DTS published on the event day covers the previous
// business day.
func dtsArrived(st *store.Store, et *time.Location) func(context.Context, model.ReleaseEvent) (bool, error) {
	return func(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
		last, ok, err := st.LastObsDate(ctx, treasury.Source, treasury.SeriesTGA)
		if err != nil || !ok {
			return false, err
		}
		return !last.Before(prevWeekday(model.Day(releaseDay(ev, et)))), nil
	}
}

// dtsFresh: the TGA is at most a few days old (holidays move the dates).
func dtsFresh(st *store.Store, et *time.Location) func(context.Context, model.ReleaseEvent) (bool, error) {
	return func(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
		last, ok, err := st.LastObsDate(ctx, treasury.Source, treasury.SeriesTGA)
		if err != nil || !ok {
			return false, err
		}
		return !last.Before(model.Day(releaseDay(ev, et)).AddDate(0, 0, -5)), nil
	}
}

// mtsArrived: the statement published in month M reports month M−1.
func mtsArrived(st *store.Store, et *time.Location) func(context.Context, model.ReleaseEvent) (bool, error) {
	return func(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
		last, ok, err := st.LastObsDate(ctx, treasury.Source, treasury.SeriesMTSDeficit)
		if err != nil || !ok {
			return false, err
		}
		d := releaseDay(ev, et)
		want := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		return !last.Before(want), nil
	}
}

// auctionArrived: the event's auction (CUSIP on the event day) has results.
func auctionArrived(st *store.Store, et *time.Location) func(context.Context, model.ReleaseEvent) (bool, error) {
	return func(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
		day := model.Day(releaseDay(ev, et))
		as, err := st.Auctions(ctx, day, day)
		if err != nil {
			return false, err
		}
		for _, a := range as {
			if a.CUSIP == ev.Related.CUSIP {
				return a.HasResults(), nil
			}
		}
		return false, nil
	}
}

// TreasuryDocument polls the refunding page until a document of DocType
// published on the event day or later is stored.
type TreasuryDocument struct {
	Texts    *ingest.Texts
	Source   sources.TextSource
	Store    *store.Store
	DocType  string
	Location *time.Location
}

// Poll implements Handler.
func (h *TreasuryDocument) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
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
func (h *TreasuryDocument) Expire(ctx context.Context, ev model.ReleaseEvent) string {
	return statusOf(h.have(ctx, ev))
}

func (h *TreasuryDocument) have(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	docs, err := h.Store.LatestDocuments(ctx, treasury.Source, h.DocType, 1)
	if err != nil || len(docs) == 0 {
		return false, err
	}
	return !docs[0].PublishedAt.Before(releaseDay(ev, h.Location)), nil
}

// treasuryRoutes are the event routes of the Treasury calendar.
func treasuryRoutes(cfg *config.Config, st *store.Store, runner *TreasuryRunner, texts *ingest.Texts,
	refunding sources.TextSource) []Route {
	et := cfg.Calendar.ReleaseLocation
	var routes []Route
	if _, ok := cfg.Treasury.Dataset(config.DatasetDTS); ok {
		var also []string
		if _, ok := cfg.Treasury.Dataset(config.DatasetPublicDebt); ok {
			also = append(also, config.DatasetPublicDebt)
		}
		routes = append(routes, Route{Source: treasury.Source, Name: treasury.NameDTS, Handler: &TreasuryDataset{
			Runner: runner, Dataset: config.DatasetDTS, Arrived: dtsArrived(st, et), Fresh: dtsFresh(st, et), Also: also}})
	}
	if _, ok := cfg.Treasury.Dataset(config.DatasetMTS); ok {
		routes = append(routes, Route{Source: treasury.Source, Name: treasury.NameMTS, Handler: &TreasuryDataset{
			Runner: runner, Dataset: config.DatasetMTS, Arrived: mtsArrived(st, et)}})
	}
	if _, ok := cfg.Treasury.Dataset(config.DatasetAuctions); ok {
		routes = append(routes, Route{Source: treasury.SourceAuction, Handler: &TreasuryDataset{
			Runner: runner, Dataset: config.DatasetAuctions, Arrived: auctionArrived(st, et)}})
	}
	doc := func(docType string) Handler {
		return &TreasuryDocument{Texts: texts, Source: refunding, Store: st, DocType: docType, Location: et}
	}
	return append(routes,
		Route{Source: treasury.Source, Name: treasury.NameFinancingEstimates, Handler: doc(treasury.DocBorrowingEstimates)},
		Route{Source: treasury.Source, Name: treasury.NameRefundingStatement, Handler: doc(treasury.DocRefunding)},
	)
}
