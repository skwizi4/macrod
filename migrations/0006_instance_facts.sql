-- 0006: facts read from the XBRL instance of a 10-Q/10-K (phase 5d, ТЗ §20,
-- DECISIONS D15): company segments and totals that company facts leave out
-- (AMZN's RPO is filed only with the expected-timing axis). member is the
-- local name of the segment member (AmazonWebServicesSegmentMember); '' is
-- a company total. Like company_facts, a row per filing: a restated value
-- is a new row.
CREATE TABLE instance_facts (
  ticker       TEXT NOT NULL,
  member       TEXT NOT NULL,
  concept      TEXT NOT NULL,
  unit         TEXT NOT NULL,
  period_start DATE NOT NULL,   -- '' for a balance sheet date
  period_end   DATE NOT NULL,
  value        REAL NOT NULL,
  accn         TEXT NOT NULL,
  form         TEXT NOT NULL,
  filed        DATE NOT NULL,
  fetched_at   TIMESTAMP NOT NULL,
  PRIMARY KEY (ticker, member, concept, unit, period_start, period_end, accn)
);
