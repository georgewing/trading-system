# Phase 3: Web Trading Terminal

Status: vertical slice running end-to-end with real Binance market data.

## What runs

| Layer | Package | Notes |
|---|---|---|
| Domain contract | `internal/hft/domain` | Event/Kind, Source/Sink/Fanout/Snapshotter ports, MemFanout |
| Binance adapter | `internal/gateway/binance/market` | pure decoder + injectable `Dialer` (coder/websocket) |
| Snapshot adapter | `internal/hft/infra/memstore` | bounded depth/trades/klines read model |
| Order read model | `internal/orders` | submission + latest execution state |
| WS transport | `internal/transport/ws` | `/ws`, topics, seq, snapshots |
| Wire formatting | `internal/transport/wire` | int64 minimal units -> decimal strings |
| REST | `internal/transport/http` | depth, klines, trades, orders, meta |
| Terminal | `frontend/` | React + TS + lightweight-charts |

## Run

```bash
# backend (real Binance public streams, no API key)
SNOWFLAKE_WORKER_ID=1 GOCACHE=/tmp/go-build \
  go run ./cmd/hft-server -config ./configs/config.example.yaml

# frontend (proxies /api and /ws to :8090)
cd frontend && npm install && npm run dev
# open http://127.0.0.1:5173
```

Dev probes:

```bash
node scripts/dev/ws-probe.mjs                 # market data over /ws
node scripts/dev/e2e-order.mjs                # order -> exec report round trip
go run ./scripts/dev/feedcheck                # feed only, no server
```

## HTTP

```
GET  /api/v1/meta                          instrument + precision
GET  /api/v1/market/depth?symbol=&limit=
GET  /api/v1/market/kline?symbol=&interval=&limit=          live buffer
GET  /api/v1/market/kline/history?symbol=&from=&to=&limit=   ClickHouse, unix ms
GET  /api/v1/market/trades?symbol=&limit=
GET  /api/v1/positions?account_id=
GET  /api/v1/account?account_id=
GET  /api/v1/orders?limit=
GET  /api/v1/orders/{order_id}
POST /api/v1/order
POST /api/v1/order/cancel
POST /api/v1/order/cancel/batch
```

`/api/v1/market/kline/history` returns the same JSON shape as the live endpoint
and answers `503` when ClickHouse is disabled or the query fails; it never falls
back to the in-memory buffer, so the terminal cannot mistake stale data for
history. Only `interval=1m` is supported.

`POST /api/v1/order/cancel/batch` accepts up to 100 ids and reports per-order
outcomes; a partial failure still returns `200`, because cancels that already
reached the matcher cannot be rolled back:

```json
{"canceled":[{"order_id":"123","filled":0}],"failed":[{"order_id":"456","error":"order not found"}]}
```

`POST /api/v1/order` keeps the existing contract: `price` is integer ticks
(`price * 10^matcher.price_precision`), `quantity` is a decimal string. The
response is `202` with the assigned `order_id`; the outcome arrives on `/ws`.

## WebSocket

```
GET /ws
```

Client:

```json
{"op":"sub","topics":["depth:BTCUSDT","trade:BTCUSDT","kline:BTCUSDT:1m","order"],"last_seq":0}
{"op":"unsub","topics":["trade:BTCUSDT"]}
{"op":"ping","ts":1700000000000}
```

Server envelope:

```json
{"v":1,"seq":42,"ts":1700000000000,"type":"market.depth","symbol":"BTCUSDT","data":{}}
```

Types: `system.hello`, `system.pong`, `system.error`, `market.depth`,
`market.trade`, `market.trade.snapshot`, `market.kline`,
`market.kline.snapshot`, `order.exec`.

### Sequence and resync

`seq` is monotonic per connection.

- Stateful topics (`depth`, `kline`) answer a subscribe with a **full snapshot**.
- On reconnect the client sends `last_seq`. The server cannot replay events, so
  it re-sends snapshots and emits `system.error{code:"resync"}`.
- The client re-subscribes at most once per second, so a lossy link cannot turn
  into a resubscribe storm.

## Data conventions

- Market prices/quantities travel as **decimal strings**, exactly as the venue
  published them. Nothing parses them into float on the way in.
- Order prices travel as **int64 ticks**; order ids are **strings**, because a
  64-bit snowflake id does not survive a JS number.
- Times are Unix milliseconds.

## Frontend notes

- `src/store/createStore.ts` is a tiny `useSyncExternalStore` store. High-rate
  updates never go through React state directly.
- `src/api/ws.ts` batches one animation frame worth of messages into a single
  commit; a 100ms depth stream plus a busy tape would otherwise re-render per
  message.
- `lightweight-charts` is used for candles; ECharts was rejected as ~20x the
  bundle for this use case.

## Known gaps

1. **Execution is internal.** Orders match against the in-process paper book
   unless a live venue is configured. Depth and candles are real; paper fills
   are simulated.
2. **Single node.** `ws.Hub`, the market store and the position ledger are
   process-local. The k8s manifest asks for 10 replicas, which would need
   sticky sessions or a shared bus.
3. **Depth is a snapshot stream.** Binance's `depth20@100ms` already sends full
   top-20 snapshots, so no delta reconstruction is needed.
4. **Historical klines need ClickHouse.** The materialized view only covers
   ticks written after it was created; existing ticks are not backfilled.
5. **Precision matters for notional math.** `price_precision + quantity_precision`
   at 8 + 8 overflows int64 when multiplying ticks by lots. Keep the sum small
   (the risk E2E config uses 2 + 6).

## Architecture: ports and adapters

Market data follows a ports/adapters split, so the ingest path can change venue
or storage without touching the transports:

- **Ports** (`internal/hft/domain`): `Source` (streams events), `Fanout`
  (broadcast), `Snapshotter` (state), `Sink` (persistence).
- **Adapters**: `infra/binance` implements `Source` and owns decoding plus the
  injectable `Dialer`; `infra/memstore` implements `Snapshotter`; the WS and
  HTTP transports are pure adapters over those ports.
- **Wiring** (`internal/app`): a Binance source feeds the memstore and the
  fan-out from one emit callback, so the read model and the live stream can
  never disagree about what arrived.

Price representation is deliberately split:

- **Inside** the domain, prices and quantities are `int64` in the instrument's
  minimal unit. This matches the matching engine and makes arithmetic exact.
- **On the wire**, `internal/transport/wire.Decimal` renders those integers as
  decimal strings. No `float64` is involved anywhere on the market-data path.

Earlier in this branch there were two competing market-data packages
(`internal/market` + `internal/gateway/binance` alongside `internal/hft/...`).
They have been consolidated onto this one: the duplicate packages are gone, and
their useful regression tests moved into `infra/binance`.
