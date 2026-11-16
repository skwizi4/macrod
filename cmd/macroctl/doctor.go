package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/config"
	"macro-collector/internal/delivery"
	"macro-collector/internal/doctor"
	"macro-collector/internal/export"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/fred"
	"macro-collector/internal/sources/httpx"
)

// doctorCmd checks the installation (ТЗ §21.1): the config is valid (it
// loaded, or macroctl would have stopped), keys are set, every source answers
// one request, the database is writable and the disk has room. --local skips
// the network: it is the container healthcheck.
func doctorCmd(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	local := fs.Bool("local", false, "skip source requests (healthcheck)")
	minFreeGB := fs.Float64("min-free-gb", 1, "minimum free disk space in data_dir, GB")
	timeout := fs.Duration("timeout", 30*time.Second, "timeout of each check")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := env.Config
	checks := []doctor.Check{
		{Name: "config", Run: func(context.Context) (string, error) { return "valid, data_dir " + cfg.DataDir, nil }},
		{Name: "templates", Run: func(context.Context) (string, error) {
			_, err := export.LoadTemplates(cfg)
			return "ok", err
		}},
		{Name: "prompts", Run: func(context.Context) (string, error) {
			n, err := export.CheckPrompts(cfg)
			return fmt.Sprintf("%d agents, %s", n, cfg.Export.PromptsDir), err
		}},
		doctor.DBWritable(cfg.DBPath),
		doctor.DiskFree(cfg.DataDir, uint64(*minFreeGB*(1<<30))),
	}
	if keys := requiredKeys(cfg); len(keys) > 0 {
		checks = append(checks, doctor.EnvSet(keys...))
	}
	if cfg.EDGAR.Enabled {
		checks = append(checks, doctor.Check{Name: "edgar user_agent", Run: func(context.Context) (string, error) {
			if !strings.Contains(cfg.EDGAR.UserAgent, "@") || strings.Contains(cfg.EDGAR.UserAgent, "example.com") {
				return "", fmt.Errorf("edgar.user_agent %q: SEC wants a real contact e-mail", cfg.EDGAR.UserAgent)
			}
			return "set", nil
		}})
	}
	if cfg.Git.Enabled {
		checks = append(checks, doctor.FileReadable("git deploy key", os.Getenv(config.EnvGitSSHKey)))
	}
	if cfg.Site.Enabled {
		checks = append(checks, doctor.FileReadable("site deploy key", os.Getenv(config.EnvSiteSSHKey)))
	}
	if !*local {
		checks = append(checks, sourceChecks(env)...)
		checks = append(checks, deliveryChecks(env)...)
	}

	results := doctor.Run(ctx, checks, *timeout)
	if !doctor.Print(os.Stdout, results) {
		return fmt.Errorf("some checks failed")
	}
	return nil
}

// requiredKeys lists the env variables the enabled features need.
func requiredKeys(cfg *config.Config) []string {
	var keys []string
	if len(cfg.FRED.Series) > 0 {
		keys = append(keys, fred.EnvAPIKey)
	}
	if cfg.Classify.Gemini.Enabled {
		keys = append(keys, config.EnvGeminiKey)
	}
	if cfg.Git.Enabled {
		keys = append(keys, config.EnvGitSSHKey)
	}
	if cfg.Site.Enabled {
		keys = append(keys, config.EnvSiteSSHKey)
	}
	if cfg.Notify.TelegramBot {
		keys = append(keys, config.EnvTGBotToken, config.EnvTGChatID)
	}
	if cfg.Webhook.Enabled {
		keys = append(keys, config.EnvWebhookURL) // WEBHOOK_SECRET is optional
	}
	return keys
}

