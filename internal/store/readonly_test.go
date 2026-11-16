package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func writeObs(t *testing.T, s *Store, id string, day time.Time) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := UpsertObservations(tx, "fred", id, []model.Observation{{Date: day, Value: 1}}, time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenReadOnlySeesWritesOfAnotherConnection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "macro.db")
	w, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	writeObs(t, w, "AAA", d("2026-01-01"))

	r, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	writeObs(t, w, "AAA", d("2026-01-02"))
	obs, err := r.Observations(ctx, "fred", "AAA", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Fatalf("read %d observations while the writer is open, want 2", len(obs))
	}
}

func TestOpenReadOnlyNeverWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "macro.db")
	w, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	writeObs(t, w, "AAA", d("2026-01-01"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	r, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Write(ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Write = %v, want ErrReadOnly", err)
	}
	if _, err := r.ReadDB().ExecContext(ctx, "DELETE FROM observations"); err == nil {
		t.Fatal("DELETE through a read-only store succeeded")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the database file changed")
	}
}

func TestOpenReadOnlyNeedsExistingCurrentDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	missing := filepath.Join(dir, "sub", "none.db")
	if _, err := OpenReadOnly(ctx, missing); err == nil {
		t.Fatal("opened a missing database")
	}
	if _, err := os.Stat(filepath.Dir(missing)); err == nil {
		t.Fatal("created the directory of a missing database")
	}

	old := filepath.Join(dir, "old.db")
	db, err := sql.Open("sqlite", "file:"+old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t(x); PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := OpenReadOnly(ctx, old); err == nil {
		t.Fatal("opened a database with an old schema")
	}
}
