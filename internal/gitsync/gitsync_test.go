package gitsync

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bare makes an empty remote repository.
func bare(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	run(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", dir)
	return dir
}

func newRepo(dir, remote string) *Repo {
	return &Repo{Dir: dir, Remote: remote, Branch: "main", AuthorName: "t", AuthorEmail: "t@t",
		Retry: []time.Duration{time.Millisecond}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncCommitsAndPushes(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote := bare(t)
	dir := filepath.Join(t.TempDir(), "exchange")
	write(t, filepath.Join(dir, "usa", "news", "current_week.md"), "w44\n")
	r := newRepo(dir, remote)

	res, err := r.Sync(ctx, "export 2026-W44 weekly")
	if err != nil || !res.Committed || !res.Pushed || res.Commit == "" {
		t.Fatalf("first sync: %+v, %v", res, err)
	}
	if got := run(t, remote, "log", "--format=%s", "main"); got != "export 2026-W44 weekly" {
		t.Errorf("remote log %q", got)
	}
	if got := run(t, remote, "show", "main:usa/news/current_week.md"); got != "w44" {
		t.Errorf("remote file %q", got)
	}

	// nothing changed: no commit, nothing to push
	res, err = r.Sync(ctx, "again")
	if err != nil || res.Committed || !res.Pushed {
		t.Fatalf("no-op sync: %+v, %v", res, err)
	}
	if n := run(t, remote, "rev-list", "--count", "main"); n != "1" {
		t.Errorf("remote commits = %s", n)
	}
}

func TestFreshRepoAdoptsRemoteHistory(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote := bare(t)
	first := filepath.Join(t.TempDir(), "a")
	write(t, filepath.Join(first, "f.md"), "1\n")
	if _, err := newRepo(first, remote).Sync(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	// a new server: exchange files exist, .git does not
	second := filepath.Join(t.TempDir(), "b")
	write(t, filepath.Join(second, "f.md"), "2\n")
	res, err := newRepo(second, remote).Sync(ctx, "two")
	if err != nil || !res.Committed || !res.Pushed {
		t.Fatalf("sync: %+v, %v", res, err)
	}
	if got := run(t, remote, "log", "--format=%s", "main"); got != "two\none" {
		t.Errorf("remote log %q", got)
	}
}

func TestFailedPushIsSentLater(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote := bare(t)
	dir := filepath.Join(t.TempDir(), "exchange")
	write(t, filepath.Join(dir, "f.md"), "1\n")
	r := newRepo(dir, remote)
	if _, err := r.Sync(ctx, "one"); err != nil {
		t.Fatal(err)
	}

	moved := remote + ".away"
	if err := os.Rename(remote, moved); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "f.md"), "2\n")
	res, err := r.Sync(ctx, "two")
	if err == nil || !res.Committed || res.Pushed || !strings.Contains(err.Error(), "git push") {
		t.Fatalf("sync with remote gone: %+v, %v", res, err)
	}
	if pushed, err := r.PushPending(ctx); err == nil || pushed {
		t.Fatalf("pending while gone: %v, %v", pushed, err)
	}

	if err := os.Rename(moved, remote); err != nil {
		t.Fatal(err)
	}
	if pushed, err := r.PushPending(ctx); err != nil || !pushed {
		t.Fatalf("pending: %v, %v", pushed, err)
	}
	if got := run(t, remote, "log", "--format=%s", "main"); got != "two\none" {
		t.Errorf("remote log %q", got)
	}
	if pushed, err := r.PushPending(ctx); err != nil || pushed {
		t.Errorf("nothing pending: %v, %v", pushed, err)
	}
}

func TestPushPendingWithoutRepo(t *testing.T) {
	needGit(t)
	r := newRepo(t.TempDir(), "/nowhere")
	if pushed, err := r.PushPending(context.Background()); err != nil || pushed {
		t.Errorf("%v, %v", pushed, err)
	}
}

func TestSSHCommand(t *testing.T) {
	r := &Repo{SSHKey: "/app/secrets/deploy_key", KnownHosts: "/app/deploy/github_known_hosts"}
	got := r.sshCommand()
	for _, want := range []string{"-i /app/secrets/deploy_key", "IdentitiesOnly=yes", "StrictHostKeyChecking=yes", "UserKnownHostsFile=/app/deploy/github_known_hosts"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if (&Repo{}).sshCommand() != "" {
		t.Error("no key: want default ssh")
	}
	if subcommand([]string{"-c", "user.name=x", "commit", "-m", "y"}) != "commit" {
		t.Error("subcommand")
	}
}
