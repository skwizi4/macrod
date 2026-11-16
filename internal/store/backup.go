package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Backup writes a consistent copy of the database to path with VACUUM INTO
// (ТЗ §14). It runs on the writer goroutine (rule 9), between writes, via a
// temporary file, so path is either the old or the complete new copy.
func (s *Store) Backup(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("backup mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp) // VACUUM INTO refuses an existing file
	err := s.writer.send(writeReq{ctx: ctx, done: make(chan error, 1), raw: func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, "VACUUM INTO ?", tmp)
		return err
	}})
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("backup %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("backup rename %s: %w", path, err)
	}
	return nil
}

// BackupPrefix and BackupExt name daily backups: macro-2026-11-06.db.
const (
	BackupPrefix = "macro-"
	BackupExt    = ".db"
)

// DailyBackup writes <dir>/macro-<day>.db and deletes all but the newest keep
// daily backups. It returns the new file.
func (s *Store) DailyBackup(ctx context.Context, dir string, day time.Time, keep int) (string, error) {
	path := filepath.Join(dir, BackupPrefix+day.Format(time.DateOnly)+BackupExt)
	if err := s.Backup(ctx, path); err != nil {
		return "", err
	}
	return path, PruneBackups(dir, keep)
}

// PruneBackups deletes all but the newest keep files macro-YYYY-MM-DD.db in dir.
func PruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("backup prune %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, BackupPrefix) && strings.HasSuffix(n, BackupExt) {
			if _, err := time.Parse(time.DateOnly, strings.TrimSuffix(strings.TrimPrefix(n, BackupPrefix), BackupExt)); err == nil {
				names = append(names, n)
			}
		}
	}
	slices.Sort(names) // dates sort as text
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return fmt.Errorf("backup prune: %w", err)
		}
		names = names[1:]
	}
	return nil
}
