// Package predict reads prediction market prices (FIXES_W44 F6, LOOKUP
// 2026-11-02): Kalshi trade API v2 and Polymarket Gamma + CLOB, both public
// for market data without a key. Non-critical sources (D7): a failure
// leaves a gap in sentiment.md, nothing else.
package predict

import (
	"strconv"
	"strings"
	"time"
)

// Source names (observations.source, fetch_state keys).
const (
	SourceKalshi     = "kalshi"
	SourcePolymarket = "polymarket"
)

// Market is one open yes/no market.
type Market struct {
	ID    string // Kalshi ticker; Polymarket market slug
	Label string // "<question> — <outcome>", or the question alone
	Event string // Kalshi event ticker; Polymarket event slug
	// Question is the event title, shared by its outcomes; Outcome is this
	// market's ("Hike 25bps", "Democratic Party"), empty for a yes/no event.
	Question, Outcome string
	Close             time.Time
	Price             float64 // last price of "yes", 0–1
	// Token is the Polymarket CLOB token of "yes", for the price history.
	Token string
}

// label is "<question> — <outcome>", or the question alone.
func label(question, outcome string) string {
	if outcome == "" {
		return question
	}
	return question + " — " + outcome
}

// Point is a daily closing price of "yes".
type Point struct {
	Date  time.Time // New York calendar day, UTC midnight
	Price float64
}

// SeriesID is the stored series of a topic's market: "<topic>|<market>",
// so the export finds a topic's markets by prefix.
func SeriesID(topic, market string) string { return topic + "|" + market }

// TopicOf splits a series ID into topic and market.
func TopicOf(seriesID string) (topic, market string, ok bool) {
	return strings.Cut(seriesID, "|")
}

// newYork is the day boundary of daily prices.
var newYork = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return l
}()

// Day is the New York calendar day of t as UTC midnight.
func Day(t time.Time) time.Time {
	y, m, d := t.In(newYork).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// price parses a decimal string; "" and junk give ok = false.
func price(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 || v > 1 {
		return 0, false
	}
	return v, true
}

// lastPerDay keeps the last point of each day, in date order.
func lastPerDay(ps []Point) []Point {
	var out []Point
	for _, p := range ps {
		if n := len(out); n > 0 && out[n-1].Date.Equal(p.Date) {
			out[n-1] = p
			continue
		}
		out = append(out, p)
	}
	return out
}
