// Package live drives a strategy from the real-time market feed and submits
// its orders through the matching pipeline.
//
// The live adapter deliberately reuses the same strategy contract as the
// backtest simulator: a strategy cannot tell whether its Context is historical
// or live. Orders carry the strategy name, so the risk engine can apply
// strategy-scoped breakers and metrics.
package live

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"trading-system/internal/hft/domain"
	"trading-system/internal/hft/engine"
	"trading-system/internal/hft/risk"
	"trading-system/internal/hft/strategy"
	"trading-system/internal/pipeline"
	"trading-system/internal/transport/wire"
	"trading-system/pkg/idgen"
)

// Deps are the runtime dependencies of a live strategy.
type Deps struct {
	Bus       *pipeline.EventBus
	Matcher   *engine.Matcher
	Positions *risk.Positions
	Stream    domain.Fanout
	Snapshot  domain.Snapshotter
	Metrics   strategy.Metrics

	AccountID         uint64
	Symbol            string
	QuantityPrecision int
	// Timer is the OnTimer cadence, default 5s.
	Timer time.Duration
	// SubmitTimeout bounds one order round trip to the matching goroutine,
	// default 2s.
	SubmitTimeout time.Duration
}

// Config configures the strategy instance.
type Config struct {
	Strategy strategy.Strategy
	Params   map[string]string
	Runner   strategy.RunnerConfig
}

// Runner owns the strategy goroutine and its market-feed subscription.
type Runner struct {
	name    string
	ctx     *runnerContext
	runner  *strategy.Runner
	cancel  context.CancelFunc
	done    chan struct{}
	stopOne sync.Once
}

// Start validates the dependencies, seeds the book from the snapshot and
// starts the event loop. Call Stop to shut it down.
func Start(ctx context.Context, deps Deps, cfg Config) (*Runner, error) {
	if cfg.Strategy == nil {
		return nil, fmt.Errorf("live: nil strategy")
	}
	if deps.Bus == nil || deps.Matcher == nil {
		return nil, fmt.Errorf("live: matching pipeline is required")
	}
	if deps.Positions == nil {
		return nil, fmt.Errorf("live: position ledger is required")
	}
	if deps.Stream == nil {
		return nil, fmt.Errorf("live: market data stream is required")
	}
	if deps.AccountID == 0 {
		return nil, fmt.Errorf("live: account_id is required")
	}
	if deps.Symbol == "" {
		return nil, fmt.Errorf("live: symbol is required")
	}
	if deps.Timer <= 0 {
		deps.Timer = 5 * time.Second
	}
	if deps.SubmitTimeout <= 0 {
		deps.SubmitTimeout = 2 * time.Second
	}

	ctxImpl := newRunnerContext(deps, cfg.Strategy.Name())
	if deps.Snapshot != nil {
		if depth, ok := deps.Snapshot.Depth(deps.Symbol); ok {
			ctxImpl.setDepth(deps.Symbol, depth, time.Now().UnixMilli())
		}
	}

	runner := strategy.NewRunner(cfg.Strategy, ctxImpl, cfg.Runner)
	if deps.Metrics != nil {
		runner.SetMetrics(deps.Metrics)
	}
	if err := runner.Init(cfg.Params); err != nil {
		return nil, err
	}

	ch, stopStream, err := deps.Stream.Subscribe([]string{
		"depth:" + deps.Symbol,
		"trade:" + deps.Symbol,
		"tick:" + deps.Symbol,
	})
	if err != nil {
		_ = runner.Stop()
		return nil, fmt.Errorf("live: subscribe market stream: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	r := &Runner{
		name:   cfg.Strategy.Name(),
		ctx:    ctxImpl,
		runner: runner,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go r.loop(runCtx, ch, stopStream)

	log.Printf("live strategy started: name=%s account=%d symbol=%s timer=%s",
		r.name, deps.AccountID, deps.Symbol, deps.Timer)
	return r, nil
}

// Name returns the strategy name.
func (r *Runner) Name() string { return r.name }

// Stop cancels the loop and waits for it to finish. It is idempotent.
func (r *Runner) Stop() {
	if r == nil {
		return
	}
	r.stopOne.Do(func() {
		r.cancel()
		select {
		case <-r.done:
		case <-time.After(3 * time.Second):
			log.Printf("live strategy %s did not stop before timeout", r.name)
		}
	})
}

func (r *Runner) loop(ctx context.Context, ch <-chan domain.Event, stopStream func()) {
	defer close(r.done)
	defer stopStream()
	defer func() {
		if err := r.runner.Stop(); err != nil {
			log.Printf("live strategy %s stop: %v", r.name, err)
		}
	}()

	ticker := time.NewTicker(r.ctx.deps.Timer)
	defer ticker.Stop()

	// Give the strategy the seeded snapshot before the first market event.
	if book, ok := r.ctx.Book(r.ctx.deps.Symbol); ok {
		r.logErr("on_book", r.runner.OnBook(book))
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				ch = nil
				continue
			}
			r.handle(ev)
		case <-ticker.C:
			r.logErr("on_timer", r.runner.OnTimer(time.Now().UnixMilli()))
		}
	}
}

