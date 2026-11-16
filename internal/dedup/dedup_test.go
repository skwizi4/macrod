package dedup

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

var t0 = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

const long = "ФРС утримала ставку, але Пауелл натякнув на зниження вже у грудні."

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func add(t *testing.T, s *store.Store, ref, body, fwd string, at time.Time) {
	t.Helper()
	source, id := "telegram:"+ref[:len(ref)-3], ref[len(ref)-2:]
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, _, err := store.UpsertDocument(tx, model.Document{
			Source: source, ExternalID: id, DocType: "post", Region: "usa", PublishedAt: at, Body: body, ForwardedFrom: fwd,
		}, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// dups maps "<handle>/<id>" to the ref of its canonical post ("" = canonical).
func dups(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	rows, err := s.ReadDB().Query(`SELECT substr(d.source, 10) || '/' || d.external_id,
		coalesce((SELECT substr(c.source, 10) || '/' || c.external_id FROM documents c WHERE c.id = d.dup_of), '')
		FROM documents d`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var ref, canon string
		rows.Scan(&ref, &canon)
		out[ref] = canon
	}
	return out
}

func TestHash(t *testing.T) {
	a := Hash("🏦 ФРС утримала ставку!  https://t.me/x/1\nДеталі нижче.")
	if a != Hash("ФРС   утримала ставку — деталі нижче") || a == Hash("ФРС підняла ставку, деталі нижче") || len(a) != 64 {
		t.Error("hash should ignore emoji, links, punctuation, case and spacing only")
	}
}

func TestRun(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	add(t, s, "chan_a/10", long, "", t0)
	add(t, s, "chan_b/20", "🔥 "+long+" https://t.me/chan_a/10", "", t0.Add(time.Hour))              // same text
	add(t, s, "chan_b/21", "Коментар до посту: ставка без змін.", "chan_a/10", t0.Add(2*time.Hour)) // forward of chan_a/10
	add(t, s, "chan_a/11", "Перший репост зовнішнього каналу.", "external/5", t0.Add(3*time.Hour))
	add(t, s, "chan_b/22", "Другий репост, інший підпис.", "External/5", t0.Add(4*time.Hour))
	add(t, s, "chan_a/12", long, "", t0.Add(8*24*time.Hour)) // same text, 8 days later
	add(t, s, "chan_a/13", "$META -7%", "", t0.Add(5*time.Hour))
	add(t, s, "chan_b/23", "🔽 $META -7%", "", t0.Add(6*time.Hour)) // short: not merged
	add(t, s, "chan_c/30", "Переслано без посилання.", "Some Person", t0.Add(7*time.Hour))
	add(t, s, "chan_d/40", "Переслано без посилання.", "Some Person", t0.Add(7*time.Hour)) // short, name only

	st, err := Run(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"chan_a/10": "", "chan_b/20": "chan_a/10", "chan_b/21": "chan_a/10",
		"chan_a/11": "", "chan_b/22": "chan_a/11",
		"chan_a/12": "", "chan_a/13": "", "chan_b/23": "", "chan_c/30": "", "chan_d/40": "",
	}
	got := dups(t, s)
	for ref, w := range want {
		if got[ref] != w {
			t.Errorf("%s → %q, want %q", ref, got[ref], w)
		}
	}
	if st.Hashed != 10 || st.Duplicates != 3 || st.Changed != 3 {
		t.Errorf("stats = %+v", st)
	}

	// Nothing new: nothing to do.
	if st, err := Run(ctx, s); err != nil || st != (Stats{}) {
		t.Errorf("repeat = %+v, %v", st, err)
	}

	// A late post earlier than chan_a/12 with its text becomes canonical.
	add(t, s, "chan_c/31", long, "", t0.Add(7*24*time.Hour))
	if st, err = Run(ctx, s); err != nil || st.Hashed != 1 || st.Changed != 2 {
		t.Errorf("late post: %+v, %v", st, err)
	}
	got = dups(t, s)
	// chan_c/31 is 6 days after chan_b/20 (same text): it joins that group.
	if got["chan_c/31"] != "chan_a/10" || got["chan_a/12"] != "chan_a/10" {
		t.Errorf("late post: c/31 → %q, a/12 → %q", got["chan_c/31"], got["chan_a/12"])
	}

	// Editing the canonical post's text splits the text group; the forward
	// stays with its original.
	add(t, s, "chan_a/10", "Оновлено: ФРС утримала ставку, коментарі Пауелла згодом.", "", t0)
	if _, err = Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	got = dups(t, s)
	if got["chan_a/10"] != "" || got["chan_b/21"] != "chan_a/10" || got["chan_b/20"] != "" || got["chan_c/31"] != "chan_b/20" {
		t.Errorf("after edit = %v", got)
	}
}
