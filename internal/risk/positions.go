package risk

import (
	"math"
	"sync"

	"trading-system/internal/engine"
)

// PositionKey identifies one account's position in one symbol.
type PositionKey struct {
	AccountID uint64
	Symbol    string
}

// Position is a signed position with average cost. AvgCost is in price ticks,
// Realized and Unrealized are in notional units (ticks * lots).
type Position struct {
	Qty      int64
	AvgCost  int64
	Mark     int64
	Realized int64
}

// Unrealized returns Qty * (Mark - AvgCost).
func (p Position) Unrealized() int64 {
	if p.Qty == 0 || p.Mark == 0 {
		return 0
	}
	return satMul(p.Qty, satSub(p.Mark, p.AvgCost))
}

// Positions is the authoritative in-memory position ledger. The matching
// pipeline applies fills to it and the risk monitor reads from it.
//
// It is safe for concurrent use: writes happen on the matching goroutine, reads
// may come from HTTP or WebSocket handlers.
type Positions struct {
	mu     sync.RWMutex
	pos    map[PositionKey]Position
	wins   int
	closed int
}

// NewPositions builds an empty ledger.
func NewPositions() *Positions {
	return &Positions{pos: make(map[PositionKey]Position)}
}

// Qty returns the signed position for one account and symbol.
func (p *Positions) Qty(accountID uint64, symbol string) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pos[PositionKey{AccountID: accountID, Symbol: symbol}].Qty
}

// Snapshot returns a copy of one position.
func (p *Positions) Snapshot(accountID uint64, symbol string) Position {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pos[PositionKey{AccountID: accountID, Symbol: symbol}]
}

// ApplyFill books one fill. Quantity is always positive; side determines sign.
func (p *Positions) ApplyFill(accountID uint64, symbol string, side engine.Side, price, qty int64) {
	if accountID == 0 || symbol == "" || qty <= 0 || price <= 0 {
		return
	}
	delta := qty
	if side == engine.SideSell {
		delta = -qty
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	key := PositionKey{AccountID: accountID, Symbol: symbol}
	pos := p.pos[key]
	pos.Mark = price

	oldQty := pos.Qty
	switch {
	case oldQty == 0 || (oldQty > 0) == (delta > 0):
		newQty := satAdd(oldQty, delta)
		absOld := satAbs(oldQty)
		absDelta := satAbs(delta)
		absNew := satAbs(newQty)
		if absNew == 0 {
			pos.AvgCost = 0
		} else {
			cost := satAdd(satMul(pos.AvgCost, absOld), satMul(price, absDelta))
			pos.AvgCost = cost / absNew
		}
		pos.Qty = newQty

	default:
		closing := satAbs(oldQty)
		if satAbs(delta) < closing {
			closing = satAbs(delta)
		}
		var pnl int64
		if oldQty > 0 {
			pnl = satMul(satSub(price, pos.AvgCost), closing)
		} else {
			pnl = satMul(satSub(pos.AvgCost, price), closing)
		}
		pos.Realized = satAdd(pos.Realized, pnl)
		p.closed++
		if pnl > 0 {
			p.wins++
		}

		newQty := satAdd(oldQty, delta)
		switch {
		case newQty == 0:
			pos.AvgCost = 0
		case (newQty > 0) != (oldQty > 0):
			pos.AvgCost = price
		}
		pos.Qty = newQty
	}

	p.pos[key] = pos
}

// Mark records the latest price for a symbol across all accounts. It is used to
// value open positions for the intraday drawdown and loss checks.
func (p *Positions) Mark(symbol string, price int64) {
	if symbol == "" || price <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, pos := range p.pos {
		if key.Symbol != symbol {
			continue
		}
		pos.Mark = price
		p.pos[key] = pos
	}
}

// AccountEquity returns realized plus unrealized PnL for one account.
func (p *Positions) AccountEquity(accountID uint64) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var equity int64
	for key, pos := range p.pos {
		if key.AccountID != accountID {
			continue
		}
		equity = satAdd(equity, pos.Realized)
		equity = satAdd(equity, pos.Unrealized())
	}
	return equity
}

// ClosedTrades returns the number of closing fills and how many were profitable.
func (p *Positions) ClosedTrades() (wins, total int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.wins, p.closed
}

func satAdd(a, b int64) int64 {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		if b > 0 {
			return math.MaxInt64
		}
		return math.MinInt64
	}
	return sum
}

func satSub(a, b int64) int64 {
	if b == math.MinInt64 {
		if a >= 0 {
			return math.MaxInt64
		}
		return a - b
	}
	return satAdd(a, -b)
}

func satMul(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > 0 && b > 0 && a > math.MaxInt64/b {
		return math.MaxInt64
	}
	if a < 0 && b < 0 && a < math.MaxInt64/b {
		return math.MaxInt64
	}
	if a > 0 && b < 0 && b < math.MinInt64/a {
		return math.MinInt64
	}
	if a < 0 && b > 0 && a < math.MinInt64/b {
		return math.MinInt64
	}
	return a * b
}

func satAbs(v int64) int64 {
	if v == math.MinInt64 {
		return math.MaxInt64
	}
	if v < 0 {
		return -v
	}
	return v
}
