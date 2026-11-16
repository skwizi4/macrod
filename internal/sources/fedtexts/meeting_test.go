package fedtexts

import (
	"context"
	"strings"
	"testing"
	"time"
)

// pdfPosted is the Last-Modified of the recorded transcript.
const pdfPosted = "Thu, 29 Oct 2026 15:22:11 GMT"

func TestMeetingDocs(t *testing.T) {
	site, base := newSite(t)
	var since time.Time
	m := &MeetingDocs{Client: newTestClient(), BaseURL: base, Location: ny(t),
		Meetings: func(_ context.Context, s time.Time) ([]time.Time, error) {
			since = s
			// 10-28 is recorded; 12-09 is ahead; for 10-07 nothing is published.
			return []time.Time{
				time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC), time.Date(2026, 10, 28, 18, 0, 0, 0, time.UTC),
				time.Date(2026, 12, 9, 19, 0, 0, 0, time.UTC),
			}, nil
		},
		Now: func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) },
	}
	docs, cursor, err := m.FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !since.Equal(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("since = %s", since)
	}
	if len(docs) != 2 || cursor != "monetary20261028a1 FOMCpresconf20261028" {
		t.Fatalf("docs = %d, cursor %q", len(docs), cursor)
	}
	note, conf := docs[0], docs[1]
	if note.DocType != DocImplementationNote || note.Title != "Implementation Note issued October 28, 2026" ||
		note.PublishedAt.Format(time.RFC3339) != "2026-10-28T18:00:00Z" ||
		!strings.Contains(note.Body, "interest rate paid on reserve balances at 3.90 percent") ||
		!strings.Contains(note.Body, "offering rate of 3.75 percent") {
		t.Errorf("note = %+v", note)
	}
	if conf.DocType != DocPressConference || conf.ExternalID != "FOMCpresconf20261028" ||
		conf.Title != "Transcript of Chairman Warsh’s Press Conference October 28, 2026" ||
		conf.PublishedAt.Format(time.RFC3339) != "2026-10-29T15:22:11Z" || !strings.HasSuffix(conf.URL, "/mediacenter/files/FOMCpresconf20261028.pdf") {
		t.Errorf("transcript = %+v", conf)
	}
	// One paragraph per speaker turn; no page footers.
	if !strings.HasPrefix(conf.Body, "CHAIRMAN WARSH. Good day. This morning the FOMC decided to maintain") ||
		!strings.Contains(conf.Body, "\n\nMICHELLE SMITH. Colby.\n\nCOLBY SMITH. Colby Smith with the New York Times.") ||
		strings.Contains(conf.Body, "Page 2 of 17") || strings.Contains(conf.Body, "PRELIMINARY") {
		t.Errorf("transcript body: %.300s", conf.Body)
	}

	// Next poll: stored documents are not fetched again.
	site.reset()
	docs, cursor2, err := m.FetchDocuments(context.Background(), cursor)
	if err != nil || len(docs) != 0 || cursor2 != cursor {
		t.Errorf("second poll: %d docs, cursor %q, %v", len(docs), cursor2, err)
	}
	for _, r := range site.reset() {
		if strings.Contains(r, "20261028") {
			t.Errorf("fetched again: %s", r)
		}
	}
}

func TestPDFTextRejectsUnreadable(t *testing.T) {
	if _, err := pdfText([]byte("%PDF-1.4\nnot much here")); err == nil {
		t.Error("no error for a PDF without text")
	}
}
