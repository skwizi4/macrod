package collect

import (
	"context"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/store"
)

// ManualInput stores one typed-in value (`macroctl input` and the bot,
// phase 14c) and recomputes the derived series built on manual ones when
// the value is new or revised.
func ManualInput(ctx context.Context, cfg *config.Config, st *store.Store, root *slog.Logger, id string, value float64, period string, now time.Time) (ingest.ManualResult, error) {
	res, err := ingest.Manual(ctx, cfg, st, id, value, period, now)
	if err != nil {
		return res, err
	}
	if res.Inserted+res.Revised > 0 {
		RecomputeDerived(ctx, cfg, st, root, config.SourceManual)
	}
	return res, nil
}
