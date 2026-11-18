# Phase 4: Backtest and Risk Enhancements

This document describes the strategy backtest framework and the enhanced risk
stage added in phase 4. It records the assumptions a report depends on, the
configuration surface, and the operational follow-ups.

## Scope of this phase

Delivered:

- strategy contract shared by backtest and live execution
  (`internal/hft/strategy`);
- deterministic historical replay and simulated execution
  (`internal/hft/backtest`);
- fee, slippage and fill-probability models;
- market-making and statistical-arbitrage sample strategies;
- performance reporting (equity, drawdown, Sharpe, fill rate, win rate,
  turnover, exposure);
- intraday risk monitoring with strategy breakers and a position ledger
  (`internal/hft/risk`);
- risk event publishing to logs, Prometheus and the WebSocket terminal;
- `risk_events` Postgres migration.

Not in this phase:

- `POST /api/v1/order/cancel`. The simulator supports cancel, but the gateway
  does not expose it yet, so live `CANCEL_ONLY` and market-making cancel/replace
  remain backtest-only;
- a Postgres writer for risk events (the migration and sink exist, the writer
  does not);
- ClickHouse-backed replay (the source interface is ready, the reader is not).

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

A sample file lives at `internal/hft/backtest/testdata/sample.jsonl`.

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

`internal/hft/strategy.Strategy` receives:

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

Decision codes: `MAX_OPEN_ORDERS`, `CANCEL_RATIO`, `DAILY_LOSS`, `DRAWDOWN`,
`STRATEGY_PAUSED`, `STRATEGY_HALTED`, `CANCEL_ONLY`, `RESUMED`.

Open-order accounting is approximate while the cancel API is missing: accepted
orders are counted and terminal reports release them, but cancels cannot happen
through the gateway.

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
`risk_events`. The table is not written yet because the Postgres writer is still
pending.

### Metrics

Added, without removing existing series:

- `trading_system_matching_risk_events_total{code,action}`
- `trading_system_matching_risk_event_dropped_total`
- `trading_system_matching_strategy_state{strategy,state}`

Existing `risk_decision_total{code,result}` still records every pre-trade
decision.

## Tests

```sh
GOCACHE=/tmp/go-build go test ./internal/hft/backtest ./internal/hft/strategy ./internal/hft/risk ./internal/pipeline ./internal/app
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
- regression: `max_position` is enforced from the real ledger.

## Follow-ups

1. `POST /api/v1/order/cancel` plus `Matcher.CancelOrder`, which unlocks live
   market making and makes `CANCEL_ONLY` meaningful.
2. Postgres writer for `risk_events` (the sink and schema exist).
3. ClickHouse replay source implementing `backtest.Source`.
4. Wire strategy runtimes into the live pipeline (orders tagged by strategy so
   `strategy_state` labels are meaningful).
