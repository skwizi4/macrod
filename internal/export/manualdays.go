package export

import (
	"context"
	"slices"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/store"
)

// ManualReleaseDays are the publication days of manual series: the
// config's release_dates and the days sent to the bot (phase 14c), merged,
// ascending, without duplicates.
func ManualReleaseDays(ctx context.Context, cfg *config.Config, st *store.Store) (map[string][]time.Time, error) {
	out := cfg.ConfigReleaseDays()
	stored, err := st.ManualReleaseDays(ctx)
	if err != nil {
		return nil, err
	}
	for id, ds := range stored {
		if _, ok := cfg.ManualSeries(id); !ok {
			continue
		}
		all := slices.Concat(out[id], ds)
		slices.SortFunc(all, func(a, b time.Time) int { return a.Compare(b) })
		out[id] = slices.CompactFunc(all, func(a, b time.Time) bool { return a.Equal(b) })
	}
	return out, nil
}