func (r *Runner) handle(ev domain.Event) {
	switch ev.Kind {
	case domain.KindDepth:
		if ev.Depth == nil {
			return
		}
		r.ctx.setDepth(ev.Symbol, *ev.Depth, ev.TS)
		if book, ok := r.ctx.Book(ev.Symbol); ok {
			r.logErr("on_book", r.runner.OnBook(book))
		}

	case domain.KindTick:
		if ev.Tick == nil {
			return
		}
		r.ctx.setTick(ev.Symbol, *ev.Tick, ev.TS)
		if book, ok := r.ctx.Book(ev.Symbol); ok {
			r.logErr("on_book", r.runner.OnBook(book))
		}

	case domain.KindTrade:
		if ev.Trade == nil {
			return
		}
		r.logErr("on_trade", r.runner.OnTrade(strategy.TradeView{
			Symbol: ev.Symbol,
			Price:  ev.Trade.Price,
			Qty:    ev.Trade.Qty,
			TS:     ev.TS,
		}))
	}
}

func (r *Runner) logErr(op string, err error) {
	if err != nil {
		log.Printf("live strategy %s %s: %v", r.name, op, err)
	}
}

// runnerContext implements strategy.Context and translates strategy orders into
// engine orders tagged with the strategy name.
type runnerContext struct {
	deps Deps
	name string

	mu    sync.RWMutex
	books map[string]strategy.BookView
}

func newRunnerContext(deps Deps, name string) *runnerContext {
	return &runnerContext{
		deps:  deps,
		name:  name,
		books: make(map[string]strategy.BookView),
	}
}

func (c *runnerContext) Now() int64 { return time.Now().UnixMilli() }

func (c *runnerContext) Position(symbol string) int64 {
	return c.deps.Positions.Qty(c.deps.AccountID, symbol)
}

func (c *runnerContext) Book(symbol string) (strategy.BookView, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	book, ok := c.books[symbol]
	return book, ok
}

func (c *runnerContext) OpenOrders(symbol string) []uint64 {
	return c.deps.Matcher.OpenOrders(c.deps.AccountID, c.name, symbol)
}

func (c *runnerContext) Cancel(orderID uint64) error {
	_, err := c.deps.Bus.CancelOrder(context.Background(), orderID, "strategy "+c.name)
	return err
}

func (c *runnerContext) Submit(req strategy.OrderRequest) (strategy.SubmitResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.deps.SubmitTimeout)
	defer cancel()
	return c.submit(ctx, req)
}

