package main

import (
	"context"
	"slices"

	"macro-collector/internal/app"
	"macro-collector/internal/bot"
	"macro-collector/internal/config"
	"macro-collector/internal/delivery"
	"macro-collector/internal/guidance"
	"macro-collector/internal/logging"
	"macro-collector/internal/scheduler"
	"macro-collector/internal/store"
)

// newBot is the daemon's Telegram poller and its jobs (the manual
// reminder); nil when the bot is off (notify.telegram_bot, TG_BOT_TOKEN,
// TG_CHAT_ID).
func newBot(env *app.Env, st *store.Store, gsvc *guidance.Service) (*bot.Bot, []scheduler.Job) {
	tg := delivery.NewTelegram(env.Config, env.Root)
	if tg == nil {
		return nil, nil
	}
	log := logging.For(env.Root, "telegram_bot")
	b := &bot.Bot{TG: tg, Store: st, Log: log}
	if gsvc != nil && gsvc.TG != nil {
		b.Callback = gsvc.HandleCallback
	}
	var jobs []scheduler.Job
	if cfg := env.Config; cfg.Issuers.Enabled && slices.ContainsFunc(cfg.Issuers.Companies, func(c config.Issuer) bool { return c.Quarterly != nil }) {
		h := &bot.IssuerFile{Config: cfg, Store: st, TG: tg, Log: log}
		b.Handlers = append(b.Handlers, h)
		if slices.ContainsFunc(cfg.Issuers.Companies, func(c config.Issuer) bool { return c.Quarterly != nil && c.Quarterly.FromFile() }) {
			// After a report day, ask for the file the user sends (once a day).
			jobs = append(jobs, scheduler.Job{Name: "issuer_file_reminder", Spec: cfg.Issuers.PollCron, Run: func(ctx context.Context) {
				if asked, err := h.Remind(ctx); err != nil {
					log.Error("issuer file reminder", "err", err)
				} else if len(asked) > 0 {
					log.Info("issuer file reminder", "tickers", asked)
				}
			}})
		}
	}
	if r := env.Config.Manual.Reminder; r != nil {
		m := &bot.Manual{Config: env.Config, Store: st, TG: tg, Root: env.Root, Log: log}
		b.Handlers = append(b.Handlers, m)
		jobs = append(jobs, scheduler.Job{Name: "manual_reminder", Spec: r.Cron(), Run: func(ctx context.Context) {
			if did, err := m.Remind(ctx); err != nil {
				log.Error("manual reminder", "err", err)
			} else {
				log.Debug("manual reminder", "result", did)
			}
		}})
	}
	return b, jobs
}
