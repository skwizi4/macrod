package delivery

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"macro-collector/internal/export"
	"macro-collector/internal/gitsync"
)

// Two regions exported one after the other (step 19): each delivery
// commits only its own files — the region's folders, its JSON and archive,
// and its archive-only news files — and each message names its region and
// its own commit. Before, the first `git add -A .` took both regions and
// the second message said "изменений нет".
func TestTwoRegionsTwoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	dir := filepath.Join(tmp, "exchange")
	repo := &gitsync.Repo{Dir: dir, Remote: remote, Branch: "main", AuthorName: "t", AuthorEmail: "t@example.com",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	write := func(rel string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("---\nperiod: P\nsources_failed: []\n---\n"+rel+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	exp := func(region string) export.Exported {
		return export.Exported{Kind: export.KindWeekly, Region: region, ISOWeek: "2026-W46", Files: []string{
			write(region + "/news/current_week.md"), write("json/" + region + "/news/current_week.json"),
			write("archive/2026-W46/" + region + "/news/current_week.md"), write("archive/2026-W46/news_filtered.md"),
		}}
	}
	// Both regions' files are on disk before the first delivery runs.
	usa, eu := exp("usa"), exp("eu")
	n := &fakeNotify{}
	c := &Chain{Ctx: ctx, Sinks: []Sink{gitSink(repo, "", dir), tgSink(n)}, Log: discard}
	for _, e := range []export.Exported{usa, eu} {
		if err := c.Exported(ctx, e); err != nil {
			t.Fatal(err)
		}
		c.Wait()
	}
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	files := func(rev string) []string {
		return strings.Split(git("show", "--name-only", "--format=", rev), "\n")
	}
	if subj := git("log", "--format=%s"); subj != "export 2026-W46 eu weekly\nexport 2026-W46 usa weekly" {
		t.Fatalf("commits:\n%s", subj)
	}
	first, second := files("HEAD~1"), files("HEAD")
	if slices.ContainsFunc(first, func(f string) bool { return strings.Contains(f, "eu/") }) || !slices.Contains(first, "archive/2026-W46/news_filtered.md") {
		t.Errorf("usa commit: %v", first)
	}
	if len(second) != 3 || slices.ContainsFunc(second, func(f string) bool { return strings.Contains(f, "usa/") }) {
		t.Errorf("eu commit: %v (news_filtered.md is unchanged: same content)", second)
	}
	if git("status", "--porcelain") != "" {
		t.Errorf("left uncommitted:\n%s", git("status", "--porcelain"))
	}
	if len(n.sent) != 2 {
		t.Fatalf("messages %q", n.sent)
	}
	for i, want := range []string{"✅ Недельный экспорт 2026-W46 — США готов", "✅ Недельный экспорт 2026-W46 — Европа готов"} {
		hash := git("rev-parse", "--short", []string{"HEAD~1", "HEAD"}[i])
		if !strings.HasPrefix(n.sent[i], want) || !strings.Contains(n.sent[i], "git: "+hash) {
			t.Errorf("message %d:\n%s\nwant %q and git: %s", i, n.sent[i], want, hash)
		}
	}
}
