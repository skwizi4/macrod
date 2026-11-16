package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Env variables of delivery (phase 6, ТЗ §6). Values never appear in the
// config file.
const (
	EnvGitSSHKey   = "GIT_SSH_KEY_PATH" // private deploy key with write access to git.repo
	EnvTGBotToken  = "TG_BOT_TOKEN"
	EnvTGChatID    = "TG_CHAT_ID"
	EnvWebhookURL  = "WEBHOOK_URL"    // may carry a token: a secret, never in the config
	EnvWebhookKey  = "WEBHOOK_SECRET" // optional HMAC-SHA256 key of X-Macro-Signature
	defaultTGAPI   = "https://api.telegram.org"
	defaultBranch  = "main"
	defaultCommit  = "export {iso_week} {kind}"
	defaultDownFor = 24 * time.Hour
	defaultBackup  = "30 3 * * *"
	defaultKeep    = 7
)

// Git pushes exchange/ to a private repository after each export (§12.10, D10).
type Git struct {
	Enabled       bool   `yaml:"enabled"`
	Repo          string `yaml:"repo"`   // owner/name on GitHub
	Branch        string `yaml:"branch"` // default main
	Remote        string `yaml:"remote"` // default git@github.com:<repo>.git
	AuthorName    string `yaml:"author_name"`
	AuthorEmail   string `yaml:"author_email"`
	CommitMessage string `yaml:"commit_message"` // {iso_week}, {kind}, {region}
}

// Notify sends Telegram messages through a bot (§12.11).
type Notify struct {
	TelegramBot     bool          `yaml:"telegram_bot"`
	APIURL          string        `yaml:"api_url"`           // default https://api.telegram.org
	SourceDownAfter time.Duration `yaml:"source_down_after"` // default 24h
}

// Webhook posts each export as JSON to WEBHOOK_URL (§21.6, phase 14).
type Webhook struct {
	Enabled bool          `yaml:"enabled"`
	Timeout time.Duration `yaml:"timeout"` // one attempt; default 10s
	// Retries after the first attempt, on network errors, 429 and 5xx;
	// default 3, 0 — none.
	Retries    int  `yaml:"-"`
	RetriesSet *int `yaml:"retries"`
}

// Backup is the daily copy of the database (§14).
type Backup struct {
	Cron string `yaml:"cron"` // config timezone; default 03:30
	Keep int    `yaml:"keep"` // copies kept; default 7
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func (c *Config) validateDelivery() error {
	var errs []error
	g := &c.Git
	if g.Branch == "" {
		g.Branch = defaultBranch
	}
	if g.CommitMessage == "" {
		g.CommitMessage = defaultCommit
	}
	if g.AuthorName == "" {
		g.AuthorName = "macro-collector"
	}
	if g.AuthorEmail == "" {
		g.AuthorEmail = "macro-collector@localhost"
	}
	if g.Enabled {
		if !repoRe.MatchString(g.Repo) {
			errs = append(errs, fmt.Errorf("git.repo %q: want owner/name", g.Repo))
		}
		if g.Remote == "" && repoRe.MatchString(g.Repo) {
			g.Remote = "git@github.com:" + g.Repo + ".git"
		}
	}
	n := &c.Notify
	if n.APIURL == "" {
		n.APIURL = defaultTGAPI
	}
	n.APIURL = strings.TrimRight(n.APIURL, "/")
	if n.SourceDownAfter == 0 {
		n.SourceDownAfter = defaultDownFor
	}
	if n.SourceDownAfter < time.Hour {
		errs = append(errs, fmt.Errorf("notify.source_down_after %s: want at least 1h", n.SourceDownAfter))
	}
	if n.TelegramBot {
		errs = append(errs, checkURL("notify.api_url", n.APIURL))
	}
	w := &c.Webhook
	if w.Timeout == 0 {
		w.Timeout = 10 * time.Second
	}
	if w.Timeout < time.Second {
		errs = append(errs, fmt.Errorf("webhook.timeout %s: want at least 1s", w.Timeout))
	}
	w.Retries = 3
	if w.RetriesSet != nil {
		w.Retries = *w.RetriesSet
	}
	if w.Retries < 0 || w.Retries > 10 {
		errs = append(errs, fmt.Errorf("webhook.retries %d: want 0..10", w.Retries))
	}
	b := &c.Backup
	if b.Cron == "" {
		b.Cron = defaultBackup
	}
	if b.Keep == 0 {
		b.Keep = defaultKeep
	}
	if _, err := cron.ParseStandard(b.Cron); err != nil {
		errs = append(errs, fmt.Errorf("backup.cron %q: %w", b.Cron, err))
	}
	if b.Keep < 1 {
		errs = append(errs, errors.New("backup.keep: want at least 1"))
	}
	return errors.Join(errs...)
}

// Message fills the git commit message template; {region} is the region id.
func (g Git) Message(isoWeek, kind, region string) string {
	return strings.NewReplacer("{iso_week}", isoWeek, "{kind}", kind, "{region}", region).Replace(g.CommitMessage)
}
