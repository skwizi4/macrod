package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "sub", "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func d(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.ReadDB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenMigratesOnceAndReopens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "macro.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.SchemaVersion(ctx); err != nil || v != 15 {
		t.Fatalf("version = %d, %v; want 15", v, err)
	}
	tables := count(t, s, `SELECT count(*) FROM sqlite_master WHERE type = 'table'`)
	if tables != 20 {
		t.Errorf("tables = %d, want 20", tables)
	}
	var mode string
	if err := s.ReadDB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening must not re-apply 0001 (CREATE TABLE would fail).
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s.Close()
}

func TestReadPoolIsReadOnly(t *testing.T) {
	s := openTest(t)
	if _, err := s.ReadDB().Exec(`INSERT INTO fetch_state (source, updated_at) VALUES ('x', 'y')`); err == nil {
		t.Error("write through read pool succeeded")
	}
}

func upsert(t *testing.T, s *Store, obs []model.Observation, now time.Time) UpsertResult {
	t.Helper()
	var res UpsertResult
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		res, err = UpsertObservations(tx, "fred", "PAYEMS", obs, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestUpsertObservations(t *testing.T) {
	s := openTest(t)
	t1 := time.Date(2026, 10, 2, 12, 30, 0, 0, time.UTC)
	t2 := t1.Add(30 * 24 * time.Hour)
	first := []model.Observation{
		{Date: d("2026-07-01"), Value: 159000},
		{Date: d("2026-08-01"), Value: 159100},
		{Date: d("2026-09-01"), Value: 159250},
	}

	if got := upsert(t, s, first, t1); got != (UpsertResult{Inserted: 3}) {
		t.Errorf("first = %+v", got)
	}
	// Same data again: no duplicates, nothing touched.
	if got := upsert(t, s, first, t2); got != (UpsertResult{Unchanged: 3}) {
		t.Errorf("repeat = %+v", got)
	}
	if n := count(t, s, `SELECT count(*) FROM observations`); n != 3 {
		t.Errorf("rows = %d, want 3", n)
	}

	// Next release revises August and adds October.
	second := []model.Observation{
		{Date: d("2026-08-01"), Value: 159050},
		{Date: d("2026-09-01"), Value: 159250},
		{Date: d("2026-10-01"), Value: 159400},
	}
	if got := upsert(t, s, second, t2); got != (UpsertResult{Inserted: 1, Revised: 1, Unchanged: 1}) {
		t.Errorf("second = %+v", got)
	}

	var oldV, newV float64
	var obsDate time.Time
	err := s.ReadDB().QueryRow(`SELECT obs_date, old_value, new_value FROM revisions`).Scan(&obsDate, &oldV, &newV)
	if err != nil || !obsDate.Equal(d("2026-08-01")) || oldV != 159100 || newV != 159050 {
		t.Errorf("revision = %s %v→%v, %v", obsDate, oldV, newV, err)
	}
	// On disk dates are plain 'YYYY-MM-DD' text, so SQL comparisons work.
	if n := count(t, s, `SELECT count(*) FROM observations WHERE typeof(obs_date) = 'text' AND obs_date >= '2026-08-01'`); n != 3 {
		t.Errorf("text dates >= 2026-08-01 = %d, want 3", n)
	}

	// fetched_at: first-seen time kept for unchanged rows, bumped on revision.
	fetched := func(date string) string {
		var ts string
		if err := s.ReadDB().QueryRow(`SELECT fetched_at FROM observations WHERE obs_date = ?`, date).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		return ts
	}
	if got := fetched("2026-07-01"); got != formatTS(t1) {
		t.Errorf("July fetched_at = %s, want %s", got, formatTS(t1))
	}
	if got := fetched("2026-08-01"); got != formatTS(t2) {
		t.Errorf("August fetched_at = %s, want %s", got, formatTS(t2))
	}

	got, err := s.Observations(context.Background(), "fred", "PAYEMS", d("2026-08-01"))
	if err != nil {
		t.Fatal(err)
	}
	want := []model.Observation{second[0], second[1], second[2]}
	if len(got) != len(want) {
		t.Fatalf("Observations = %v", got)
	}
	for i := range want {
		if !got[i].Date.Equal(want[i].Date) || got[i].Value != want[i].Value {
			t.Errorf("Observations[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestWriteRollsBackOnErrorAndPanic(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	insert := func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO fetch_state (source, updated_at) VALUES ('fred:X', 'now')`)
		return err
	}
	boom := errors.New("boom")

	err := s.Write(ctx, func(tx *sql.Tx) error {
		if err := insert(tx); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		insert(tx)
		panic("bad fn")
	})
	if err == nil {
		t.Error("panic not reported")
	}
	if n := count(t, s, `SELECT count(*) FROM fetch_state`); n != 0 {
		t.Errorf("rows after rollback = %d", n)
	}
	// The writer survives both and keeps working.
	if err := s.Write(ctx, insert); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM fetch_state`); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestConcurrentWrites(t *testing.T) {
	s := openTest(t)
	const writers, perWriter = 16, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				err := s.Write(context.Background(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO fetch_state (source, updated_at) VALUES (?, 'now')`, fmt.Sprintf("s%d-%d", w, i))
					return err
				})
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := count(t, s, `SELECT count(*) FROM fetch_state`); n != writers*perWriter {
		t.Errorf("rows = %d, want %d", n, writers*perWriter)
	}
}

func TestCloseDrainsQueueThenRejects(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "macro.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	// Block the writer so later requests pile up in the queue.
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 11)
	wg.Go(func() {
		results <- s.Write(ctx, func(*sql.Tx) error { close(started); <-release; return nil })
	})
	<-started
	for i := range 10 {
		wg.Go(func() {
			results <- s.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO fetch_state (source, updated_at) VALUES (?, 'now')`, fmt.Sprint(i))
				return err
			})
		})
	}
	time.Sleep(50 * time.Millisecond) // let the 10 writes enqueue
	closed := make(chan error)
	go func() { closed <- s.Close() }()
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("queued write: %v", err)
		}
	}
	if err := s.Write(ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("write after close = %v, want ErrClosed", err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := count(t, s, `SELECT count(*) FROM fetch_state`); n != 10 {
		t.Errorf("rows = %d, want 10 (queue drained before close)", n)
	}
}
