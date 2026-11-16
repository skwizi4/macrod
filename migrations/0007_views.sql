-- 0007: Telegram post views (FIXES_W44 E5). views is the counter of t.me/s
-- at views_at: set when the post is fetched and re-read before the weekly
-- export. NULL for RSS and other documents.
ALTER TABLE documents ADD COLUMN views INTEGER;
ALTER TABLE documents ADD COLUMN views_at TIMESTAMP;
