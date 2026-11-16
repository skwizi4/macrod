// Package gitsync delivers exchange/ to a private git repository after each
// export (ТЗ §12.10, D10): commit everything, push, retry on failure. It runs
// the git CLI; the push goes over ssh with a deploy key.
package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultRetry are the pauses between push attempts: 4 attempts in ~21 min.
var DefaultRetry = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

// Repo is a working tree (the exchange directory) with one remote.
type Repo struct {
	Dir         string
	Remote      string // git@github.com:owner/name.git, or a path in tests
	Branch      string
	AuthorName  string
	AuthorEmail string
	SSHKey      string // private key path; empty: ssh defaults
	KnownHosts  string // known_hosts path for the remote host; empty: ssh defaults
	Retry       []time.Duration
	Log         *slog.Logger

	mu sync.Mutex // one git operation at a time
}

// Result of a Sync.
type Result struct {
	Committed bool   // a new commit was made
	Pushed    bool   // the branch is on the remote
	Commit    string // short hash of HEAD
}

// Sync initialises the repository if needed, commits the changes under
// paths (relative to Dir; none: every change) with msg and pushes, retrying
// the push per Retry. Paths keep a region's export to its own commit: the
// files another region wrote meanwhile wait for that region's Sync. A
// failed push leaves the commit in place: the next Sync or PushPending
// sends it.
func (r *Repo) Sync(ctx context.Context, msg string, paths ...string) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res Result
	if err := r.ensure(ctx); err != nil {
		return res, err
	}
	committed, err := r.commit(ctx, msg, paths)
	if err != nil {
		return res, err
	}
	res.Committed = committed
	res.Commit, _ = r.git(ctx, "rev-parse", "--short", "HEAD")
	ahead, err := r.ahead(ctx)
	if err != nil || !ahead {
		res.Pushed = err == nil
		return res, err
	}
	if err := r.pushRetry(ctx); err != nil {
		return res, err
	}
	res.Pushed = true
	return res, nil
}

// PushPending pushes local commits the remote does not have yet (one
// attempt); it does nothing when there are none.
func (r *Repo) PushPending(ctx context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); err != nil {
		return false, nil
	}
	ahead, err := r.ahead(ctx)
	if err != nil || !ahead {
		return false, err
	}
	if err := r.push(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// LsRemote checks that the remote answers with the configured key.
func (r *Repo) LsRemote(ctx context.Context) error {
	_, err := r.gitIn(ctx, "", "ls-remote", "--heads", r.Remote)
	return err
}

// ensure creates the repository. A fresh one adopts the remote branch, if
// any, so local history continues it (files on disk are kept).
func (r *Repo) ensure(ctx context.Context) error {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return fmt.Errorf("gitsync mkdir %s: %w", r.Dir, err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, err := r.git(ctx, "init", "-q", "-b", r.Branch); err != nil {
			return err
		}
		if _, err := r.git(ctx, "remote", "add", "origin", r.Remote); err != nil {
			return err
		}
		r.Log.Info("git repository created", "dir", r.Dir, "remote", r.Remote)
	} else if err != nil {
		return fmt.Errorf("gitsync stat %s: %w", r.Dir, err)
	}
	if _, err := r.git(ctx, "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		return nil // has history
	}
	if _, err := r.git(ctx, "fetch", "-q", "origin"); err != nil {
		return err
	}
	if _, err := r.git(ctx, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+r.Branch); err == nil {
		if _, err := r.git(ctx, "reset", "-q", "--mixed", "origin/"+r.Branch); err != nil {
			return err
		}
		r.Log.Info("git history taken from the remote", "branch", r.Branch)
	}
	return nil
}

func (r *Repo) commit(ctx context.Context, msg string, paths []string) (bool, error) {
	args := []string{"add", "-A", "--", "."}
	if len(paths) > 0 {
		args = []string{"add", "-A", "--"}
		for _, p := range paths {
			// A path that never existed is no pathspec git accepts; a
			// deleted tracked file still is.
			if _, err := os.Stat(filepath.Join(r.Dir, p)); err == nil {
				args = append(args, p)
			} else if out, _ := r.git(ctx, "ls-files", "--", p); out != "" {
				args = append(args, p)
			}
		}
		if len(args) == 3 {
			return false, nil
		}
	}
	if _, err := r.git(ctx, args...); err != nil {
		return false, err
	}
	// Only what is staged counts: other regions' files may be changed too.
	out, err := r.git(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return false, err
	}
	if out == "" {
		return false, nil
	}
	if _, err := r.git(ctx, "-c", "user.name="+r.AuthorName, "-c", "user.email="+r.AuthorEmail,
		"commit", "-q", "-m", msg); err != nil {
		return false, err
	}
	return true, nil
}

// ahead reports whether HEAD has commits the remote-tracking branch lacks
// (no network: the tracking ref is updated by fetch and push).
func (r *Repo) ahead(ctx context.Context) (bool, error) {
	if _, err := r.git(ctx, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		return false, nil // nothing committed yet
	}
	if _, err := r.git(ctx, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+r.Branch); err != nil {
		return true, nil
	}
	out, err := r.git(ctx, "rev-list", "--count", "origin/"+r.Branch+"..HEAD")
	if err != nil {
		return false, err
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return false, fmt.Errorf("gitsync rev-list %q: %w", out, err)
	}
	return n > 0, nil
}

func (r *Repo) push(ctx context.Context) error {
	_, err := r.git(ctx, "push", "-q", "origin", "HEAD:refs/heads/"+r.Branch)
	if err == nil {
		// keep the tracking ref in step, as a push to a configured upstream would
		_, err = r.git(ctx, "update-ref", "refs/remotes/origin/"+r.Branch, "HEAD")
	}
	return err
}

func (r *Repo) pushRetry(ctx context.Context) error {
	err := r.push(ctx)
	for i := 0; err != nil && i < len(r.Retry); i++ {
		r.Log.Warn("git push failed, retrying", "err", err, "attempt", i+1, "pause", r.Retry[i])
		select {
		case <-ctx.Done():
			return fmt.Errorf("gitsync push: %w (stopped: %w)", err, ctx.Err())
		case <-time.After(r.Retry[i]):
		}
		err = r.push(ctx)
	}
	return err
}

func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	return r.gitIn(ctx, r.Dir, args...)
}

// gitIn runs git in dir. safe.directory=*: the data volume belongs to the
// host user, not to the container's uid.
func (r *Repo) gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "safe.directory=*"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if ssh := r.sshCommand(); ssh != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+ssh)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", subcommand(args), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// subcommand is the git verb of args, skipping "-c key=value" pairs.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

func (r *Repo) sshCommand() string {
	if r.SSHKey == "" {
		return ""
	}
	s := "ssh -i " + r.SSHKey + " -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes"
	if r.KnownHosts != "" {
		s += " -o UserKnownHostsFile=" + r.KnownHosts
	}
	return s
}
