package collect

import (
	"context"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/store"
)

// TreasurySummary is the result of one Treasury pass.
type TreasurySummary struct {
	Datasets  ingest.TreasuryReport
	Refunding ingest.TextResult
	Duration  time.Duration
	// Interrupted is set when ctx was cancelled before the pass finished.
	Interrupted bool
}

// Failures is the number of failed datasets plus the refunding documents.
func (s TreasurySummary) Failures() int {
	n := len(s.Datasets.Failed())
	if s.Refunding.Err != nil {
		n++
	}
	return n
}

// RunTreasury loads the Treasury datasets (DTS, MTS, auctions, buybacks)
// and the refunding documents.
func RunTreasury(ctx context.Context, cfg *config.Config, st *store.Store, client *treasury.Client,
	root *slog.Logger, mode ingest.Mode) TreasurySummary {
	start := time.Now()
	log := logging.For(root, treasury.Source)
	ing := &ingest.Treasury{Client: client, Store: st, Config: &cfg.Treasury, History: cfg.History, Log: log}
	sum := TreasurySummary{Datasets: ing.Run(ctx, mode)}
	if ctx.Err() == nil {
		sum.Refunding = NewTexts(cfg, st, root).Run(ctx, &treasury.Refunding{Client: client, ET: cfg.Calendar.ReleaseLocation})
	}
	sum.Duration = time.Since(start)
	sum.Interrupted = sum.Datasets.Interrupted || ctx.Err() != nil
	return sum
}
