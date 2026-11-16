package delivery

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/store"
)

func TestSourceAlerts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	set := func(fn func(tx *sql.Tx) error) {
		t.Helper()
		if err := st.Write(ctx, fn); err != nil {
			t.Fatal(err)
		}
	}
	set(func(tx *sql.Tx) error {
		// fred: two series failing since a success 30 h ago
		for _, id := range []string{"fred:GDPC1", "fred:UNRATE"} {
			if err := store.SetFetchOK(tx, id, "c", t0.Add(-30*time.Hour)); err != nil {
				return err
			}
			if err := store.SetFetchError(tx, id, errors.New("503 Service Unavailable"), t0.Add(-time.Hour)); err != nil {
				return err
			}
		}
		// yahoo: failing, but the last success is only 5 h old
		if err := store.SetFetchOK(tx, "yahoo:^NDX", "c", t0.Add(-5*time.Hour)); err != nil {
			return err
		}
		if err := store.SetFetchError(tx, "yahoo:^NDX", errors.New("429"), t0.Add(-time.Hour)); err != nil {
			return err
		}
		// cftc: old success, no error since (weekly source)
		if err := store.SetFetchOK(tx, "cftc", "c", t0.Add(-72*time.Hour)); err != nil {
			return err
		}
		// telegram: never succeeded, first failure 2 days ago
		return store.SetFetchError(tx, "telegram:chan", errors.New("dial tcp: timeout"), t0.Add(-48*time.Hour))
	})

	n := &fakeNotify{}
	now := t0
	a := &SourceAlerts{Store: st, Notify: n, After: 24 * time.Hour, Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if len(n.sent) != 1 {
		t.Fatalf("sent %q", n.sent)
	}
	msg := n.sent[0]
	for _, want := range []string{"⚠ Источник недоступен > 24 ч:", "• fred — 2, без успеха 30 ч: 503 Service Unavailable", "• telegram — 1, без успеха 2 дн: dial tcp: timeout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "yahoo") || strings.Contains(msg, "cftc") {
		t.Errorf("message has a source that is not down:\n%s", msg)
	}

	// an hour later: already reported
	now = t0.Add(time.Hour)
	if err := a.Check(ctx); err != nil || len(n.sent) != 1 {
		t.Fatalf("repeat within a day: %v, %d messages", err, len(n.sent))
	}
	// the alert mark is not a source and is not reported itself
	if st2, ok, _ := st.FetchStateOf(ctx, "notify:down:fred"); !ok || !st2.UpdatedAt.Equal(t0) {
		t.Errorf("mark %+v %v", st2, ok)
	}
	// a day later, still down: reported again
	now = t0.Add(25 * time.Hour)
	if err := a.Check(ctx); err != nil || len(n.sent) != 2 {
		t.Fatalf("after a day: %v, %d messages", err, len(n.sent))
	}
}
