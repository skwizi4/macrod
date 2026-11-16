package config

import (
	"strings"
	"testing"
	"time"
)

func TestDeliveryDefaults(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	c, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Git.Enabled || c.Git.Branch != "main" || c.Notify.TelegramBot {
		t.Errorf("git %+v notify %+v", c.Git, c.Notify)
	}
	if c.Notify.APIURL != "https://api.telegram.org" || c.Notify.SourceDownAfter != 24*time.Hour {
		t.Errorf("notify %+v", c.Notify)
	}
	if c.Backup.Cron != "30 3 * * *" || c.Backup.Keep != 7 {
		t.Errorf("backup %+v", c.Backup)
	}
	if got := c.Git.Message("2026-W45", "weekly", "eu"); got != "export 2026-W45 weekly" {
		t.Errorf("message %q", got)
	}
}

func TestDeliveryGit(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	body := minimal + "git:\n  enabled: true\n  repo: \"example/macro_exchange\"\n"
	c, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if c.Git.Remote != "git@github.com:example/macro_exchange.git" {
		t.Errorf("remote %q", c.Git.Remote)
	}
	tests := []struct{ name, extra, wantErr string }{
		{"bad repo", "git:\n  enabled: true\n  repo: \"[[FILL]]\"\n", "git.repo"},
		{"short down", "notify:\n  source_down_after: 10m\n", "source_down_after"},
		{"bad cron", "backup:\n  cron: \"0 3 *\"\n", "backup.cron"},
		{"bad keep", "backup:\n  keep: -1\n", "backup.keep"},
		{"bad api url", "notify:\n  telegram_bot: true\n  api_url: \"ftp://x\"\n", "notify.api_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, minimal+tt.extra))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestWebhookConfig(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	c, err := Load(writeConfig(t, minimal+"webhook:\n  enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Webhook.Timeout != 10*time.Second || c.Webhook.Retries != 3 {
		t.Errorf("webhook %+v", c.Webhook)
	}
	if c, err := Load(writeConfig(t, minimal+"webhook:\n  retries: 0\n")); err != nil || c.Webhook.Retries != 0 {
		t.Errorf("retries 0: %+v, %v", c.Webhook, err)
	}
	for extra, want := range map[string]string{
		"webhook:\n  timeout: 100ms\n": "webhook.timeout",
		"webhook:\n  retries: 11\n":    "webhook.retries",
		"webhook:\n  retries: -1\n":    "webhook.retries",
	} {
		if _, err := Load(writeConfig(t, minimal+extra)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", extra, err)
		}
	}
}
