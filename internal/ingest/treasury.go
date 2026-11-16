package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/store"
)

// TreasuryClient is the part of treasury.Client that ingest uses.
type TreasuryClient interface {
	FetchTGA(ctx context.Context, since time.Time) ([]model.Observation, error)
	FetchMTS(ctx context.Context, since time.Time) (map[string][]model.Observation, error)
	FetchPublicDebt(ctx context.Context, since time.Time) (map[string][]model.Observation, error)
	FetchAuctions(ctx context.Context, types []string, from, to time.Time) ([]model.Auction, error)
	FetchBuybacks(ctx context.Context, since time.Time) ([]model.Buyback, error)
}

// Incremental look-back of the Treasury datasets.
const (
	treasuryDTSWindow      = 14 // days before the last stored DTS day
	treasuryMTSWindow      = 70 // days of statements: the last two repeat the whole fiscal year
	treasuryAuctionWindow  = 14 // days before the last stored auction (late results, revisions)
	treasuryBuybacksWindow = 30 // days
	// treasuryPublicDebtYears of public debt transactions are loaded first.
	treasuryPublicDebtYears = 2
)

// treasurySeries describes the observation series of the datasets.
var treasurySeries = map[string]struct{ label, freq, units string }{
	treasury.SeriesTGA:         {"TGA daily (DTS)", "d", treasury.UnitsMillionsUSD},
	treasury.SeriesMTSReceipts: {"MTS receipts", "m", treasury.UnitsUSD},
	treasury.SeriesMTSOutlays:  {"MTS outlays", "m", treasury.UnitsUSD},
	treasury.SeriesMTSDeficit:  {"MTS deficit (+) / surplus (−)", "m", treasury.UnitsUSD},

	treasury.SeriesBillsIssued:     {"Bills issued (DTS, cash basis)", "d", treasury.UnitsMillionsUSD},
	treasury.SeriesBillsRedeemed:   {"Bills redeemed (DTS, cash basis)", "d", treasury.UnitsMillionsUSD},
	treasury.SeriesCouponsIssued:   {"Notes and bonds issued (DTS, cash basis)", "d", treasury.UnitsMillionsUSD},
	treasury.SeriesCouponsRedeemed: {"Notes and bonds redeemed (DTS, cash basis)", "d", treasury.UnitsMillionsUSD},
}

// publicDebtSeries are the series of the dts_public_debt dataset.
var publicDebtSeries = []string{treasury.SeriesBillsIssued, treasury.SeriesBillsRedeemed,
	treasury.SeriesCouponsIssued, treasury.SeriesCouponsRedeemed}

// DatasetResult is the result of one Treasury dataset.
type DatasetResult struct {
	Dataset string
	Since   time.Time
	store.UpsertResult
	Auctions store.AuctionUpsert // auctions only
	Buybacks int                 // buybacks: rows inserted or changed
	Err      error
}

// TreasuryReport summarises a run.
type TreasuryReport struct {
	Results     []DatasetResult
	Interrupted bool
}

// Failed returns the failed datasets.
func (r TreasuryReport) Failed() []DatasetResult {
	var out []DatasetResult
	for _, d := range r.Results {
		if d.Err != nil {
			out = append(out, d)
		}
	}
	return out
}

// Treasury loads the enabled Treasury datasets (DTS, MTS, auctions,
// buybacks). Upcoming auctions go to the calendar, not here.
type Treasury struct {
	Client  TreasuryClient
	Store   *store.Store
	Config  *config.Treasury
	History time.Time
	Log     *slog.Logger
	Now     func() time.Time
}

// Run fetches every enabled dataset. A failing dataset is recorded in
// fetch_state["treasury:<dataset>"] and does not stop the others.
func (t *Treasury) Run(ctx context.Context, mode Mode) TreasuryReport {
	var rep TreasuryReport
	for _, name := range []string{config.DatasetDTS, config.DatasetPublicDebt, config.DatasetMTS, config.DatasetAuctions, config.DatasetBuybacks} {
		ds, ok := t.Config.Dataset(name)
		if !ok {
			continue
		}
		if ctx.Err() != nil {
			rep.Interrupted = true
			break
		}
		res := t.run(ctx, mode, ds)
		if res.Err != nil && ctx.Err() != nil {
			rep.Interrupted = true
			break
		}
		rep.Results = append(rep.Results, res)
	}
	return rep
}

// RunDataset fetches one enabled dataset (event pollers); ok is false if it
// is not configured or disabled.
func (t *Treasury) RunDataset(ctx context.Context, mode Mode, name string) (res DatasetResult, ok bool) {
	ds, ok := t.Config.Dataset(name)
	if !ok {
		return DatasetResult{Dataset: name}, false
	}
	return t.run(ctx, mode, ds), true
}

// run fetches one dataset and records an error in fetch_state (unless ctx
// was cancelled).
func (t *Treasury) run(ctx context.Context, mode Mode, ds config.TreasuryDataset) DatasetResult {
	res := t.runDataset(ctx, mode, ds)
	switch {
	case res.Err != nil && ctx.Err() != nil:
	case res.Err != nil:
		t.Log.Error("dataset failed", "dataset", ds.Name, "err", res.Err)
		if err := t.Store.Write(ctx, func(tx *sql.Tx) error {
			return store.SetFetchError(tx, treasuryKey(ds.Name), res.Err, t.now())
		}); err != nil {
			t.Log.Error("record fetch error", "dataset", ds.Name, "err", err)
		}
	default:
		t.Log.Info("dataset done", "dataset", ds.Name, "since", res.Since.Format(time.DateOnly),
			"inserted", res.Inserted+res.Auctions.Inserted, "revised", res.Revised+res.Auctions.Updated,
			"auction_results", res.Auctions.Resulted, "buybacks", res.Buybacks)
	}
	return res
}

