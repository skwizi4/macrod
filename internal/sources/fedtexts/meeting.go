package fedtexts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/htmltext"
)

// documents.doc_type values of the meeting documents (FIXES_W44 B2).
const (
	DocImplementationNote = "implementation_note"
	DocPressConference    = "press_conference"
)

// meetingDocsFor is how long after a decision its documents are looked
// for: the transcript has come 1–8 days after the press conference.
const meetingDocsFor = 21 * 24 * time.Hour

// MeetingDocs fetches the documents of recent FOMC meetings that are not
// in the press-release feed: the Implementation Note
// (newsevents/pressreleases/monetaryYYYYMMDDa1.htm, with the statement at
// 2:00 p.m.) and the Chair's press conference transcript, a PDF only
// (mediacenter/files/FOMCpresconfYYYYMMDD.pdf, posted days later). A
// document not published yet (404) is asked for again at the next poll.
//
// The cursor is the space-separated list of document ids already stored.
type MeetingDocs struct {
	Client   *Client
	BaseURL  string
	Location *time.Location // America/New_York
	// Meetings returns the decision days (any time of the day) since a
	// moment.
	Meetings func(ctx context.Context, since time.Time) ([]time.Time, error)
	Now      func() time.Time // defaults to time.Now
}

// Name implements sources.TextSource.
func (m *MeetingDocs) Name() string { return "fed:meeting_docs" }

// FetchDocuments implements sources.TextSource.
func (m *MeetingDocs) FetchDocuments(ctx context.Context, cursor string) ([]model.Document, string, error) {
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	days, err := m.Meetings(ctx, now.Add(-meetingDocsFor))
	if err != nil {
		return nil, cursor, fmt.Errorf("fomc meetings: %w", err)
	}
	seen := strings.Fields(cursor)
	var docs []model.Document
	var errs []error
	for _, day := range days {
		day = day.In(m.Location)
		if day.After(now) {
			continue
		}
		stamp := day.Format("20060102")
		for _, f := range []struct {
			id    string
			fetch func(context.Context, time.Time) (model.Document, error)
		}{
			{"monetary" + stamp + "a1", m.note},
			{"FOMCpresconf" + stamp, m.transcript},
		} {
			if slices.Contains(seen, f.id) {
				continue
			}
			d, err := f.fetch(ctx, day)
			switch {
			case errors.Is(err, ErrNotFound):
				continue
			case err != nil:
				errs = append(errs, err)
				continue
			}
			docs = append(docs, d)
			seen = append(seen, f.id)
		}
	}
	// Ids older than the window are not asked for again.
	if len(seen) > 20 {
		seen = seen[len(seen)-20:]
	}
	return docs, strings.Join(seen, " "), errors.Join(errs...)
}

func (m *MeetingDocs) note(ctx context.Context, day time.Time) (model.Document, error) {
	u, err := resolve(m.BaseURL, "/newsevents/pressreleases/monetary"+day.Format("20060102")+"a1.htm")
	if err != nil {
		return model.Document{}, err
	}
	b, err := m.Client.Get(ctx, u)
	if err != nil {
		return model.Document{}, fmt.Errorf("implementation note %s: %w", day.Format(time.DateOnly), err)
	}
	return implementationNote(u, b, m.Location)
}

// implementationNote reads the note page. It has no #article: the text is
// the column of #content after the heading. It is issued with the
// statement, at 2:00 p.m. ET of its date.
func implementationNote(u string, b []byte, loc *time.Location) (model.Document, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return model.Document{}, fmt.Errorf("%s: %w", u, err)
	}
	content := doc.Find("#content").First()
	title := htmltext.Clean(content.Find(".heading h3").First().Text())
	if !strings.HasPrefix(title, "Implementation Note") {
		return model.Document{}, fmt.Errorf("%s: not an implementation note: %q", u, title)
	}
	date, err := time.Parse("January 02, 2006", htmltext.Clean(content.Find(".article__time").First().Text()))
	if err != nil {
		return model.Document{}, fmt.Errorf("%s date: %w", u, err)
	}
	body := articleText(content.Find(".row > .col-xs-12").Not(".heading").First())
	if body == "" {
		return model.Document{}, fmt.Errorf("%s: empty body", u)
	}
	published := time.Date(date.Year(), date.Month(), date.Day(), 14, 0, 0, 0, loc).UTC()
	return newDocument(u, DocImplementationNote, title, "", body, published), nil
}

func (m *MeetingDocs) transcript(ctx context.Context, day time.Time) (model.Document, error) {
	u, err := resolve(m.BaseURL, "/mediacenter/files/FOMCpresconf"+day.Format("20060102")+".pdf")
	if err != nil {
		return model.Document{}, err
	}
	b, modified, err := m.Client.GetModified(ctx, u)
	if err != nil {
		return model.Document{}, fmt.Errorf("press conference %s: %w", day.Format(time.DateOnly), err)
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	return pressConference(u, b, modified, now)
}

// pressConference makes the transcript document. It is dated by the PDF's
// Last-Modified (when it was posted), else by when it was fetched.
func pressConference(u string, b []byte, modified, fetched time.Time) (model.Document, error) {
	text, err := pdfText(b)
	if err != nil {
		return model.Document{}, fmt.Errorf("%s: %w", u, err)
	}
	title, body, _ := strings.Cut(text, "\n\n")
	if !strings.HasPrefix(title, "Transcript of") {
		return model.Document{}, fmt.Errorf("%s: not a press conference transcript: %.80q", u, title)
	}
	published := modified
	if published.IsZero() {
		published = fetched
	}
	d := newDocument(u, DocPressConference, title, "", body, published.UTC())
	d.ExternalID = strings.TrimSuffix(d.ExternalID, ".pdf")
	return d, nil
}
