package ingest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

type fakeText struct {
	docs    []model.Document
	err     error
	cursors []string // cursors received
	next    string
}

func (f *fakeText) Name() string { return "fed:speeches" }

func (f *fakeText) FetchDocuments(_ context.Context, cursor string) ([]model.Document, string, error) {
	f.cursors = append(f.cursors, cursor)
	return f.docs, f.next, f.err
}

func TestTextsRun(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	tx := &Texts{Store: st, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
	ctx := context.Background()
	speech := model.Document{Source: "fed", ExternalID: "waller20261008a", DocType: "speech", PublishedAt: now, Body: "Thank you."}
	src := &fakeText{docs: []model.Document{speech}, next: "waller20261008a"}

	res := tx.Run(ctx, src)
	if res.Err != nil || len(res.Inserted) != 1 || res.Inserted[0].ID == 0 {
		t.Fatalf("first run = %+v", res)
	}
	res = tx.Run(ctx, src)
	if res.Err != nil || len(res.Inserted) != 0 || res.Unchanged != 1 {
		t.Fatalf("second run = %+v", res)
	}
	if src.cursors[0] != "" || src.cursors[1] != "waller20261008a" {
		t.Errorf("cursors = %q", src.cursors)
	}

	// A failing source keeps the cursor and records the error.
	src.err, src.next = errors.New("feed down"), "x"
	if res := tx.Run(ctx, src); res.Err == nil {
		t.Fatal("want error")
	}
	fs, _, _ := st.FetchStateOf(ctx, "fed:speeches")
	if fs.Cursor != "waller20261008a" || fs.LastError != "feed down" {
		t.Errorf("fetch_state = %+v", fs)
	}
}

// A watched document type changed after storing is a revision: updated in
// place, reported as a warning.
func TestTextsWarnRevised(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var buf bytes.Buffer
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	tx := &Texts{Store: st, Log: slog.New(slog.NewTextHandler(&buf, nil)), WarnRevised: []string{"ecb_statement"}, Now: func() time.Time { return now }}
	ctx := context.Background()
	doc := model.Document{Source: "ecb_texts", ExternalID: "ecb.is261029~5c5ffe99e5", DocType: "ecb_statement", PublishedAt: now, Body: "Good afternoon."}
	src := &fakeText{docs: []model.Document{doc}}
	tx.Run(ctx, src)
	if strings.Contains(buf.String(), "revised") {
		t.Fatal("first store reported as a revision")
	}
	src.docs[0].Body = "Good afternoon, corrected."
	if res := tx.Run(ctx, src); res.Updated != 1 {
		t.Fatalf("revision = %+v", res)
	}
	if !strings.Contains(buf.String(), "document revised after publication") {
		t.Errorf("no warning: %s", buf.String())
	}
}
