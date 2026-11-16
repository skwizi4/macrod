package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

// Handler polls the source of one kind of event.
type Handler interface {
	// Poll fetches what the event publishes; done reports that it arrived.
	Poll(ctx context.Context, ev model.ReleaseEvent) (done bool, err error)
}

// Expirer is implemented by handlers that decide the final status
// themselves when the plan runs out (e.g. a release whose series arrived
// only in part is done, not timed out).
type Expirer interface {
	Expire(ctx context.Context, ev model.ReleaseEvent) (status string)
}

// Route is how one event series (releases.source, name) is polled. An empty
// Name matches every event of Source.
type Route struct {
	Source, Name string
	Handler      Handler
	Plan         Plan // nil: StandardPlan
}

// Events runs a poller for every due event in releases.
type Events struct {
	Store  *store.Store
	Routes []Route
	Clock  Clock // nil: real time
	Log    *slog.Logger
	// Tick is how often releases is scanned for due events (default 30 s);
	// Lead is how far ahead of its first poll an event is picked up
	// (default 2 min, covers FOMC polling from 13:59).
	Tick, Lead time.Duration

	// spawn starts a poller; tests run them inline.
	spawn   func(func())
	mu      sync.Mutex
	running map[int64]bool
	wg      sync.WaitGroup
}

// recheckWindow is how far back Run re-decides timed-out events (one
// weekly export).
const recheckWindow = 7 * 24 * time.Hour

// Run scans releases until ctx is done, then waits for running pollers.
func (e *Events) Run(ctx context.Context) {
	tick := e.Tick
	if tick <= 0 {
		tick = 30 * time.Second
	}
	e.Recheck(ctx, recheckWindow)
	for {
		e.Dispatch(ctx)
		if e.clock().Sleep(ctx, tick) != nil {
			break
		}
	}
	e.wg.Wait()
}

// Dispatch makes one pass: events left active for longer than the poll
// window (the daemon was down) time out, and pollers start for due events
// that are not already being polled.
func (e *Events) Dispatch(ctx context.Context) {
	now := e.clock().Now()
	stale, err := e.Store.ActiveReleases(ctx, time.Time{}, now.Add(-pollWindow-time.Hour))
	if err != nil {
		e.Log.Error("load stale events", "err", err)
		return
	}
	for _, ev := range stale {
		// The data may have come meanwhile from the daily safety jobs.
		status := e.expire(ctx, ev)
		e.Log.Warn("event missed (not polled within its window)", "event", ev.Name, "scheduled_at", ev.ScheduledAt, "status", status)
		e.setStatus(ctx, ev, status)
	}

	lead := e.Lead
	if lead <= 0 {
		lead = 2 * time.Minute
	}
	due, err := e.Store.ActiveReleases(ctx, now.Add(-pollWindow-time.Hour), now.Add(2*lead))
	if err != nil {
		e.Log.Error("load due events", "err", err)
		return
	}
	for _, ev := range due {
		r, ok := e.route(ev)
		if !ok || e.planOf(r).Start(ev).After(now.Add(lead)) {
			continue
		}
		e.mu.Lock()
		if e.running == nil {
			e.running = make(map[int64]bool)
		}
		if e.running[ev.ID] {
			e.mu.Unlock()
			continue
		}
		e.running[ev.ID] = true
		e.mu.Unlock()
		e.wg.Add(1)
		run := func() {
			defer e.wg.Done()
			defer func() {
				e.mu.Lock()
				delete(e.running, ev.ID)
				e.mu.Unlock()
			}()
			e.poll(ctx, ev, r)
		}
		if e.spawn != nil {
			e.spawn(run)
		} else {
			go run()
		}
	}
}

// poll runs the plan of one event until the handler reports done, the plan
// ends (timeout) or ctx is cancelled (the event stays active for the next start).
func (e *Events) poll(ctx context.Context, ev model.ReleaseEvent, r Route) {
	clock, plan := e.clock(), e.planOf(r)
	log := e.Log.With("event", ev.Name, "event_id", ev.ID, "scheduled_at", ev.ScheduledAt)
	at := plan.Start(ev)
	if now := clock.Now(); at.Before(now) {
		at = now // restart, or picked up late: poll now, then follow the plan
	}
	for {
		if err := clock.Sleep(ctx, at.Sub(clock.Now())); err != nil {
			return
		}
		// The calendar may have moved the event meanwhile.
		cur, ok, err := e.Store.Release(ctx, ev.ID)
		if err != nil {
			log.Error("reload event", "err", err)
		} else if !ok || (cur.Status != model.StatusScheduled && cur.Status != model.StatusPolling) {
			log.Info("event no longer active, polling stopped", "status", cur.Status)
			return
		}
		if cur.Status == model.StatusScheduled {
			e.setStatus(ctx, ev, model.StatusPolling)
			log.Info("polling started")
		}

		done, err := r.Handler.Poll(ctx, ev)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warn("poll failed", "err", err)
		}
		if done {
			e.setStatus(ctx, ev, model.StatusDone)
			log.Info("event done", "after", clock.Now().Sub(ev.ScheduledAt).Round(time.Second))
			return
		}
		next, ok := plan.Next(ev, at)
		if !ok {
			status := e.expire(ctx, ev)
			if status == model.StatusTimeout {
				log.Warn("event timed out: nothing new within the poll window")
			} else {
				log.Info("event closed at the end of the poll window", "status", status)
			}
			e.setStatus(ctx, ev, status)
			return
		}
		at = next
	}
}

// expire is the final status of an event that is no longer polled: the
// route's Expirer decides, timeout without one.
func (e *Events) expire(ctx context.Context, ev model.ReleaseEvent) string {
	if r, ok := e.route(ev); ok {
		if x, isExp := r.Handler.(Expirer); isExp {
			return x.Expire(ctx, ev)
		}
	}
	return model.StatusTimeout
}

// Recheck re-decides the timed-out events of the last `since`: their data
// may have arrived later from the daily safety jobs. Run calls it once at
// start.
func (e *Events) Recheck(ctx context.Context, since time.Duration) {
	now := e.clock().Now()
	evs, err := e.Store.Releases(ctx, now.Add(-since), now)
	if err != nil {
		e.Log.Error("load timed-out events", "err", err)
		return
	}
	for _, ev := range evs {
		if ev.Status != model.StatusTimeout {
			continue
		}
		if status := e.expire(ctx, ev); status != model.StatusTimeout {
			e.Log.Info("timed-out event has its data, status updated", "event", ev.Name, "scheduled_at", ev.ScheduledAt, "status", status)
			e.setStatus(ctx, ev, status)
		}
	}
}

func (e *Events) setStatus(ctx context.Context, ev model.ReleaseEvent, status string) {
	if err := e.Store.Write(ctx, func(tx *sql.Tx) error {
		return store.SetReleaseStatus(tx, ev.ID, status, e.clock().Now())
	}); err != nil && ctx.Err() == nil {
		e.Log.Error("set event status", "event_id", ev.ID, "status", status, "err", err)
	}
}

func (e *Events) route(ev model.ReleaseEvent) (Route, bool) {
	for _, r := range e.Routes {
		if r.Source == ev.Source && (r.Name == "" || r.Name == ev.Name) {
			return r, true
		}
	}
	return Route{}, false
}

func (e *Events) planOf(r Route) Plan {
	if r.Plan != nil {
		return r.Plan
	}
	return StandardPlan{}
}

func (e *Events) clock() Clock {
	if e.Clock != nil {
		return e.Clock
	}
	return realClock{}
}
