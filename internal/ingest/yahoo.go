package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

// YahooClient is the part of yahoo.Client that ingest uses.
type YahooClient interface {
	Fetch(ctx context.Context, symbol string, since, now time.Time) (yahoo.Series, error)
}

// yahooWindow is the incremental look-back before the last stored bar.
const yahooWindow = 10 // days

// YahooSymbol is one symbol to load.
type YahooSymbol struct {
	Symbol, Label, Agent string
	Units                string    // series_meta.units; empty: the quote currency
	Volume               bool      // also store the volume series (ZQ: trades or not)
	History              time.Time // first date of a backfill; zero = Yahoo.History
}

// CompanySymbols lists the tickers of the EDGAR companies of region, loaded
// from edgar.history_start: their closes give the price reaction to reports.
// The issuers of region (EU Micro) follow with their Yahoo symbols, loaded
// from yahoo's history start.
func CompanySymbols(cfg *config.Config, region string) []YahooSymbol {
	var out []YahooSymbol
	if cfg.EDGAR.Enabled {
		for _, c := range cfg.EDGAR.Companies {
			if r, tag, ok := cfg.RegionOfCompany(c.Ticker); ok && r == region {
				out = append(out, YahooSymbol{Symbol: c.Ticker, Label: c.Ticker, Agent: tag, History: cfg.EDGAR.History})
			}
		}
	}
	if cfg.Issuers.Enabled {
		for _, c := range cfg.Issuers.Companies {
			if r, tag, ok := cfg.RegionOfCompany(c.Ticker); ok && r == region {
				out = append(out, YahooSymbol{Symbol: c.Symbol, Label: c.Name, Agent: tag})
			}
		}
	}
	return out
}

// YahooSymbols lists the configured symbols of region plus, when the
// fed funds futures belong to region, the ZQ contracts from now's month to
// months_ahead later.
func YahooSymbols(cfg *config.Yahoo, region string, now time.Time) []YahooSymbol {
	var out []YahooSymbol
	for _, s := range cfg.SymbolsFor(region) {
		out = append(out, YahooSymbol{Symbol: s.Symbol, Label: s.Label, Agent: s.Agent, Units: s.Units})
	}
	f := cfg.FedFundsFutures
	if !cfg.FuturesIn(region) {
		return out
	}
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= f.MonthsAhead; i++ {
		m := month.AddDate(0, i, 0)
		out = append(out, YahooSymbol{
			Symbol: f.Symbol(m), Label: "Fed funds future " + m.Format("Jan 2006"), Agent: f.Agent, Volume: true,
		})
	}
	return out
}

// SymbolResult is the result of one symbol.
type SymbolResult struct {
	Symbol string
	Since  time.Time
	store.UpsertResult
	Dropped int // anomalous bars left out
	Err     error
}

// YahooReport summarises a run.
type YahooReport struct {
	Results     []SymbolResult
	Interrupted bool
}

// Failed returns the failed symbols.
func (r YahooReport) Failed() []SymbolResult {
	var out []SymbolResult
	for _, s := range r.Results {
		if s.Err != nil {
			out = append(out, s)
		}
	}
	return out
}

// Yahoo loads daily closes into observations (source yahoo, series = symbol).
type Yahoo struct {
	Client  YahooClient
	Store   *store.Store
	Symbols []YahooSymbol
	History time.Time
	Log     *slog.Logger
	Now     func() time.Time
}

// Run fetches every symbol; a failing symbol is recorded in
// fetch_state["yahoo:<symbol>"] and does not stop the others.
func (y *Yahoo) Run(ctx context.Context, mode Mode) YahooReport {
	var rep YahooReport
	for _, s := range y.Symbols {
		if ctx.Err() != nil {
			rep.Interrupted = true
			break
		}
		res := y.runSymbol(ctx, mode, s)
		if res.Err != nil && ctx.Err() != nil {
			rep.Interrupted = true
			break
		}
		if res.Err != nil {
			y.Log.Error("symbol failed", "symbol", s.Symbol, "err", res.Err)
			if err := y.Store.Write(ctx, func(tx *sql.Tx) error {
				return store.SetFetchError(tx, yahooKey(s.Symbol), res.Err, y.now())
			}); err != nil {
				y.Log.Error("record fetch error", "symbol", s.Symbol, "err", err)
			}
		} else {
			y.Log.Info("symbol done", "symbol", s.Symbol, "since", res.Since.Format(time.DateOnly),
				"inserted", res.Inserted, "revised", res.Revised, "dropped", res.Dropped)
		}
		rep.Results = append(rep.Results, res)
	}
	return rep
}

// yahooKey is the fetch_state key of a symbol.
func yahooKey(symbol string) string { return yahoo.Source + ":" + symbol }

func (y *Yahoo) runSymbol(ctx context.Context, mode Mode, s YahooSymbol) SymbolResult {
	res := SymbolResult{Symbol: s.Symbol}
	now := y.now()
	history := y.History
	if !s.History.IsZero() {
		history = s.History
	}
	since := history
	if mode == Incremental {
		last, ok, err := y.Store.LastObsDate(ctx, yahoo.Source, s.Symbol)
		if err != nil {
			res.Err = err
			return res
		}
		if ok {
			since = latest(history, last.AddDate(0, 0, -yahooWindow))
		}
	}
	res.Since = since
	series, err := y.Client.Fetch(ctx, s.Symbol, since, now)
	if err != nil {
		res.Err = err
		return res
	}
	res.Dropped = len(series.Dropped)
	units := series.Currency
	if s.Units != "" {
		units = s.Units
	}
	err = y.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
			Source: yahoo.Source, SeriesID: s.Symbol, Label: s.Label, Agent: s.Agent, Frequency: "d", Units: units,
		}); err != nil {
			return err
		}
		up, err := store.UpsertObservations(tx, yahoo.Source, s.Symbol, series.Observations(), now)
		if err != nil {
			return err
		}
		res.UpsertResult = up
		if s.Volume {
			vol := s.Symbol + yahoo.VolumeSuffix
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
				Source: yahoo.Source, SeriesID: vol, Label: s.Label + " volume", Agent: s.Agent, Frequency: "d", Units: "Contracts",
			}); err != nil {
				return err
			}
			if _, err := store.UpsertObservations(tx, yahoo.Source, vol, series.Volumes(), now); err != nil {
				return err
			}
		}
		return store.SetFetchOK(tx, yahooKey(s.Symbol), now.Format(time.DateOnly), now)
	})
	if err != nil {
		res.Err = fmt.Errorf("yahoo store %s: %w", s.Symbol, err)
	}
	return res
}

func (y *Yahoo) now() time.Time {
	if y.Now != nil {
		return y.Now()
	}
	return time.Now()
}
