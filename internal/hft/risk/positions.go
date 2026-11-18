package risk

import (
	"math"
	"sort"
	"sync"

	"trading-system/internal/hft/engine"
)

// PositionKey identifies one account's position in one symbol.
type PositionKey struct {
	AccountID uint64
	Symbol    string
}

// StrategyKey identifies one strategy's attribution inside an account. It is
// used only for risk PnL scoping; the authoritative account position remains
// keyed by account and symbol.
type StrategyKey struct {
	AccountID uint64
	Strategy  string
}

// DefaultStrategy is the attribution used for untagged orders.
const DefaultStrategy = "default"

// NormalizeStrategy maps an empty strategy to DefaultStrategy, so callers never
// have to branch on "legacy client" versus "strategy client".
func NormalizeStrategy(strategy string) string {
	if strategy == "" {
		return DefaultStrategy
	}
	return strategy
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

	// strategy attributes PnL to the strategy that submitted the order. It is
	// in-memory only: the persisted snapshot stores account positions, so after a
	// restart restored positions are attributed to DefaultStrategy.
	strategy map[StrategyKey]map[string]Position
}

// NewPositions builds an empty ledger.
func NewPositions() *Positions {
	return &Positions{
		pos:      make(map[PositionKey]Position),
		strategy: make(map[StrategyKey]map[string]Position),
	}
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

// SnapshotAll returns a deep copy of the whole ledger. Position contains only
// scalar fields, so copying each map entry is a deep copy: later fills or marks
// do not affect the returned map, and mutating the returned map does not affect
// the ledger. The returned map is never nil.
func (p *Positions) SnapshotAll() map[PositionKey]Position {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make(map[PositionKey]Position, len(p.pos))
	for key, pos := range p.pos {
		out[key] = pos
	}
	return out
}

// Restore replaces the ledger contents with entries. It uses replace semantics
// (not merge) because it exists for startup recovery: the persisted snapshot is
// the authoritative starting state, so any in-memory position that is not in
// entries must be dropped.
//
// Entries are validated individually. An entry is skipped when its account ID
// is zero, its symbol is empty, or its average cost or mark is negative. The
// closed-trade counters are reset because they are not part of a snapshot.
func (p *Positions) Restore(entries map[PositionKey]Position) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.pos = make(map[PositionKey]Position, len(entries))
	p.strategy = make(map[StrategyKey]map[string]Position)
	p.wins = 0
	p.closed = 0

	for key, pos := range entries {
		if key.AccountID == 0 || key.Symbol == "" || pos.AvgCost < 0 || pos.Mark < 0 {
			continue
		}
		p.pos[key] = pos

		sk := StrategyKey{AccountID: key.AccountID, Strategy: DefaultStrategy}
		bySymbol := p.strategy[sk]
		if bySymbol == nil {
			bySymbol = make(map[string]Position)
			p.strategy[sk] = bySymbol
		}
		bySymbol[key.Symbol] = pos
	}
}

// ApplyFill books one fill under DefaultStrategy. Quantity is always positive;
// side determines sign.
func (p *Positions) ApplyFill(accountID uint64, symbol string, side engine.Side, price, qty int64) {
	p.ApplyFillFor(accountID, DefaultStrategy, symbol, side, price, qty)
}

// ApplyFillFor books one fill for both the account position and the strategy
// attribution. Quantity is always positive; side determines sign.
func (p *Positions) ApplyFillFor(accountID uint64, strategy, symbol string, side engine.Side, price, qty int64) {
	if accountID == 0 || symbol == "" || qty <= 0 || price <= 0 {
		return
	}
	delta := qty
	if side == engine.SideSell {
		delta = -qty
	}
	strategy = NormalizeStrategy(strategy)

	p.mu.Lock()
	defer p.mu.Unlock()

	key := PositionKey{AccountID: accountID, Symbol: symbol}
	pos, closed, win := applyFillTo(p.pos[key], delta, price)
	p.pos[key] = pos
	if closed {
		p.closed++
		if win {
			p.wins++
		}
	}

	sk := StrategyKey{AccountID: accountID, Strategy: strategy}
	bySymbol := p.strategy[sk]
	if bySymbol == nil {
		bySymbol = make(map[string]Position)
		p.strategy[sk] = bySymbol
	}
	bySymbol[symbol], _, _ = applyFillTo(bySymbol[symbol], delta, price)
}

// applyFillTo updates one position with a signed delta and returns the new
// position plus whether the fill closed quantity and whether it was profitable.
func applyFillTo(pos Position, delta, price int64) (Position, bool, bool) {
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
		return pos, false, false

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

		newQty := satAdd(oldQty, delta)
		switch {
		case newQty == 0:
			pos.AvgCost = 0
		case (newQty > 0) != (oldQty > 0):
			pos.AvgCost = price
		}
		pos.Qty = newQty
		return pos, true, pnl > 0
	}
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
	for _, bySymbol := range p.strategy {
		pos, ok := bySymbol[symbol]
		if !ok {
			continue
		}
		pos.Mark = price
		bySymbol[symbol] = pos
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

// StrategyEquity returns realized plus unrealized PnL attributed to one
// strategy inside an account. The intraday loss and drawdown checks use it so a
// breach pauses only the strategy that caused it.
func (p *Positions) StrategyEquity(accountID uint64, strategy string) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	sk := StrategyKey{AccountID: accountID, Strategy: NormalizeStrategy(strategy)}
	var equity int64
	for _, pos := range p.strategy[sk] {
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

// PositionSnapshot is one position enriched with its owning account and the
// derived unrealized PnL.
type PositionSnapshot struct {
	AccountID  uint64
	Symbol     string
	Qty        int64
	AvgCost    int64
	Mark       int64
	Realized   int64
	Unrealized int64
}

// List returns positions for one account, or for every account when accountID
// is zero. Results are sorted by account then symbol so the wire order is
// stable across reads.
func (p *Positions) List(accountID uint64) []PositionSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]PositionSnapshot, 0, len(p.pos))
	for key, pos := range p.pos {
		if accountID != 0 && key.AccountID != accountID {
			continue
		}
		out = append(out, PositionSnapshot{
			AccountID:  key.AccountID,
			Symbol:     key.Symbol,
			Qty:        pos.Qty,
			AvgCost:    pos.AvgCost,
			Mark:       pos.Mark,
			Realized:   pos.Realized,
			Unrealized: pos.Unrealized(),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].AccountID != out[j].AccountID {
			return out[i].AccountID < out[j].AccountID
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// Realized returns closed PnL for one account, or for every account when
// accountID is zero.
func (p *Positions) Realized(accountID uint64) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var total int64
	for key, pos := range p.pos {
		if accountID != 0 && key.AccountID != accountID {
			continue
		}
		total = satAdd(total, pos.Realized)
	}
	return total
}

// Unrealized returns open PnL for one account, or for every account when
// accountID is zero.
func (p *Positions) Unrealized(accountID uint64) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var total int64
	for key, pos := range p.pos {
		if accountID != 0 && key.AccountID != accountID {
			continue
		}
		total = satAdd(total, pos.Unrealized())
	}
	return total
}

// OpenPositions counts non-zero positions for one account, or for every account
// when accountID is zero.
func (p *Positions) OpenPositions(accountID uint64) int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	n := 0
	for key, pos := range p.pos {
		if accountID != 0 && key.AccountID != accountID {
			continue
		}
		if pos.Qty != 0 {
			n++
		}
	}
	return n
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
