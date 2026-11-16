package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

// fakeClock is virtual time: Sleep returns at once and moves the clock.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	onSleep func(now time.Time) // called after each move
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if d > 0 {
		c.now = c.now.Add(d)
	}
	now, hook := c.now, c.onSleep
	c.mu.Unlock()
	if hook != nil {
		hook(now)
	}
	return nil
}

// fakeHandler records poll times and is done at poll doneAt (1-based; 0 = never).
type fakeHandler struct {
	clock  Clock
	polls  []time.Time
	doneAt int
	expire string
}

func (h *fakeHandler) Poll(context.Context, model.ReleaseEvent) (bool, error) {
	h.polls = append(h.polls, h.clock.Now())
	return h.doneAt > 0 && len(h.polls) >= h.doneAt, nil
}

type expiringHandler struct{ *fakeHandler }

func (h expiringHandler) Expire(context.Context, model.ReleaseEvent) string { return h.expire }

var t0 = time.Date(2026, 10, 29, 12, 30, 0, 0, time.UTC) // claims, 08:30 EDT

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func addEvent(t *testing.T, st *store.Store, at time.Time, status string) int64 {
	t.Helper()
	ev := model.ReleaseEvent{Source: "fred_release", Name: "Claims", ScheduledAt: at, Region: "usa",
		Related: model.Related{ReleaseID: 180, Series: []string{"ICSA"}}}
	ctx := context.Background()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := store.SyncReleases(tx, ev.Source, ev.Name, at, at.Add(time.Second), []model.ReleaseEvent{ev})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	evs, err := st.Releases(ctx, at, at.Add(time.Second))
	if err != nil || len(evs) != 1 {
		t.Fatalf("event: %v %v", evs, err)
	}
	if status != model.StatusScheduled {
		if err := st.Write(ctx, func(tx *sql.Tx) error { return store.SetReleaseStatus(tx, evs[0].ID, status, at) }); err != nil {
			t.Fatal(err)
		}
	}
	return evs[0].ID
}

func newEvents(st *store.Store, clock Clock, h Handler) *Events {
	return &Events{Store: st, Clock: clock, Log: slog.New(slog.DiscardHandler),
		Routes: []Route{{Source: "fred_release", Handler: h}}, spawn: func(f func()) { f() }}
}

func eventOf(t *testing.T, st *store.Store, id int64) model.ReleaseEvent {
	t.Helper()
	e, ok, err := st.Release(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("release %d: %v", id, err)
	}
	return e
}

func offsets(polls []time.Time, from time.Time) []time.Duration {
	var out []time.Duration
	for _, p := range polls {
		out = append(out, p.Sub(from))
	}
	return out
}

func TestEventPolledFromScheduledTimeUntilDone(t *testing.T) {
	st := openStore(t)
	id := addEvent(t, st, t0, model.StatusScheduled)
	clock := &fakeClock{now: t0.Add(-5 * time.Minute)}
	h := &fakeHandler{clock: clock, doneAt: 3}
	ev := newEvents(st, clock, h)
	ctx := context.Background()

	ev.Dispatch(ctx) // 5 min early: outside the 2-min lead
	if len(h.polls) != 0 || eventOf(t, st, id).Status != model.StatusScheduled {
		t.Fatal("polled too early")
	}
	clock.now = t0.Add(-90 * time.Second)
	ev.Dispatch(ctx)
	if got := offsets(h.polls, t0); len(got) != 3 || got[0] != 0 || got[1] != time.Minute || got[2] != 2*time.Minute {
		t.Fatalf("polls at %v, want +0, +1m, +2m", got)
	}
	e := eventOf(t, st, id)
	if e.Status != model.StatusDone || !e.DoneAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("event = %+v", e)
	}
	// Done events are not picked up again.
	ev.Dispatch(ctx)
	if len(h.polls) != 3 {
		t.Error("done event polled again")
	}
}

func TestEventTimesOut(t *testing.T) {
	st := openStore(t)
	id := addEvent(t, st, t0, model.StatusScheduled)
	clock := &fakeClock{now: t0}
	h := &fakeHandler{clock: clock}
	newEvents(st, clock, h).Dispatch(context.Background())
	if len(h.polls) != 91 {
		t.Fatalf("polls = %d, want 91", len(h.polls))
	}
	if last := h.polls[len(h.polls)-1].Sub(t0); last != 24*time.Hour {
		t.Errorf("last poll at +%v", last)
	}
	if e := eventOf(t, st, id); e.Status != model.StatusTimeout || !e.DoneAt.Equal(t0.Add(24*time.Hour)) {
		t.Errorf("event = %+v", e)
	}
}

func TestEventExpirerDecidesStatus(t *testing.T) {
	st := openStore(t)
	id := addEvent(t, st, t0, model.StatusScheduled)
	clock := &fakeClock{now: t0}
	h := expiringHandler{&fakeHandler{clock: clock, expire: model.StatusDone}}
	newEvents(st, clock, h).Dispatch(context.Background())
	if e := eventOf(t, st, id); e.Status != model.StatusDone {
		t.Errorf("status = %s", e.Status)
	}
}

