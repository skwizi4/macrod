package collect

import (
	"context"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/derived"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/bundesbank"
	"macro-collector/internal/sources/ecbdata"
	"macro-collector/internal/sources/eurostat"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/stoxx"
	"macro-collector/internal/sources/zew"
	"macro-collector/internal/store"
)

// numericInterval spaces the requests to one source: the ECB Data Portal
// answers 504 to bursts, the others ask for restraint.
const numericInterval = 2 * time.Second

// NewNumericSource is the client of an enabled numeric source section; ok
// is false for a section that is disabled or has no client.
func NewNumericSource(cfg *config.Config, name string, root *slog.Logger) (sources.NumericSource, bool) {
	sec, ok := cfg.NumericSourceByName(name)
	if !ok {
		return nil, false
	}
	log := logging.For(root, name)
	h := httpx.New(httpx.WithLogger(log), httpx.WithInterval(numericInterval))
	switch name {
	case config.SourceECB:
		return ecbdata.New(sec.Endpoint, h, log), true
	case config.SourceBundesbank:
		return bundesbank.New(sec.Endpoint, h, log), true
	case config.SourceEurostat:
		return eurostat.New(sec.Endpoint, h, log), true
	case config.SourceSTOXX:
		return stoxx.New(sec.Endpoint, h, log), true
	case config.SourceZEW:
		return zew.New(sec.Endpoint, sec.Sheet, h, log), true
	}
	return nil, false
}

// NumericRunner loads one numeric source on its schedule.
type NumericRunner struct {
	*ingest.Numeric
	PollCron string
}

// NumericRunners are the loaders of the enabled numeric sources.
func NumericRunners(cfg *config.Config, st *store.Store, root *slog.Logger) []NumericRunner {
	var out []NumericRunner
	for _, sec := range cfg.NumericSources() {
		src, ok := NewNumericSource(cfg, sec.Name, root)
		if !ok {
			continue
		}
		out = append(out, NumericRunner{
			Numeric:  &ingest.Numeric{Source: src, Store: st, Series: sec.Series, History: cfg.History, Log: logging.For(root, sec.Name)},
			PollCron: sec.PollCron,
		})
	}
	return out
}

// RunNumeric loads the series of one source and, if any row changed,
// recomputes the derived series.
func RunNumeric(ctx context.Context, cfg *config.Config, st *store.Store, r NumericRunner,
	root *slog.Logger, mode ingest.Mode) Summary {
	start := time.Now()
	sum := Summary{FRED: r.Run(ctx, mode)}
	if inserted, revised := sum.Rows(); inserted+revised > 0 && ctx.Err() == nil {
		comp := &derived.Computer{Store: st, Config: cfg, Defs: cfg.Derived, Log: logging.For(root, "derived")}
		sum.Derived = comp.Run(ctx)
	}
	sum.Duration = time.Since(start)
	sum.Interrupted = sum.FRED.Interrupted || ctx.Err() != nil
	return sum
}

// RecomputeDerived recomputes the derived series if any of them reads a
// series of source.
func RecomputeDerived(ctx context.Context, cfg *config.Config, st *store.Store, root *slog.Logger, source string) []derived.Result {
	if !derived.Uses(cfg, source) {
		return nil
	}
	comp := &derived.Computer{Store: st, Config: cfg, Defs: cfg.Derived, Log: logging.For(root, "derived")}
	return comp.Run(ctx)
}
