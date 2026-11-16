-- 0004: SEC EDGAR — XBRL company facts and earnings reports (USA Micro, phase 5).

-- XBRL facts as filed (levels, rule 8): quarterly, year-to-date, annual and
-- trailing-12-month durations alike; quarters are derived in transform.
-- One row per filing, so a value restated in a later 10-Q/10-K is a new row
-- with its own accn — the latest `filed` wins, older rows are the revision
-- history. fy / fp belong to the filing, not to the fact (LOOKUP E3).
-- period_start is '' for instant facts.
CREATE TABLE company_facts (
  ticker       TEXT NOT NULL,
  concept      TEXT NOT NULL,       -- us-gaap tag, e.g. PaymentsToAcquirePropertyPlantAndEquipment
  unit         TEXT NOT NULL,       -- USD | USD/shares
  period_start DATE NOT NULL,
  period_end   DATE NOT NULL,
  value        REAL NOT NULL,
  accn         TEXT NOT NULL,       -- accession number of the filing
  form         TEXT NOT NULL,       -- 10-Q | 10-K | ...
  filed        DATE NOT NULL,
  fy           INTEGER,
  fp           TEXT,                -- Q1 | Q2 | Q3 | FY
  frame        TEXT,                -- CY2026Q2, when SEC assigned one
  fetched_at   TIMESTAMP NOT NULL,
  PRIMARY KEY (ticker, concept, unit, period_start, period_end, accn)
);
CREATE INDEX idx_facts_period ON company_facts(ticker, concept, period_end);

-- company_reports (§7): one row per earnings 8-K (item 2.02; a TSLA 2.02 on
-- deliveries is not one, LOOKUP E2). Revenue, EPS, capex and the price
-- reaction are not stored here: they are computed from company_facts and
-- Yahoo prices (rule 8), so the §7 columns revenue / eps / capex /
-- guidance_txt / price_reaction_pct stay NULL. The press release text lives
-- in documents (source=edgar, doc_type=8-K, external_id=accn).
-- form = '8-K'; period_end = the quarter reported (from the 10-Q/10-K that
-- follows; until then the end of the last quarter before report_date).
ALTER TABLE company_reports ADD COLUMN accn TEXT;
ALTER TABLE company_reports ADD COLUMN accepted_at TIMESTAMP;   -- UTC, acceptanceDateTime
ALTER TABLE company_reports ADD COLUMN session TEXT;            -- amc | bmo | intraday
ALTER TABLE company_reports ADD COLUMN document_id INTEGER REFERENCES documents(id);
CREATE UNIQUE INDEX idx_reports_accn ON company_reports(accn);
CREATE INDEX idx_reports_accepted ON company_reports(accepted_at);
