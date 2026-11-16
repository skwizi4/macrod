-- 0014: report dates read from issuers' official financial calendar pages
-- (phase 14c, D29). One row per issuer and period; a moved date replaces
-- the row. The export merges them with issuers[].report_dates of the
-- config: a confirmed config date wins, a calendar date beats an estimate.
CREATE TABLE issuer_report_dates (
  ticker      TEXT NOT NULL,
  period      TEXT NOT NULL,      -- "Q1 FY2027", "FY 2026" (as in report_dates)
  date        DATE NOT NULL,
  title       TEXT NOT NULL,      -- the event text on the page
  url         TEXT NOT NULL,      -- the calendar page
  fetched_at  TIMESTAMP NOT NULL, -- last seen on the page
  changed_at  TIMESTAMP NOT NULL, -- first seen with this date
  PRIMARY KEY (ticker, period)
);
