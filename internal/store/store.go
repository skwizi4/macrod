// Package store owns the SQLite database. All writes go through a single
// writer goroutine (Store.Write); reads use a separate read-only pool.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"macro-collector/migrations"
)

const (
	dateLayout = time.DateOnly
	tsLayout   = time.RFC3339Nano
)

// Store is the database handle.
type Store struct {
	path   string
	read   *sql.DB
	writer *writer
}

// Open opens (creating if needed) the database at path, applies pending
// migrations and starts the writer goroutine.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := checkOwner(path, time.Now()); err != nil {
		return nil, fmt.Errorf("store open %s: %w", path, err)
	}
	pragmas := "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"

	wdb, err := sql.Open("sqlite", "file:"+path+"?"+pragmas+"&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("store open %s: %w", path, err)
	}
	wdb.SetMaxOpenConns(1)
	if err := migrate(ctx, wdb, migrations.FS); err != nil {
		wdb.Close()
		return nil, fmt.Errorf("store migrate %s: %w", path, err)
	}

	rdb, err := sql.Open("sqlite", "file:"+path+"?"+pragmas+"&_pragma=query_only(1)")
	if err != nil {
		wdb.Close()
		return nil, fmt.Errorf("store open read %s: %w", path, err)
	}
	return &Store{path: path, read: rdb, writer: newWriter(wdb)}, nil
}

// ErrReadOnly is returned by Write on a store from OpenReadOnly.
var ErrReadOnly = errors.New("store: opened read-only")

// OpenReadOnly opens an existing database for a process that must never write
// to it (the dashboard). Unlike Open it creates nothing, applies no migrations
// and does not change the journal mode. It does not check the daemon's owner
// mark either: that guards writers, and the dashboard runs in a container
// next to the daemon.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("store open read-only: %w", err)
	}
	rdb, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("store open read-only %s: %w", path, err)
	}
	s := &Store{path: path, read: rdb}
	have, err := s.SchemaVersion(ctx)
	if err != nil {
		rdb.Close()
		return nil, fmt.Errorf("store open read-only %s: %w", path, err)
	}
	want, err := latestMigration(migrations.FS)
	if err != nil {
		rdb.Close()
		return nil, err
	}
	if have < want {
		rdb.Close()
		return nil, fmt.Errorf("store open read-only %s: schema v%d, this build needs v%d: start the daemon of this version first", path, have, want)
	}
	return s, nil
}

// latestMigration is the highest migration number in fsys.
func latestMigration(fsys fs.FS) (int, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return 0, err
	}
	latest := 0
	for _, name := range names {
		num, err := migrationNumber(name)
		if err != nil {
			return 0, err
		}
		latest = max(latest, num)
	}
	return latest, nil
}

// Close stops accepting writes, finishes queued ones and closes the database.
func (s *Store) Close() error {
	if s.writer == nil {
		return s.read.Close()
	}
	werr := s.writer.close()
	rerr := s.read.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

// Write runs fn in its own transaction on the writer goroutine. fn's error
// (or panic) rolls the transaction back.
func (s *Store) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.writer == nil {
		return ErrReadOnly
	}
	return s.writer.do(ctx, fn)
}

// ReadDB is the read-only pool for queries.
func (s *Store) ReadDB() *sql.DB { return s.read }

// SchemaVersion returns the applied migration number.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.read.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// migrate applies NNNN_*.sql files with a number above PRAGMA user_version,
// each in its own transaction together with the version bump.
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, name := range names {
		num, err := migrationNumber(name)
		if err != nil {
			return err
		}
		if num <= current {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", num)); err != nil {
			tx.Rollback()
			return fmt.Errorf("set version %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
		current = num
	}
	return nil
}

func migrationNumber(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	n, err := strconv.Atoi(prefix)
	if !ok || err != nil || n <= 0 {
		return 0, fmt.Errorf("migration %s: name must be NNNN_name.sql", name)
	}
	return n, nil
}

func formatDate(t time.Time) string { return t.Format(dateLayout) }

func formatTS(t time.Time) string { return t.UTC().Format(tsLayout) }

// parseTS reads a TIMESTAMP stored by formatTS (aggregates return it as text).
func parseTS(s string) (time.Time, error) {
	t, err := time.Parse(tsLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %q: %w", s, err)
	}
	return t, nil
}