func TestEventResumedAfterRestart(t *testing.T) {
	st := openStore(t)
	id := addEvent(t, st, t0, model.StatusPolling)
	start := t0.Add(2*time.Hour + 7*time.Minute) // daemon back in the 5-minute phase
	clock := &fakeClock{now: start}
	h := &fakeHandler{clock: clock, doneAt: 2}
	newEvents(st, clock, h).Dispatch(context.Background())
	if got := offsets(h.polls, t0); len(got) != 2 || got[0] != 2*time.Hour+7*time.Minute || got[1] != 2*time.Hour+12*time.Minute {
		t.Fatalf("polls at %v", got)
	}
	if eventOf(t, st, id).Status != model.StatusDone {
		t.Error("not done")
	}
}

func TestMissedEventTimesOutWithoutPolling(t *testing.T) {
	st := openStore(t)
	id := addEvent(t, st, t0, model.StatusScheduled)
	clock := &fakeClock{now: t0.Add(50 * time.Hour)}
	h := &fakeHandler{clock: clock}
	newEvents(st, clock, h).Dispatch(context.Background())
	if len(h.polls) != 0 || eventOf(t, st, id).Status != model.StatusTimeout {
		t.Errorf("polls %d, status %s", len(h.polls), eventOf(t, st, id).Status)
	}
}

func TestRescheduledWhileWaiting(t *testing.T) {
	st := openStore(t)
	id := addEvent(t, st, t0, model.StatusScheduled)
	clock := &fakeClock{now: t0.Add(-time.Minute)}
	// The calendar marks the event rescheduled during the wait for its start.
	clock.onSleep = func(time.Time) {
		clock.onSleep = nil
		if err := st.Write(context.Background(), func(tx *sql.Tx) error {
			return store.SetReleaseStatus(tx, id, model.StatusRescheduled, t0)
		}); err != nil {
			t.Error(err)
		}
	}
	h := &fakeHandler{clock: clock, doneAt: 1}
	newEvents(st, clock, h).Dispatch(context.Background())
	if len(h.polls) != 0 || eventOf(t, st, id).Status != model.StatusRescheduled {
		t.Errorf("polls %d, status %s", len(h.polls), eventOf(t, st, id).Status)
	}
}

// blockingHandler blocks until released, to check that a running event is
// not started twice.
type blockingHandler struct {
	mu      sync.Mutex
	calls   int
	release chan struct{}
}

func (h *blockingHandler) Poll(ctx context.Context, _ model.ReleaseEvent) (bool, error) {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return true, nil
}

func TestRunningEventNotStartedTwice(t *testing.T) {
	st := openStore(t)
	id := addEvent(t, st, time.Now().Add(-time.Second).Truncate(time.Second), model.StatusScheduled)
	h := &blockingHandler{release: make(chan struct{})}
	ev := &Events{Store: st, Log: slog.New(slog.DiscardHandler), Routes: []Route{{Source: "fred_release", Handler: h}}}
	ctx := context.Background()
	ev.Dispatch(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		c := h.calls
		h.mu.Unlock()
		if c == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("poller did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ev.Dispatch(ctx) // still polling: no second poller
	close(h.release)
	ev.wg.Wait()
	if h.calls != 1 || eventOf(t, st, id).Status != model.StatusDone {
		t.Errorf("calls %d, status %s", h.calls, eventOf(t, st, id).Status)
	}
}

// A missed event whose data came from the safety jobs is done, not timed out.
func TestMissedEventWithDataIsDone(t *testing.T) {
	st := openStore(t)
	id := addEvent(t, st, t0, model.StatusScheduled)
	clock := &fakeClock{now: t0.Add(50 * time.Hour)}
	h := expiringHandler{&fakeHandler{clock: clock, expire: model.StatusDone}}
	newEvents(st, clock, h).Dispatch(context.Background())
	if len(h.polls) != 0 || eventOf(t, st, id).Status != model.StatusDone {
		t.Errorf("polls %d, status %s", len(h.polls), eventOf(t, st, id).Status)
	}
}

func TestRecheckTimedOutEvents(t *testing.T) {
	st := openStore(t)
	recent := addEvent(t, st, t0, model.StatusTimeout)
	old := addEvent(t, st, t0.Add(-8*24*time.Hour), model.StatusTimeout)
	clock := &fakeClock{now: t0.Add(30 * time.Hour)}
	ctx := context.Background()

	h := expiringHandler{&fakeHandler{clock: clock, expire: model.StatusTimeout}}
	newEvents(st, clock, h).Recheck(ctx, recheckWindow)
	if eventOf(t, st, recent).Status != model.StatusTimeout {
		t.Fatal("still no data: must stay timeout")
	}
	h.expire = model.StatusDone
	newEvents(st, clock, h).Recheck(ctx, recheckWindow)
	if e := eventOf(t, st, recent); e.Status != model.StatusDone || !e.DoneAt.Equal(clock.now) {
		t.Errorf("recent = %+v, want done now", e)
	}
	if eventOf(t, st, old).Status != model.StatusTimeout {
		t.Error("event older than the window rechecked")
	}
	if len(h.polls) != 0 {
		t.Errorf("recheck polled %d times", len(h.polls))
	}
}
