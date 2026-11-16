package store

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func cpi(when string) model.ReleaseEvent {
	return model.ReleaseEvent{
		Source: "fred_release", Name: "Consumer Price Index", ScheduledAt: at(when), Region: "usa",
		Related: model.Related{ReleaseID: 10, Series: []string{"CPIAUCSL", "CPILFESL"}},
	}
}

func syncCPI(t *testing.T, s *Store, from, to time.Time, events ...model.ReleaseEvent) SyncResult {
	t.Helper()
	var res SyncResult
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		res, err = SyncReleases(tx, "fred_release", "Consumer Price Index", from, to, events)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func statuses(t *testing.T, s *Store, from, to time.Time) map[string]string {
	t.Helper()
	evs, err := s.Releases(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string)
	for _, e := range evs {
		out[e.ScheduledAt.UTC().Format(time.RFC3339)] = e.Status
	}
	return out
}

func TestSyncReleasesInsertUpdateReschedule(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	from, to := at("2026-10-24T00:00:00Z"), at("2026-12-31T00:00:00Z")

	res := syncCPI(t, s, from, to, cpi("2026-11-10T13:30:00Z"), cpi("2026-12-10T13:30:00Z"))
	if res != (SyncResult{Inserted: 2}) {
		t.Fatalf("first sync = %+v", res)
	}
	// Same schedule again: no new rows, no reschedules.
	if res := syncCPI(t, s, from, to, cpi("2026-11-10T13:30:00Z"), cpi("2026-12-10T13:30:00Z")); res != (SyncResult{Updated: 2}) {
		t.Fatalf("repeat sync = %+v", res)
	}
	if n := count(t, s, `SELECT count(*) FROM releases`); n != 2 {
		t.Fatalf("rows = %d", n)
	}

	// December release moves to the 11th: old row rescheduled, new inserted.
	res = syncCPI(t, s, from, to, cpi("2026-11-10T13:30:00Z"), cpi("2026-12-11T13:30:00Z"))
	if res != (SyncResult{Inserted: 1, Updated: 1, Rescheduled: 1}) {
		t.Fatalf("move sync = %+v", res)
	}
	want := map[string]string{
		"2026-11-10T13:30:00Z": model.StatusScheduled,
		"2026-12-10T13:30:00Z": model.StatusRescheduled,
		"2026-12-11T13:30:00Z": model.StatusScheduled,
	}
	if got := statuses(t, s, from, to); !mapsEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}

	// Events outside the window and non-scheduled ones are not rescheduled.
	evs, _ := s.Releases(ctx, at("2026-11-10T00:00:00Z"), at("2026-11-11T00:00:00Z"))
	if len(evs) != 1 {
		t.Fatalf("november events = %d", len(evs))
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		return SetReleaseStatus(tx, evs[0].ID, model.StatusPolling, at("2026-11-10T13:30:00Z"))
	}); err != nil {
		t.Fatal(err)
	}
	if res := syncCPI(t, s, from, at("2026-12-01T00:00:00Z")); res != (SyncResult{}) {
		t.Fatalf("empty sync = %+v", res)
	}
	if got := statuses(t, s, from, to)["2026-11-10T13:30:00Z"]; got != model.StatusPolling {
		t.Errorf("polling event became %s", got)
	}
	if got := statuses(t, s, from, to)["2026-12-11T13:30:00Z"]; got != model.StatusScheduled {
		t.Errorf("event outside window became %s", got)
	}

	// A date the source lists again comes back from rescheduled.
	syncCPI(t, s, at("2026-12-01T00:00:00Z"), to, cpi("2026-12-10T13:30:00Z"))
	got := statuses(t, s, from, to)
	if got["2026-12-10T13:30:00Z"] != model.StatusScheduled || got["2026-12-11T13:30:00Z"] != model.StatusRescheduled {
		t.Errorf("after move back: %v", got)
	}
}

func TestSyncReleasesRejectsForeignEvent(t *testing.T) {
	s := openTest(t)
	e := cpi("2026-11-10T13:30:00Z")
	e.Name = "Producer Price Index"
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := SyncReleases(tx, "fred_release", "Consumer Price Index", at("2026-10-24T00:00:00Z"), at("2026-12-31T00:00:00Z"), []model.ReleaseEvent{e})
		return err
	})
	if err == nil {
		t.Fatal("want error for event of another series")
	}
}

func TestReleaseRoundTripAndStatus(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	fomc := model.ReleaseEvent{
		Source: "fomc", Name: "FOMC decision", ScheduledAt: at("2026-10-28T18:00:00Z"), Region: "usa",
		Importance: "high", Related: model.Related{Meeting: "2026-10-27/28"},
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := SyncReleases(tx, "fomc", "FOMC decision", at("2026-10-24T00:00:00Z"), at("2026-11-07T00:00:00Z"), []model.ReleaseEvent{fomc})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	syncCPI(t, s, at("2026-10-24T00:00:00Z"), at("2026-11-30T00:00:00Z"), cpi("2026-11-10T13:30:00Z"))

	// Bounds: from is inclusive, to exclusive, sub-second bounds handled.
	evs, err := s.Releases(ctx, at("2026-10-28T18:00:00Z"), at("2026-11-10T13:30:00Z").Add(time.Nanosecond))
	if err != nil || len(evs) != 2 {
		t.Fatalf("releases = %d, %v", len(evs), err)
	}
	if evs, _ := s.Releases(ctx, at("2026-10-28T18:00:00Z").Add(time.Nanosecond), at("2026-11-10T13:30:00Z")); len(evs) != 0 {
		t.Fatalf("exclusive bounds: %d events", len(evs))
	}

	evs, _ = s.Releases(ctx, at("2026-10-28T00:00:00Z"), at("2026-10-29T00:00:00Z"))
	got := evs[0]
	if !got.ScheduledAt.Equal(fomc.ScheduledAt) || got.Importance != "high" || got.Related.Meeting != "2026-10-27/28" ||
		got.Status != model.StatusScheduled || !got.DoneAt.IsZero() {
		t.Fatalf("round trip = %+v", got)
	}
	evs, _ = s.Releases(ctx, at("2026-11-10T00:00:00Z"), at("2026-11-11T00:00:00Z"))
	if r := evs[0].Related; r.ReleaseID != 10 || !slices.Equal(r.Series, []string{"CPIAUCSL", "CPILFESL"}) {
		t.Fatalf("related = %+v", r)
	}

	done := at("2026-10-28T18:00:40Z")
	if err := s.Write(ctx, func(tx *sql.Tx) error { return SetReleaseStatus(tx, got.ID, model.StatusDone, done) }); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveReleases(ctx, at("2026-10-24T00:00:00Z"), at("2026-12-01T00:00:00Z"))
	if err != nil || len(active) != 1 || active[0].Name != "Consumer Price Index" {
		t.Fatalf("active = %+v, %v", active, err)
	}
	evs, _ = s.Releases(ctx, at("2026-10-28T00:00:00Z"), at("2026-10-29T00:00:00Z"))
	if evs[0].Status != model.StatusDone || !evs[0].DoneAt.Equal(done) {
		t.Fatalf("done = %+v", evs[0])
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error { return SetReleaseStatus(tx, 999, model.StatusDone, done) }); err == nil {
		t.Error("want error for unknown release")
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
