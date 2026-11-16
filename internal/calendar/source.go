package calendar

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/store"
)

// Source syncs a calendar source that lists its own events (the Fed
// calendar). Keys are the (releases.source, name) series it produces: each
// is synced even when the source lists nothing for it in the window, so a
// moved event is marked rescheduled.
//
// Dynamic lists releases.source values whose names are not known in advance
// (Treasury auctions are named after the security): their keys are the ones
// listed now plus the ones already stored in the window.
type Source struct {
	Source   sources.CalendarSource
	Keys     [][2]string
	Dynamic  []string
	Region   string // of the events that do not state one
	Store    *store.Store
	Calendar *config.Calendar
	Log      *slog.Logger
	Now      func() time.Time // defaults to time.Now
}

// SourceReport summarises a refresh.
type SourceReport struct {
	From, To time.Time // calendar days in release_timezone, inclusive
	Events   int
	store.SyncResult
}

// Refresh fetches the events of the window and syncs them. A source error
// fails the refresh and is recorded in fetch_state["calendar:<name>"]. A
// sources.PartialError syncs the series that were listed, records the error
// and is returned with the report.
func (s *Source) Refresh(ctx context.Context) (SourceReport, error) {
	w := windowAt(s.now(), s.Calendar)
	rep := SourceReport{From: w.FromDay, To: w.ToDay}
	stateKey := "calendar:" + s.Source.Name()

	events, err := s.Source.Upcoming(ctx, w.From, w.To)
	var partial *sources.PartialError
	if err != nil && !errors.As(err, &partial) {
		if ctx.Err() == nil {
			if werr := s.Store.Write(ctx, func(tx *sql.Tx) error {
				return store.SetFetchError(tx, stateKey, err, s.now())
			}); werr != nil {
				s.Log.Error("record calendar error", "err", werr)
			}
		}
		return rep, fmt.Errorf("calendar %s: %w", s.Source.Name(), err)
	}

	for i := range events {
		if events[i].Region == "" {
			events[i].Region = s.Region
		}
	}

	keys := slices.Clone(s.Keys)
	if partial != nil {
		keys = slices.DeleteFunc(keys, func(k [2]string) bool { return slices.Contains(partial.Keys, k) })
	}
	if len(s.Dynamic) > 0 {
		stored, err := s.Store.Releases(ctx, w.From, w.To)
		if err != nil {
			return rep, fmt.Errorf("calendar %s: %w", s.Source.Name(), err)
		}
		for _, e := range stored {
			if k := [2]string{e.Source, e.Name}; slices.Contains(s.Dynamic, e.Source) && !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	byKey := make(map[[2]string][]model.ReleaseEvent)
	for _, e := range events {
		k := [2]string{e.Source, e.Name}
		byKey[k] = append(byKey[k], e)
	}
	for k := range byKey {
		if slices.Contains(keys, k) {
			continue
		}
		if !slices.Contains(s.Dynamic, k[0]) || partial != nil && slices.Contains(partial.Keys, k) {
			return rep, fmt.Errorf("calendar %s: unexpected event series %s/%s", s.Source.Name(), k[0], k[1])
		}
		keys = append(keys, k)
	}
	err = s.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			res, err := store.SyncReleases(tx, k[0], k[1], w.From, w.To, byKey[k])
			if err != nil {
				return err
			}
			rep.Events += len(byKey[k])
			rep.Inserted += res.Inserted
			rep.Updated += res.Updated
			rep.Rescheduled += res.Rescheduled
		}
		if err := store.SetFetchOK(tx, stateKey, rep.To.Format(time.DateOnly), s.now()); err != nil || partial == nil {
			return err
		}
		return store.SetFetchError(tx, stateKey, partial, s.now())
	})
	if err != nil {
		return rep, fmt.Errorf("calendar %s: %w", s.Source.Name(), err)
	}
	if partial != nil {
		s.Log.Warn("calendar refreshed in part", "calendar", s.Source.Name(), "events", rep.Events, "err", partial)
		return rep, fmt.Errorf("calendar %s: %w", s.Source.Name(), partial)
	}
	s.Log.Info("calendar refreshed", "calendar", s.Source.Name(), "from", rep.From.Format(time.DateOnly),
		"to", rep.To.Format(time.DateOnly), "events", rep.Events, "inserted", rep.Inserted, "rescheduled", rep.Rescheduled)
	return rep, nil
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
