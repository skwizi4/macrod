package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/store"
)

// BundAuctionsResult is one load of the Bund auction results.
type BundAuctionsResult struct {
	store.AuctionUpsert
	Rows int       // auctions read from the files
	Last time.Time // latest auction date read
	Err  error
}

// NewFinanzagenturClient returns the client, or nil when the section is
// disabled.
func NewFinanzagenturClient(cfg *config.Config, root *slog.Logger) *finanzagentur.Client {
	if !cfg.Finanzagentur.Enabled {
		return nil
	}
	log := logging.For(root, finanzagentur.Source)
	return finanzagentur.New(httpx.New(httpx.WithLogger(log), httpx.WithInterval(numericInterval)), log)
}

// RunBundAuctions loads the results workbook (Backfill: the history since
// 1999 first) and upserts the auctions. A failure is recorded in
// fetch_state["finanzagentur"] and nothing of that file is stored.
func RunBundAuctions(ctx context.Context, cfg *config.Config, st *store.Store, c *finanzagentur.Client,
	root *slog.Logger, mode ingest.Mode, now time.Time) BundAuctionsResult {
	log := logging.For(root, finanzagentur.Source)
	sec := cfg.Finanzagentur
	urls := []string{sec.ResultsURL}
	if mode == ingest.Backfill {
		urls = []string{sec.HistoryURL, sec.ResultsURL}
	}
	var res BundAuctionsResult
	var all []model.Auction
	for _, u := range urls {
		as, err := c.Results(ctx, u, now)
		if err != nil {
			res.Err = err
			break
		}
		all = append(all, as...)
	}
	region := cfg.RegionOfTag(sec.Agent)
	if res.Err != nil {
		if ctx.Err() == nil {
			if werr := st.Write(ctx, func(tx *sql.Tx) error {
				return store.SetFetchError(tx, finanzagentur.Source, res.Err, now)
			}); werr != nil {
				log.Error("record fetch error", "err", werr)
			}
		}
		return res
	}
	for i := range all {
		all[i].Region = region
		if all[i].AuctionDate.After(res.Last) {
			res.Last = all[i].AuctionDate
		}
	}
	res.Rows = len(all)
	if err := st.Write(ctx, func(tx *sql.Tx) (err error) {
		if res.AuctionUpsert, err = store.UpsertAuctions(tx, all, now); err != nil {
			return err
		}
		return store.SetFetchOK(tx, finanzagentur.Source, res.Last.Format(time.DateOnly), now)
	}); err != nil {
		res.Err = errors.Join(res.Err, fmt.Errorf("finanzagentur store: %w", err))
	}
	return res
}
