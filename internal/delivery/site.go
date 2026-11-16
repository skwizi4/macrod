package delivery

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/dashboard"
	"macro-collector/internal/gitsync"
	"macro-collector/internal/logging"
	"macro-collector/internal/notify"
	"macro-collector/internal/store"
	"macro-collector/web"
)

// Site publishes the public demo dashboard after the weekly export (D24):
// build site --demo into the working tree, check that nothing closed got
// in, commit and push. A failure is logged and sent to Telegram; it never
// touches the export.
type Site struct {
	Ctx    context.Context // lives as long as the daemon
	Build  func(ctx context.Context, dir string) (int, error)
	Check  func(dir string) error
	Git    Git
	Dir    string
	URL    string
	Msg    func(isoWeek string) string
	Notify Notifier // nil: log only
	Log    *slog.Logger

	mu sync.Mutex // one publication at a time
	wg sync.WaitGroup
}

// SiteResult is one publication.
type SiteResult struct {
	Files  int
	Commit string
	Pushed bool // a new commit is on the remote
}

// Start publishes in the background (the push may retry for ~20 minutes).
func (s *Site) Start(isoWeek string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_, _ = s.Publish(s.Ctx, isoWeek)
	}()
}

// Wait blocks until a started publication ends.
func (s *Site) Wait() { s.wg.Wait() }

// Publish builds, checks, commits and pushes the site and reports the
// result to Telegram.
func (s *Site) Publish(ctx context.Context, isoWeek string) (SiteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.publish(ctx, isoWeek)
	var text string
	switch {
	case err != nil:
		s.Log.Error("site publication failed", "iso_week", isoWeek, "err", err)
		text = fmt.Sprintf("⚠ Демо-сайт %s не опубликован: %v", isoWeek, err)
	case res.Pushed:
		s.Log.Info("site published", "iso_week", isoWeek, "files", res.Files, "commit", res.Commit)
		text = fmt.Sprintf("🌐 Демо-сайт обновлён (%s): %s", isoWeek, s.URL)
	default:
		s.Log.Info("site: nothing changed", "iso_week", isoWeek, "files", res.Files)
		text = fmt.Sprintf("🌐 Демо-сайт %s: изменений нет — %s", isoWeek, s.URL)
	}
	if s.Notify != nil {
		if nerr := s.Notify.Send(ctx, text); nerr != nil {
			s.Log.Error("telegram notification failed", "kind", "site", "err", nerr)
		}
	}
	return res, err
}

func (s *Site) publish(ctx context.Context, isoWeek string) (SiteResult, error) {
	var res SiteResult
	n, err := s.Build(ctx, s.Dir)
	if err != nil {
		return res, fmt.Errorf("build: %w", err)
	}
	res.Files = n
	if err := s.Check(s.Dir); err != nil {
		return res, err
	}
	g, err := s.Git.Sync(ctx, s.Msg(isoWeek))
	res.Commit = g.Commit
	res.Pushed = g.Committed && g.Pushed
	if err != nil {
		return res, fmt.Errorf("git: %w", err)
	}
	return res, nil
}

// NewSiteRepo is the site repository of the config; nil when the site is
// off. Its key is SITE_SSH_KEY_PATH.
func NewSiteRepo(cfg *config.Config, root *slog.Logger) *gitsync.Repo {
	s := cfg.Site
	if !s.Enabled {
		return nil
	}
	known := filepath.Join(cfg.Dir, KnownHostsFile)
	if _, err := os.Stat(known); err != nil {
		known = ""
	}
	return &gitsync.Repo{Dir: s.Dir, Remote: s.Remote, Branch: s.Branch,
		AuthorName: cfg.Git.AuthorName, AuthorEmail: cfg.Git.AuthorEmail,
		SSHKey: os.Getenv(config.EnvSiteSSHKey), KnownHosts: known,
		Retry: gitsync.DefaultRetry, Log: logging.For(root, "site")}
}

// NewSite is the demo site publisher of the config; nil when the site is
// off. It reads st (the daemon's store, or a read-only one in macroctl).
func NewSite(ctx context.Context, cfg *config.Config, st *store.Store, repo *gitsync.Repo, tg *notify.Telegram, root *slog.Logger) *Site {
	if repo == nil {
		return nil
	}
	d := &dashboard.Data{Config: cfg, Store: st, Now: time.Now, Demo: true}
	s := &Site{Ctx: ctx, Git: repo, Dir: cfg.Site.Dir, URL: cfg.Site.URL, Msg: cfg.Site.Message,
		Build: func(ctx context.Context, dir string) (int, error) { return d.BuildSite(ctx, dir, web.FS) },
		Check: func(dir string) error { return dashboard.CheckDemoSite(dir, cfg.Dashboard.DemoAllow) },
		Log:   logging.For(root, "site")}
	if tg != nil {
		s.Notify = tg
	}
	return s
}
