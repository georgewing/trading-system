// Package strategy defines the strategy contract shared by the backtest
// simulator and, later, the live order pipeline.
//
// The contract only talks in the same integer units the matching engine uses:
// prices in ticks and quantities in lots. Strategies never see floats, decimal
// strings or exchange-specific types, so the same implementation can be driven
// by historical data today and by the live venue adapter later.
package strategy

import (
	"errors"

	"trading-system/internal/hft/engine"
)

// OrderRequest is a strategy order. Price is ignored for MARKET orders and Qty
// is already scaled by the symbol quantity precision.
type OrderRequest struct {
	Symbol string
	Side   engine.Side
	Type   engine.OrderType
	TIF    engine.TimeInForce
	Price  int64
	Qty    int64
	Tag    string
	// Strategy is filled by Runner with the strategy name. A live adapter maps
	// it onto the engine order so risk breaks and metrics are strategy-scoped.
	Strategy string
}

// Fill is one execution produced by a venue or the simulator.
type Fill struct {
	OrderID uint64
	Symbol  string
	Side    engine.Side
	Price   int64
	Qty     int64
	Maker   bool
	Fee     int64
	TS      int64
}

// SubmitResult mirrors the engine result shape in strategy-friendly units.
type SubmitResult struct {
	OrderID  uint64
	Filled   int64
	Leaves   int64
	Canceled int64
	Status   engine.OrderStatus
	Fills    []Fill
}

// Level is one aggregated price level.
type Level struct {
	Price int64
	Qty   int64
}

// BookView is an immutable depth snapshot passed to strategies.
type BookView struct {
	Symbol string
	Bids   []Level // price descending
	Asks   []Level // price ascending
	TS     int64
}

// BestBid returns the highest bid.
func (b BookView) BestBid() (Level, bool) {
	if len(b.Bids) == 0 {
		return Level{}, false
	}
	return b.Bids[0], true
}

// BestAsk returns the lowest ask.
func (b BookView) BestAsk() (Level, bool) {
	if len(b.Asks) == 0 {
		return Level{}, false
	}
	return b.Asks[0], true
}

// Mid returns the mid price rounded down. It reports false when either side is
// missing or the book is crossed.
func (b BookView) Mid() (int64, bool) {
	bid, ok := b.BestBid()
	if !ok {
		return 0, false
	}
	ask, ok := b.BestAsk()
	if !ok || ask.Price < bid.Price {
		return 0, false
	}
	return bid.Price + (ask.Price-bid.Price)/2, true
}

// TradeView is a normalized public trade print.
type TradeView struct {
	Symbol string
	Price  int64
	Qty    int64
	TS     int64
}

// Context is the strategy's view of the world. Implementations must be safe to
// call from the strategy's single driving goroutine.
type Context interface {
	// Now returns the current event time in unix milliseconds. Backtests drive
	// it from the replay clock; live trading uses the wall clock.
	Now() int64
	// Position returns the signed position for symbol in lots.
	Position(symbol string) int64
	// Book returns the latest depth snapshot for symbol.
	Book(symbol string) (BookView, bool)
	// Submit sends an order and returns what executed immediately.
	Submit(req OrderRequest) (SubmitResult, error)
	// Cancel cancels a resting order.
	Cancel(orderID uint64) error
	// OpenOrders returns the strategy's resting order ids for symbol.
	OpenOrders(symbol string) []uint64
}

// Strategy is implemented by every algorithmic strategy.
//
// Callbacks are advisory: a panic is isolated by the Runner, and returning an
// error never stops the replay. Implementations must not spawn goroutines that
// call Context concurrently with the callbacks.
type Strategy interface {
	Name() string
	Init(ctx Context, params map[string]string) error
	OnBook(ctx Context, book BookView) error
	OnTrade(ctx Context, trade TradeView) error
	OnTimer(ctx Context, now int64) error
	Stop(ctx Context) error
}

// Errors returned by the execution layer.
var (
	ErrNilContext     = errors.New("strategy: nil context")
	ErrMaxOpenOrders  = errors.New("strategy: max open orders reached")
	ErrOrderRateLimit = errors.New("strategy: order rate limit reached")
)
