package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestDailyBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(ctx, filepath.Join(dir, "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 3, 30, 0, 0, time.UTC)
	if err := st.Write(ctx, func(tx *sql.Tx) error { return SetFetchOK(tx, "fred:GDPC1", "c1", now) }); err != nil {
		t.Fatal(err)
	}
	bdir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// older backups and an unrelated file
	for _, n := range []string{"macro-2026-10-30.db", "macro-2026-11-01.db", "macro-2026-11-05.db", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(bdir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path, err := st.DailyBackup(ctx, bdir, now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "macro-2026-11-06.db" {
		t.Errorf("path %s", path)
	}
	var names []string
	entries, _ := os.ReadDir(bdir)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"macro-2026-11-05.db", "macro-2026-11-06.db", "notes.txt"}; !slices.Equal(names, want) {
		t.Errorf("files %v, want %v", names, want)
	}

	// the copy is a database with the data
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if fs, ok, err := b.FetchStateOf(ctx, "fred:GDPC1"); err != nil || !ok || fs.Cursor != "c1" {
		t.Errorf("backup row %+v %v %v", fs, ok, err)
	}

	// the same day again overwrites
	if _, err := st.DailyBackup(ctx, bdir, now, 2); err != nil {
		t.Fatalf("second backup of the day: %v", err)
	}
}
