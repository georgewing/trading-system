// Package risk provides pre-trade risk checks that are independent of
// transport, storage, and pipeline layers.
package risk

import (
	"context"
	"fmt"
	"math/bits"
	"sync"
	"time"

	"trading-system/internal/hft/engine"
)

// Decision codes returned by Guard.Check.
const (
	CodeOK               = "OK"
	CodeCanceled         = "CANCELED"
	CodeInvalidRequest   = "INVALID_REQUEST"
	CodeSymbolNotAllowed = "SYMBOL_NOT_ALLOWED"
	CodeMaxQty           = "MAX_QTY"
	CodeMaxNotional      = "MAX_NOTIONAL"
	CodePriceBand        = "PRICE_BAND"
	CodeMaxPosition      = "MAX_POSITION"
	CodeRateLimit        = "RATE_LIMIT"
)

// Request describes a single order for risk evaluation. Price and Qty use the
// integer units understood by the engine.
type Request struct {
	AccountID       uint64
	ClientOrderID   string
	Symbol          string
	Side            engine.Side
	Type            engine.OrderType
	TIF             engine.TimeInForce
	Price           int64
	Qty             int64
	Notional        int64     // Optional; when > 0 it replaces Price*Qty.
	ReferencePrice  int64     // Optional; enables the price band check when > 0.
	CurrentPosition int64     // Signed position before this order.
	Now             time.Time // Optional; rate limit clock, defaults to time.Now.
}

// Decision is the outcome of a risk check.
type Decision struct {
	Approved bool
	Code     string
	Message  string
}

// Config controls the checks performed by Guard. Zero or negative numeric
// limits disable the corresponding check. An empty SymbolWhitelist disables
// the whitelist check.
type Config struct {
	SymbolWhitelist    []string
	MaxOrderQty        int64
	MaxNotional        int64
	PriceBandBps       int64
	MaxOrdersPerWindow int
	Window             time.Duration
	MaxPosition        int64
}

// Guard evaluates pre-trade risk checks. It is safe for concurrent use.
type Guard struct {
	cfg       Config
	whitelist map[string]struct{}

	mu        sync.Mutex
	orders    map[rateKey][]time.Time
	lastSweep time.Time
}

type rateKey struct {
	accountID uint64
	symbol    string
}

// NewGuard builds a Guard from cfg. The symbol whitelist is copied, so later
// changes to cfg.SymbolWhitelist do not affect the Guard.
func NewGuard(cfg Config) *Guard {
	g := &Guard{
		cfg:    cfg,
		orders: make(map[rateKey][]time.Time),
	}
	if len(cfg.SymbolWhitelist) > 0 {
		g.whitelist = make(map[string]struct{}, len(cfg.SymbolWhitelist))
		for _, symbol := range cfg.SymbolWhitelist {
			g.whitelist[symbol] = struct{}{}
		}
	}
	return g
}

// Check runs all configured checks in a fixed order and returns the first
// rejection. Only requests that pass every earlier check consume rate-limit
// quota.
func (g *Guard) Check(ctx context.Context, req Request) Decision {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return reject(CodeCanceled, err.Error())
		}
	}

	if msg, ok := validate(req); !ok {
		return reject(CodeInvalidRequest, "invalid request: "+msg)
	}

	if len(g.whitelist) > 0 {
		if _, ok := g.whitelist[req.Symbol]; !ok {
			return reject(CodeSymbolNotAllowed, "symbol not allowed: "+req.Symbol)
		}
	}

	cfg := g.cfg
	if cfg.MaxOrderQty > 0 && req.Qty > cfg.MaxOrderQty {
		return reject(CodeMaxQty, fmt.Sprintf("order qty %d exceeds max %d", req.Qty, cfg.MaxOrderQty))
	}

	if cfg.MaxNotional > 0 && notionalExceeds(req, cfg.MaxNotional) {
		return reject(CodeMaxNotional, fmt.Sprintf("order notional exceeds max %d", cfg.MaxNotional))
	}

	if req.ReferencePrice > 0 && cfg.PriceBandBps > 0 &&
		outsidePriceBand(req.Price, req.ReferencePrice, cfg.PriceBandBps) {
		return reject(CodePriceBand, fmt.Sprintf(
			"price %d outside reference %d band %d bps",
			req.Price, req.ReferencePrice, cfg.PriceBandBps,
		))
	}

	if cfg.MaxPosition > 0 && positionExceeds(req, cfg.MaxPosition) {
		return reject(CodeMaxPosition, fmt.Sprintf("position after order exceeds max %d", cfg.MaxPosition))
	}

	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !g.allow(req.AccountID, req.Symbol, now) {
		return reject(CodeRateLimit, fmt.Sprintf(
			"order rate exceeds %d per %s",
			cfg.MaxOrdersPerWindow, cfg.Window,
		))
	}

	return Decision{Approved: true, Code: CodeOK}
}

