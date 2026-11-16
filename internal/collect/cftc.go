package collect

import (
	"context"
	"log/slog"

	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources/cftc"
	"macro-collector/internal/store"
)

// RunCFTC loads the COT positions of region's contracts; nothing when CFTC
// is disabled.
func RunCFTC(ctx context.Context, cfg *config.Config, st *store.Store, client ingest.CFTCClient,
	root *slog.Logger, mode ingest.Mode, region string) ingest.CFTCResult {
	if !cfg.CFTC.Enabled {
		return ingest.CFTCResult{}
	}
	ing := &ingest.CFTC{Client: client, Store: st, Contracts: cfg.CFTC.ContractsFor(region), History: cfg.History,
		Log: logging.For(root, cftc.Source)}
	return ing.Run(ctx, mode)
}
