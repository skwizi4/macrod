package collect

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/store"
)

func euPreset(t *testing.T) *config.Config {
	t.Helper()
	root := filepath.Join("..", "..")
	cfg, err := config.LoadAt(filepath.Join(root, "presets", "eu-equities.yaml"), root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func storeDocs(t *testing.T, st *store.Store, docs ...model.Document) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, d := range docs {
			if _, _, err := store.UpsertDocument(tx, d, d.PublishedAt); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ECB documents take the region of the ecb_texts agent, not the Fed's.
func TestECBDocumentRegion(t *testing.T) {
	cfg := euPreset(t)
	texts := NewTexts(cfg, nil, slog.New(slog.DiscardHandler))
	d := model.Document{Source: ecbtexts.Source}
	texts.Stamp(&d)
	if want := cfg.RegionOfTag(cfg.ECBTexts.Agent); d.Region != want || want == "" {
		t.Errorf("region = %q, want %q", d.Region, want)
	}
}

func TestPendingQA(t *testing.T) {
	st := openStore(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	oct := model.Document{Source: ecbtexts.Source, ExternalID: "ecb.is261029~5c5ffe99e5", DocType: ecbtexts.DocStatement,
		Body: "intro", PublishedAt: time.Date(2026, 10, 29, 13, 45, 0, 0, time.UTC)}
	sep := model.Document{Source: ecbtexts.Source, ExternalID: "ecb.is260910~6a45359cfc", DocType: ecbtexts.DocStatement,
		Body: "intro", PublishedAt: time.Date(2026, 9, 10, 12, 45, 0, 0, time.UTC)}
	storeDocs(t, st, oct, sep)
	got, err := pendingQA(context.Background(), st, now)
	if err != nil || len(got) != 1 || got[0].ExternalID != oct.ExternalID {
		t.Fatalf("pending = %v, err %v; want the October statement only (September is past 21 days)", got, err)
	}
	qa := oct
	qa.ExternalID += ecbtexts.QASuffix
	qa.DocType, qa.Body = ecbtexts.DocPressConference, "Q&A"
	storeDocs(t, st, qa)
	if got, _ := pendingQA(context.Background(), st, now); len(got) != 0 {
		t.Errorf("pending after the Q&A = %v", got)
	}
}

func TestCheckECBDecisions(t *testing.T) {
	cfg := euPreset(t)
	st := openStore(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx := context.Background()
	body := "The Governing Council today decided to raise the three key ECB interest rates by 25 basis points. " +
		"Accordingly, the interest rates on the deposit facility, the main refinancing operations and the marginal lending facility " +
		"will be increased to 2.50%, 2.65% and 2.90% respectively, with effect from 16 September 2026."
	storeDocs(t, st, model.Document{Source: ecbtexts.Source, ExternalID: "ecb.mp260910~314e508016", DocType: ecbtexts.DocDecision,
		Body: body, PublishedAt: time.Date(2026, 9, 10, 12, 15, 0, 0, time.UTC)})
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	// No series in the database: skipped, logged, no failure.
	CheckECBDecisions(ctx, cfg, st, now, log)
	if !strings.Contains(buf.String(), "skipped") || strings.Contains(buf.String(), "does not match") {
		t.Errorf("without the series: %s", buf.String())
	}
	ser, _ := cfg.ResolveSeries(cfg.ECBTexts.CheckSeries)
	put := func(v float64) {
		if err := st.Write(ctx, func(tx *sql.Tx) error {
			_, err := store.UpsertObservations(tx, ser.Source, ser.ID, []model.Observation{{Date: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), Value: v}}, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	put(2.50)
	buf.Reset()
	CheckECBDecisions(ctx, cfg, st, now, log)
	if strings.Contains(buf.String(), "does not match") {
		t.Errorf("matching series reported: %s", buf.String())
	}
	put(2.25)
	buf.Reset()
	CheckECBDecisions(ctx, cfg, st, now, log)
	if !strings.Contains(buf.String(), "does not match") {
		t.Errorf("mismatch not reported: %s", buf.String())
	}
	// Not in force yet: nothing to compare.
	buf.Reset()
	CheckECBDecisions(ctx, cfg, st, time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), log)
	if buf.Len() != 0 {
		t.Errorf("before 16 September: %s", buf.String())
	}
	// Without check_series in the config nothing happens.
	cfg.ECBTexts.CheckSeries = ""
	buf.Reset()
	CheckECBDecisions(ctx, cfg, st, now, log)
	if buf.Len() != 0 {
		t.Errorf("without check_series: %s", buf.String())
	}
}
