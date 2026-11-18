BEGIN;

CREATE TABLE IF NOT EXISTS risk_events (
    id         BIGSERIAL PRIMARY KEY,
    ts         TIMESTAMPTZ NOT NULL,
    account_id BIGINT NOT NULL DEFAULT 0 CHECK (account_id >= 0),
    symbol     TEXT NOT NULL DEFAULT '',
    strategy   TEXT NOT NULL DEFAULT '',
    order_id   BIGINT NOT NULL DEFAULT 0 CHECK (order_id >= 0),
    code       TEXT NOT NULL CHECK (code <> ''),
    action     TEXT NOT NULL CHECK (action <> ''),
    message    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_risk_events_ts
    ON risk_events (ts DESC);

CREATE INDEX IF NOT EXISTS idx_risk_events_account_ts
    ON risk_events (account_id, ts DESC);

CREATE INDEX IF NOT EXISTS idx_risk_events_code_ts
    ON risk_events (code, ts DESC);

COMMIT;
