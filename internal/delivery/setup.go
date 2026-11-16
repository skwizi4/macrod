package delivery

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"macro-collector/internal/config"
	"macro-collector/internal/gitsync"
	"macro-collector/internal/guidance"
	"macro-collector/internal/logging"
	"macro-collector/internal/notify"
	"macro-collector/internal/store"
)

// KnownHostsFile is GitHub's published host key, relative to the config file
// directory (/app in the image).
const KnownHostsFile = "deploy/github_known_hosts"

// NewRepo is the exchange repository of the config; nil when git is off.
func NewRepo(cfg *config.Config, root *slog.Logger) *gitsync.Repo {
	g := cfg.Git
	if !g.Enabled {
		return nil
	}
	known := filepath.Join(cfg.Dir, KnownHostsFile)
	if _, err := os.Stat(known); err != nil {
		known = "" // ssh defaults (~/.ssh/known_hosts)
	}
	return &gitsync.Repo{Dir: cfg.Export.ExchangeDir, Remote: g.Remote, Branch: g.Branch,
		AuthorName: g.AuthorName, AuthorEmail: g.AuthorEmail,
		SSHKey: os.Getenv(config.EnvGitSSHKey), KnownHosts: known,
		Retry: gitsync.DefaultRetry, Log: logging.For(root, "git")}
}

// NewTelegram is the bot of the config; nil when notifications are off or
// the token or chat id is missing (logged; doctor reports it).
func NewTelegram(cfg *config.Config, root *slog.Logger) *notify.Telegram {
	if !cfg.Notify.TelegramBot {
		return nil
	}
	token, chat := os.Getenv(config.EnvTGBotToken), os.Getenv(config.EnvTGChatID)
	if token == "" || chat == "" {
		logging.For(root, "notify").Error("notify.telegram_bot is on but TG_BOT_TOKEN or TG_CHAT_ID is not set: notifications off")
		return nil
	}
	return notify.New(cfg.Notify.APIURL, token, chat)
}

// NewWebhook is the webhook of the config; nil when it is off or
// WEBHOOK_URL is not set (logged; doctor reports it).
func NewWebhook(cfg *config.Config, root *slog.Logger) *Webhook {
	w := cfg.Webhook
	if !w.Enabled {
		return nil
	}
	u := os.Getenv(config.EnvWebhookURL)
	log := logging.For(root, "webhook")
	if u == "" {
		log.Error("webhook.enabled is on but WEBHOOK_URL is not set: webhook off")
		return nil
	}
	return &Webhook{URL: u, Secret: os.Getenv(config.EnvWebhookKey), Timeout: w.Timeout, Retries: w.Retries,
		Dir: cfg.Export.ExchangeDir, Title: cfg.RegionTitle, Log: log}
}

// New is the sink of the config: git, webhook and Telegram, in that order,
// those that are on. It is nil when none is (the caller keeps
// export.LogSink). A nil repo, webhook or bot is left out.
func New(ctx context.Context, cfg *config.Config, repo *gitsync.Repo, hook *Webhook, tg *notify.Telegram, root *slog.Logger) *Chain {
	var sinks []Sink
	if repo != nil {
		sinks = append(sinks, &GitSink{Git: repo, Repo: cfg.Git.Repo, Message: cfg.Git.Message, Dir: cfg.Export.ExchangeDir, Log: logging.For(root, "git")})
	}
	if hook != nil {
		sinks = append(sinks, hook)
	}
	if tg != nil {
		sinks = append(sinks, &TelegramSink{Notify: tg, Title: cfg.RegionTitle})
	}
	if len(sinks) == 0 {
		return nil
	}
	return &Chain{Ctx: ctx, Sinks: sinks, Log: logging.For(root, "delivery")}
}

// NewGuidance is the capex guidance service of the config (phase 14b): nil
// without edgar.guidance_tickers; Telegram as in notify.telegram_bot.
func NewGuidance(cfg *config.Config, st *store.Store, root *slog.Logger) *guidance.Service {
	if len(cfg.EDGAR.GuidanceTickers) == 0 {
		return nil
	}
	return &guidance.Service{Store: st, Config: cfg, TG: NewTelegram(cfg, root), Log: logging.For(root, guidance.Source)}
}
