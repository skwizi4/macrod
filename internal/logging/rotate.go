package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// KeepDays is how many daily log files are kept (ТЗ §14: 7 days).
const KeepDays = 7

// dailyFile is <dir>/<app>.log that, on the first write of a new day, is
// renamed to <app>-YYYY-MM-DD.log (the day it covers); files older than
// keep days are deleted. The name of the current file stays the same.
type dailyFile struct {
	dir, app string
	keep     int
	loc      *time.Location
	now      func() time.Time

	mu  sync.Mutex
	f   *os.File
	day string // day of the records in f
}

func openDaily(dir, app string, keep int, loc *time.Location, now func() time.Time) (*dailyFile, error) {
	if loc == nil {
		loc = time.Local
	}
	d := &dailyFile{dir: dir, app: app, keep: keep, loc: loc, now: now}
	path := d.current()
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		d.day = fi.ModTime().In(loc).Format(time.DateOnly) // a file left from an earlier day rotates on the first write
	}
	if err := d.open(); err != nil {
		return nil, err
	}
	if d.day == "" {
		d.day = d.today()
	}
	return d, nil
}

func (d *dailyFile) current() string { return filepath.Join(d.dir, d.app+".log") }

func (d *dailyFile) today() string { return d.now().In(d.loc).Format(time.DateOnly) }

func (d *dailyFile) open() error {
	f, err := os.OpenFile(d.current(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("logging open %s: %w", d.current(), err)
	}
	d.f = f
	return nil
}

func (d *dailyFile) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if today := d.today(); today != d.day {
		d.rotate(today)
	}
	return d.f.Write(p)
}

// rotate never fails the write: on error it keeps appending to the old file.
func (d *dailyFile) rotate(today string) {
	old := filepath.Join(d.dir, d.app+"-"+d.day+".log")
	if err := d.f.Close(); err != nil {
		return
	}
	if _, err := os.Stat(old); err != nil { // never overwrite an existing day
		_ = os.Rename(d.current(), old)
	}
	if err := d.open(); err != nil {
		// cannot reopen: try the old name so records are not lost
		d.f, _ = os.OpenFile(old, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	d.day = today
	d.prune()
}

// prune deletes <app>-YYYY-MM-DD.log beyond the newest keep.
func (d *dailyFile) prune() {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	prefix := d.app + "-"
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".log") {
			if _, err := time.Parse(time.DateOnly, strings.TrimSuffix(strings.TrimPrefix(n, prefix), ".log")); err == nil {
				names = append(names, n)
			}
		}
	}
	slices.Sort(names)
	for len(names) > d.keep {
		_ = os.Remove(filepath.Join(d.dir, names[0]))
		names = names[1:]
	}
}

func (d *dailyFile) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.f.Close()
}
