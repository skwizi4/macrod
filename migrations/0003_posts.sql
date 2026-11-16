-- 0003: Telegram/RSS posts — classification (§11, D14) and repost dedup.

-- forwarded_from: the original post of a Telegram forward, "<handle>/<id>"
-- (LOOKUP G1). dedup_hash: hash of the normalised text (no links, emoji,
-- whitespace); dup_of: the canonical post this one repeats, NULL for
-- canonical posts and for posts not checked yet.
ALTER TABLE documents ADD COLUMN forwarded_from TEXT;
ALTER TABLE documents ADD COLUMN dedup_hash TEXT;
ALTER TABLE documents ADD COLUMN dup_of INTEGER REFERENCES documents(id);
CREATE INDEX idx_docs_dedup ON documents(dedup_hash, published_at);
CREATE INDEX idx_docs_dup ON documents(dup_of);

-- classified_at / classifier mark a post as processed, including posts that
-- got no tags (uncategorized) or were marked junk.
ALTER TABLE documents ADD COLUMN classified_at TIMESTAMP;
ALTER TABLE documents ADD COLUMN classifier TEXT;          -- gemini | keywords
CREATE INDEX idx_docs_unclassified ON documents(source, classified_at);

ALTER TABLE document_tags ADD COLUMN classifier TEXT NOT NULL DEFAULT 'keywords';  -- gemini | keywords

-- Gemini answers by content hash, so a repeated text (re-fetch, repost,
-- edit back) does not spend quota. prompt_hash ties an answer to the prompt
-- and model it came from; a changed prompt makes old answers stale.
CREATE TABLE classify_cache (
  content_hash TEXT PRIMARY KEY,
  model        TEXT NOT NULL,
  prompt_hash  TEXT NOT NULL,
  result_json  TEXT NOT NULL,       -- {tags:[{tag,confidence}], primary, is_junk, reason}
  created_at   TIMESTAMP NOT NULL
);
