// Package sources declares the interfaces implemented by data sources (ТЗ §5).
package sources

import (
	"context"
	"time"

	"macro-collector/internal/model"
)

// NumericSource fetches level observations of a series.
type NumericSource interface {
	Name() string
	FetchSeries(ctx context.Context, seriesID string, since time.Time) ([]model.Observation, error)
}

// TextSource fetches documents published after cursor and returns the cursor
// to save once they are stored.
type TextSource interface {
	Name() string
	FetchDocuments(ctx context.Context, cursor string) (docs []model.Document, nextCursor string, err error)
}

// CalendarSource lists release events scheduled in [from, to).
type CalendarSource interface {
	Name() string
	Upcoming(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error)
}

// PartialError is returned by a CalendarSource together with the events of
// a listing that is complete except for the Keys series ((releases.source,
// name) pairs) whose part failed: the caller syncs the other series and
// leaves the stored events of these as they are.
type PartialError struct {
	Keys [][2]string
	Err  error
}

func (e *PartialError) Error() string { return e.Err.Error() }

func (e *PartialError) Unwrap() error { return e.Err }
