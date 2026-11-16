package collect

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/derived"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

// RunYahoo loads the Yahoo symbols of region (configured symbols and, for
// usa, the ZQ strip and the EDGAR companies' tickers); with only, just
// those of them.
func RunYahoo(ctx context.Context, cfg *config.Config, st *store.Store, client ingest.YahooClient,
	root *slog.Logger, mode ingest.Mode, region string, only ...string) (ingest.YahooReport, time.Duration) {
	start := time.Now()
	symbols := YahooSymbols(cfg, region, time.Now())
	if len(only) > 0 {
		symbols = slices.DeleteFunc(symbols, func(s ingest.YahooSymbol) bool { return !slices.Contains(only, s.Symbol) })
	}
	ing := &ingest.Yahoo{
		Client: client, Store: st, Symbols: symbols,
		History: cfg.History, Log: logging.For(root, yahoo.Source),
	}
	rep := ing.Run(ctx, mode)
	changed := 0
	for _, r := range rep.Results {
		changed += r.Inserted + r.Revised
	}
	// Spreads of Yahoo yields (BTP-Bund) follow their inputs.
	if changed > 0 && ctx.Err() == nil && derived.Uses(cfg, yahoo.Source) {
		comp := &derived.Computer{Store: st, Config: cfg, Defs: cfg.Derived, Log: logging.For(root, "derived")}
		comp.Run(ctx)
	}
	return rep, time.Since(start)
}

// YahooSymbols are the symbols RunYahoo polls for region at now.
func YahooSymbols(cfg *config.Config, region string, now time.Time) []ingest.YahooSymbol {
	symbols := ingest.YahooSymbols(&cfg.Yahoo, region, now)
	return append(symbols, ingest.CompanySymbols(cfg, region)...)
}
