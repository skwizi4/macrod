package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/export"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/store"
)

// FOMCDay polls for the statement of a decision day (§8.2): the statement
// page by its URL, and after the fast phase also the press-release feed.
// Once the statement is stored it is diffed with the previous one and
// exported at once (§12.7); the export happens exactly once per statement,
// also when the periodic RSS job stored it first.
type FOMCDay struct {
	Client  *fedtexts.Client
	BaseURL string
	Texts   *ingest.Texts
	Press   sources.TextSource
	// MeetingDocs fetches the implementation note before the export, so
	// fomc_diff.md carries it; nil or a failure only leaves it out.
	MeetingDocs sources.TextSource
	Store       *store.Store
	Export      *export.Events
	Location    *time.Location // America/New_York
	Clock       Clock          // nil: real time
	Log         *slog.Logger
}

// Poll implements Handler.
func (h *FOMCDay) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	st, ok, err := h.statementOf(ctx, ev)
	if err != nil {
		return false, err
	}
	if !ok {
		day := ev.ScheduledAt.In(h.Location)
		doc, err := fedtexts.FetchStatement(ctx, h.Client, h.BaseURL, day, h.Location)
		switch {
		case errors.Is(err, fedtexts.ErrNotFound):
			if h.now().Sub(ev.ScheduledAt) <= fomcFastFor {
				return false, nil
			}
			// Past the fast phase: the feed may know a different URL.
			if res := h.Texts.Run(ctx, h.Press); res.Err != nil {
				return false, res.Err
			}
		case err != nil:
			return false, err
		default:
			h.Texts.Stamp(&doc)
			if err := h.Store.Write(ctx, func(tx *sql.Tx) error {
				_, _, err := store.UpsertDocument(tx, doc, h.now())
				return err
			}); err != nil {
				return false, err
			}
			h.Log.Info("fomc statement found", "id", doc.ExternalID, "after", h.now().Sub(ev.ScheduledAt).Round(time.Second))
		}
		if st, ok, err = h.statementOf(ctx, ev); err != nil || !ok {
			return false, err
		}
	}
	if h.MeetingDocs != nil {
		if done, err := h.Export.Exported(ctx, st); err == nil && !done {
			if res := h.Texts.Run(ctx, h.MeetingDocs); res.Err != nil {
				h.Log.Warn("fomc meeting documents", "err", res.Err)
			}
		}
	}
	// A failed export keeps the event polling, so it is retried.
	if err := h.exportOnce(ctx, st); err != nil {
		return false, err
	}
	return true, nil
}

// statementOf returns the stored statement released on the event day.
func (h *FOMCDay) statementOf(ctx context.Context, ev model.ReleaseEvent) (model.Document, bool, error) {
	docs, err := h.Store.LatestDocuments(ctx, fedtexts.Source, fedtexts.DocStatement, 1)
	if err != nil || len(docs) == 0 {
		return model.Document{}, false, err
	}
	d := docs[0]
	y1, m1, d1 := d.PublishedAt.In(h.Location).Date()
	y2, m2, d2 := ev.ScheduledAt.In(h.Location).Date()
	return d, y1 == y2 && m1 == m2 && d1 == d2, nil
}

func (h *FOMCDay) exportOnce(ctx context.Context, st model.Document) error {
	done, err := h.Export.Exported(ctx, st)
	if err != nil || done {
		return err
	}
	docs, err := h.Store.LatestDocuments(ctx, fedtexts.Source, fedtexts.DocStatement, 2)
	if err != nil {
		return err
	}
	var prev model.Document
	if len(docs) == 2 && docs[1].PublishedAt.Before(st.PublishedAt) {
		prev = docs[1]
	}
	e, err := h.Export.FOMC(ctx, st, prev)
	if err != nil {
		return fmt.Errorf("fomc export: %w", err)
	}
	h.Log.Info("fomc statement exported", "id", st.ExternalID, "previous", prev.ExternalID, "iso_week", e.ISOWeek)
	return nil
}

func (h *FOMCDay) now() time.Time {
	if h.Clock != nil {
		return h.Clock.Now()
	}
	return time.Now()
}
