package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// ViewsDays is how far back Telegram views are re-read before the weekly
// export: the export week and a day of margin.
const ViewsDays = 8

// ViewsKeeper re-reads Telegram post views once before each weekly export
// (FIXES_W44 E5), in the same window as the Yahoo top-up.
type ViewsKeeper struct {
	// Refresh re-reads the views of posts published since cutoff.
	Refresh  func(ctx context.Context, cutoff time.Time) (updated int, err error)
	Weekly   cron.Schedule
	Location *time.Location
	Clock    Clock // nil: real time
	Log      *slog.Logger

	mu   sync.Mutex
	done time.Time // the weekly export the refresh ran for
}

// Tick runs every YahooRetryEvery.
func (k *ViewsKeeper) Tick(ctx context.Context) {
	now := time.Now()
	if k.Clock != nil {
		now = k.Clock.Now()
	}
	export := k.Weekly.Next(now.In(k.Location))
	if ahead := export.Sub(now); ahead > yahooCatchUpBefore || ahead < yahooCatchUpMin {
		return
	}
	k.mu.Lock()
	done := k.done.Equal(export)
	k.done = export
	k.mu.Unlock()
	if done {
		return
	}
	n, err := k.Refresh(ctx, now.AddDate(0, 0, -ViewsDays))
	logResult(k.Log, "telegram views before the weekly export", err, "posts_updated", n)
}
