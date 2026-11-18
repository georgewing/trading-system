package risk

import (
	"context"
	"time"

	"trading-system/internal/hft/engine"
)

// EngineConfig bundles the pre-trade and intraday configurations.
type EngineConfig struct {
	PreTrade Config
	Intraday IntradayConfig
}

// Engine is the single risk entry point for the order pipeline. It implements
// the same Check contract as Guard, but enriches the request with the live
// position and a market reference price before evaluating the rules.
type Engine struct {
	guard     *Guard
	positions *Positions
	monitor   *Monitor
	events    Sink
	metrics   Metrics
	refPrice  func(symbol string) int64
	clock     func() time.Time
}

// EngineOption customizes an Engine.
type EngineOption func(*Engine)

// WithReferencePrice injects the price used by the price-band check. Without
// it the check falls back to whatever the caller supplied.
func WithReferencePrice(fn func(symbol string) int64) EngineOption {
	return func(e *Engine) {
		if fn != nil {
			e.refPrice = fn
		}
	}
}

// WithEvents injects the risk event sink.
func WithEvents(s Sink) EngineOption {
	return func(e *Engine) {
		if s != nil {
			e.events = s
		}
	}
}

// WithMetrics injects the risk metrics recorder.
func WithMetrics(m Metrics) EngineOption {
	return func(e *Engine) {
		if m != nil {
			e.metrics = m
		}
	}
}

// WithClock overrides the clock used for rate limits, cooldowns and event
// timestamps. Tests use it to keep time deterministic.
func WithClock(fn func() time.Time) EngineOption {
	return func(e *Engine) {
		if fn != nil {
			e.clock = fn
			e.monitor.SetClock(fn)
		}
	}
}

// NewEngine builds a risk engine. A nil positions ledger is replaced by an empty
// one so callers cannot accidentally disable accounting with a nil pointer.
func NewEngine(cfg EngineConfig, positions *Positions, opts ...EngineOption) *Engine {
	if positions == nil {
		positions = NewPositions()
	}
	e := &Engine{
		guard:     NewGuard(cfg.PreTrade),
		positions: positions,
		events:    nil,
		metrics:   noopMetrics{},
		clock:     time.Now,
	}
	e.monitor = NewMonitor(cfg.Intraday, positions)
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

// Check implements the pipeline pre-trade risk contract.
func (e *Engine) Check(ctx context.Context, req Request) Decision {
	now := req.Now
	if now.IsZero() {
		now = e.clock()
	}
	req.Now = now
	req.Strategy = NormalizeStrategy(req.Strategy)

	if e.positions != nil {
		req.CurrentPosition = e.positions.Qty(req.AccountID, req.Symbol)
	}
	if e.refPrice != nil {
		if price := e.refPrice(req.Symbol); price > 0 {
			req.ReferencePrice = price
		}
	}

	if decision := e.guard.Check(ctx, req); !decision.Approved {
		e.emitReject(req, decision, now)
		return decision
	}

	sub := Subject{AccountID: req.AccountID, Strategy: req.Strategy}
	decision, triggers := e.monitor.Check(sub, now)
	for _, t := range triggers {
		e.emitTrigger(req.AccountID, req.Symbol, req.Strategy, t, now)
	}
	if !decision.Approved {
		e.emitReject(req, decision, now)
	}
	return decision
}

// OnOrderAccepted records an accepted order so open-order and cancel-ratio
// accounting sees it.
func (e *Engine) OnOrderAccepted(order engine.Order) {
	e.monitor.OnOrderAccepted(subjectFor(order), e.clock())
}

// OnExecReport applies a fill to the position ledger and re-evaluates PnL-based
// limits. Terminal reports release the order's open-order slot.
func (e *Engine) OnExecReport(order engine.Order, report engine.ExecReport) {
	now := e.clock()
	strategy := NormalizeStrategy(order.Strategy)

	if trade := report.Trade; trade != nil && trade.Quantity > 0 {
		e.positions.ApplyFillFor(order.AccountID, strategy, order.Symbol, order.Side,
			trade.Price, trade.Quantity)

		// Venues that report both sides in one message need the maker leg booked
		// here. The paper matcher publishes maker reports separately, so skip the
		// leg in that case to avoid counting the quantity twice. The maker's
		// strategy is unknown for a combined venue report, so it falls back to
		// DefaultStrategy.
		if !report.MakerReported && trade.MakerAccountID != 0 &&
			trade.MakerAccountID != order.AccountID && validSide(trade.MakerSide) {
			e.positions.ApplyFillFor(trade.MakerAccountID, DefaultStrategy, order.Symbol, trade.MakerSide,
				trade.Price, trade.Quantity)
		}
	}

	sub := subjectFor(order)
	switch report.Status {
	case engine.OrderStatusFilled, engine.OrderStatusCanceled, engine.OrderStatusRejected:
		e.monitor.OnOrderClosed(sub)
	}

	for _, t := range e.monitor.Evaluate(sub, now) {
		e.emitTrigger(order.AccountID, order.Symbol, strategy, t, now)
	}
}

// OnCancel records a cancellation so the cancel-ratio breaker sees it.
func (e *Engine) OnCancel(order engine.Order) {
	e.monitor.OnCancel(subjectFor(order), e.clock())
}

func subjectFor(order engine.Order) Subject {
	return Subject{AccountID: order.AccountID, Strategy: NormalizeStrategy(order.Strategy)}
}

// Positions exposes the ledger for query endpoints and tests.
func (e *Engine) Positions() *Positions { return e.positions }

// Monitor exposes the intraday supervisor for query endpoints and tests.
func (e *Engine) Monitor() *Monitor { return e.monitor }

// Guard exposes the pre-trade guard for tests.
func (e *Engine) Guard() *Guard { return e.guard }

func (e *Engine) emitReject(req Request, decision Decision, now time.Time) {
	ev := Event{
		TS:        now.UnixMilli(),
		AccountID: req.AccountID,
		Symbol:    req.Symbol,
		Strategy:  req.Strategy,
		Code:      decision.Code,
		Action:    ActionReject,
		Message:   decision.Message,
	}
	e.record(ev)
	e.metrics.IncRiskEvent(decision.Code, string(ActionReject))
}

func (e *Engine) emitTrigger(accountID uint64, symbol, strategy string, t Trigger, now time.Time) {
	ev := Event{
		TS:        now.UnixMilli(),
		AccountID: accountID,
		Symbol:    symbol,
		Strategy:  strategy,
		Code:      t.Code,
		Action:    t.Action,
		Message:   t.Message,
	}
	e.record(ev)
	e.metrics.IncRiskEvent(t.Code, string(t.Action))

	if t.Action == ActionAllow {
		e.metrics.SetStrategyState(label(strategy), "running")
		return
	}
	e.metrics.SetStrategyState(label(strategy), string(t.Action))
}

func (e *Engine) record(ev Event) {
	if e.events != nil {
		e.events.Record(ev)
	}
}

func label(strategy string) string {
	if strategy == "" {
		return "default"
	}
	return strategy
}

// validSide reports whether s is one of the engine's two trade directions.
// MakerSide is zero when the venue does not expose the counterparty, so the
// maker leg must be skipped instead of being defaulted to a buy.
func validSide(s engine.Side) bool {
	return s == engine.SideBuy || s == engine.SideSell
}

var _ interface {
	Check(context.Context, Request) Decision
} = (*Engine)(nil)
