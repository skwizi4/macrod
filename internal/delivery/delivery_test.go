package delivery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"macro-collector/internal/export"
	"macro-collector/internal/gitsync"
)

type fakeGit struct {
	res   gitsync.Result
	err   error
	msg   string
	paths []string
}

func (g *fakeGit) Sync(_ context.Context, msg string, paths ...string) (gitsync.Result, error) {
	g.msg, g.paths = msg, paths
	return g.res, g.err
}

type fakeNotify struct {
	mu   sync.Mutex
	sent []string
}

func (n *fakeNotify) Send(_ context.Context, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, text)
	return nil
}

// weeklyExport writes two agent files and an archive copy under a temp
// exchange dir.
func weeklyExport(t *testing.T) export.Exported {
	t.Helper()
	dir := t.TempDir()
	head := "---\nregion: usa\niso_week: 2026-W45\nperiod: 2026-11-01 16:00 → 2026-11-08 16:00 (Europe/Bratislava)\n"
	files := map[string]string{
		"usa/news/current_week.md":                  head + "sources_failed: [yahoo]\nstale_series: [CCSA]\n---\n" + strings.Repeat("я", 1500),
		"usa/master/markets_usa.md":                 head + "sources_failed: []\nstale_series: [CCSA, ULCNFB]\n---\nshort",
		"archive/2026-W45/usa/news/current_week.md": "copy",
	}
	var paths []string
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return export.Exported{Kind: export.KindWeekly, Region: "usa", ISOWeek: "2026-W45", Files: paths}
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func gitSink(g Git, repo, dir string) *GitSink {
	return &GitSink{Git: g, Repo: repo, Dir: dir, Log: discard,
		Message: func(w, k, r string) string { return "export " + w + " " + r + " " + k }}
}

func tgSink(n Notifier) *TelegramSink {
	return &TelegramSink{Notify: n, Title: func(r string) string { return map[string]string{"usa": "США", "eu": "Европа"}[r] }}
}

// chain is git then Telegram, as the daemon builds it; nil ones left out.
func chain(g Git, n Notifier, extra ...Sink) *Chain {
	c := &Chain{Ctx: context.Background(), Log: discard}
	if g != nil {
		c.Sinks = append(c.Sinks, gitSink(g, "example/macro_exchange", ""))
	}
	c.Sinks = append(c.Sinks, extra...)
	if n != nil {
		c.Sinks = append(c.Sinks, tgSink(n))
	}
	return c
}

func TestChainWeekly(t *testing.T) {
	g := &fakeGit{res: gitsync.Result{Committed: true, Pushed: true, Commit: "abc1234"}}
	n := &fakeNotify{}
	c := chain(g, n)
	if err := c.Exported(context.Background(), weeklyExport(t)); err != nil {
		t.Fatal(err)
	}
	c.Wait()
	if g.msg != "export 2026-W45 usa weekly" {
		t.Errorf("commit message %q", g.msg)
	}
	if len(n.sent) != 1 {
		t.Fatalf("sent %q", n.sent)
	}
	msg := n.sent[0]
	for _, want := range []string{
		"✅ Недельный экспорт 2026-W45 — США готов\nПериод: 2026-11-01 16:00 → 2026-11-08 16:00 (Europe/Bratislava)",
		"Файлы (2):\n• master — markets_usa.md ",
		"\n• news — current_week.md 1.",
		"⚠ Не ответили: yahoo",
		"⚠ Устаревшие ряды (2): CCSA, ULCNFB",
		"git: abc1234 → https://github.com/example/macro_exchange/commit/abc1234",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "archive") {
		t.Errorf("archive copy listed:\n%s", msg)
	}
}

func TestChainPushFailureDoesNotFailExport(t *testing.T) {
	g := &fakeGit{res: gitsync.Result{Committed: true}, err: errors.New("git push: Permission denied")}
	n := &fakeNotify{}
	c := chain(g, n)
	if err := c.Exported(context.Background(), weeklyExport(t)); err != nil {
		t.Fatalf("export failed on delivery: %v", err)
	}
	c.Wait()
	if len(n.sent) != 1 || !strings.Contains(n.sent[0], "⚠ git push не прошёл: git push: Permission denied") {
		t.Errorf("sent %q", n.sent)
	}
}

func TestChainFOMCWithoutGit(t *testing.T) {
	n := &fakeNotify{}
	c := chain(nil, n)
	e := export.Exported{Kind: export.KindEventFOMC, Region: "usa", ISOWeek: "2026-W45", Files: []string{
		"/d/exchange/usa/monetary/fomc_diff.md", "/d/exchange/archive/2026-W45/usa/monetary/fomc_diff.md"}}
	_ = c.Exported(context.Background(), e)
	c.Wait()
	if len(n.sent) != 1 || !strings.HasPrefix(n.sent[0], "🏛 FOMC statement экспортирован (2026-W45)") ||
		!strings.Contains(n.sent[0], "• monetary — fomc_diff.md") || strings.Contains(n.sent[0], "git:") {
		t.Errorf("sent %q", n.sent)
	}
}

// The ECB decision day is its own message (phase 11).
func TestChainECBMessage(t *testing.T) {
	n := &fakeNotify{}
	c := chain(nil, n)
	e := export.Exported{Kind: export.KindEventECB, Region: "eu", ISOWeek: "2026-W51", Files: []string{
		"/d/exchange/eu/monetary_macro/ecb_diff.md", "/d/exchange/archive/2026-W51/eu/monetary_macro/ecb_diff.md"}}
	_ = c.Exported(context.Background(), e)
	c.Wait()
	if len(n.sent) != 1 || !strings.HasPrefix(n.sent[0], "🏛 ЕЦБ: решение экспортировано (2026-W51)") ||
		!strings.Contains(n.sent[0], "• monetary_macro — ecb_diff.md") {
		t.Errorf("sent %q", n.sent)
	}
}

func TestChainGitOnly(t *testing.T) {
	g := &fakeGit{}
	c := chain(g, nil)
	_ = c.Exported(context.Background(), weeklyExport(t))
	c.Wait()
	if g.msg == "" {
		t.Error("git not called")
	}
}
