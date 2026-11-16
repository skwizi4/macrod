package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"macro-collector/internal/model"
)

// isPost selects Telegram posts and RSS articles: the documents that are
// classified and deduplicated (official texts are not).
const isPost = `(source LIKE 'telegram:%' OR source LIKE 'rss:%')`

// UnclassifiedPosts returns up to limit posts not classified yet, oldest
// first.
func (s *Store) UnclassifiedPosts(ctx context.Context, limit int) ([]model.Document, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+documentColumns+` FROM documents
		WHERE `+isPost+` AND classified_at IS NULL ORDER BY published_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("unclassified posts: %w", err)
	}
	defer rows.Close()
	var out []model.Document
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("unclassified posts scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TagScore is a row of document_tags.
type TagScore struct {
	Tag     string
	Score   float64
	Primary bool
}

// PostTags is the classification of one post.
type PostTags struct {
	DocumentID int64
	Tags       []TagScore
	IsJunk     bool
	Classifier string // gemini | keywords
}

// SavePostTags replaces the tags of a post and marks it classified.
func SavePostTags(tx *sql.Tx, p PostTags, now time.Time) error {
	if _, err := tx.Exec(`DELETE FROM document_tags WHERE document_id = ?`, p.DocumentID); err != nil {
		return fmt.Errorf("post %d clear tags: %w", p.DocumentID, err)
	}
	for _, t := range p.Tags {
		if _, err := tx.Exec(`INSERT INTO document_tags (document_id, tag, score, is_primary, classifier) VALUES (?, ?, ?, ?, ?)`,
			p.DocumentID, t.Tag, t.Score, t.Primary, p.Classifier); err != nil {
			return fmt.Errorf("post %d tag %s: %w", p.DocumentID, t.Tag, err)
		}
	}
	if _, err := tx.Exec(`UPDATE documents SET is_junk = ?, classified_at = ?, classifier = ? WHERE id = ?`,
		p.IsJunk, formatTS(now), p.Classifier, p.DocumentID); err != nil {
		return fmt.Errorf("post %d classified: %w", p.DocumentID, err)
	}
	return nil
}

// CachedClassifications maps content hashes to cached Gemini answers made
// with model and promptHash; answers of another model or prompt are not
// returned.
func (s *Store) CachedClassifications(ctx context.Context, hashes []string, model, promptHash string) (map[string]string, error) {
	out := make(map[string]string)
	for len(hashes) > 0 {
		n := min(len(hashes), 500)
		args := []any{model, promptHash}
		for _, h := range hashes[:n] {
			args = append(args, h)
		}
		rows, err := s.read.QueryContext(ctx, `SELECT content_hash, result_json FROM classify_cache
			WHERE model = ? AND prompt_hash = ? AND content_hash IN (?`+strings.Repeat(", ?", n-1)+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("classify cache: %w", err)
		}
		for rows.Next() {
			var h, js string
			if err := rows.Scan(&h, &js); err != nil {
				rows.Close()
				return nil, fmt.Errorf("classify cache scan: %w", err)
			}
			out[h] = js
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("classify cache: %w", err)
		}
		hashes = hashes[n:]
	}
	return out, nil
}

// PutClassifyCache stores a Gemini answer for a content hash.
func PutClassifyCache(tx *sql.Tx, contentHash, model, promptHash, resultJSON string, now time.Time) error {
	_, err := tx.Exec(`INSERT INTO classify_cache (content_hash, model, prompt_hash, result_json, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (content_hash) DO UPDATE SET model = excluded.model, prompt_hash = excluded.prompt_hash,
			result_json = excluded.result_json, created_at = excluded.created_at`,
		contentHash, model, promptHash, resultJSON, formatTS(now))
	if err != nil {
		return fmt.Errorf("classify cache put: %w", err)
	}
	return nil
}

// ResetClassification marks posts published since as not classified and
// drops their tags, for `macroctl classify --reprocess`. It returns the
// number of posts reset.
func ResetClassification(tx *sql.Tx, since time.Time) (int64, error) {
	if _, err := tx.Exec(`DELETE FROM document_tags WHERE document_id IN (
		SELECT id FROM documents WHERE `+isPost+` AND published_at >= ?)`, formatTS(since)); err != nil {
		return 0, fmt.Errorf("reset tags: %w", err)
	}
	r, err := tx.Exec(`UPDATE documents SET classified_at = NULL, classifier = NULL, is_junk = 0
		WHERE `+isPost+` AND published_at >= ?`, formatTS(since))
	if err != nil {
		return 0, fmt.Errorf("reset classification: %w", err)
	}
	return r.RowsAffected()
}

// DedupPost is a post as seen by repost dedup.
type DedupPost struct {
	ID            int64
	Source        string
	ExternalID    string
	PublishedAt   time.Time
	Body          string
	ForwardedFrom string
	DedupHash     string // empty if not computed yet
	DupOf         int64  // 0 for canonical posts
}

// UndedupedRange is the publication range of posts without a dedup hash
// (new or edited); ok is false if there are none.
func (s *Store) UndedupedRange(ctx context.Context) (from, to time.Time, ok bool, err error) {
	var lo, hi sql.NullString
	err = s.read.QueryRowContext(ctx, `SELECT min(published_at), max(published_at) FROM documents
		WHERE `+isPost+` AND dedup_hash IS NULL`).Scan(&lo, &hi)
	if err != nil || !lo.Valid {
		return time.Time{}, time.Time{}, false, err
	}
	// Aggregates lose the column type: parse the stored text.
	if from, err = parseTS(lo.String); err != nil {
		return
	}
	if to, err = parseTS(hi.String); err != nil {
		return
	}
	return from, to, true, nil
}

// PostsPublished returns posts published in [from, to], oldest first.
func (s *Store) PostsPublished(ctx context.Context, from, to time.Time) ([]DedupPost, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id, source, external_id, published_at, body,
		coalesce(forwarded_from, ''), coalesce(dedup_hash, ''), coalesce(dup_of, 0)
		FROM documents WHERE `+isPost+` AND published_at >= ? AND published_at <= ?
		ORDER BY published_at, id`, formatTS(from), formatTS(to))
	if err != nil {
		return nil, fmt.Errorf("posts published: %w", err)
	}
	defer rows.Close()
	var out []DedupPost
	for rows.Next() {
		var p DedupPost
		if err := rows.Scan(&p.ID, &p.Source, &p.ExternalID, &p.PublishedAt, &p.Body, &p.ForwardedFrom, &p.DedupHash, &p.DupOf); err != nil {
			return nil, fmt.Errorf("posts published scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetDedup stores the dedup hash of a post and the canonical post it
// repeats (0 = canonical).
func SetDedup(tx *sql.Tx, id int64, hash string, dupOf int64) error {
	var dup any
	if dupOf != 0 {
		dup = dupOf
	}
	if _, err := tx.Exec(`UPDATE documents SET dedup_hash = ?, dup_of = ? WHERE id = ?`, hash, dup, id); err != nil {
		return fmt.Errorf("post %d dedup: %w", id, err)
	}
	return nil
}

// PostSourceStats is the post count of one channel or feed.
type PostSourceStats struct {
	Source   string
	Posts    int       // all stored
	Recent   int       // published since the given time
	LastPost time.Time // newest publication
}

// PostSources counts stored posts per channel and feed.
func (s *Store) PostSources(ctx context.Context, since time.Time) ([]PostSourceStats, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT source, count(*), coalesce(sum(published_at >= ?), 0), max(published_at)
		FROM documents WHERE `+isPost+` GROUP BY source ORDER BY source`, formatTS(since))
	if err != nil {
		return nil, fmt.Errorf("post sources: %w", err)
	}
	defer rows.Close()
	var out []PostSourceStats
	for rows.Next() {
		var p PostSourceStats
		var last string
		if err := rows.Scan(&p.Source, &p.Posts, &p.Recent, &last); err != nil {
			return nil, fmt.Errorf("post sources scan: %w", err)
		}
		if p.LastPost, err = parseTS(last); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClassifyCounts summarises classification of posts published since a time.
type ClassifyCounts struct {
	Gemini, Keywords, Junk, Uncategorized, Pending, Duplicates int
}

// PostClassifyCounts counts posts published since by classifier and outcome.
func (s *Store) PostClassifyCounts(ctx context.Context, since time.Time) (ClassifyCounts, error) {
	var c ClassifyCounts
	err := s.read.QueryRowContext(ctx, `SELECT
		coalesce(sum(classifier = 'gemini'), 0),
		coalesce(sum(classifier = 'keywords'), 0),
		coalesce(sum(classified_at IS NOT NULL AND is_junk), 0),
		coalesce(sum(classified_at IS NOT NULL AND NOT is_junk AND NOT EXISTS (SELECT 1 FROM document_tags t WHERE t.document_id = documents.id)), 0),
		coalesce(sum(classified_at IS NULL), 0),
		coalesce(sum(dup_of IS NOT NULL), 0)
		FROM documents WHERE `+isPost+` AND published_at >= ?`, formatTS(since)).
		Scan(&c.Gemini, &c.Keywords, &c.Junk, &c.Uncategorized, &c.Pending, &c.Duplicates)
	if err != nil {
		return ClassifyCounts{}, fmt.Errorf("classify counts: %w", err)
	}
	return c, nil
}

// WeekPost is a post with its classification and dedup state, for export.
type WeekPost struct {
	model.Document
	Classified bool
	DupOf      int64      // canonical post, 0 if canonical
	Tags       []TagScore // primary first, then by score
}

// PostsBetween returns posts published in [from, to), oldest first.
func (s *Store) PostsBetween(ctx context.Context, from, to time.Time) ([]WeekPost, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+documentColumns+`, classified_at IS NOT NULL, coalesce(dup_of, 0)
		FROM documents WHERE `+isPost+` AND published_at >= ? AND published_at < ? ORDER BY published_at, id`,
		formatTS(from), formatTS(to))
	if err != nil {
		return nil, fmt.Errorf("posts between: %w", err)
	}
	var out []WeekPost
	index := make(map[int64]int)
	for rows.Next() {
		var p WeekPost
		d, err := scanDocument(rowWithExtra{rows, []any{&p.Classified, &p.DupOf}})
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("posts between scan: %w", err)
		}
		p.Document = d
		index[d.ID] = len(out)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("posts between: %w", err)
	}
	tags, err := s.read.QueryContext(ctx, `SELECT t.document_id, t.tag, t.score, t.is_primary
		FROM document_tags t JOIN documents d ON d.id = t.document_id
		WHERE `+strings.ReplaceAll(isPost, "source", "d.source")+` AND d.published_at >= ? AND d.published_at < ?
		ORDER BY t.document_id, t.is_primary DESC, t.score DESC, t.tag`, formatTS(from), formatTS(to))
	if err != nil {
		return nil, fmt.Errorf("posts between tags: %w", err)
	}
	defer tags.Close()
	for tags.Next() {
		var id int64
		var t TagScore
		if err := tags.Scan(&id, &t.Tag, &t.Score, &t.Primary); err != nil {
			return nil, fmt.Errorf("posts between tags scan: %w", err)
		}
		if i, ok := index[id]; ok {
			out[i].Tags = append(out[i].Tags, t)
		}
	}
	return out, tags.Err()
}

// rowWithExtra scans documentColumns followed by extra columns.
type rowWithExtra struct {
	row   interface{ Scan(...any) error }
	extra []any
}

func (r rowWithExtra) Scan(dest ...any) error { return r.row.Scan(append(dest, r.extra...)...) }
