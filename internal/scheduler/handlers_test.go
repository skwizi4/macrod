package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"slices"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/fred"
	"macro-collector/internal/store"
)

// fakeFRED serves ICSA/CCSA with a settable last_updated; fetched lists
// the series asked about (metadata requests).
type fakeFRED struct {
	updated map[string]time.Time
	freq    map[string]string // default "w"
	fetched []string
}

func (f *fakeFRED) FetchMeta(_ context.Context, id string) (fred.Meta, error) {
	f.fetched = append(f.fetched, id)
	freq := f.freq[id]
	if freq == "" {
		freq = "w"
	}
	return fred.Meta{ID: id, Frequency: freq, Units: "Number", LastUpdated: f.updated[id]}, nil
}

func (f *fakeFRED) FetchSeries(_ context.Context, id string, _ time.Time) ([]model.Observation, error) {
	return []model.Observation{{Date: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), Value: float64(f.updated[id].Unix() % 1000)}}, nil
}

func nyLoc(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func testConfig(t *testing.T) *config.Config {
	return &config.Config{
		FRED: config.FRED{Series: []config.Series{
			{ID: "ICSA", Agent: "usa_monetary", Label: "Initial Claims"},
			{ID: "CCSA", Agent: "usa_monetary", Label: "Continued Claims"},
			{ID: "DGS10", Agent: "usa_fiscal", Label: "UST 10Y"},
		}},
		Calendar: config.Calendar{ReleaseLocation: nyLoc(t)},
	}
}

func TestFREDReleaseHandler(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	ny := nyLoc(t)
	ev := model.ReleaseEvent{ScheduledAt: t0, Related: model.Related{Series: []string{"CCSA", "ICSA"}}}
	before := time.Date(2026, 10, 22, 7, 31, 0, 0, ny) // last week's release
	client := &fakeFRED{updated: map[string]time.Time{"ICSA": before, "CCSA": before}}
	h := &FREDRelease{Config: testConfig(t), Store: st, Client: client, Root: slog.New(slog.DiscardHandler)}

	// Nothing new yet: both fetched, not done; at the window end: timeout.
	if done, err := h.Poll(ctx, ev); done || err != nil {
		t.Fatalf("poll 1: %v %v", done, err)
	}
	if h.Expire(ctx, ev) != model.StatusTimeout {
		t.Error("expire with nothing new should time out")
	}
	// ICSA arrives: only CCSA is still asked about; Expire would now close as done.
	client.updated["ICSA"] = time.Date(2026, 10, 29, 7, 32, 0, 0, ny)
	if done, _ := h.Poll(ctx, ev); done {
		t.Fatal("done with CCSA pending")
	}
	client.fetched = nil
	if done, _ := h.Poll(ctx, ev); done || !slices.Equal(client.fetched, []string{"CCSA"}) {
		t.Fatalf("fetched %v", client.fetched)
	}
	if h.Expire(ctx, ev) != model.StatusDone {
		t.Error("partial release should close as done")
	}
	client.updated["CCSA"] = time.Date(2026, 10, 29, 7, 33, 0, 0, ny)
	if done, err := h.Poll(ctx, ev); !done || err != nil {
		t.Fatalf("final poll: %v %v", done, err)
	}
	if lu, ok, _ := ingest.FREDLastUpdated(ctx, st, "CCSA"); !ok || !lu.Equal(client.updated["CCSA"]) {
		t.Errorf("CCSA cursor = %v", lu)
	}
}

// FRED posted Debt-to-GDP of 2026-10-29 at 19:22 ET the evening before: a
// non-daily series counts from the previous day, a daily one does not (its
// update the day before was the previous release).
func TestFREDReleaseUpdatedEveningBefore(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	ny := nyLoc(t)
	ev := model.ReleaseEvent{ScheduledAt: t0, Related: model.Related{Series: []string{"ICSA", "CCSA"}}}
	evening := time.Date(2026, 10, 28, 19, 22, 0, 0, ny)
	// CCSA plays a daily series here.
	client := &fakeFRED{updated: map[string]time.Time{"ICSA": time.Date(2026, 10, 27, 23, 0, 0, 0, ny), "CCSA": evening},
		freq: map[string]string{"CCSA": "d"}}
	h := &FREDRelease{Config: testConfig(t), Store: st, Client: client, Root: slog.New(slog.DiscardHandler)}

	// Two days before: a weekly series has not arrived either.
	if done, err := h.Poll(ctx, ev); done || err != nil {
		t.Fatalf("poll 1: %v %v", done, err)
	}
	if pending, _ := h.pending(ctx, ev); !slices.Equal(pending, []string{"ICSA", "CCSA"}) {
		t.Errorf("pending = %v, want [ICSA CCSA]", pending)
	}
	client.updated["ICSA"] = evening
	if done, err := h.Poll(ctx, ev); done || err != nil {
		t.Fatalf("poll 2: %v %v", done, err)
	}
	if pending, _ := h.pending(ctx, ev); !slices.Equal(pending, []string{"CCSA"}) {
		t.Errorf("pending = %v, want [CCSA]", pending)
	}
}

type fakeTextSource struct {
	docs  []model.Document
	calls int
}

func (f *fakeTextSource) Name() string { return "fed:press_monetary" }

func (f *fakeTextSource) FetchDocuments(context.Context, string) ([]model.Document, string, error) {
	f.calls++
	return f.docs, "", nil
}

func TestFedDocumentHandler(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	src := &fakeTextSource{docs: []model.Document{{Source: "fed", ExternalID: "fomcminutes20260729", DocType: "minutes",
		PublishedAt: time.Date(2026, 8, 19, 18, 0, 0, 0, time.UTC), Body: "July minutes"}}}
	h := &FedDocument{Texts: &ingest.Texts{Store: st, Log: slog.New(slog.DiscardHandler)}, Source: src,
		Store: st, DocType: "minutes", Location: nyLoc(t)}
	ev := model.ReleaseEvent{ScheduledAt: time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)}
	// Only older minutes in the feed: not done.
	if done, err := h.Poll(ctx, ev); done || err != nil {
		t.Fatalf("old minutes: %v %v", done, err)
	}
	src.docs = append(src.docs, model.Document{Source: "fed", ExternalID: "fomcminutes20260916", DocType: "minutes",
		PublishedAt: time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC), Body: "September minutes"})
	if done, err := h.Poll(ctx, ev); !done || err != nil {
		t.Fatalf("new minutes: %v %v", done, err)
	}
	// Already stored: done without fetching.
	calls := src.calls
	if done, _ := h.Poll(ctx, ev); !done || src.calls != calls {
		t.Error("refetched a stored document")
	}
}

func TestUncoveredDaily(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.FRED.Series = append(cfg.FRED.Series, config.Series{ID: "THREEFYTP10", Agent: "usa_fiscal", Label: "Term premium"})
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		for id, f := range map[string]string{"ICSA": "w", "DGS10": "d", "THREEFYTP10": "d"} {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: "fred", SeriesID: id, Label: id, Agent: "usa_fiscal", Frequency: f}); err != nil {
				return err
			}
		}
		// H.15 (DGS10) has an event today; the term premium has none.
		h15 := time.Date(2026, 10, 29, 20, 15, 0, 0, time.UTC)
		_, err := store.SyncReleases(tx, "fred_release", "H.15", h15, h15.Add(time.Second), []model.ReleaseEvent{{
			Source: "fred_release", Name: "H.15", ScheduledAt: h15, Region: "usa", Related: model.Related{Series: []string{"DGS10"}}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// 23:00 Bratislava = 17:00 ET on 2026-10-29.
	ids, err := uncoveredDaily(ctx, cfg, st, time.Date(2026, 10, 29, 22, 0, 0, 0, time.UTC))
	if err != nil || !slices.Equal(ids, []string{"THREEFYTP10"}) {
		t.Fatalf("uncovered = %v, %v", ids, err)
	}
}
