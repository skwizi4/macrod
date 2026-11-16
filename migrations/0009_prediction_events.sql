-- 0009: prediction markets grouped by question (FIXES_W44 G4a). event is the
-- Kalshi event ticker or the Polymarket event slug of a market, question
-- the event title; the label is "<question> — <outcome>". NULL elsewhere.
ALTER TABLE series_meta ADD COLUMN event TEXT;
ALTER TABLE series_meta ADD COLUMN question TEXT;