func (c *runnerContext) submit(ctx context.Context, req strategy.OrderRequest) (strategy.SubmitResult, error) {
	symbol := req.Symbol
	if symbol == "" {
		symbol = c.deps.Symbol
	}
	if symbol != c.deps.Symbol {
		return strategy.SubmitResult{}, fmt.Errorf("live: unsupported symbol %q, gateway only serves %q", symbol, c.deps.Symbol)
	}
	if req.Qty <= 0 {
		return strategy.SubmitResult{}, fmt.Errorf("live: qty must be positive")
	}

	orderType := req.Type
	if orderType == "" {
		orderType = engine.OrderTypeLimit
	}
	tif := req.TIF
	if tif == "" {
		tif = engine.TimeInForceGTC
	}
	price := req.Price
	if orderType == engine.OrderTypeMarket && price <= 0 {
		// The risk guard requires a positive price. Market orders ignore it, so a
		// nominal 1 keeps the validation path happy without changing execution.
		price = 1
	}

	strategyName := req.Strategy
	if strategyName == "" {
		strategyName = c.name
	}

	order := engine.Order{
		ID:        idgen.NextID(),
		AccountID: c.deps.AccountID,
		Symbol:    symbol,
		Strategy:  strategyName,
		Side:      req.Side,
		Type:      orderType,
		TIF:       tif,
		Price:     price,
		Quantity:  wire.Decimal(req.Qty, c.deps.QuantityPrecision),
		CreatedAt: time.Now().UnixNano(),
	}

	resultCh := make(chan pipeline.MatchResult, 1)
	if err := c.deps.Bus.SubmitWithContext(ctx, pipeline.OrderCommand{
		Order:    order,
		ResultCh: resultCh,
	}); err != nil {
		return strategy.SubmitResult{}, err
	}

	select {
	case <-ctx.Done():
		return strategy.SubmitResult{OrderID: order.ID}, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return strategy.SubmitResult{OrderID: order.ID, Status: engine.OrderStatusRejected}, result.Err
		}
		return buildResult(order, req.Qty, result.Trades), nil
	}
}

func buildResult(order engine.Order, qty int64, trades []engine.Trade) strategy.SubmitResult {
	fills := make([]strategy.Fill, 0, len(trades))
	var filled int64
	now := time.Now().UnixMilli()
	for _, trade := range trades {
		filled += trade.Quantity
		fills = append(fills, strategy.Fill{
			OrderID: order.ID,
			Symbol:  order.Symbol,
			Side:    order.Side,
			Price:   trade.Price,
			Qty:     trade.Quantity,
			Maker:   false,
			TS:      now,
		})
	}

	result := strategy.SubmitResult{
		OrderID: order.ID,
		Filled:  filled,
		Leaves:  qty - filled,
		Fills:   fills,
		Status:  engine.OrderStatusNew,
	}

	switch {
	case filled == qty:
		result.Status = engine.OrderStatusFilled
		result.Leaves = 0
	case order.Type == engine.OrderTypeMarket || order.TIF == engine.TimeInForceIOC:
		result.Canceled = qty - filled
		result.Leaves = 0
		result.Status = engine.OrderStatusCanceled
		if filled > 0 {
			result.Status = engine.OrderStatusPartiallyFilled
		}
	case filled > 0:
		result.Status = engine.OrderStatusPartiallyFilled
	}
	return result
}

func (c *runnerContext) setDepth(symbol string, depth domain.Depth, ts int64) {
	book := strategy.BookView{Symbol: symbol, TS: ts}
	book.Bids = make([]strategy.Level, 0, len(depth.Bids))
	for _, level := range depth.Bids {
		if level.Price > 0 && level.Qty > 0 {
			book.Bids = append(book.Bids, strategy.Level{Price: level.Price, Qty: level.Qty})
		}
	}
	book.Asks = make([]strategy.Level, 0, len(depth.Asks))
	for _, level := range depth.Asks {
		if level.Price > 0 && level.Qty > 0 {
			book.Asks = append(book.Asks, strategy.Level{Price: level.Price, Qty: level.Qty})
		}
	}

	c.mu.Lock()
	c.books[symbol] = book
	c.mu.Unlock()
}

func (c *runnerContext) setTick(symbol string, tick domain.Tick, ts int64) {
	book := strategy.BookView{Symbol: symbol, TS: ts}
	if tick.BidPrice > 0 && tick.BidQty > 0 {
		book.Bids = []strategy.Level{{Price: tick.BidPrice, Qty: tick.BidQty}}
	}
	if tick.AskPrice > 0 && tick.AskQty > 0 {
		book.Asks = []strategy.Level{{Price: tick.AskPrice, Qty: tick.AskQty}}
	}

	c.mu.Lock()
	c.books[symbol] = book
	c.mu.Unlock()
}

var _ strategy.Context = (*runnerContext)(nil)
