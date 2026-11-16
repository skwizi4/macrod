-- 0008: one-minute bars around high-importance events (FIXES_W44 F1). A short
-- history: rows older than sentiment.reaction.keep_days are deleted on write.
-- ts is the bar start, UTC, RFC3339.
CREATE TABLE intraday_bars (
    symbol TEXT NOT NULL,
    ts     TIMESTAMP NOT NULL,
    close  REAL NOT NULL,
    PRIMARY KEY (symbol, ts)
);
