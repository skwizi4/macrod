package scheduler

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/calendar"
	"macro-collector/internal/config"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/fred"
)

type fakeFull struct{ *fakeFRED }

func (fakeFull) FetchSeriesRelease(context.Context, string) (fred.Release, error) {
	return fred.Release{}, nil
}

func (fakeFull) FetchReleasesDates(context.Context, time.Time, time.Time) ([]fred.ReleaseDate, error) {
	return nil, nil
}

func TestNewFromCommittedConfig(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	cfg, err := config.LoadAt(filepath.Join("..", "..", "presets", "us-equities.yaml"), filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Deps{Config: cfg, Store: openStore(t), FRED: fakeFull{&fakeFRED{}}, Fed: fedtexts.New(),
		Root: slog.New(slog.DiscardHandler), FetchCron: "0 6 * * *"})

	names := map[string]bool{}
	for _, r := range s.Events.Routes {
		names[r.Source+"/"+r.Name] = true
	}
	for _, k := range []string{calendar.SourceFRED + "/", "fomc/FOMC decision", "fomc/FOMC minutes", "beige_book/Beige Book"} {
		if !names[k] {
			t.Errorf("no route for %s", k)
		}
	}
	if len(s.Jobs) != 6 { // phase 2 jobs + timeout recheck + weekly export
		t.Errorf("jobs = %d", len(s.Jobs))
	}

	// All cron specs parse, and Run returns once ctx is cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// The demo site goes out once per weekly export, after every region.
func TestWeeklyExportCallsAfterWeekly(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	cfg, err := config.LoadAt(filepath.Join("..", "..", "presets", "us-equities.yaml"), filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var weeks []string
	s := New(Deps{Config: cfg, Store: openStore(t), FRED: fakeFull{&fakeFRED{}}, Fed: fedtexts.New(),
		Root: slog.New(slog.DiscardHandler), FetchCron: "0 6 * * *",
		AfterWeekly: func(w string) { weeks = append(weeks, w) }})
	for _, j := range s.Jobs {
		if j.Name == "weekly export" {
			j.Run(context.Background())
		}
	}
	if len(weeks) != 1 || weeks[0] == "" {
		t.Fatalf("afterWeekly calls: %q", weeks)
	}
}
