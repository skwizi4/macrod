-- 0005: when an observation was first stored (point-in-time exports, §17.1).
-- fetched_at moves with every revision, so a past week cannot tell whether
-- a revised value existed by its end. first_fetched_at is set once, on insert.
-- NULL (rows written by an older binary) reads as fetched_at.

ALTER TABLE observations ADD COLUMN first_fetched_at TIMESTAMP;

UPDATE observations SET first_fetched_at = fetched_at
WHERE NOT EXISTS (
  SELECT 1 FROM revisions r
  WHERE r.source = observations.source AND r.series_id = observations.series_id AND r.obs_date = observations.obs_date
);

-- Revised rows: the first fetch is lost; the earliest revision is the
-- closest known bound (the row existed by then).
UPDATE observations SET first_fetched_at = (
  SELECT min(r.detected_at) FROM revisions r
  WHERE r.source = observations.source AND r.series_id = observations.series_id AND r.obs_date = observations.obs_date
)
WHERE first_fetched_at IS NULL;
