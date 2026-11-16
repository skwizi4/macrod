-- 0001: initial schema (ТЗ §7 + revisions from §17.3).
-- Dates are TEXT 'YYYY-MM-DD'; timestamps are TEXT RFC 3339 in UTC.

CREATE TABLE series_meta (
  source      TEXT NOT NULL,       -- fred | yahoo | treasury | ecb | eu_bonds | eu_surveys | cftc | derived
  series_id   TEXT NOT NULL,
  label       TEXT NOT NULL,
  agent       TEXT NOT NULL,
  frequency   TEXT,                -- d | w | m | q
  units       TEXT,
  release_id  TEXT,                -- FRED release id (phase 2)
  PRIMARY KEY (source, series_id)
);

CREATE TABLE observations (
  source     TEXT NOT NULL,
  series_id  TEXT NOT NULL,
  obs_date   DATE NOT NULL,
  value      REAL,
  fetched_at TIMESTAMP NOT NULL,   -- first fetch, or last revision
  PRIMARY KEY (source, series_id, obs_date)
);

-- §17.3: every value change of an existing obs_date.
CREATE TABLE revisions (
  id          INTEGER PRIMARY KEY,
  source      TEXT NOT NULL,
  series_id   TEXT NOT NULL,
  obs_date    DATE NOT NULL,
  old_value   REAL,
  new_value   REAL,
  detected_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_revisions_series ON revisions(source, series_id, obs_date);

CREATE TABLE releases (
  id            INTEGER PRIMARY KEY,
  source        TEXT NOT NULL,     -- fred_release | fomc | ecb | treasury_auction | ifo | zew | earnings | ...
  name          TEXT NOT NULL,
  scheduled_at  TIMESTAMP NOT NULL,
  region        TEXT NOT NULL,     -- usa | eu
  importance    TEXT,              -- high | medium | low
  related       TEXT,              -- JSON: series_id / ticker / security term
  status        TEXT NOT NULL DEFAULT 'scheduled',  -- scheduled | polling | done | timeout | rescheduled
  done_at       TIMESTAMP,
  UNIQUE (source, name, scheduled_at)
);

CREATE TABLE documents (
  id           INTEGER PRIMARY KEY,
  source       TEXT NOT NULL,      -- fed | ecb | telegram:<handle> | rss:<name> | edgar | euir | treasury
  external_id  TEXT NOT NULL,
  doc_type     TEXT,
  region       TEXT,
  published_at TIMESTAMP NOT NULL,
  author       TEXT,
  title        TEXT,
  body         TEXT NOT NULL,
  url          TEXT,
  content_hash TEXT NOT NULL,
  is_opinion   BOOLEAN NOT NULL DEFAULT 0,
  is_junk      BOOLEAN NOT NULL DEFAULT 0,
  fetched_at   TIMESTAMP NOT NULL,
  UNIQUE (source, external_id)
);
CREATE INDEX idx_docs_hash ON documents(content_hash);
CREATE INDEX idx_docs_pub  ON documents(published_at);

CREATE TABLE document_tags (
  document_id INTEGER NOT NULL REFERENCES documents(id),
  tag         TEXT NOT NULL,
  score       REAL NOT NULL,
  is_primary  BOOLEAN NOT NULL,
  PRIMARY KEY (document_id, tag)
);

CREATE TABLE auctions (
  id             INTEGER PRIMARY KEY,
  auction_date   DATE NOT NULL,
  security_type  TEXT NOT NULL,
  term           TEXT NOT NULL,
  cusip          TEXT,
  offering_amt   REAL,
  high_yield     REAL,
  wi_yield       REAL,
  bid_to_cover   REAL,
  indirect_pct   REAL,
  direct_pct     REAL,
  dealer_pct     REAL,
  UNIQUE (auction_date, cusip)
);

CREATE TABLE company_reports (
  id           INTEGER PRIMARY KEY,
  ticker       TEXT NOT NULL,
  period_end   DATE NOT NULL,
  form         TEXT,
  revenue      REAL,
  eps          REAL,
  capex        REAL,
  guidance_txt TEXT,
  report_date  DATE,
  price_reaction_pct REAL,
  UNIQUE (ticker, period_end, form)
);

-- last_error / last_error_at: addition to §7 for `macroctl status`.
CREATE TABLE fetch_state (
  source        TEXT PRIMARY KEY,
  cursor        TEXT,
  updated_at    TIMESTAMP NOT NULL,
  last_error    TEXT,
  last_error_at TIMESTAMP
);

CREATE TABLE exports (
  id         INTEGER PRIMARY KEY,
  iso_week   TEXT NOT NULL,
  kind       TEXT NOT NULL,        -- weekly | event_fomc | event_ecb
  region     TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
  git_sha    TEXT
);

-- phase 8
CREATE TABLE forecasts (
  id            INTEGER PRIMARY KEY,
  iso_week      TEXT NOT NULL,
  instrument    TEXT NOT NULL,
  direction     TEXT NOT NULL,     -- up | down | range
  invalidation  REAL,
  ref_price     REAL,
  result_price  REAL,
  outcome       TEXT,              -- hit | miss | invalidated | pending
  UNIQUE (iso_week, instrument)
);
