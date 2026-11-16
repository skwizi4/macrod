package ingest

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/sources/predict"
	"macro-collector/internal/store"
)

type fakeKalshi struct{ histories int }

func (f *fakeKalshi) Open(_ context.Context, series, event string, nearest bool) ([]predict.Market, error) {
	return []predict.Market{{ID: "KXRECSSNBER-27", Label: "Recession in 2027? (In 2027)", Event: "KXRECSSNBER-27", Question: "Recession in 2027? (In 2027)", Price: 0.21}}, nil
}

func (f *fakeKalshi) History(_ context.Context, series, ticker string, from, to time.Time) ([]predict.Point, error) {
	f.histories++
	return []predict.Point{{Date: day("2026-10-26"), Price: 0.18}, {Date: day("2026-11-02"), Price: 0.25}}, nil
}

type failingPoly struct{}

func (failingPoly) Event(context.Context, string) ([]predict.Market, error) {
	return nil, errors.New("status 503")
}
func (failingPoly) Search(context.Context, string) ([]predict.Market, error) { return nil, nil }
func (failingPoly) History(context.Context, string, time.Time, time.Time) ([]predict.Point, error) {
	return nil, nil
}

func TestPredictHistoryOnceAndPlatformErrors(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	k := &fakeKalshi{}
	ing := &Predict{Kalshi: k, Polymarket: failingPoly{}, Store: st, HistoryDays: 35, Agent: "usa_news",
		Topics: []config.PredictionTopic{{Name: "recession", KalshiSeries: "KXRECSSNBER", PolymarketSlugs: []string{"us-recession-by-end-of-2026"}}},
		Log:    slog.New(slog.DiscardHandler),
		Now:    func() time.Time { return time.Date(2026, 11, 2, 21, 30, 0, 0, time.UTC) }}

	res := ing.Run(ctx, Incremental)
	if res.Markets != 1 || res.Err == nil {
		t.Fatalf("result = %+v, want 1 market and the Polymarket error", res)
	}
	obs, err := st.Observations(ctx, predict.SourceKalshi, "recession|KXRECSSNBER-27", time.Time{})
	if err != nil || len(obs) != 2 || obs[0].Value != 18 || obs[1].Value != 21 {
		t.Fatalf("obs = %+v %v (history up to yesterday, today from the snapshot)", obs, err)
	}
	qs, err := st.SeriesQuestions(ctx, predict.SourceKalshi)
	if q := qs["recession|KXRECSSNBER-27"]; err != nil || q.Event != "KXRECSSNBER-27" || q.Question != "Recession in 2027? (In 2027)" {
		t.Errorf("question = %+v %v", q, err)
	}
	if st, ok, _ := st.FetchStateOf(ctx, predict.SourcePolymarket); !ok || st.LastError == "" {
		t.Errorf("polymarket state = %+v", st)
	}
	if st, ok, _ := st.FetchStateOf(ctx, predict.SourceKalshi); !ok || st.LastError != "" {
		t.Errorf("kalshi state = %+v", st)
	}
	// A known market gets no history on the next run.
	ing.Run(ctx, Incremental)
	if k.histories != 1 {
		t.Errorf("history requests = %d, want 1", k.histories)
	}
}
