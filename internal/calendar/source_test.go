package calendar

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/store"
)

type fakeSource struct {
	events   []model.ReleaseEvent
	err      error
	from, to time.Time
}

func (f *fakeSource) Name() string { return "fed" }

func (f *fakeSource) Upcoming(_ context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	f.from, f.to = from, to
	return f.events, f.err
}

func fomcEvent(at string) model.ReleaseEvent {
	t, _ := time.Parse(time.RFC3339, at)
	return model.ReleaseEvent{Source: "fomc", Name: "FOMC decision", ScheduledAt: t, Region: "usa", Importance: "high"}
}

func newSource(t *testing.T, src *fakeSource) (*Source, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ny, _ := time.LoadLocation("America/New_York")
	return &Source{
		Source: src, Keys: [][2]string{{"fomc", "FOMC decision"}, {"fomc", "FOMC minutes"}},
		Store: st, Calendar: &config.Calendar{HorizonDays: 14, ReleaseLocation: ny},
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return monday },
	}, st
}

func TestSourceRefresh(t *testing.T) {
	src := &fakeSource{events: []model.ReleaseEvent{fomcEvent("2026-10-28T18:00:00Z")}}
	s, st := newSource(t, src)
	ctx := context.Background()
	rep, err := s.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Events != 1 || rep.Inserted != 1 {
		t.Fatalf("report = %+v", rep)
	}
	// Window: midnight ET of 2026-10-26 to midnight ET after 2026-11-09.
	if got := src.from.UTC().Format(time.RFC3339); got != "2026-10-26T04:00:00Z" {
		t.Errorf("from = %s", got)
	}
	if got := src.to.UTC().Format(time.RFC3339); got != "2026-11-10T05:00:00Z" {
		t.Errorf("to = %s", got)
	}

	// The meeting moves a day: old event rescheduled.
	src.events = []model.ReleaseEvent{fomcEvent("2026-10-29T18:00:00Z")}
	if rep, err = s.Refresh(ctx); err != nil || rep.Inserted != 1 || rep.Rescheduled != 1 {
		t.Fatalf("move: %+v %v", rep, err)
	}
	evs, _ := st.Releases(ctx, monday, monday.AddDate(0, 0, 14))
	if len(evs) != 2 || evs[0].Status != model.StatusRescheduled || evs[1].Status != model.StatusScheduled {
		t.Fatalf("events = %+v", evs)
	}
	if fs, _, _ := st.FetchStateOf(ctx, "calendar:fed"); fs.Cursor != "2026-11-09" {
		t.Errorf("cursor = %q", fs.Cursor)
	}
}

func TestSourceRefreshErrors(t *testing.T) {
	src := &fakeSource{err: errors.New("page down")}
	s, st := newSource(t, src)
	ctx := context.Background()
	if _, err := s.Refresh(ctx); err == nil {
		t.Fatal("want error")
	}
	if fs, ok, _ := st.FetchStateOf(ctx, "calendar:fed"); !ok || fs.LastError != "page down" {
		t.Errorf("fetch_state = %+v", fs)
	}

	src.err = nil
	src.events = []model.ReleaseEvent{{Source: "beige_book", Name: "Beige Book", ScheduledAt: monday}}
	if _, err := s.Refresh(ctx); err == nil || !strings.Contains(err.Error(), "unexpected event series") {
		t.Errorf("err = %v", err)
	}
}

// Dynamic sources: names come from the events, and a stored event that the
// source stops listing is still synced (marked rescheduled).
func TestSourceDynamicKeys(t *testing.T) {
	auction := func(name, at string) model.ReleaseEvent {
		ts, _ := time.Parse(time.RFC3339, at)
		return model.ReleaseEvent{Source: "treasury_auction", Name: name, ScheduledAt: ts, Region: "usa", Importance: "high"}
	}
	src := &fakeSource{events: []model.ReleaseEvent{
		auction("10-Year Note", "2026-10-27T17:00:00Z"), auction("13-Week Bill", "2026-10-26T15:30:00Z"),
	}}
	s, st := newSource(t, src)
	s.Dynamic = []string{"treasury_auction"}
	ctx := context.Background()
	if rep, err := s.Refresh(ctx); err != nil || rep.Inserted != 2 {
		t.Fatalf("first: %+v %v", rep, err)
	}
	src.events = []model.ReleaseEvent{auction("10-Year Note", "2026-10-27T17:00:00Z")}
	rep, err := s.Refresh(ctx)
	if err != nil || rep.Inserted != 0 || rep.Rescheduled != 1 {
		t.Fatalf("second: %+v %v", rep, err)
	}
	evs, _ := st.Releases(ctx, monday, monday.AddDate(0, 0, 14))
	for _, e := range evs {
		want := model.StatusScheduled
		if e.Name == "13-Week Bill" {
			want = model.StatusRescheduled
		}
		if e.Status != want {
			t.Errorf("%s status = %s, want %s", e.Name, e.Status, want)
		}
	}
	// A source outside Dynamic still may not invent series.
	src.events = []model.ReleaseEvent{{Source: "other", Name: "x", ScheduledAt: monday.Add(time.Hour), Region: "usa"}}
	if _, err := s.Refresh(ctx); err == nil || !strings.Contains(err.Error(), "unexpected event series") {
		t.Errorf("err = %v", err)
	}
}

// A PartialError syncs the listed series, keeps the failed ones as stored
// (not rescheduled) and records the error.
func TestSourcePartialError(t *testing.T) {
	minutes := fomcEvent("2026-10-29T18:00:00Z")
	minutes.Name = "FOMC minutes"
	src := &fakeSource{events: []model.ReleaseEvent{fomcEvent("2026-10-28T18:00:00Z"), minutes}}
	s, st := newSource(t, src)
	ctx := context.Background()
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	src.events = []model.ReleaseEvent{fomcEvent("2026-10-28T18:00:00Z")}
	src.err = &sources.PartialError{Keys: [][2]string{{"fomc", "FOMC minutes"}}, Err: errors.New("minutes page down")}
	rep, err := s.Refresh(ctx)
	if err == nil || !strings.Contains(err.Error(), "minutes page down") {
		t.Fatalf("err = %v", err)
	}
	if rep.Events != 1 || rep.Rescheduled != 0 {
		t.Errorf("report = %+v", rep)
	}
	evs, _ := st.Releases(ctx, monday, monday.AddDate(0, 0, 14))
	if len(evs) != 2 || evs[0].Status != model.StatusScheduled || evs[1].Status != model.StatusScheduled {
		t.Errorf("events = %+v", evs)
	}
	if fs, _, _ := st.FetchStateOf(ctx, "calendar:fed"); fs.LastError != "minutes page down" || fs.Cursor != "2026-11-09" {
		t.Errorf("fetch_state = %+v", fs)
	}
	// A failed series may not come back in the events.
	src.events = append(src.events, minutes)
	if _, err := s.Refresh(ctx); err == nil || !strings.Contains(err.Error(), "unexpected event series") {
		t.Errorf("err = %v", err)
	}
}