// sourceChecks makes one cheap request per source with the clients the
// collector uses. Cboe and prediction markets are left out: non-critical
// (D7), and Cboe answers 403 on days without a file.
func sourceChecks(env *app.Env) []doctor.Check {
	cfg := env.Config
	var checks []doctor.Check
	if len(cfg.FRED.Series) > 0 {
		id := cfg.FRED.Series[0].ID
		checks = append(checks, doctor.Check{Name: "fred", Run: func(ctx context.Context) (string, error) {
			c, err := fred.New(os.Getenv(fred.EnvAPIKey), fred.WithLogger(logging.For(env.Root, "fred")))
			if err != nil {
				return "", err
			}
			m, err := c.FetchMeta(ctx, id)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s: last updated %s", id, m.LastUpdated.Format(time.DateOnly)), nil
		}})
	}
	checks = append(checks, doctor.Check{Name: "treasury", Run: func(ctx context.Context) (string, error) {
		a, err := newTreasuryClient(env).FetchUpcoming(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d upcoming auctions", len(a)), nil
	}})
	if syms := cfg.Yahoo.SymbolsFor(cfg.RegionIDs()[0]); len(syms) > 0 {
		sym := syms[0].Symbol
		checks = append(checks, doctor.Check{Name: "yahoo", Run: func(ctx context.Context) (string, error) {
			now := time.Now()
			s, err := newYahooClient(env).Fetch(ctx, sym, now.AddDate(0, 0, -7), now)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s: %d bars", sym, len(s.Bars)), nil
		}})
	}
	if cfg.FedTexts.BaseURL != "" {
		checks = append(checks, doctor.Check{Name: "fed", Run: func(ctx context.Context) (string, error) {
			_, err := fedtexts.New(httpx.WithLogger(logging.For(env.Root, "fed"))).Get(ctx, cfg.FedTexts.BaseURL)
			return cfg.FedTexts.BaseURL, err
		}})
	}
	if cfg.CFTC.Enabled {
		url := cfg.CFTC.Endpoint + "?$limit=1"
		checks = append(checks, doctor.Check{Name: "cftc", Run: func(ctx context.Context) (string, error) {
			_, err := httpx.New(httpx.WithLogger(logging.For(env.Root, "cftc"))).Get(ctx, url)
			return cfg.CFTC.Endpoint, err
		}})
	}
	if cfg.EDGAR.Enabled && len(cfg.EDGAR.Companies) > 0 {
		co := cfg.EDGAR.Companies[0]
		checks = append(checks, doctor.Check{Name: "edgar", Run: func(ctx context.Context) (string, error) {
			c, err := newEDGARClient(env)
			if err != nil {
				return "", err
			}
			if _, err := c.Submissions(ctx, co.CIK); err != nil {
				return "", err
			}
			return co.Ticker + " submissions", nil
		}})
	}
	if cfg.Telegram.Enabled && len(cfg.Telegram.Channels) > 0 {
		url := strings.TrimRight(cfg.Telegram.BaseURL, "/") + "/s/" + cfg.Telegram.Channels[0].Handle
		checks = append(checks, doctor.Check{Name: "telegram", Run: func(ctx context.Context) (string, error) {
			_, err := httpx.New(httpx.WithLogger(logging.For(env.Root, "telegram"))).Get(ctx, url)
			return url, err
		}})
	}
	if g := cfg.Classify.Gemini; g.Enabled {
		checks = append(checks, doctor.GeminiModel(&http.Client{Timeout: 30 * time.Second}, g.Endpoint, g.Model, os.Getenv(config.EnvGeminiKey)))
	}
	return checks
}

// deliveryChecks reach the exchange remote with the deploy key and the bot.
func deliveryChecks(env *app.Env) []doctor.Check {
	var checks []doctor.Check
	if repo := delivery.NewRepo(env.Config, env.Root); repo != nil {
		checks = append(checks, doctor.Check{Name: "git remote", Run: func(ctx context.Context) (string, error) {
			if err := repo.LsRemote(ctx); err != nil {
				return "", err
			}
			return repo.Remote + " (push access is checked by the first export)", nil
		}})
	}
	if repo := delivery.NewSiteRepo(env.Config, env.Root); repo != nil {
		checks = append(checks, doctor.Check{Name: "site remote", Run: func(ctx context.Context) (string, error) {
			if err := repo.LsRemote(ctx); err != nil {
				return "", err
			}
			return repo.Remote + " (push access is checked by site --publish)", nil
		}})
	}
	if hook := delivery.NewWebhook(env.Config, env.Root); hook != nil {
		// No request: it would be a fake delivery to the receiver.
		checks = append(checks, doctor.Check{Name: "webhook", Run: func(context.Context) (string, error) {
			return delivery.CheckWebhookURL(hook.URL, hook.Secret != "")
		}})
	}
	if tg := delivery.NewTelegram(env.Config, env.Root); tg != nil {
		checks = append(checks, doctor.Check{Name: "telegram bot", Run: func(ctx context.Context) (string, error) {
			name, err := tg.Me(ctx)
			if err != nil {
				return "", err
			}
			return "@" + name, nil
		}})
	}
	return checks
}
