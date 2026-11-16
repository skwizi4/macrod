package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestGuidanceCandidates(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 22, 0, 0, 0, time.UTC)
	c := GuidanceCandidate{Ticker: "GOOGL", Year: 2026, Low: 195, High: 205, Quote: "q", URL: "https://abc.xyz/x",
		DocSource: "ir_transcript", DocRef: "0001652044-26-000090", DocTitle: "GOOGL earnings call transcript, FY2026 Q3", Given: d("2026-10-28")}
	insert := func(c GuidanceCandidate) (int64, bool) {
		t.Helper()
		var id int64
		var ins bool
		if err := s.Write(ctx, func(tx *sql.Tx) (err error) {
			id, ins, err = InsertGuidanceCandidate(tx, c, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id, ins
	}
	id, ins := insert(c)
	if !ins {
		t.Fatal("first insert not inserted")
	}
	if id2, ins := insert(c); ins || id2 != id {
		t.Fatalf("second insert = %d, %v; want %d, false", id2, ins, id)
	}
	c2 := c
	c2.Low, c2.High, c2.Note = 210, 220, "differs"
	id2, _ := insert(c2)

	decide := func(id int64, approve bool) (GuidanceCandidate, error) {
		var got GuidanceCandidate
		err := s.Write(ctx, func(tx *sql.Tx) (err error) {
			got, err = DecideGuidanceCandidate(tx, id, approve, "cli", now)
			return err
		})
		return got, err
	}
	if got, err := decide(id, true); err != nil || got.Status != GuidanceApproved {
		t.Fatalf("approve = %+v, %v", got, err)
	}
	if _, err := decide(id, false); !errors.Is(err, ErrDecided) {
		t.Fatalf("second decision err = %v, want ErrDecided", err)
	}
	if _, err := decide(id2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := decide(999, true); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("unknown id err = %v", err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error { return SetGuidanceMessage(tx, id2, 42) }); err != nil {
		t.Fatal(err)
	}

	pend, err := s.GuidanceCandidates(ctx, GuidancePending)
	if err != nil || len(pend) != 0 {
		t.Fatalf("pending = %+v, %v", pend, err)
	}
	all, err := s.GuidanceCandidates(ctx)
	if err != nil || len(all) != 2 || all[0].ID != id2 || all[0].Note != "differs" || all[0].TGMessageID != 42 || all[0].Status != GuidanceRejected {
		t.Fatalf("all = %+v, %v", all, err)
	}
	conf, err := s.ConfirmedGuidance(ctx)
	if err != nil || len(conf) != 1 {
		t.Fatalf("confirmed = %+v, %v", conf, err)
	}
	g := conf[0]
	if g.Ticker != "GOOGL" || g.Low != 195 || g.High != 205 || !g.Given.Equal(d("2026-10-28")) || g.CandidateID != id ||
		g.Source != "GOOGL earnings call transcript, FY2026 Q3, 2026-10-28" {
		t.Fatalf("confirmed = %+v", g)
	}
}
