-- 0013: semi-automatic capex guidance (phase 14b, ТЗ §21.6 п. 3, D15, D28).
-- A candidate is a sentence of a call transcript or an earnings 8-K with a
-- capex amount and a year; it is never used as a number until the user
-- approves it. An approval adds a row to capex_guidance, which the export
-- merges with edgar.capex_guidance of config.yaml (newer given wins).
CREATE TABLE guidance_candidates (
  id             INTEGER PRIMARY KEY,
  ticker         TEXT NOT NULL,
  year           INTEGER NOT NULL,
  low            REAL NOT NULL,      -- billions of dollars
  high           REAL NOT NULL,
  quote          TEXT NOT NULL,      -- the whole sentence
  url            TEXT NOT NULL,
  doc_source     TEXT NOT NULL,      -- transcript | edgar (documents.source)
  doc_ref        TEXT NOT NULL,      -- accession of the 8-K (the transcript is keyed by it too)
  doc_title      TEXT NOT NULL,
  given          DATE NOT NULL,      -- New York date of the document
  note           TEXT,               -- differs from / confirms the approved value
  status         TEXT NOT NULL DEFAULT 'pending', -- pending | approved | rejected
  found_at       TIMESTAMP NOT NULL,
  decided_at     TIMESTAMP,
  decided_via    TEXT,               -- cli | telegram
  tg_message_id  INTEGER,
  UNIQUE (ticker, year, low, high, doc_source, doc_ref)
);

CREATE TABLE capex_guidance (
  id            INTEGER PRIMARY KEY,
  ticker        TEXT NOT NULL,
  year          INTEGER NOT NULL,
  low           REAL NOT NULL,
  high          REAL NOT NULL,
  given         DATE NOT NULL,
  source        TEXT NOT NULL,
  candidate_id  INTEGER NOT NULL UNIQUE REFERENCES guidance_candidates(id),
  approved_at   TIMESTAMP NOT NULL
);
