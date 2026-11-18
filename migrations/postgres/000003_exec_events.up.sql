BEGIN;

CREATE TABLE IF NOT EXISTS exec_events (
    exec_id         BIGINT PRIMARY KEY,
    seq             BIGINT NOT NULL,
    order_id        BIGINT NOT NULL,
    client_order_id TEXT,
    account_id      BIGINT NOT NULL,
    symbol          TEXT NOT NULL,
    side            SMALLINT NOT NULL,
    exec_type       TEXT NOT NULL,
    status          SMALLINT NOT NULL,
    last_px         BIGINT NOT NULL,
    last_qty        BIGINT NOT NULL,
    cum_qty         BIGINT NOT NULL,
    leaves_qty      BIGINT NOT NULL,
    liquidity       TEXT NOT NULL,
    trade_id        BIGINT,
    reason          TEXT NOT NULL DEFAULT '',
    venue           TEXT NOT NULL DEFAULT '',
    reported_at     TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_exec_events_order_seq
    ON exec_events (order_id, seq);

CREATE INDEX IF NOT EXISTS idx_exec_events_order_reported_at
    ON exec_events (order_id, reported_at DESC);

CREATE INDEX IF NOT EXISTS idx_exec_events_symbol_reported_at
    ON exec_events (symbol, reported_at DESC);

CREATE INDEX IF NOT EXISTS idx_exec_events_trade_id
    ON exec_events (trade_id);

COMMIT;