func treasuryKey(dataset string) string { return treasury.Source + ":" + dataset }

func (t *Treasury) runDataset(ctx context.Context, mode Mode, ds config.TreasuryDataset) DatasetResult {
	res := DatasetResult{Dataset: ds.Name}
	now := t.now()
	since, err := t.since(ctx, mode, ds.Name, now)
	if err != nil {
		res.Err = err
		return res
	}
	res.Since = since
	cursor := now.Format(time.DateOnly)

	var write func(tx *sql.Tx) error
	switch ds.Name {
	case config.DatasetDTS:
		obs, err := t.Client.FetchTGA(ctx, since)
		if err != nil {
			res.Err = err
			return res
		}
		write = func(tx *sql.Tx) (err error) {
			res.UpsertResult, err = t.upsertSeries(tx, ds, treasury.SeriesTGA, obs, now)
			return err
		}
	case config.DatasetMTS, config.DatasetPublicDebt:
		fetch, ids := t.Client.FetchMTS, []string{treasury.SeriesMTSReceipts, treasury.SeriesMTSOutlays, treasury.SeriesMTSDeficit}
		if ds.Name == config.DatasetPublicDebt {
			fetch, ids = t.Client.FetchPublicDebt, publicDebtSeries
		}
		bySeries, err := fetch(ctx, since)
		if err != nil {
			res.Err = err
			return res
		}
		write = func(tx *sql.Tx) error {
			res.UpsertResult = store.UpsertResult{}
			for _, id := range ids {
				up, err := t.upsertSeries(tx, ds, id, bySeries[id], now)
				if err != nil {
					return err
				}
				res.Inserted += up.Inserted
				res.Revised += up.Revised
				res.Unchanged += up.Unchanged
			}
			return nil
		}
	case config.DatasetAuctions:
		as, err := t.Client.FetchAuctions(ctx, ds.SecurityTypes, since, model.Day(now))
		if err != nil {
			res.Err = err
			return res
		}
		write = func(tx *sql.Tx) (err error) {
			res.Auctions, err = store.UpsertAuctions(tx, as, now)
			return err
		}
	case config.DatasetBuybacks:
		bs, err := t.Client.FetchBuybacks(ctx, since)
		if err != nil {
			res.Err = err
			return res
		}
		write = func(tx *sql.Tx) (err error) {
			res.Buybacks, err = store.UpsertBuybacks(tx, bs, now)
			return err
		}
	default:
		res.Err = fmt.Errorf("treasury dataset %s: not collected here", ds.Name)
		return res
	}
	if err := t.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := write(tx); err != nil {
			return err
		}
		return store.SetFetchOK(tx, treasuryKey(ds.Name), cursor, now)
	}); err != nil {
		res.Err = fmt.Errorf("treasury store %s: %w", ds.Name, err)
	}
	return res
}

func (t *Treasury) upsertSeries(tx *sql.Tx, ds config.TreasuryDataset, id string, obs []model.Observation, now time.Time) (store.UpsertResult, error) {
	s := treasurySeries[id]
	if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
		Source: treasury.Source, SeriesID: id, Label: s.label, Agent: ds.Agent, Frequency: s.freq, Units: s.units,
	}); err != nil {
		return store.UpsertResult{}, err
	}
	return store.UpsertObservations(tx, treasury.Source, id, obs, now)
}

// since is where a run starts: History for a backfill or an empty dataset,
// else a look-back window before the last stored data.
func (t *Treasury) since(ctx context.Context, mode Mode, dataset string, now time.Time) (time.Time, error) {
	today := model.Day(now)
	if mode == Backfill {
		if dataset == config.DatasetPublicDebt {
			// ~40 rows a day; the export needs this and the last quarter.
			return latest(t.History, today.AddDate(-treasuryPublicDebtYears, 0, 0)), nil
		}
		return t.History, nil
	}
	switch dataset {
	case config.DatasetPublicDebt:
		last, ok, err := t.Store.LastObsDate(ctx, treasury.Source, treasury.SeriesBillsIssued)
		if err != nil || !ok {
			return latest(t.History, today.AddDate(-treasuryPublicDebtYears, 0, 0)), err
		}
		return latest(t.History, last.AddDate(0, 0, -treasuryDTSWindow)), nil
	case config.DatasetDTS:
		last, ok, err := t.Store.LastObsDate(ctx, treasury.Source, treasury.SeriesTGA)
		if err != nil || !ok {
			return t.History, err
		}
		return latest(t.History, last.AddDate(0, 0, -treasuryDTSWindow)), nil
	case config.DatasetMTS:
		if _, ok, err := t.Store.LastObsDate(ctx, treasury.Source, treasury.SeriesMTSDeficit); err != nil || !ok {
			return t.History, err
		}
		return latest(t.History, today.AddDate(0, 0, -treasuryMTSWindow)), nil
	case config.DatasetAuctions:
		last, ok, err := t.Store.LastAuctionDate(ctx)
		if err != nil || !ok {
			return t.History, err
		}
		return latest(t.History, last.AddDate(0, 0, -treasuryAuctionWindow)), nil
	case config.DatasetBuybacks:
		st, ok, err := t.Store.FetchStateOf(ctx, treasuryKey(dataset))
		if err != nil || !ok || st.Cursor == "" {
			return t.History, err
		}
		return latest(t.History, today.AddDate(0, 0, -treasuryBuybacksWindow)), nil
	}
	return t.History, nil
}

func (t *Treasury) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}
