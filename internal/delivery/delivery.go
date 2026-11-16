// Package delivery is the export sink of phase 6 (ТЗ §12.9–§12.11): after
// each export it commits and pushes exchange/ (gitsync) and sends a Telegram
// message (notify). Delivery runs in the background and never fails the
// export: errors go to the log and to the message.
package delivery

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"macro-collector/internal/export"
	"macro-collector/internal/gitsync"
)

// Git is the part of gitsync.Repo the sink uses.
type Git interface {
	Sync(ctx context.Context, msg string, paths ...string) (gitsync.Result, error)
}

// Notifier sends one message.
type Notifier interface {
	Send(ctx context.Context, text string) error
}

// Sink is one receiver of an export (ТЗ §21.6): git, webhook, Telegram.
// The files are already in the exchange directory (the folder "sink").
type Sink interface {
	Name() string
	// Deliver delivers d.Event. It may read what the sinks before it did
	// (d.Git) and adds its status line to d.Lines for the ones after it.
	Deliver(ctx context.Context, d *Delivery) error
}

// Delivery is one export on its way through the sinks.
type Delivery struct {
	Event export.Exported
	Git   *GitOutcome // nil: no git sink, or it has not run
	Lines []string    // status lines for the message, in sink order
}

// GitOutcome is what the git sink did.
type GitOutcome struct {
	Committed bool
	Commit    string // short hash; "" when nothing changed
	URL       string // the commit on GitHub; "" without a repository name
	Err       error
}

// Chain implements export.EventSink: it runs the sinks in order, in the
// background. A failing sink is logged and does not stop the next ones.
type Chain struct {
	Ctx   context.Context // lives as long as the daemon; deliveries stop with it
	Sinks []Sink
	Log   *slog.Logger

	wg sync.WaitGroup
}

// Exported implements export.EventSink: it starts the delivery and returns.
func (c *Chain) Exported(_ context.Context, e export.Exported) error {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.deliver(e)
	}()
	return nil
}

// Wait blocks until started deliveries finish (macroctl export, shutdown).
func (c *Chain) Wait() { c.wg.Wait() }

func (c *Chain) deliver(e export.Exported) {
	d := &Delivery{Event: e}
	for _, s := range c.Sinks {
		if err := c.run(s, d); err != nil {
			c.Log.Error(s.Name()+" delivery failed", "kind", e.Kind, "region", e.Region, "iso_week", e.ISOWeek, "err", err)
		}
	}
}

// run delivers to one sink; a panic is its error, not the daemon's end.
func (c *Chain) run(s Sink, d *Delivery) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return s.Deliver(c.Ctx, d)
}

// GitSink commits and pushes the export's paths of exchange/ (§12.10).
type GitSink struct {
	Git     Git
	Repo    string                                    // owner/name, for the commit link
	Message func(isoWeek, kind, region string) string // commit message
	Dir     string                                    // the exchange directory (the repository)
	Log     *slog.Logger
}

// Name implements Sink.
func (g *GitSink) Name() string { return "git" }

// Deliver implements Sink. A failed push is retried by the hourly job;
// the message says so.
func (g *GitSink) Deliver(ctx context.Context, d *Delivery) error {
	e := d.Event
	res, err := g.Git.Sync(ctx, g.Message(e.ISOWeek, e.Kind, e.Region), RegionPaths(g.Dir, e)...)
	out := &GitOutcome{Committed: res.Committed, Err: err}
	d.Git = out
	switch {
	case err != nil:
		d.Lines = append(d.Lines, fmt.Sprintf("⚠ git push не прошёл: %v — повторю (раз в час)", err))
		return fmt.Errorf("committed %v: %w", res.Committed, err)
	case res.Committed:
		out.Commit = res.Commit
		line := "git: " + res.Commit
		if g.Repo != "" {
			out.URL = "https://github.com/" + g.Repo + "/commit/" + res.Commit
			line += " → " + out.URL
		}
		d.Lines = append(d.Lines, line)
		g.Log.Info("git delivery done", "kind", e.Kind, "iso_week", e.ISOWeek, "commit", res.Commit)
	default:
		d.Lines = append(d.Lines, "git: изменений нет")
		g.Log.Info("git delivery: nothing changed", "kind", e.Kind, "iso_week", e.ISOWeek)
	}
	return nil
}

// TelegramSink sends the message about the export (§12.11), with the
// status lines of the sinks before it.
type TelegramSink struct {
	Notify Notifier
	Title  func(region string) string // the region's name in the message; nil: the id
}

// Name implements Sink.
func (t *TelegramSink) Name() string { return "telegram" }

// Deliver implements Sink.
func (t *TelegramSink) Deliver(ctx context.Context, d *Delivery) error {
	title := d.Event.Region
	if t.Title != nil {
		title = t.Title(d.Event.Region)
	}
	return t.Notify.Send(ctx, Text(d.Event, title, strings.Join(d.Lines, "\n")))
}

// RegionPaths are the paths of an export's commit, relative to the
// exchange directory: the region's folders in the tree, the JSON tree and
// the week's archive, and the export's own files outside them (the
// archive-only news files at the week's root). Two regions exported one
// after the other make two commits, each with its own files.
func RegionPaths(dir string, e export.Exported) []string {
	region := e.Region
	out := []string{region + "/", "json/" + region + "/"}
	if e.ISOWeek != "" {
		out = append(out, "archive/"+e.ISOWeek+"/"+region+"/", "archive/"+e.ISOWeek+"/json/"+region+"/")
	}
	for _, f := range e.Files {
		rel, err := filepath.Rel(dir, f)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		if !slices.ContainsFunc(out, func(p string) bool { return strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p) }) && !slices.Contains(out, rel) {
			out = append(out, rel)
		}
	}
	return out
}
