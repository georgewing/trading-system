# Phase 4: Backtest and Risk Enhancements

This document describes the strategy backtest framework and the enhanced risk
stage added in phase 4. It records the assumptions a report depends on, the
configuration surface, and the operational follow-ups.

## Scope of this phase

Delivered:

- strategy contract shared by backtest and live execution
  (`internal/strategy`);
- live single-symbol strategy runtime (`internal/live`) driven by the
  market-data fanout and submitting through the matching pipeline;
- deterministic historical replay and simulated execution
  (`internal/backtest`), with JSONL and ClickHouse trade sources;
- fee, slippage and fill-probability models;
- market-making and statistical-arbitrage sample strategies;
- performance reporting (equity, drawdown, Sharpe, fill rate, win rate,
  turnover, exposure);
- intraday risk monitoring with per-strategy breakers and a position ledger
  (`internal/hft/risk`);
- strategy-tagged orders and execution reports, with per-strategy PnL
  attribution and `strategy_state` metrics;
- risk events published to logs, Prometheus, WebSocket and Postgres, plus
  `GET /api/v1/risk/events`;
- Web terminal Risk panel fed by the WS `risk` topic and the REST endpoint;
- `risk_events` Postgres migration.

Not in this phase:

- multi-symbol live strategies. `stat_arb` remains backtest-only because the
  gateway matches a single symbol;
- persisting per-strategy PnL attribution. Account positions are persisted and
  restored, but restored positions are attributed to `default`;
- depth-based historical replay. The ClickHouse source replays trades only; a
  strategy that needs a book needs a depth table first;
- breakers are scoped per `(account, strategy)` name, not per strategy instance
  or version.

## Units

Everything uses the matching engine's integer units:

| Concept | Unit |
| --- | --- |
| price | integer ticks (`matcher.price_precision`) |
| quantity | integer lots (`matcher.quantity_precision`) |
| cash, PnL, fees | ticks * lots |
| fees | basis points of notional |
| timestamps | unix milliseconds |

No float crosses a data or strategy boundary. The only float use is in report
statistics (Sharpe, turnover ratio), which are outputs, not inputs.

## Backtest

### Run

```sh
GOCACHE=/tmp/go-build go run ./cmd/backtest \
  -config ./configs/backtest.example.yaml
```

The command writes JSON and Markdown reports to the paths in the config and logs
a one-line summary.

### Data format

One JSON object per line, monotonically non-decreasing `ts`:

```json
{"ts":1700000000000,"symbol":"BTCUSDT","kind":"book","book":{"bids":[[10000,300000000]],"asks":[[10001,300000000]]}}
{"ts":1700000030000,"symbol":"BTCUSDT","kind":"trade","trade":{"id":1,"price":9997,"qty":100000000}}
```

A sample file lives at `internal/backtest/testdata/sample.jsonl`.

### ClickHouse replay

`backtest.OpenClickHouseSource(ctx, backtest.ClickHouseConfig{...})` implements
`Source` over the Tick/成交 table (`market.trade_ticks` by default). It streams
`FORMAT JSONEachRow` rows, converts `ts` with `toUnixTimestamp64Milli`, and
emits `KindTrade` events. Database and table identifiers are validated, the
symbol is escaped, and the HTTP call supports basic auth and timeouts.

This source replays trades only, so a book-driven strategy will not see depth
from it. Use JSONL fixtures with `kind: "book"` for book-based tests.

### Simulation model

Aggressive orders sweep the book level by level, capped by
`participation_bps`, with `slippage_bps` applied to the fill price and clamped
to the limit price.

Resting orders fill from two sources:

- a trade print that strictly crosses the resting price fills it;
- a trade print exactly at the resting price fills with probability
  `participation_bps * (10000 - queue_position_bps) / 10000`;
- a book update that makes the order marketable fills it at the opposite best
  price.

`participation_bps = 0` means unlimited participation. `queue_position_bps = 0`
is the front of the queue, `10000` is the back (touch never fills).

### Assumptions

Reports carry these strings in an `assumptions` field. Do not treat a backtest
as a live-trading guarantee:

