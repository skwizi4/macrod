package delivery

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/store"
)

// alertPrefix marks fetch_state rows that remember the last alert per source
// family, so a restart does not repeat it.
const alertPrefix = "notify:down:"

// SourceAlerts sends «Источник X недоступен > 24 ч» (ТЗ §12.11). A row of
// fetch_state is down when it has an error (a success clears it) and its last
// success — or first failure, if it never succeeded — is older than After. Rows are grouped by family (the part
// before ':' — fred, yahoo, telegram…); a family is reported at most once per
// After while it stays down, all families of a check in one message.
type SourceAlerts struct {
	Store  *store.Store
	Notify Notifier
	After  time.Duration
	Now    func() time.Time // defaults to time.Now
	Log    *slog.Logger
}

type downFamily struct {
	name    string
	rows    int
	lastErr string
	since   time.Time // oldest last success
}

// Check sends the message if some family is newly down.
func (a *SourceAlerts) Check(ctx context.Context) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	states, err := a.Store.FetchStates(ctx)
	if err != nil {
		return err
	}
	down := map[string]*downFamily{}
	for src, st := range states {
		fam := family(src)
		if fam == "export" || fam == "notify" {
			continue
		}
		// A success clears last_error, so an error on the row means failing
		// now; updated_at is the last success (or the first failure).
		if st.LastErrorAt.IsZero() || now.Sub(st.UpdatedAt) < a.After {
			continue
		}
		d := down[fam]
		if d == nil {
			d = &downFamily{name: fam, since: st.UpdatedAt}
			down[fam] = d
		}
		d.rows++
		if st.UpdatedAt.Before(d.since) {
			d.since = st.UpdatedAt
		}
		if d.lastErr == "" || st.LastErrorAt.After(now.Add(-time.Hour)) {
			d.lastErr = st.LastError
		}
	}
	var report []*downFamily
	for fam, d := range down {
		if mark, ok := states[alertPrefix+fam]; ok && now.Sub(mark.UpdatedAt) < a.After {
			continue
		}
		report = append(report, d)
	}
	if len(report) == 0 {
		return nil
	}
	slices.SortFunc(report, func(x, y *downFamily) int { return strings.Compare(x.name, y.name) })
	if err := a.Notify.Send(ctx, alertText(report, a.After, now)); err != nil {
		return fmt.Errorf("source alerts: %w", err)
	}
	return a.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, d := range report {
			if err := store.SetFetchOK(tx, alertPrefix+d.name, "", now); err != nil {
				return err
			}
		}
		return nil
	})
}

func family(source string) string {
	if i := strings.IndexByte(source, ':'); i >= 0 {
		return source[:i]
	}
	return source
}

func alertText(fams []*downFamily, after time.Duration, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠ Источник недоступен > %s:", fmtDuration(after))
	for _, d := range fams {
		err := d.lastErr
		if r := []rune(err); len(r) > 200 {
			err = string(r[:200]) + "…"
		}
		fmt.Fprintf(&b, "\n• %s — %d, без успеха %s: %s", d.name, d.rows, fmtDuration(now.Sub(d.since)), err)
	}
	return b.String()
}

func fmtDuration(d time.Duration) string {
	if h := int(d.Hours()); h < 48 {
		return fmt.Sprintf("%d ч", h)
	}
	return fmt.Sprintf("%d дн", int(d.Hours()/24))
}
