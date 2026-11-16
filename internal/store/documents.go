package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
)

// DocOutcome is what UpsertDocument did.
type DocOutcome string

// Document outcomes.
const (
	DocInserted  DocOutcome = "inserted"
	DocUpdated   DocOutcome = "updated"
	DocUnchanged DocOutcome = "unchanged"
)

// ContentHash is the sha256 of body with whitespace collapsed, so that
// re-fetching a page with different formatting is not a change.
func ContentHash(body string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(body), " ")))
	return hex.EncodeToString(sum[:])
}

// UpsertDocument stores d keyed by (source, external_id). A document whose
// content hash is unchanged is left alone, so fetched_at keeps the time it
// was first seen; a changed one (an edited post) is overwritten with a new
// fetched_at and its tags, classification and dedup state are cleared, so
// it is classified and checked for duplicates again.
func UpsertDocument(tx *sql.Tx, d model.Document, now time.Time) (id int64, outcome DocOutcome, err error) {
	if d.ContentHash == "" {
		d.ContentHash = ContentHash(d.Body)
	}
	where := d.Source + "/" + d.ExternalID
	var oldHash string
	err = tx.QueryRow(`SELECT id, content_hash FROM documents WHERE source = ? AND external_id = ?`,
		d.Source, d.ExternalID).Scan(&id, &oldHash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		r, err := tx.Exec(`
			INSERT INTO documents (source, external_id, doc_type, region, published_at, author, title,
				body, url, content_hash, is_opinion, is_junk, fetched_at, forwarded_from, views, views_at, has_photo)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.Source, d.ExternalID, nullString(d.DocType), nullString(d.Region), formatTS(d.PublishedAt),
			nullString(d.Author), nullString(d.Title), d.Body, nullString(d.URL), d.ContentHash,
			d.IsOpinion, d.IsJunk, formatTS(now), nullString(d.ForwardedFrom), nullViews(d.Views), nullViewsAt(d), d.HasPhoto)
		if err != nil {
			return 0, "", fmt.Errorf("document %s insert: %w", where, err)
		}
		id, err = r.LastInsertId()
		if err != nil {
			return 0, "", fmt.Errorf("document %s insert id: %w", where, err)
		}
		return id, DocInserted, nil
	case err != nil:
		return 0, "", fmt.Errorf("document %s select: %w", where, err)
	case oldHash == d.ContentHash:
		if d.Views > 0 {
			if _, err := tx.Exec(`UPDATE documents SET views = ?, views_at = ? WHERE id = ?`, d.Views, nullViewsAt(d), id); err != nil {
				return 0, "", fmt.Errorf("document %s views: %w", where, err)
			}
		}
		if d.HasPhoto {
			if _, err := tx.Exec(`UPDATE documents SET has_photo = 1 WHERE id = ?`, id); err != nil {
				return 0, "", fmt.Errorf("document %s photo: %w", where, err)
			}
		}
		return id, DocUnchanged, nil
	}
	_, err = tx.Exec(`
		UPDATE documents SET doc_type = ?, region = ?, published_at = ?, author = ?, title = ?,
			body = ?, url = ?, content_hash = ?, is_opinion = ?, is_junk = ?, fetched_at = ?,
			forwarded_from = ?, classified_at = NULL, classifier = NULL, dedup_hash = NULL, dup_of = NULL,
			views = coalesce(?, views), views_at = coalesce(?, views_at), has_photo = ?
		WHERE id = ?`,
		nullString(d.DocType), nullString(d.Region), formatTS(d.PublishedAt), nullString(d.Author),
		nullString(d.Title), d.Body, nullString(d.URL), d.ContentHash, d.IsOpinion, d.IsJunk,
		formatTS(now), nullString(d.ForwardedFrom), nullViews(d.Views), nullViewsAt(d), d.HasPhoto, id)
	if err != nil {
		return 0, "", fmt.Errorf("document %s update: %w", where, err)
	}
	if _, err := tx.Exec(`DELETE FROM document_tags WHERE document_id = ?`, id); err != nil {
		return 0, "", fmt.Errorf("document %s clear tags: %w", where, err)
	}
	return id, DocUpdated, nil
}

const documentColumns = `id, source, external_id, doc_type, region, published_at, author, title, body, url,
	content_hash, is_opinion, is_junk, fetched_at, forwarded_from, coalesce(views, 0), views_at, coalesce(has_photo, 0)`

func nullViews(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullViewsAt(d model.Document) any {
	if d.Views <= 0 || d.ViewsAt.IsZero() {
		return nil
	}
	return formatTS(d.ViewsAt)
}

// PostSeen is what a re-read of a Telegram post brings: its views and
// whether it has a photo.
type PostSeen struct {
	Views    int64
	HasPhoto bool
}

// UpdateViews stores the views and photo flag of posts of source re-read
// at "at" (external id → seen). Unknown ids are ignored; views of 0 keep
// the stored ones.
func UpdateViews(tx *sql.Tx, source string, seen map[string]PostSeen, at time.Time) (int, error) {
	n := 0
	for id, v := range seen {
		r, err := tx.Exec(`UPDATE documents SET views = coalesce(?, views), views_at = coalesce(?, views_at), has_photo = ?
			WHERE source = ? AND external_id = ?`,
			nullViews(v.Views), nullViewsAt(model.Document{Views: v.Views, ViewsAt: at}), v.HasPhoto, source, id)
		if err != nil {
			return n, fmt.Errorf("views %s/%s: %w", source, id, err)
		}
		k, _ := r.RowsAffected()
		n += int(k)
	}
	return n, nil
}

// LatestDocuments returns up to n documents of source and docType, newest
// published first.
func (s *Store) LatestDocuments(ctx context.Context, source, docType string, n int) ([]model.Document, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+documentColumns+` FROM documents
		WHERE source = ? AND doc_type = ? ORDER BY published_at DESC, id DESC LIMIT ?`, source, docType, n)
	if err != nil {
		return nil, fmt.Errorf("documents %s/%s: %w", source, docType, err)
	}
	defer rows.Close()
	var out []model.Document
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("documents %s/%s scan: %w", source, docType, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Document returns a document by its id or, if ref is not a number, by
// external_id within source; ok is false if there is none.
func (s *Store) Document(ctx context.Context, source, ref string) (d model.Document, ok bool, err error) {
	q := `SELECT ` + documentColumns + ` FROM documents WHERE source = ? AND external_id = ?`
	args := []any{source, ref}
	if id, perr := strconv.ParseInt(ref, 10, 64); perr == nil {
		q, args = `SELECT `+documentColumns+` FROM documents WHERE source = ? AND id = ?`, []any{source, id}
	}
	d, err = scanDocument(s.read.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Document{}, false, nil
	}
	if err != nil {
		return model.Document{}, false, fmt.Errorf("document %s/%s: %w", source, ref, err)
	}
	return d, true, nil
}

// DocumentCounts maps doc_type to the number of documents of source.
func (s *Store) DocumentCounts(ctx context.Context, source string) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT coalesce(doc_type, ''), count(*) FROM documents WHERE source = ? GROUP BY 1`, source)
	if err != nil {
		return nil, fmt.Errorf("document counts %s: %w", source, err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, fmt.Errorf("document counts %s scan: %w", source, err)
		}
		out[t] = n
	}
	return out, rows.Err()
}

func scanDocument(row interface{ Scan(...any) error }) (model.Document, error) {
	var d model.Document
	var dt, region, author, title, url, fwd sql.NullString
	var viewsAt sql.NullTime
	if err := row.Scan(&d.ID, &d.Source, &d.ExternalID, &dt, &region, &d.PublishedAt,
		&author, &title, &d.Body, &url, &d.ContentHash, &d.IsOpinion, &d.IsJunk, &d.FetchedAt, &fwd,
		&d.Views, &viewsAt, &d.HasPhoto); err != nil {
		return model.Document{}, err
	}
	d.ViewsAt = viewsAt.Time
	d.DocType, d.Region, d.Author, d.Title, d.URL = dt.String, region.String, author.String, title.String, url.String
	d.ForwardedFrom = fwd.String
	return d, nil
}
