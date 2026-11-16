-- 0010: a Telegram post has a photo (news cleanup: an image with a short
-- caption is not exported). NULL = unknown, for posts fetched before v10
-- until the views refresh re-reads their page.
ALTER TABLE documents ADD COLUMN has_photo INTEGER;
