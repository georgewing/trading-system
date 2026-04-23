CREATE DATABASE IF NOT EXISTS market;

CREATE TABLE IF NOT EXISTS market.trade_ticks
(
    ts            DateTime64(3, 'Asia/Shanghai'),
    symbol        LowCardinality(String),
    trade_id      UInt64,
    price         Int64,
    quantity      Int64,
    buy_order_id  UInt64,
    sell_order_id UInt64
    )
    ENGINE = MergeTree
    PARTITION BY toYYYYMM(ts)
    ORDER BY (symbol, ts, trade_id)
    SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS market.kline_1m_agg
(
    bucket         DateTime('Asia/Shanghai'),
    symbol         LowCardinality(String),
    open_state     AggregateFunction(argMin, Int64, DateTime64(3, 'Asia/Shanghai')),
    high_state     AggregateFunction(max, Int64),
    low_state      AggregateFunction(min, Int64),
    close_state    AggregateFunction(argMax, Int64, DateTime64(3, 'Asia/Shanghai')),
    volume_state   AggregateFunction(sum, Int64),
    turnover_state AggregateFunction(sum, Int128),
    trades_state   AggregateFunction(count)
    )
    ENGINE = AggregatingMergeTree
    PARTITION BY toYYYYMM(bucket)
    ORDER BY (symbol, bucket);

CREATE MATERIALIZED VIEW IF NOT EXISTS market.mv_trade_ticks_to_kline_1m
TO market.kline_1m_agg
AS
SELECT
    toStartOfMinute(ts) AS bucket,
    symbol,
    argMinState(price, ts) AS open_state,
    maxState(price) AS high_state,
    minState(price) AS low_state,
    argMaxState(price, ts) AS close_state,
    sumState(quantity) AS volume_state,
    sumState(toInt128(price) * toInt128(quantity)) AS turnover_state,
    countState() AS trades_state
FROM market.trade_ticks
GROUP BY bucket, symbol;

CREATE VIEW IF NOT EXISTS market.kline_1m AS
SELECT
    bucket AS open_time,
    symbol,
    argMinMerge(open_state) AS open,
    maxMerge(high_state) AS high,
    minMerge(low_state) AS low,
    argMaxMerge(close_state) AS close,
    sumMerge(volume_state) AS volume,
    sumMerge(turnover_state) AS turnover,
    countMerge(trades_state) AS trades
FROM market.kline_1m_agg
GROUP BY open_time, symbol;
