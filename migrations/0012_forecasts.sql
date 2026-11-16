-- 0012: forecast log (phase 13). The table of 0001 was never written; it is
-- rebuilt with the region (a master per region), the source report and the
-- scoring details. One row per region, report week and instrument; iso_week
-- is the week of the report, the forecast is for the next trading week.
DROP TABLE forecasts;

CREATE TABLE forecasts (
  id            INTEGER PRIMARY KEY,
  region        TEXT NOT NULL,
  iso_week      TEXT NOT NULL,
  instrument    TEXT NOT NULL,
  direction     TEXT NOT NULL,     -- up | down | range
  invalidation  REAL NOT NULL,
  source_file   TEXT NOT NULL,     -- reports/<region>/<week>/<agent>.md
  imported_at   TIMESTAMP NOT NULL,
  price_series  TEXT,              -- source/series the outcome was priced on
  ref_date      DATE,
  ref_price     REAL,
  result_date   DATE,
  result_price  REAL,
  change_pct    REAL,
  outcome       TEXT NOT NULL DEFAULT 'pending', -- hit | miss | invalidated | pending
  scored_at     TIMESTAMP,
  UNIQUE (region, iso_week, instrument)
);
