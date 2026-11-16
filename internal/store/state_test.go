package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func TestFetchStateLifecycle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	t1 := time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)
	t2 := t1.Add(24 * time.Hour)
	t3 := t2.Add(24 * time.Hour)
	write := func(fn func(*sql.Tx) error) {
		t.Helper()
		if err := s.Write(ctx, fn); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok, err := s.FetchStateOf(ctx, "fred:DGS10"); ok || err != nil {
		t.Fatalf("empty state: ok=%v err=%v", ok, err)
	}

	// Failure before any success: no cursor.
	write(func(tx *sql.Tx) error { return SetFetchError(tx, "fred:DGS10", errors.New("timeout"), t1) })
	st, ok, err := s.FetchStateOf(ctx, "fred:DGS10")
	if err != nil || !ok || st.Cursor != "" || st.LastError != "timeout" || !st.LastErrorAt.Equal(t1) {
		t.Fatalf("after error: %+v ok=%v err=%v", st, ok, err)
	}

	// Success sets cursor and clears the error.
	write(func(tx *sql.Tx) error { return SetFetchOK(tx, "fred:DGS10", "c1", t2) })
	st, _, _ = s.FetchStateOf(ctx, "fred:DGS10")
	if st.Cursor != "c1" || !st.UpdatedAt.Equal(t2) || st.LastError != "" || !st.LastErrorAt.IsZero() {
		t.Fatalf("after ok: %+v", st)
	}

	// Later failure keeps cursor and last success time.
	write(func(tx *sql.Tx) error { return SetFetchError(tx, "fred:DGS10", errors.New("503"), t3) })
	st, _, _ = s.FetchStateOf(ctx, "fred:DGS10")
	if st.Cursor != "c1" || !st.UpdatedAt.Equal(t2) || st.LastError != "503" || !st.LastErrorAt.Equal(t3) {
		t.Fatalf("after second error: %+v", st)
	}
}

func TestSeriesMetaAndLastObsDate(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, ok, err := s.LastObsDate(ctx, "fred", "DGS10"); ok || err != nil {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
	err := s.Write(ctx, func(tx *sql.Tx) error {
		m := SeriesMeta{Source: "fred", SeriesID: "DGS10", Label: "old", Agent: "usa_fiscal", Frequency: "d", Units: "Percent"}
		if err := UpsertSeriesMeta(tx, m); err != nil {
			return err
		}
		m.Label = "UST 10Y"
		if err := UpsertSeriesMeta(tx, m); err != nil {
			return err
		}
		_, err := UpsertObservations(tx, "fred", "DGS10", []model.Observation{
			{Date: d("2026-10-21"), Value: 4.1}, {Date: d("2026-10-22"), Value: 4.2},
		}, time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var label string
	if err := s.ReadDB().QueryRow(`SELECT label FROM series_meta WHERE series_id = 'DGS10'`).Scan(&label); err != nil || label != "UST 10Y" {
		t.Errorf("label = %q, %v", label, err)
	}
	last, ok, err := s.LastObsDate(ctx, "fred", "DGS10")
	if err != nil || !ok || !last.Equal(d("2026-10-22")) {
		t.Errorf("last = %v ok=%v err=%v", last, ok, err)
	}
}

func TestSeriesReleases(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		// Existing row keeps label and units; a new row is created.
		if err := UpsertSeriesMeta(tx, SeriesMeta{Source: "fred", SeriesID: "CPIAUCSL", Label: "CPI", Agent: "usa_monetary", Frequency: "m", Units: "Index"}); err != nil {
			return err
		}
		if err := UpsertSeriesMeta(tx, SeriesMeta{Source: "fred", SeriesID: "GDPC1", Label: "GDP", Agent: "usa_monetary", Frequency: "q", Units: "Billions"}); err != nil {
			return err
		}
		if err := SetSeriesRelease(tx, "fred", "CPIAUCSL", "ignored", "usa_monetary", 10); err != nil {
			return err
		}
		return SetSeriesRelease(tx, "fred", "ICSA", "Initial Claims", "usa_monetary", 180)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SeriesReleases(ctx, "fred")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["CPIAUCSL"] != 10 || got["ICSA"] != 180 {
		t.Fatalf("releases = %v", got)
	}
	var label string
	var units sql.NullString
	if err := s.ReadDB().QueryRow(`SELECT label, units FROM series_meta WHERE series_id = 'CPIAUCSL'`).Scan(&label, &units); err != nil || label != "CPI" || units.String != "Index" {
		t.Errorf("CPIAUCSL meta = %q %q %v", label, units.String, err)
	}
	// A later UpsertSeriesMeta keeps release_id.
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		return UpsertSeriesMeta(tx, SeriesMeta{Source: "fred", SeriesID: "ICSA", Label: "Initial Claims", Agent: "usa_monetary", Frequency: "w", Units: "Number"})
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SeriesReleases(ctx, "fred"); got["ICSA"] != 180 {
		t.Errorf("ICSA release after meta upsert = %d", got["ICSA"])
	}
}
