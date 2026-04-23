BEGIN;

CREATE TABLE IF NOT EXISTS orders (
                                      id              BIGINT PRIMARY KEY CHECK (id >= 0),
    client_order_id TEXT,
    account_id      BIGINT NOT NULL DEFAULT 0 CHECK (account_id >= 0),
    symbol          TEXT NOT NULL,
    side            SMALLINT NOT NULL CHECK (side IN (1, -1)),
    order_type      TEXT NOT NULL CHECK (order_type IN ('LIMIT', 'MARKET')),
    time_in_force   TEXT NOT NULL CHECK (time_in_force IN ('GTC', 'IOC', 'FOK')),
    price           BIGINT NOT NULL CHECK (price > 0),
    quantity        NUMERIC(38, 18) NOT NULL CHECK (quantity > 0),
    leaves          BIGINT NOT NULL DEFAULT 0 CHECK (leaves >= 0),
    filled          BIGINT NOT NULL DEFAULT 0 CHECK (filled >= 0),
    status          SMALLINT NOT NULL CHECK (status BETWEEN 1 AND 5),
    hidden          BOOLEAN NOT NULL DEFAULT FALSE,
    post_only       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );

CREATE UNIQUE INDEX IF NOT EXISTS uq_orders_account_client_oid
    ON orders (account_id, client_order_id)
    WHERE client_order_id IS NOT NULL AND client_order_id <> '';

CREATE INDEX IF NOT EXISTS idx_orders_symbol_created_at
    ON orders (symbol, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_orders_account_created_at
    ON orders (account_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_orders_status_updated_at
    ON orders (status, updated_at DESC);


CREATE TABLE IF NOT EXISTS trades (
                                      id            BIGINT PRIMARY KEY CHECK (id >= 0),
    symbol        TEXT NOT NULL,
    price         BIGINT NOT NULL CHECK (price > 0),
    quantity      BIGINT NOT NULL CHECK (quantity > 0),
    buy_order_id  BIGINT NOT NULL CHECK (buy_order_id >= 0),
    sell_order_id BIGINT NOT NULL CHECK (sell_order_id >= 0),
    maker_order_id BIGINT CHECK (maker_order_id >= 0),
    traded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );

CREATE INDEX IF NOT EXISTS idx_trades_symbol_traded_at
    ON trades (symbol, traded_at DESC);

CREATE INDEX IF NOT EXISTS idx_trades_buy_order_id
    ON trades (buy_order_id);

CREATE INDEX IF NOT EXISTS idx_trades_sell_order_id
    ON trades (sell_order_id);


CREATE TABLE IF NOT EXISTS execution_reports (
                                                 id          BIGSERIAL PRIMARY KEY,
                                                 order_id    BIGINT NOT NULL CHECK (order_id >= 0),
    status      SMALLINT NOT NULL CHECK (status BETWEEN 1 AND 5),
    filled      BIGINT NOT NULL CHECK (filled >= 0),
    leaves      BIGINT NOT NULL CHECK (leaves >= 0),
    trade_id    BIGINT CHECK (trade_id >= 0),
    reason      TEXT NOT NULL DEFAULT '',
    reported_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );

CREATE INDEX IF NOT EXISTS idx_exec_reports_order_time
    ON execution_reports (order_id, reported_at DESC);

CREATE INDEX IF NOT EXISTS idx_exec_reports_trade_id
    ON execution_reports (trade_id);

COMMIT;
