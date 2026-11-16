package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/cftc"
	"macro-collector/internal/store"
)

// CFTCClient is the part of cftc.Client that ingest uses.
type CFTCClient interface {
	Fetch(ctx context.Context, codes []string, since time.Time) (map[string][]model.Observation, error)
}

// cftcWindow re-reads the last weeks: the CFTC occasionally corrects a
// report after publication.
const cftcWindow = 28 // days

// cftcLabels name the stored fields.
var cftcLabels = map[string]string{
	cftc.FieldLevLong: "Leveraged Funds long", cftc.FieldLevShort: "Leveraged Funds short",
	cftc.FieldAMLong: "Asset Managers long", cftc.FieldAMShort: "Asset Managers short",
	cftc.FieldOpenInterest: "open interest",
}

// CFTCResult is the result of a run: all contracts come in one request.
type CFTCResult struct {
	Since      time.Time
	LastReport time.Time // latest report date stored, zero if none
	store.UpsertResult
	Err error
}

// CFTC loads the COT positions of the configured contracts.
type CFTC struct {
	Client    CFTCClient
	Store     *store.Store
	Contracts []config.CFTCContract
	History   time.Time
	Log       *slog.Logger
	Now       func() time.Time
}

// Run fetches every contract; an error is recorded in fetch_state["cftc"].
func (c *CFTC) Run(ctx context.Context, mode Mode) CFTCResult {
	res := c.run(ctx, mode)
	if res.Err != nil && ctx.Err() == nil {
		c.Log.Error("cftc failed", "err", res.Err)
		if err := c.Store.Write(ctx, func(tx *sql.Tx) error {
			return store.SetFetchError(tx, cftc.Source, res.Err, c.now())
		}); err != nil {
			c.Log.Error("record fetch error", "err", err)
		}
	} else if res.Err == nil {
		c.Log.Info("cftc done", "since", res.Since.Format(time.DateOnly), "last_report", res.LastReport.Format(time.DateOnly),
			"inserted", res.Inserted, "revised", res.Revised)
	}
	return res
}

func (c *CFTC) run(ctx context.Context, mode Mode) CFTCResult {
	var res CFTCResult
	if len(c.Contracts) == 0 {
		return res
	}
	codes := make([]string, len(c.Contracts))
	for i, k := range c.Contracts {
		codes[i] = k.Code
	}
	// Incremental: from the window before the oldest last report of the
	// contracts, so a contract added to the config gets its history.
	res.Since = c.History
	if mode == Incremental {
		var oldest time.Time
		for i, code := range codes {
			last, ok, err := c.Store.LastObsDate(ctx, cftc.Source, cftc.SeriesID(code, cftc.FieldOpenInterest))
			if err != nil {
				res.Err = err
				return res
			}
			if !ok {
				oldest = time.Time{}
				break
			}
			if i == 0 || last.Before(oldest) {
				oldest = last
			}
		}
		if !oldest.IsZero() {
			res.Since = latest(c.History, oldest.AddDate(0, 0, -cftcWindow))
		}
	}
	got, err := c.Client.Fetch(ctx, codes, res.Since)
	if err != nil {
		res.Err = err
		return res
	}
	now := c.now()
	err = c.Store.Write(ctx, func(tx *sql.Tx) error {
		res.UpsertResult = store.UpsertResult{}
		for _, k := range c.Contracts {
			for _, f := range cftc.Fields {
				id := cftc.SeriesID(k.Code, f)
				if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
					Source: cftc.Source, SeriesID: id, Label: k.Name + " " + cftcLabels[f], Agent: k.Agent,
					Frequency: "w", Units: "Contracts",
				}); err != nil {
					return err
				}
				obs := got[id]
				up, err := store.UpsertObservations(tx, cftc.Source, id, obs, now)
				if err != nil {
					return err
				}
				res.Inserted += up.Inserted
				res.Revised += up.Revised
				res.Unchanged += up.Unchanged
				if n := len(obs); n > 0 && obs[n-1].Date.After(res.LastReport) {
					res.LastReport = obs[n-1].Date
				}
			}
		}
		// The cursor is the latest report date: the scheduler tells a new
		// report from it.
		cursor := ""
		if !res.LastReport.IsZero() {
			cursor = res.LastReport.Format(time.DateOnly)
		}
		return store.SetFetchOK(tx, cftc.Source, cursor, now)
	})
	if err != nil {
		res.Err = fmt.Errorf("cftc store: %w", err)
	}
	return res
}

func (c *CFTC) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
