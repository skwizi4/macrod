package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"macro-collector/internal/model"
	"macro-collector/migrations"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// A weekly series: history loaded on 1 October, then two releases, each
// revising the previous week.
func TestObservationsAsOf(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	load, rel1, rel2 := ts("2026-10-01T06:00:00Z"), ts("2026-10-22T12:31:00Z"), ts("2026-10-29T12:31:00Z")
	upsert(t, s, []model.Observation{{Date: d("2026-09-26"), Value: 100}, {Date: d("2026-10-03"), Value: 110}}, load)
	upsert(t, s, []model.Observation{{Date: d("2026-10-03"), Value: 111}, {Date: d("2026-10-17"), Value: 300}}, rel1)
	upsert(t, s, []model.Observation{{Date: d("2026-10-17"), Value: 310}, {Date: d("2026-10-24"), Value: 400}}, rel2)

	type want struct {
		date    string
		value   float64
		seen    time.Time
		initial bool
	}
	for _, tc := range []struct {
		name    string
		knownAt time.Time
		want    []want
	}{
		{"before the history load: history still counts", ts("2026-09-01T00:00:00Z"), []want{
			{"2026-09-26", 100, load, true}, {"2026-10-03", 110, load, true}}},
		{"W42 end: no later release, no later revision", ts("2026-10-18T14:00:00Z"), []want{
			{"2026-09-26", 100, load, true}, {"2026-10-03", 110, load, true}}},
		{"W43 end: first release and its revision", ts("2026-10-25T15:00:00Z"), []want{
			{"2026-09-26", 100, load, true}, {"2026-10-03", 111, rel1, false}, {"2026-10-17", 300, rel1, false}}},
		{"now: everything, latest values", ts("2026-11-01T15:00:00Z"), []want{
			{"2026-09-26", 100, load, true}, {"2026-10-03", 111, rel1, false}, {"2026-10-17", 310, rel2, false},
			{"2026-10-24", 400, rel2, false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ObservationsAsOf(ctx, "fred", "PAYEMS", time.Time{}, tc.knownAt)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows %v, want %d", len(got), got, len(tc.want))
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Date.Format(time.DateOnly) != w.date || g.Value != w.value || !g.Seen.Equal(w.seen) || g.Initial != w.initial {
					t.Errorf("row %d = %s %v seen %s initial %v, want %+v", i, g.Date.Format(time.DateOnly), g.Value,
						g.Seen.Format(time.RFC3339), g.Initial, w)
				}
			}
		})
	}

	// since cuts rows, not the history window of the series.
	got, err := s.ObservationsAsOf(ctx, "fred", "PAYEMS", d("2026-10-03"), ts("2026-10-25T15:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Value != 111 || got[1].Initial {
		t.Errorf("since 2026-10-03: %+v", got)
	}
	if got, err := s.ObservationsAsOf(ctx, "fred", "NONE", time.Time{}, time.Now()); err != nil || len(got) != 0 {
		t.Errorf("missing series: %v, %v", got, err)
	}
}

// Migration 0005 fills first_fetched_at of existing rows: fetched_at when
// never revised, else the earliest revision.
func TestMigration0005FirstFetched(t *testing.T) {
	ctx := context.Background()
	upTo4 := fstest.MapFS{}
	names, err := fs.Glob(migrations.FS, "000[1-4]_*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		b, err := fs.ReadFile(migrations.FS, n)
		if err != nil {
			t.Fatal(err)
		}
		upTo4[n] = &fstest.MapFile{Data: b}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "v4.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db, upTo4); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO observations (source, series_id, obs_date, value, fetched_at) VALUES
		 ('fred', 'ICSA', '2026-10-10', 210, '2026-10-22T12:31:00Z'),
		 ('fred', 'ICSA', '2026-10-17', 300, '2026-10-22T12:31:00Z')`,
		`INSERT INTO revisions (source, series_id, obs_date, old_value, new_value, detected_at) VALUES
		 ('fred', 'ICSA', '2026-10-10', 200, 205, '2026-10-15T12:31:00Z'),
		 ('fred', 'ICSA', '2026-10-10', 205, 210, '2026-10-22T12:31:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for date, want := range map[string]string{"2026-10-10": "2026-10-15T12:31:00Z", "2026-10-17": "2026-10-22T12:31:00Z"} {
		var got string
		if err := db.QueryRowContext(ctx, `SELECT first_fetched_at FROM observations WHERE obs_date = ?`, date).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s first_fetched_at = %s, want %s", date, got, want)
		}
	}
}
