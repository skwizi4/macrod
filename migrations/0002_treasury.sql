-- 0002: Treasury auction details (TreasuryDirect TA_WS) and buybacks (Fiscal Data).

-- auctions.term is the security term as auctioned ("19-Year 10-Month" for a
-- reopening); original_term is the term of the original issue ("20-Year"),
-- used to group auctions of the same tenor. wi_yield stays NULL: no free
-- source publishes the when-issued yield (LOOKUP T5).
ALTER TABLE auctions ADD COLUMN original_term TEXT;
ALTER TABLE auctions ADD COLUMN kind TEXT;                  -- Bill | Note | Bond | TIPS | FRN | CMB
ALTER TABLE auctions ADD COLUMN reopening BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE auctions ADD COLUMN closing_time TEXT;          -- competitive close, "HH:MM" ET
ALTER TABLE auctions ADD COLUMN issue_date DATE;
ALTER TABLE auctions ADD COLUMN maturity_date DATE;
ALTER TABLE auctions ADD COLUMN high_investment_rate REAL;  -- bills: bond-equivalent rate, %
ALTER TABLE auctions ADD COLUMN low_yield REAL;
ALTER TABLE auctions ADD COLUMN median_yield REAL;
ALTER TABLE auctions ADD COLUMN total_accepted REAL;        -- dollars
ALTER TABLE auctions ADD COLUMN comp_accepted REAL;         -- dollars; base of the *_pct shares
ALTER TABLE auctions ADD COLUMN updated_at TIMESTAMP;       -- TA_WS updatedTimestamp
ALTER TABLE auctions ADD COLUMN fetched_at TIMESTAMP;

CREATE INDEX idx_auctions_term ON auctions(original_term, kind, auction_date);

CREATE TABLE buybacks (
  operation_date  DATE NOT NULL,
  start_time      TEXT NOT NULL,      -- "HH:MM" ET
  operation_type  TEXT,               -- Liquidity Support | Cash Management
  security_type   TEXT NOT NULL,      -- Nominal Coupons | TIPS
  maturity_bucket TEXT NOT NULL,
  par_offered     REAL,               -- dollars
  par_accepted    REAL,               -- dollars
  max_redeemed    REAL,               -- dollars
  fetched_at      TIMESTAMP NOT NULL,
  PRIMARY KEY (operation_date, start_time, security_type, maturity_bucket)
);
