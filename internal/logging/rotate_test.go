package logging

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestDailyFileRotates(t *testing.T) {
	dir := t.TempDir()
	// rotated files 2026-10-28 … 2026-11-04 (8 days)
	for _, day := range []string{"2026-10-28", "2026-10-29", "2026-10-30", "2026-10-31", "2026-11-01", "2026-11-02", "2026-11-03", "2026-11-04"} {
		if err := os.WriteFile(filepath.Join(dir, "macrod-"+day+".log"), []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 5, 23, 59, 0, 0, time.UTC)
	f, err := openDaily(dir, "macrod", 7, time.UTC, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("day 5\n")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute) // 2026-11-06
	if _, err := f.Write([]byte("day 6\n")); err != nil {
		t.Fatal(err)
	}

	cur, _ := os.ReadFile(filepath.Join(dir, "macrod.log"))
	if string(cur) != "day 6\n" {
		t.Errorf("current file %q", cur)
	}
	var names []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// 2026-11-05 rotated in, the 7 newest kept, plus today's file
	want := []string{"macrod-2026-10-30.log", "macrod-2026-10-31.log", "macrod-2026-11-01.log", "macrod-2026-11-02.log", "macrod-2026-11-03.log", "macrod-2026-11-04.log", "macrod-2026-11-05.log", "macrod.log"}
	if !slices.Equal(names, want) {
		t.Errorf("files\n%v\nwant\n%v", names, want)
	}
}

func TestDailyFileLeftFromEarlierDay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "macroctl.log")
	if err := os.WriteFile(path, []byte("yesterday\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	y := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, y, y); err != nil {
		t.Fatal(err)
	}
	f, err := openDaily(dir, "macroctl", 7, time.UTC, func() time.Time { return y.Add(24 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("today\n")); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "macroctl-2026-11-05.log"))
	cur, _ := os.ReadFile(path)
	if string(old) != "yesterday\n" || string(cur) != "today\n" {
		t.Errorf("old %q, current %q", old, cur)
	}
}
