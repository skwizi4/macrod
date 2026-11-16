package main

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"macro-collector/internal/config"
	"macro-collector/internal/scheduler"
)

func jobNames(jobs []scheduler.Job) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.Name)
	}
	return out
}

func TestDeliverySetup(t *testing.T) {
	root := slog.New(slog.DiscardHandler)
	cfg := &config.Config{}
	cfg.Backup.Cron = "30 3 * * *"
	cfg.Export.ExchangeDir = t.TempDir()

	sink, jobs, after, wait := deliverySetup(context.Background(), cfg, root, nil)
	wait()
	if sink != nil {
		t.Errorf("delivery off: sink = %v", sink)
	}
	if got := jobNames(jobs); !slices.Equal(got, []string{"db backup"}) {
		t.Errorf("delivery off: jobs = %v", got)
	}
	if after != nil {
		t.Error("site off: afterWeekly is set")
	}

	cfg.Git.Enabled, cfg.Git.Repo, cfg.Git.Remote = true, "o/r", "git@example.test:o/r.git"
	cfg.Notify.TelegramBot = true
	t.Setenv(config.EnvTGBotToken, "token")
	t.Setenv(config.EnvTGChatID, "1")
	sink, jobs, after, wait = deliverySetup(context.Background(), cfg, root, nil)
	wait()
	if sink == nil {
		t.Error("delivery on: no sink")
	}
	if got := jobNames(jobs); !slices.Equal(got, []string{"db backup", "git push pending", "source alerts"}) {
		t.Errorf("delivery on: jobs = %v", got)
	}
	if after != nil {
		t.Error("site off: afterWeekly is set")
	}

	cfg.Site.Enabled, cfg.Site.Dir, cfg.Site.Remote = true, t.TempDir(), "git@example.test:o/site.git"
	sink, jobs, after, wait = deliverySetup(context.Background(), cfg, root, nil)
	wait()
	if after == nil {
		t.Error("site on: no afterWeekly")
	}
	if got := jobNames(jobs); !slices.Equal(got, []string{"site push pending", "db backup", "git push pending", "source alerts"}) {
		t.Errorf("site on: jobs = %v", got)
	}
	_ = sink
}

// The daemon lost its delivery once by an edit of run (e39e11d): exports were
// written and went nowhere, with `doctor` green.
func TestDaemonUsesDelivery(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"deliverySetup(ctx, env.Config, env.Root, st)", "Sink: sink, Jobs: jobs,", "AfterWeekly: afterWeekly", "waitDelivery()"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("main.go does not have %q", want)
		}
	}
}
