package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// OwnerFile, next to the database, names the host whose macrod uses it.
// SQLite locks and the WAL index do not work between macOS and the Docker
// Desktop VM: a second writer on the other side corrupts the file (it did on
// 2026-11-06 and 2026-11-07). Open refuses while another host's daemon is
// alive; on the same host SQLite's own locking applies.
const OwnerFile = ".macrod-owner"

const (
	ownerBeat  = 30 * time.Second
	ownerStale = 2 * time.Minute
)

type owner struct {
	Host string    `json:"host"`
	PID  int       `json:"pid"`
	Beat time.Time `json:"beat"`
}

// hostname is swapped in tests.
var hostname = os.Hostname

func ownerPath(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), OwnerFile) }

// ErrForeignOwner: the database belongs to a running daemon on another host.
var ErrForeignOwner = errors.New("database is used by a daemon on another host")

// checkOwner returns ErrForeignOwner (wrapped with advice) when a daemon on
// another host has refreshed the owner file within ownerStale.
func checkOwner(dbPath string, now time.Time) error {
	raw, err := os.ReadFile(ownerPath(dbPath))
	if err != nil {
		return nil // no daemon claims it
	}
	var o owner
	if json.Unmarshal(raw, &o) != nil || now.Sub(o.Beat) > ownerStale {
		return nil
	}
	if me, _ := hostname(); me == o.Host {
		return nil
	}
	return fmt.Errorf("%w: %s (pid %d, alive %s ago) — run the command inside it: "+
		"docker compose exec macrod macroctl --config /app/config.yaml <command>, or stop it first (make down)",
		ErrForeignOwner, o.Host, o.PID, now.Sub(o.Beat).Round(time.Second))
}

// ClaimOwner marks the database as used by this daemon and refreshes the mark
// until ctx ends or release is called; release removes it.
func ClaimOwner(ctx context.Context, dbPath string) (release func(), err error) {
	host, err := hostname()
	if err != nil {
		return nil, fmt.Errorf("store owner hostname: %w", err)
	}
	write := func() error {
		raw, _ := json.Marshal(owner{Host: host, PID: os.Getpid(), Beat: time.Now().UTC()})
		tmp := ownerPath(dbPath) + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, ownerPath(dbPath))
	}
	if err := write(); err != nil {
		return nil, fmt.Errorf("store owner %s: %w", ownerPath(dbPath), err)
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(ownerBeat)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = write()
			}
		}
	}()
	return func() {
		cancel()
		<-done
		_ = os.Remove(ownerPath(dbPath))
	}, nil
}

// CheckOwner is the guard of Open for code that opens the file itself
// (macroctl doctor).
func CheckOwner(dbPath string) error { return checkOwner(dbPath, time.Now()) }
