package delivery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/gitsync"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// testSite publishes into a local bare repository; build writes one page
// whose text is *page.
func testSite(t *testing.T, page *string, check error) (*Site, *fakeNotify, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	remote := filepath.Join(t.TempDir(), "site.git")
	gitRun(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", remote)
	dir := filepath.Join(t.TempDir(), "site")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n := &fakeNotify{}
	s := &Site{Ctx: context.Background(), Dir: dir, URL: "https://o.github.io/site/",
		Msg: func(w string) string { return "site " + w },
		Git: &gitsync.Repo{Dir: dir, Remote: remote, Branch: "main", AuthorName: "t", AuthorEmail: "t@t",
			Retry: []time.Duration{time.Millisecond}, Log: log},
		Build: func(_ context.Context, dir string) (int, error) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return 0, err
			}
			return 1, os.WriteFile(filepath.Join(dir, "index.html"), []byte(*page), 0o644)
		},
		Check:  func(string) error { return check },
		Notify: n, Log: log}
	return s, n, remote
}

func TestSitePublishes(t *testing.T) {
	page := "week 46"
	s, n, remote := testSite(t, &page, nil)
	s.Start("2026-W46")
	s.Wait()
	if got := gitRun(t, remote, "show", "main:index.html"); got != "week 46" {
		t.Fatalf("published %q", got)
	}
	if got := gitRun(t, remote, "log", "-1", "--format=%s", "main"); got != "site 2026-W46" {
		t.Errorf("commit %q", got)
	}
	res, err := s.Publish(context.Background(), "2026-W46")
	if err != nil || res.Pushed {
		t.Fatalf("same build: %+v %v", res, err)
	}
	page = "week 47"
	if res, err := s.Publish(context.Background(), "2026-W47"); err != nil || !res.Pushed {
		t.Fatalf("new week: %+v %v", res, err)
	}
	if got := gitRun(t, remote, "rev-list", "--count", "main"); got != "2" {
		t.Errorf("commits %s", got)
	}
	if len(n.sent) != 3 || !strings.Contains(n.sent[0], "обновлён") || !strings.Contains(n.sent[1], "изменений нет") ||
		!strings.Contains(n.sent[0], "https://o.github.io/site/") {
		t.Errorf("messages %q", n.sent)
	}
}

func TestSiteCheckStopsThePush(t *testing.T) {
	page := "closed"
	s, n, remote := testSite(t, &page, errors.New("demo site check: series yahoo/^NDX is not in dashboard.demo_allow"))
	if _, err := s.Publish(context.Background(), "2026-W46"); err == nil {
		t.Fatal("no error")
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "-q", "main")
	cmd.Dir = remote
	if cmd.Run() == nil {
		t.Error("a failed check was pushed")
	}
	if len(n.sent) != 1 || !strings.Contains(n.sent[0], "не опубликован") || !strings.Contains(n.sent[0], "yahoo/^NDX") {
		t.Errorf("messages %q", n.sent)
	}
}