- fills come from public trades and book moves, not from other participants'
  order flow;
- queue position at the touch is a probability, not a real queue;
- submission-to-match latency is zero;
- no exchange rejects, throttles or maintenance windows are modelled;
- fees are charged in notional units and rounded down.

### Determinism

The same data file plus the same `seed` produces an identical report. All
timestamps come from the event stream; the simulator never reads the wall
clock. The fill-probability generator is seeded per simulation and driven in
order-id creation order, so map iteration order cannot change results.

### Strategy contract

`internal/strategy.Strategy` receives:

- `OnBook(BookView)`: a depth snapshot;
- `OnTrade(TradeView)`: a public print;
- `OnTimer(now)`: a replay clock tick.

`Context` exposes `Position`, `Book`, `Submit`, `Cancel`, `OpenOrders` and
`Now`. The runner enforces per-strategy limits (`max_order_rate`,
`max_open_orders`, `max_panics`) and isolates panics. A strategy that panics
`max_panics` times is paused; callbacks stop until `Resume`.

Sample strategies:

- `noop`: baseline;
- `market_maker`: symmetric quotes around the mid, re-quoted on mid moves,
  inventory-capped;
- `stat_arb`: z-score of a two-symbol spread, entry/exit thresholds.

`stat_arb` needs two symbols, which the live gateway does not support yet (it
rejects symbols other than `matcher.symbol`). It is a backtest strategy today.

### Live runtime

`internal/live` drives the same `Strategy` contract from the live feed:

- it subscribes to `depth:<symbol>`, `tick:<symbol>` and `trade:<symbol>` on the
  market fanout and seeds its book from the snapshot cache;
- every strategy order is turned into an engine order tagged with the strategy
  name, submitted through the `EventBus`, and risk-checked like any other order;
- `OpenOrders` is scoped to `(account, strategy, symbol)`, so two strategies on
  one account cannot cancel each other's quotes;
- `OnTimer` runs on the configured `strategy.timer_seconds`.

Enable it with the `strategy` block in `configs/config.example.yaml`. Live
strategies require `risk.enabled` (position ledger and breakers) and
`market.enabled` (book input). Only `market_maker` and `noop` are accepted;
`stat_arb` fails startup with an explicit error.

### Report fields

`final_equity`, `realized_pnl`, `unrealized_pnl`, `fees`, `max_drawdown_bps`,
`sharpe`, `orders_submitted`, `orders_filled`, `orders_canceled`, `fills`,
`fill_rate_bps`, `win_rate_bps`, `turnover_bps`, `max_gross_exposure`, plus a
bar-downsampled `equity_curve`.

Metric definitions:

- realized PnL uses average-cost accounting on closing quantity, gross of fees;
- unrealized PnL is `qty * (mark - avg_cost)`;
- max drawdown is peak-to-trough on the bar equity curve;
- Sharpe is mean/std of bar returns annualized by `sqrt(bars_per_year)`;
- fill rate is fully filled orders over submitted orders;
- win rate is profitable closing fills over all closing fills;
- turnover is traded notional over average bar equity.

## Risk

### Position ledger

`risk.Positions` is the authoritative in-memory ledger. The pipeline applies
every trade report to it on the matching goroutine, before reports are enqueued
outbound, so positions and risk state survive outbound backpressure.

Each fill also updates a per-strategy attribution keyed by
`(account, strategy, symbol)`, and maker reports carry the maker's strategy tag.
This is what lets `max_daily_loss` and `max_drawdown_bps` pause only the
strategy that lost money. Attribution is in-memory: the persisted snapshot only
stores account positions, so a restart attributes restored positions to
`default`.

This fixes two pre-existing gaps:

- `max_position` was evaluated against a hard-coded zero position;
- `price_band_bps` used the matching book, which is empty in production because
  market data does not flow into it. The reference price now comes from the live
  market snapshot.

### Pre-trade checks

Unchanged semantics: symbol whitelist, max order quantity, max notional, price
band, max position, order rate limit.

### Intraday checks