func reject(code, message string) Decision {
	return Decision{Code: code, Message: message}
}

func validate(req Request) (string, bool) {
	switch {
	case req.AccountID == 0:
		return "account id is required", false
	case req.ClientOrderID == "":
		return "client order id is required", false
	case req.Symbol == "":
		return "symbol is required", false
	case req.Side != engine.SideBuy && req.Side != engine.SideSell:
		return "side is invalid", false
	case req.Type != engine.OrderTypeLimit && req.Type != engine.OrderTypeMarket:
		return "order type is invalid", false
	case req.TIF != engine.TimeInForceGTC &&
		req.TIF != engine.TimeInForceIOC &&
		req.TIF != engine.TimeInForceFOK:
		return "time in force is invalid", false
	case req.Price <= 0:
		return "price must be positive", false
	case req.Qty <= 0:
		return "qty must be positive", false
	}
	return "", true
}

// notionalExceeds compares the effective notional with max. A non-positive
// explicit Notional falls back to Price*Qty, and the product is compared
// without overflow.
func notionalExceeds(req Request, max int64) bool {
	if req.Notional > 0 {
		return req.Notional > max
	}
	hi, lo := bits.Mul64(uint64(req.Price), uint64(req.Qty))
	return hi != 0 || lo > uint64(max)
}

// outsidePriceBand reports whether abs(price-ref)*10000 > ref*bps.
// The comparison uses 128-bit products to avoid overflow.
func outsidePriceBand(price, ref, bps int64) bool {
	diff := price - ref
	if diff < 0 {
		diff = -diff
	}
	return mulGt(uint64(diff), 10000, uint64(ref), uint64(bps))
}

// positionExceeds applies the signed order quantity to the current position
// and rejects on overflow or on exceeding +/- max.
func positionExceeds(req Request, max int64) bool {
	delta := req.Qty
	if req.Side == engine.SideSell {
		delta = -delta
	}
	pos, ok := addInt64(req.CurrentPosition, delta)
	if !ok {
		return true
	}
	return pos > max || pos < -max
}

// mulGt reports whether a*b > c*d for non-negative operands.
func mulGt(a, b, c, d uint64) bool {
	ahi, alo := bits.Mul64(a, b)
	chi, clo := bits.Mul64(c, d)
	if ahi != chi {
		return ahi > chi
	}
	return alo > clo
}

// addInt64 adds a and b and reports whether the result overflowed.
func addInt64(a, b int64) (int64, bool) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}
	return sum, true
}

// allow applies a sliding-window rate limit for one (account, symbol) pair.
// Expired timestamps are pruned on every call, and the whole map is swept
// once per window so idle keys do not accumulate.
func (g *Guard) allow(accountID uint64, symbol string, now time.Time) bool {
	cfg := g.cfg
	if cfg.MaxOrdersPerWindow <= 0 || cfg.Window <= 0 {
		return true
	}
	key := rateKey{accountID: accountID, symbol: symbol}
	cutoff := now.Add(-cfg.Window)

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.orders == nil {
		g.orders = make(map[rateKey][]time.Time)
	}
	if g.lastSweep.IsZero() || now.Sub(g.lastSweep) >= cfg.Window {
		g.pruneAllLocked(cutoff)
		g.lastSweep = now
	}

	hits := pruneTimes(g.orders[key], cutoff)
	if len(hits) >= cfg.MaxOrdersPerWindow {
		g.orders[key] = hits
		return false
	}
	g.orders[key] = append(hits, now)
	return true
}

func (g *Guard) pruneAllLocked(cutoff time.Time) {
	for key, times := range g.orders {
		kept := pruneTimes(times, cutoff)
		if len(kept) == 0 {
			delete(g.orders, key)
			continue
		}
		g.orders[key] = kept
	}
}

func pruneTimes(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}
