package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/store"
)

// TextResult is the result of one text source.
type TextResult struct {
	Source    string
	Inserted  []model.Document // new documents, with their stored ids
	Updated   int
	Unchanged int
	Err       error
}

// Texts loads documents from text sources. The cursor of each source is
// fetch_state[source.Name()]; it is saved in the same transaction as the
// documents, so a failed write leaves it unchanged.
//
// Runs of the same source are serialised, so a calendar poller and the
// periodic job never fetch one feed at the same time.
type Texts struct {
	Store *store.Store
	Log   *slog.Logger
	Now   func() time.Time // defaults to time.Now
	// RegionOf is the region of a document whose source does not state one.
	RegionOf func(model.Document) string
	// WarnRevised are document types whose change after storing is logged
	// as a warning (an ECB statement corrected after publication).
	WarnRevised []string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (t *Texts) lock(name string) func() {
	t.mu.Lock()
	if t.locks == nil {
		t.locks = make(map[string]*sync.Mutex)
	}
	l, ok := t.locks[name]
	if !ok {
		l = &sync.Mutex{}
		t.locks[name] = l
	}
	t.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Stamp sets the region of a document whose source does not state one.
func (t *Texts) Stamp(d *model.Document) {
	if d.Region == "" && t.RegionOf != nil {
		d.Region = t.RegionOf(*d)
	}
}

// Run polls one source. A source error is recorded in fetch_state; a
// cancelled ctx is not.
func (t *Texts) Run(ctx context.Context, src sources.TextSource) TextResult {
	defer t.lock(src.Name())()
	res := TextResult{Source: src.Name()}
	st, _, err := t.Store.FetchStateOf(ctx, src.Name())
	if err != nil {
		res.Err = err
		return res
	}
	docs, next, err := src.FetchDocuments(ctx, st.Cursor)
	if err != nil {
		res.Err = err
		if ctx.Err() == nil {
			t.Log.Error("text source failed", "text_source", src.Name(), "err", err)
			if werr := t.Store.Write(ctx, func(tx *sql.Tx) error {
				return store.SetFetchError(tx, src.Name(), err, t.now())
			}); werr != nil {
				t.Log.Error("record fetch error", "text_source", src.Name(), "err", werr)
			}
		}
		return res
	}
	now := t.now()
	err = t.Store.Write(ctx, func(tx *sql.Tx) error {
		res.Inserted, res.Updated, res.Unchanged = nil, 0, 0
		for _, d := range docs {
			t.Stamp(&d)
			id, out, err := store.UpsertDocument(tx, d, now)
			if err != nil {
				return err
			}
			switch out {
			case store.DocInserted:
				d.ID, d.FetchedAt = id, now
				res.Inserted = append(res.Inserted, d)
			case store.DocUpdated:
				res.Updated++
				if slices.Contains(t.WarnRevised, d.DocType) {
					t.Log.Warn("document revised after publication", "text_source", src.Name(), "doc_type", d.DocType, "id", d.ExternalID)
				}
			default:
				res.Unchanged++
			}
		}
		return store.SetFetchOK(tx, src.Name(), next, now)
	})
	if err != nil {
		res.Err = fmt.Errorf("store %s: %w", src.Name(), err)
		return res
	}
	for _, d := range res.Inserted {
		t.Log.Info("document stored", "text_source", src.Name(), "doc_type", d.DocType, "id", d.ExternalID, "title", d.Title)
	}
	return res
}

func (t *Texts) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}