| Check | Config | On breach |
| --- | --- | --- |
| Cancel ratio | `max_cancel_ratio_bps`, `cancel_window_seconds`, `min_cancel_samples` | pause or halt |
| Open orders | `max_open_orders` | reject new orders |
| Daily loss | `max_daily_loss` | halt |
| Drawdown | `max_drawdown_bps` | pause or halt |

A breach escalates to `HALT` (manual `Monitor.Reset`) unless `resume_auto` is
true and `cooldown_seconds > 0`, in which case it becomes a `PAUSE` that resumes
after the cooldown.

Every check is scoped to `(account, strategy)`: an untagged order uses
`default`, and one strategy's cancel ratio, open-order cap, daily loss or
drawdown never pauses another strategy on the same account.

Decision codes: `MAX_OPEN_ORDERS`, `CANCEL_RATIO`, `DAILY_LOSS`, `DRAWDOWN`,
`STRATEGY_PAUSED`, `STRATEGY_HALTED`, `CANCEL_ONLY`, `RESUMED`.

The cancel API (`POST /api/v1/order/cancel`) reports cancellations to the
monitor, so cancel-ratio accounting is live rather than approximate.

### Configuration

See `configs/config.example.yaml` for the full `risk` block, including
`risk.intraday`. Zero values disable individual checks.

### Events

`risk.Event` is emitted for every rejection and breaker transition. Sinks:

- `LogSink` for the process log;
- `ws.RiskHub` for WebSocket clients subscribing to the `risk` topic;
- `AsyncSink`, which never blocks the matching goroutine and counts drops.

WebSocket message types: `risk.blocked` (reject/pause/halt/cancel-only),
`risk.event` (recovery), `risk.snapshot` (recent history on subscribe).

Persistence: `migrations/postgres/000004_risk_events.up.sql` creates
`risk_events`; the async sink writes through `postgres.Writer.InsertRiskEvent`
when Postgres is enabled. History is served by
`GET /api/v1/risk/events?limit=&account_id=&symbol=&code=&before_id=`, newest
first, limit default 50 / max 500. Without Postgres the endpoint returns an
empty page and live events still reach the WebSocket.

### Metrics

Added, without removing existing series:

- `trading_system_matching_risk_events_total{code,action}`
- `trading_system_matching_risk_event_dropped_total`
- `trading_system_matching_strategy_state{strategy,state}`
- `trading_system_matching_strategy_orders_total{strategy,result}`
- `trading_system_matching_strategy_panics_total{strategy}`

Existing `risk_decision_total{code,result}` still records every pre-trade
decision.

## Tests

```sh
GOCACHE=/tmp/go-build go test ./internal/backtest ./internal/strategy ./internal/live ./internal/hft/risk ./internal/pipeline ./internal/app
GOCACHE=/tmp/go-build go test ./...
```

Covered behaviours:

- fee rounding and overflow, level sweeping, partial fills, IOC/MARKET
  cancellation, FOK all-or-nothing, cancel semantics, slippage clamping;
- deterministic replay for a fixed seed;
- runner panic isolation, pause/resume, order rate and open-order limits;
- position average cost, realized PnL, flips, marks;
- cancel-ratio pause and cooldown resume, open-order cap, daily-loss halt,
  drawdown escalation, manual reset;
- per-strategy isolation: one strategy's open-order or daily-loss breach does
  not pause another, and PnL is attributed by strategy tag;
- strategy tag propagation from orders into execution reports;
- live runner book/tick conversion, tagged submissions, strategy-scoped open
  orders, and clean start/stop;
- risk events: Postgres insert/list, HTTP filters and limit clamping;
- ClickHouse replay parsing, bounds, and error paths;
- regression: `max_position` is enforced from the real ledger.

## Follow-ups

1. Persist per-strategy PnL attribution so breakers survive a restart without
   falling back to `default`.
2. Multi-symbol live execution, which would make `stat_arb` tradable live.
3. Depth-based historical replay: add a depth table and a `KindBook` CH source.
4. Strategy instance identity (version/config hash) if multiple instances of the
   same strategy must be paused independently.
5. Exchange-side order reconciliation for the Binance venue (steady-state
   snapshots beyond the user-data stream).
