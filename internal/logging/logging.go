// Package logging sets up slog: JSON to a file and text to the console.
// Every record must carry a "source" attribute; components get their logger
// via For, never use the root logger directly.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
)

// SourceKey is the attribute that names the component emitting a record.
const SourceKey = "source"

// Options configures New.
type Options struct {
	Dir     string     // directory for the JSON log file
	App     string     // file name without extension, e.g. "macrod"
	Level   slog.Level // minimum level for both outputs
	Console io.Writer  // defaults to os.Stderr
	// Location sets the day boundary of log rotation (the config timezone);
	// nil: local time.
	Location *time.Location
}

// New returns a root logger writing JSON to <Dir>/<App>.log (rotated daily,
// KeepDays kept) and text to the console, plus a closer for the log file.
func New(opts Options) (*slog.Logger, io.Closer, error) {
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("logging mkdir %s: %w", opts.Dir, err)
	}
	f, err := openDaily(opts.Dir, opts.App, KeepDays, opts.Location, time.Now)
	if err != nil {
		return nil, nil, err
	}
	console := opts.Console
	if console == nil {
		console = os.Stderr
	}
	hopts := &slog.HandlerOptions{Level: opts.Level}
	h := slog.NewMultiHandler(
		slog.NewJSONHandler(f, hopts),
		slog.NewTextHandler(console, hopts),
	)
	return slog.New(h), f, nil
}

// For returns a logger that tags every record with source=name.
func For(root *slog.Logger, name string) *slog.Logger {
	return root.With(SourceKey, name)
}
