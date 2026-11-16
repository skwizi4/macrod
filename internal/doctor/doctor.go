// Package doctor checks that an installation can run (ТЗ §21.1, phase 9):
// config, keys, sources, database and disk. macroctl doctor prints the
// results; any failure gives a non-zero exit code.
package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"macro-collector/internal/store"
)

// Check is one named probe; Run returns a short detail on success.
type Check struct {
	Name string
	Run  func(ctx context.Context) (string, error)
}

// Result is the outcome of one check.
type Result struct {
	Name   string
	Detail string
	Err    error
}

// OK reports whether the check passed.
func (r Result) OK() bool { return r.Err == nil }

// Run executes the checks in order, each with its own timeout.
func Run(ctx context.Context, checks []Check, timeout time.Duration) []Result {
	out := make([]Result, 0, len(checks))
	for _, c := range checks {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		detail, err := c.Run(cctx)
		cancel()
		out = append(out, Result{Name: c.Name, Detail: detail, Err: err})
	}
	return out
}

// Print writes one line per result and reports whether all passed.
func Print(w io.Writer, results []Result) bool {
	ok := true
	for _, r := range results {
		if r.OK() {
			fmt.Fprintf(w, "✅ %-22s %s\n", r.Name, r.Detail)
			continue
		}
		ok = false
		fmt.Fprintf(w, "❌ %-22s %v\n", r.Name, r.Err)
	}
	return ok
}

// EnvSet checks that the environment variables are not empty; values are
// never printed.
func EnvSet(names ...string) Check {
	return Check{Name: "env " + strings.Join(names, ", "), Run: func(context.Context) (string, error) {
		var missing []string
		for _, n := range names {
			if strings.TrimSpace(os.Getenv(n)) == "" {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			return "", fmt.Errorf("not set (.env): %s", strings.Join(missing, ", "))
		}
		return "set", nil
	}}
}

// FileReadable checks that path is set and is a readable file (a key; its
// content is not printed).
func FileReadable(name, path string) Check {
	return Check{Name: name, Run: func(context.Context) (string, error) {
		if path == "" {
			return "", errors.New("path is not set")
		}
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("not readable: %w", err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a file", path)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("%s: mode %o, ssh wants 600", path, fi.Mode().Perm())
		}
		return path, nil
	}}
}

// DBWritable checks that the database at path takes a write lock, or, when it
// does not exist yet, that its directory is writable (macrod creates it).
func DBWritable(path string) Check {
	return Check{Name: "database", Run: func(ctx context.Context) (string, error) {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := dirWritable(filepath.Dir(path)); err != nil {
				return "", err
			}
			return "not created yet, directory writable: " + filepath.Dir(path), nil
		}
		if err := store.CheckOwner(path); err != nil {
			return "", err
		}
		// SQLite opens a read-only file without complaint, and WAL needs
		// files next to it: check both before taking the write lock.
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return "", fmt.Errorf("not writable: %w", err)
		}
		f.Close()
		if err := dirWritable(filepath.Dir(path)); err != nil {
			return "", err
		}
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
		if err != nil {
			return "", fmt.Errorf("open %s: %w", path, err)
		}
		defer db.Close()
		conn, err := db.Conn(ctx)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", path, err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return "", fmt.Errorf("write lock %s: %w", path, err)
		}
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			return "", fmt.Errorf("rollback %s: %w", path, err)
		}
		return "writable: " + path, nil
	}}
}

func dirWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return fmt.Errorf("directory %s not writable: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// DiskFree checks that the filesystem holding dir has at least minBytes free.
func DiskFree(dir string, minBytes uint64) Check {
	return Check{Name: "disk space", Run: func(context.Context) (string, error) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
		free, err := freeBytes(dir)
		if err != nil {
			return "", err
		}
		detail := fmt.Sprintf("%.1f GB free in %s", float64(free)/(1<<30), dir)
		if free < minBytes {
			return "", fmt.Errorf("%s, want at least %.1f GB", detail, float64(minBytes)/(1<<30))
		}
		return detail, nil
	}}
}

// GeminiModel asks the Gemini API for the model's metadata (models.get): it
// proves the key and the model name without spending a generation request.
// endpoint is the configured generateContent URL with {model}.
func GeminiModel(client *http.Client, endpoint, model, key string) Check {
	return Check{Name: "gemini", Run: func(ctx context.Context) (string, error) {
		i := strings.Index(endpoint, "{model}")
		if i < 0 {
			return "", fmt.Errorf("endpoint %q has no {model}", endpoint)
		}
		url := endpoint[:i] + model
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("x-goog-api-key", key)
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("models.get %s: %w", model, err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("models.get %s: HTTP %d", model, resp.StatusCode)
		}
		return "model " + model + " available", nil
	}}
}
