package collect

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources/cboe"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/predict"
	"macro-collector/internal/store"
)

// RunCboe loads the daily put/call ratios (FIXES_W44 F3); nothing when Cboe
// is disabled. A backfill goes cboe.history_days back from now.
func RunCboe(ctx context.Context, cfg *config.Config, st *store.Store, client ingest.CboeClient,
	root *slog.Logger, mode ingest.Mode, now time.Time) ingest.CboeResult {
	if !cfg.Cboe.Enabled {
		return ingest.CboeResult{}
	}
	ing := &ingest.Cboe{Client: client, Store: st, Agent: cfg.Cboe.Agent,
		History: now.AddDate(0, 0, -cfg.Cboe.HistoryDays), Log: logging.For(root, cboe.Source)}
	return ing.Run(ctx, mode)
}

// PredictClients are the Kalshi and Polymarket clients of the config.
func PredictClients(cfg *config.Config, root *slog.Logger) (ingest.KalshiClient, ingest.PolymarketClient) {
	p := cfg.Prediction
	klog, plog := logging.For(root, predict.SourceKalshi), logging.For(root, predict.SourcePolymarket)
	return predict.NewKalshi(p.Kalshi, httpx.New(httpx.WithLogger(klog), httpx.WithInterval(time.Second))),
		predict.NewPolymarket(p.Gamma, p.CLOB, httpx.New(httpx.WithLogger(plog), httpx.WithInterval(time.Second)))
}

// RunPredict stores the prediction market prices (FIXES_W44 F6); nothing
// when disabled.
func RunPredict(ctx context.Context, cfg *config.Config, st *store.Store, k ingest.KalshiClient, pm ingest.PolymarketClient,
	root *slog.Logger, mode ingest.Mode) ingest.PredictResult {
	p := cfg.Prediction
	if !p.Enabled {
		return ingest.PredictResult{}
	}
	ing := &ingest.Predict{Kalshi: k, Polymarket: pm, Store: st, Topics: p.Topics, HistoryDays: p.HistoryDays, Agent: p.Agent,
		Log: logging.For(root, "predict")}
	return ing.Run(ctx, mode)
}

// RunIntraday loads the one-minute bars of the reaction events scheduled in
// [from, to) that are due (FIXES_W44 F1).
func RunIntraday(ctx context.Context, cfg *config.Config, st *store.Store, client ingest.IntradayClient,
	root *slog.Logger, from, to time.Time) ingest.IntradayResult {
	events, err := st.Releases(ctx, from, to)
	if err != nil {
		return ingest.IntradayResult{Err: err}
	}
	var res ingest.IntradayResult
	var errs []error
	for i, s := range cfg.Sentiments() {
		if len(s.Reaction.BarSymbols()) == 0 {
			continue
		}
		in := &ingest.Intraday{Client: client, Store: st, Reaction: s.Reaction, Log: logging.For(root, "intraday")}
		if i > 0 {
			in.FetchKey = ingest.IntradayFetchKey + ":" + s.Reaction.Region
		}
		r := in.Run(ctx, events)
		res.Events += r.Events
		res.Bars += r.Bars
		errs = append(errs, r.Err)
	}
	res.Err = errors.Join(errs...)
	return res
}
