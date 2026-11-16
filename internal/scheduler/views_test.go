package scheduler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

func TestViewsKeeper(t *testing.T) {
	bts, _ := time.LoadLocation("Europe/Bratislava")
	weekly, err := cron.ParseStandard("0 16 * * 0")
	if err != nil {
		t.Fatal(err)
	}
	clock := &fixedClock{}
	var cutoffs []time.Time
	k := &ViewsKeeper{
		Refresh: func(_ context.Context, cutoff time.Time) (int, error) {
			cutoffs = append(cutoffs, cutoff)
			return 3, nil
		},
		Weekly: weekly, Location: bts, Clock: clock, Log: slog.New(slog.DiscardHandler),
	}
	ctx := context.Background()
	clock.set("2026-11-01T13:50:00Z") // 14:50: the export is 70 min ahead
	k.Tick(ctx)
	clock.set("2026-11-01T14:20:00Z") // 15:20
	k.Tick(ctx)
	clock.set("2026-11-01T14:50:00Z") // 15:50: done already
	k.Tick(ctx)
	clock.set("2026-11-01T14:58:00Z") // 15:58: too late
	k.Tick(ctx)
	if want := time.Date(2026, 10, 24, 14, 20, 0, 0, time.UTC); len(cutoffs) != 1 || !cutoffs[0].Equal(want) {
		t.Errorf("refreshes = %v", cutoffs)
	}
}
