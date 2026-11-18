BEGIN;

CREATE TABLE IF NOT EXISTS positions (
    account_id BIGINT NOT NULL,
    symbol     TEXT NOT NULL,
    qty        BIGINT NOT NULL DEFAULT 0,
    avg_cost   BIGINT NOT NULL DEFAULT 0,
    mark       BIGINT NOT NULL DEFAULT 0,
    realized   BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, symbol)
);

CREATE INDEX IF NOT EXISTS idx_positions_symbol
    ON positions (symbol);

COMMIT;
