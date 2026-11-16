package transform

import (
	"slices"
	"testing"
	"time"
)

func TestStale(t *testing.T) {
	tests := []struct {
		freq, last, asOf string
		want             bool
	}{
		// Daily: more than 5 business days.
		{"d", "2026-10-16", "2026-10-23", false}, // Fri → Fri: 5
		{"d", "2026-10-16", "2026-10-26", true},  // Fri → Mon: 6
		{"d", "2026-10-23", "2026-10-25", false}, // weekend
		// Weekly: more than 14 days.
		{"w", "2026-10-10", "2026-10-24", false},
		{"w", "2026-10-09", "2026-10-24", true},
		// Monthly: August data, due by end of September + 45 days = Nov 14.
		{"m", "2026-08-01", "2026-11-14", false},
		{"m", "2026-08-01", "2026-11-15", true},
		// Quarterly: Q2 ends Jun 30, + 120 days = Oct 28.
		{"q", "2026-04-01", "2026-10-28", false},
		{"q", "2026-04-01", "2026-10-29", true},
		// Frozen OECD-style series is stale years later.
		{"m", "2024-01-01", "2026-10-24", true},
		// No rule for annual.
		{"a", "2000-01-01", "2026-10-24", false},
	}
	for _, tt := range tests {
		if got := Stale(tt.freq, day(tt.last), day(tt.asOf)); got != tt.want {
			t.Errorf("Stale(%s, %s, %s) = %v, want %v", tt.freq, tt.last, tt.asOf, got, tt.want)
		}
	}
}

func TestFlags(t *testing.T) {
	zero := 0.0
	in := FlagInput{Freq: "d", Last: day("2026-10-23"), AsOf: day("2026-10-24"), Value: 0, FlagIfGt: &zero}
	if got := Flags(in); len(got) != 0 {
		t.Errorf("flags = %v, want none (0 is not > 0)", got)
	}
	in.Value = 1.5 // Standing Repo Facility used
	in.Revised = true
	in.Last = day("2026-10-01")
	if got, want := Flags(in), []string{FlagStale, FlagRevised, FlagAboveMax}; !slices.Equal(got, want) {
		t.Errorf("flags = %v, want %v", got, want)
	}
}

func TestStaleByRelease(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	release := time.Date(2026, 10, 29, 0, 0, 0, 0, ny)
	at := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, ny) }
	for _, tt := range []struct {
		freq string
		seen time.Time
		want bool
	}{
		{"q", at(29, 1), false},  // Debt-to-GDP fetched at night after the release
		{"q", at(28, 19), false}, // posted the evening before
		{"q", at(27, 12), true},  // nothing since two days before
		{"d", at(28, 19), true},  // a daily series needs the day itself
		{"d", at(29, 16), false},
	} {
		if got := StaleByRelease(tt.freq, release, tt.seen); got != tt.want {
			t.Errorf("StaleByRelease(%s, seen %v) = %v, want %v", tt.freq, tt.seen, got, tt.want)
		}
	}
	// With a release day, §17.7 is not used: an old quarter is not stale.
	fs := Flags(FlagInput{Freq: "q", Last: day("2026-04-01"), AsOf: day("2026-11-01"), ReleaseDay: release, Seen: at(29, 1)})
	if len(fs) != 0 {
		t.Errorf("flags = %v, want none", fs)
	}
	fs = Flags(FlagInput{Freq: "q", Last: day("2026-04-01"), AsOf: day("2026-11-01")})
	if len(fs) != 1 || fs[0] != FlagStale {
		t.Errorf("flags without calendar = %v, want [stale]", fs)
	}
	// Only a later release in the calendar: ULCNFB at Q2 waits for Q3 on
	// 11-05; a series still at Q1 then has missed the Q2 release.
	fs = Flags(FlagInput{Freq: "q", Last: day("2026-04-01"), AsOf: day("2026-11-01"), NextRelease: day("2026-11-05")})
	if len(fs) != 0 {
		t.Errorf("flags with the next release ahead = %v, want none", fs)
	}
	fs = Flags(FlagInput{Freq: "q", Last: day("2026-01-01"), AsOf: day("2026-11-01"), NextRelease: day("2026-11-05")})
	if len(fs) != 1 || fs[0] != FlagStale {
		t.Errorf("flags with a missed release = %v, want [stale]", fs)
	}
}
