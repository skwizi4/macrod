package ingest

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

// recordedIntraday serves the one-minute bars of W44 recorded in
// testdata/yahoo/intraday (28–30 Oct 2026).
type recordedIntraday struct{ calls []string }

func (r *recordedIntraday) Intraday(_ context.Context, symbol string, from, to time.Time) ([]yahoo.MinuteBar, error) {
	r.calls = append(r.calls, symbol)
	name := "chart1m_" + strings.NewReplacer("^", "idx_", "=", "_", ".", "_").Replace(symbol) + ".json"
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "yahoo", "intraday", name))
	if err != nil {
		return nil, err
	}
	all, err := yahoo.ParseIntraday(b)
	if err != nil {
		return nil, err
	}
	var out []yahoo.MinuteBar
	for _, m := range all {
		if !m.TS.Before(from) && m.TS.Before(to) {
			out = append(out, m)
		}
	}
	return out, nil
}

func reactionConfig() config.Reaction {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return config.Reaction{
		Region: "usa", Location: ny, CloseAt: config.ClockTime{Hour: 16},
		Symbols: []config.ReactionSymbol{{Symbol: "NQ=F"}, {Symbol: "ZT=F", Duration: 1.9}, {Symbol: "DX-Y.NYB"}, {Symbol: "CL=F"}},
		Window:  30 * time.Minute, KeepDays: 60, FetchDelay: 20 * time.Minute, CheckEvery: 30 * time.Minute, Earnings: true,
		Events: []config.ReactionEvent{{Name: "Gross Domestic Product", Label: "GDP"}, {Name: "Personal Income and Outlays", Label: "PCE"},
			{Name: "FOMC decision", Label: "FOMC"}},
	}
}

func TestIntradayLoadsDueEventsOnce(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ts := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	events := []model.ReleaseEvent{
		{Name: "FOMC decision", ScheduledAt: ts("2026-10-28T18:00:00Z"), Region: "usa"},
		// FOMC's window ends at 16:01 ET: MSFT's first 30 minutes are not in it.
		{Name: "MSFT earnings", ScheduledAt: ts("2026-10-28T20:00:00Z"), Region: "usa", Related: model.Related{Ticker: "MSFT"}},
		{Name: "Gross Domestic Product", ScheduledAt: ts("2026-10-29T12:30:00Z"), Region: "usa"},
		{Name: "Personal Income and Outlays", ScheduledAt: ts("2026-10-29T12:30:00Z"), Region: "usa"}, // same moment: one window
		{Name: "AMZN earnings", ScheduledAt: ts("2026-10-29T20:00:00Z"), Region: "usa", Related: model.Related{Ticker: "AMZN"}},
		{Name: "Refunding: Policy Statement", ScheduledAt: ts("2026-10-28T12:30:00Z"), Region: "usa"}, // no reaction row
	}
	client := &recordedIntraday{}
	in := &Intraday{Client: client, Store: st, Reaction: reactionConfig(), Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return ts("2026-10-29T20:30:00Z") }} // Thursday 16:30 ET: AMZN's close is tomorrow
	res := in.Run(ctx, events)
	if res.Err != nil || res.Events != 2 || len(client.calls) != 8 { // FOMC, MSFT (its window covers 08:30); AMZN not due
		t.Fatalf("result = %+v, calls %v", res, client.calls)
	}
	bars, err := st.IntradayBars(ctx, "NQ=F", ts("2026-10-29T12:15:00Z"), ts("2026-10-29T20:01:00Z"))
	if err != nil || len(bars) < 400 {
		t.Fatalf("NQ=F bars = %d %v", len(bars), err)
	}
	// Again: nothing is fetched twice.
	client.calls = nil
	if res := in.Run(ctx, events); res.Events != 0 || len(client.calls) != 0 {
		t.Errorf("second run = %+v, calls %v", res, client.calls)
	}
	// Friday evening: AMZN is due although its window overlaps the bars
	// of Thursday stored already.
	in.Now = func() time.Time { return ts("2026-10-30T20:30:00Z") }
	client.calls = nil
	if res := in.Run(ctx, events); res.Err != nil || res.Events != 1 || len(client.calls) != 4 {
		t.Errorf("friday run = %+v, calls %v", res, client.calls)
	}
}
