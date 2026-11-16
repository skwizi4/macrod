-- 0015: publication days of manual series sent to the bot ("ifo даты
-- 24.11.2026", phase 14c, D29). They add to manual.series[].release_dates of
-- the config: the reminder comes on the day, staleness counts from it.
CREATE TABLE manual_release_dates (
  series_id  TEXT NOT NULL,
  date       DATE NOT NULL,
  added_at   TIMESTAMP NOT NULL,
  via        TEXT NOT NULL,      -- telegram | cli
  PRIMARY KEY (series_id, date)
);
