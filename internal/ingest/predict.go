package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/predict"
	"macro-collector/internal/store"
)

// KalshiClient is the part of predict.Kalshi that ingest uses.
type KalshiClient interface {
	Open(ctx context.Context, series, event string, nearest bool) ([]predict.Market, error)
	History(ctx context.Context, series, ticker string, from, to time.Time) ([]predict.Point, error)
}

// PolymarketClient is the part of predict.Polymarket that ingest uses.
type PolymarketClient interface {
	Event(ctx context.Context, slug string) ([]predict.Market, error)
	Search(ctx context.Context, prefix string) ([]predict.Market, error)
	History(ctx context.Context, token string, from, to time.Time) ([]predict.Point, error)
}

// PredictResult is the result of a run.
type PredictResult struct {
	Markets int // markets stored
	Err     error
}

// Predict stores today's price of every open market of the topics (in %,
// series "<topic>|<market>"); a market seen for the first time, or every
// market on a backfill, also gets history_days of daily history.
type Predict struct {
	Kalshi      KalshiClient     // nil: Kalshi skipped
	Polymarket  PolymarketClient // nil: Polymarket skipped
	Store       *store.Store
	Topics      []config.PredictionTopic
	HistoryDays int
	Agent       string
	Log         *slog.Logger
	Now         func() time.Time
}

// Run loads both platforms; a failing platform is recorded in its
// fetch_state row and does not stop the other.
func (p *Predict) Run(ctx context.Context, mode Mode) PredictResult {
	var res PredictResult
	var errs []error
	if p.Kalshi != nil {
		n, err := p.platform(ctx, mode, predict.SourceKalshi, p.kalshiTopic)
		res.Markets += n
		errs = append(errs, err)
	}
	if p.Polymarket != nil {
		n, err := p.platform(ctx, mode, predict.SourcePolymarket, p.polyTopic)
		res.Markets += n
		errs = append(errs, err)
	}
	res.Err = errors.Join(errs...)
	if res.Err == nil {
		p.Log.Info("prediction markets done", "markets", res.Markets)
	}
	return res
}

// fetched is a market with its daily history (empty when not asked).
type fetched struct {
	predict.Market
	history []predict.Point
}

func (p *Predict) platform(ctx context.Context, mode Mode, source string,
	topic func(context.Context, Mode, config.PredictionTopic) ([]fetched, error)) (int, error) {
	n := 0
	var errs []error
	for _, t := range p.Topics {
		ms, err := topic(ctx, mode, t)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", source, t.Name, err))
			continue
		}
		if err := p.store(ctx, source, t, ms); err != nil {
			return n, err
		}
		n += len(ms)
	}
	err := errors.Join(errs...)
	now := p.now()
	if werr := p.Store.Write(ctx, func(tx *sql.Tx) error {
		if err != nil {
			return store.SetFetchError(tx, source, err, now)
		}
		return store.SetFetchOK(tx, source, predict.Day(now).Format(time.DateOnly), now)
	}); werr != nil {
		return n, werr
	}
	if err != nil {
		p.Log.Error("prediction markets failed", "platform", source, "err", err)
	}
	return n, err
}

func (p *Predict) kalshiTopic(ctx context.Context, mode Mode, t config.PredictionTopic) ([]fetched, error) {
	if t.KalshiSeries == "" {
		return nil, nil
	}
	ms, err := p.Kalshi.Open(ctx, t.KalshiSeries, t.KalshiEvent, t.KalshiNearest)
	if err != nil {
		return nil, err
	}
	return p.withHistory(ctx, mode, predict.SourceKalshi, t, ms, func(m predict.Market, from, to time.Time) ([]predict.Point, error) {
		return p.Kalshi.History(ctx, t.KalshiSeries, m.ID, from, to)
	})
}

func (p *Predict) polyTopic(ctx context.Context, mode Mode, t config.PredictionTopic) ([]fetched, error) {
	var ms []predict.Market
	for _, slug := range t.PolymarketSlugs {
		got, err := p.Polymarket.Event(ctx, slug)
		if err != nil {
			return nil, err
		}
		ms = append(ms, got...)
	}
	if t.PolymarketSearch != "" {
		got, err := p.Polymarket.Search(ctx, t.PolymarketSearch)
		if err != nil {
			return nil, err
		}
		ms = append(ms, got...)
	}
	return p.withHistory(ctx, mode, predict.SourcePolymarket, t, ms, func(m predict.Market, from, to time.Time) ([]predict.Point, error) {
		if m.Token == "" {
			return nil, nil
		}
		return p.Polymarket.History(ctx, m.Token, from, to)
	})
}

// withHistory adds the daily history to new markets (all on a backfill),
// up to yesterday: today's price is the snapshot.
func (p *Predict) withHistory(ctx context.Context, mode Mode, source string, t config.PredictionTopic, ms []predict.Market,
	history func(predict.Market, time.Time, time.Time) ([]predict.Point, error)) ([]fetched, error) {
	now := p.now()
	today := predict.Day(now)
	var out []fetched
	for _, m := range ms {
		f := fetched{Market: m}
		_, known, err := p.Store.LastObsDate(ctx, source, predict.SeriesID(t.Name, m.ID))
		if err != nil {
			return nil, err
		}
		if mode == Backfill || !known {
			hs, err := history(m, now.AddDate(0, 0, -p.HistoryDays), now)
			if err != nil {
				return nil, err
			}
			for _, h := range hs {
				if h.Date.Before(today) {
					f.history = append(f.history, h)
				}
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func (p *Predict) store(ctx context.Context, source string, t config.PredictionTopic, ms []fetched) error {
	now := p.now()
	today := predict.Day(now)
	return p.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, m := range ms {
			id := predict.SeriesID(t.Name, m.ID)
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
				Source: source, SeriesID: id, Label: m.Label, Agent: p.Agent, Frequency: "d", Units: "Percent",
				Event: m.Event, Question: m.Question,
			}); err != nil {
				return err
			}
			obs := make([]model.Observation, 0, len(m.history)+1)
			for _, h := range m.history {
				obs = append(obs, model.Observation{Date: h.Date, Value: pct(h.Price)})
			}
			obs = append(obs, model.Observation{Date: today, Value: pct(m.Price)})
			if _, err := store.UpsertObservations(tx, source, id, obs, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// pct turns a 0–1 price into a probability in %, to 0.1.
func pct(price float64) float64 { return math.Round(price*1000) / 10 }

func (p *Predict) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
