-- 0011: auctions of more than one issuer (phase 11, Bund auctions of the
-- Finanzagentur). source is the loader (treasury | finanzagentur), region
-- the region of its agent; the Treasury rows keep source = 'treasury' and
-- no region. cusip holds the ISIN of a Bund issue.
ALTER TABLE auctions ADD COLUMN source TEXT NOT NULL DEFAULT 'treasury';
ALTER TABLE auctions ADD COLUMN region TEXT;
ALTER TABLE auctions ADD COLUMN avg_yield REAL;      -- weighted average yield, %
ALTER TABLE auctions ADD COLUMN bids_amt REAL;       -- all bids, in the currency
ALTER TABLE auctions ADD COLUMN retention_amt REAL;  -- kept by the issuer, in the currency
ALTER TABLE auctions ADD COLUMN coupon REAL;         -- %
ALTER TABLE auctions ADD COLUMN issue_process TEXT;  -- Auc | M-A (multi-ISIN)

CREATE INDEX idx_auctions_source ON auctions(source, original_term, kind, auction_date);
