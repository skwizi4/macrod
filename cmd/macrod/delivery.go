package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/delivery"
	"macro-collector/internal/export"
	"macro-collector/internal/logging"
	"macro-collector/internal/scheduler"
	"macro-collector/internal/store"
)

// deliverySetup is what the daemon does with an export and its database
// beyond the collecting (phase 6): the sinks that push exchange/ to git, post
// the webhook and write to Telegram (phase 14), and the jobs for the daily
// backup, pending pushes and source alerts. sink is nil when none is on (the
// scheduler then only logs exports). wait, called at shutdown, lets running
// deliveries end; a pending push goes out after the next start.
func deliverySetup(ctx context.Context, cfg *config.Config, root *slog.Logger, st *store.Store) (sink export.EventSink, jobs []scheduler.Job, afterWeekly func(string), wait func()) {
	repo := delivery.NewRepo(cfg, root)
	tg := delivery.NewTelegram(cfg, root)
	chain := delivery.New(ctx, cfg, repo, delivery.NewWebhook(cfg, root), tg, root)
	siteRepo := delivery.NewSiteRepo(cfg, root)
	site := delivery.NewSite(ctx, cfg, st, siteRepo, tg, root)
	if chain != nil {
		sink = chain
	}
	wait = func() {
		if chain != nil {
			chain.Wait()
		}
		if site != nil {
			site.Wait()
		}
	}
	if site != nil {
		afterWeekly = site.Start
		silog := logging.For(root, "site")
		jobs = append(jobs, scheduler.Job{Name: "site push pending", Spec: "@hourly", Run: func(ctx context.Context) {
			pushed, err := siteRepo.PushPending(ctx)
			if err != nil {
				silog.Warn("site push pending failed", "err", err)
			} else if pushed {
				silog.Info("site pending commits pushed")
			}
		}})
	}
	blog := logging.For(root, "backup")
	jobs = append(jobs, scheduler.Job{Name: "db backup", Spec: cfg.Backup.Cron, Run: func(ctx context.Context) {
		path, err := st.DailyBackup(ctx, filepath.Join(cfg.DataDir, "backups"), time.Now().In(cfg.Location), cfg.Backup.Keep)
		if err != nil {
			blog.Error("db backup failed", "err", err)
			return
		}
		blog.Info("db backup written", "path", path)
	}})
	if repo != nil {
		glog := logging.For(root, "git")
		jobs = append(jobs, scheduler.Job{Name: "git push pending", Spec: "@hourly", Run: func(ctx context.Context) {
			pushed, err := repo.PushPending(ctx)
			if err != nil {
				glog.Warn("git push pending failed", "err", err)
			} else if pushed {
				glog.Info("git pending commits pushed")
			}
		}})
	}
	if tg != nil {
		alerts := &delivery.SourceAlerts{Store: st, Notify: tg, After: cfg.Notify.SourceDownAfter, Log: logging.For(root, "notify")}
		jobs = append(jobs, scheduler.Job{Name: "source alerts", Spec: "@hourly", Run: func(ctx context.Context) {
			if err := alerts.Check(ctx); err != nil {
				alerts.Log.Error("source alerts failed", "err", err)
			}
		}})
	}
	return sink, jobs, afterWeekly, wait
}
