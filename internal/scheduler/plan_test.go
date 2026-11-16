package scheduler

import (
	"testing"
	"time"

	"macro-collector/internal/model"
)

func TestStandardPlan(t *testing.T) {
	start := time.Date(2026, 10, 29, 12, 30, 0, 0, time.UTC)
	ev := model.ReleaseEvent{ScheduledAt: start}
	var p StandardPlan
	if !p.Start(ev).Equal(start) {
		t.Fatal("start")
	}
	var polls []time.Duration
	for at, ok := p.Start(ev), true; ok; at, ok = p.Next(ev, at) {
		polls = append(polls, at.Sub(start))
	}
	// 0..15 min every minute (16), 20 min..3 h every 5 (33), 3h30..24h every 30 (42).
	if len(polls) != 16+33+42 {
		t.Fatalf("polls = %d", len(polls))
	}
	for i, want := range map[int]time.Duration{0: 0, 15: 15 * time.Minute, 16: 20 * time.Minute,
		48: 3 * time.Hour, 49: 3*time.Hour + 30*time.Minute, 90: 24 * time.Hour} {
		if polls[i] != want {
			t.Errorf("poll %d at +%v, want +%v", i, polls[i], want)
		}
	}
	// After a restart in the 5-minute phase the steps continue from there.
	if next, _ := p.Next(ev, start.Add(2*time.Hour+7*time.Minute)); next.Sub(start) != 2*time.Hour+12*time.Minute {
		t.Errorf("restart next = +%v", next.Sub(start))
	}
}

func TestFOMCPlan(t *testing.T) {
	decision := time.Date(2026, 10, 28, 18, 0, 0, 0, time.UTC) // 14:00 EDT
	ev := model.ReleaseEvent{ScheduledAt: decision}
	var p FOMCPlan
	at := p.Start(ev)
	if at.Sub(decision) != -time.Minute {
		t.Fatalf("start = %v", at)
	}
	var fast int
	for at.Sub(decision) <= 30*time.Minute {
		fast++
		next, _ := p.Next(ev, at)
		if next.Sub(at) != 20*time.Second && next.Sub(decision) <= 30*time.Minute {
			t.Fatalf("step at +%v = %v", at.Sub(decision), next.Sub(at))
		}
		at = next
	}
	// 13:59:00 … 14:30:00 every 20 s = 94 polls, then 5-minute steps.
	if fast != 94 || at.Sub(decision) != 35*time.Minute {
		t.Errorf("fast polls %d, then next at +%v", fast, at.Sub(decision))
	}
}
