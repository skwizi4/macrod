// Package scheduler polls sources when the calendar says data is due
// (ТЗ §8.2) and runs the periodic jobs (§8.3).
package scheduler

import (
	"context"
	"time"

	"macro-collector/internal/model"
)

// Clock is the time source; tests replace it with a virtual one.
type Clock interface {
	Now() time.Time
	// Sleep waits d or until ctx is done.
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Plan decides when an event is polled.
type Plan interface {
	// Start is the first poll.
	Start(ev model.ReleaseEvent) time.Time
	// Next is the poll after one at last; ok is false when polling is over
	// (the event times out).
	Next(ev model.ReleaseEvent, last time.Time) (next time.Time, ok bool)
}

// StandardPlan is §8.2: from scheduled_at every minute for 15 minutes, then
// every 5 minutes up to 3 hours, then every 30 minutes up to 24 hours.
type StandardPlan struct{}

const pollWindow = 24 * time.Hour

// Start implements Plan.
func (StandardPlan) Start(ev model.ReleaseEvent) time.Time { return ev.ScheduledAt }

// Next implements Plan.
func (StandardPlan) Next(ev model.ReleaseEvent, last time.Time) (time.Time, bool) {
	elapsed := last.Sub(ev.ScheduledAt)
	var step time.Duration
	switch {
	case elapsed < 15*time.Minute:
		step = time.Minute
	case elapsed < 3*time.Hour:
		step = 5 * time.Minute
	default:
		step = 30 * time.Minute
	}
	next := last.Add(step)
	if next.Sub(ev.ScheduledAt) > pollWindow {
		return time.Time{}, false
	}
	return next, true
}

// FOMCPlan is the decision-day mode of §8.2: from one minute before the
// 14:00 ET release every 20 seconds for 30 minutes, then the standard steps.
type FOMCPlan struct{}

const (
	fomcLead     = time.Minute
	fomcInterval = 20 * time.Second
	fomcFastFor  = 30 * time.Minute
)

// Start implements Plan.
func (FOMCPlan) Start(ev model.ReleaseEvent) time.Time { return ev.ScheduledAt.Add(-fomcLead) }

// Next implements Plan.
func (FOMCPlan) Next(ev model.ReleaseEvent, last time.Time) (time.Time, bool) {
	if next := last.Add(fomcInterval); next.Sub(ev.ScheduledAt) <= fomcFastFor {
		return next, true
	}
	return StandardPlan{}.Next(ev, last)
}
