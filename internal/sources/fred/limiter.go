package fred

import (
	"context"
	"sync"
	"time"
)

// limiter spaces requests at least interval apart.
type limiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

func newLimiter(interval time.Duration) *limiter {
	return &limiter{interval: interval}
}

// wait blocks until the caller may send a request.
func (l *limiter) wait(ctx context.Context) error {
	if l.interval <= 0 {
		return ctx.Err()
	}
	l.mu.Lock()
	now := time.Now()
	slot := l.next
	if now.After(slot) {
		slot = now
	}
	l.next = slot.Add(l.interval)
	l.mu.Unlock()
	if d := slot.Sub(now); d > 0 {
		return sleepCtx(ctx, d)
	}
	return ctx.Err()
}
