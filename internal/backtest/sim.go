package backtest

import (
	"errors"
	"fmt"
	"math/rand"

	"trading-system/internal/hft/engine"
	"trading-system/internal/strategy"
)

// Errors returned by the simulator.
var (
	ErrInvalidOrder = errors.New("backtest: invalid order")
	ErrFOKUnfilled  = errors.New("backtest: FOK cannot be fully filled")
	ErrUnknownOrder = errors.New("backtest: unknown or closed order")
)

// FillModel controls how much of the public liquidity a resting order can take.
//
// ParticipationBps caps fills at a share of each trade or book level.
// QueuePositionBps is the assumed place in the queue at the touch: 0 means the
// order is first (a touch always fills) and 10000 means last (never fills).
type FillModel struct {
	ParticipationBps int64
	QueuePositionBps int64
}

// Config configures a Simulator.
type Config struct {
	InitialCash int64
	Fee         FeeModel
	SlippageBps int64
	Fill        FillModel
	// MaxLevels caps how many book levels one aggressive order may sweep.
	MaxLevels int
	Seed      uint64
}

// DefaultConfig returns conservative simulation defaults: taker fees only, no
// slippage, and a 50% participation cap.
func DefaultConfig() Config {
	return Config{
		InitialCash: 0,
		Fee:         FeeModel{},
		SlippageBps: 0,
		Fill:        FillModel{ParticipationBps: 5000, QueuePositionBps: 5000},
		MaxLevels:   10,
		Seed:        1,
	}
}

// Stats aggregates order-level counters for the report.
type Stats struct {
	Submitted      int
	Rejected       int
	FilledOrders   int // fully filled orders
	CanceledOrders int
	CanceledQty    int64
	Fills          int
}

type simOrder struct {
	id     uint64
	symbol string
	side   engine.Side
	typ    engine.OrderType
	tif    engine.TimeInForce
	price  int64
	leaves int64
	filled int64
	status engine.OrderStatus
	open   bool
}

// Simulator implements strategy.Context against replayed market data.
//
// It is single-threaded by contract: the replay loop calls ApplyBook/ApplyTrade
// and the strategy callbacks it triggers. Do not share one simulator between
// goroutines.
type Simulator struct {
	cfg Config
	now int64

	books   map[string]*depthBook
	orders  map[uint64]*simOrder
	openIDs []uint64 // creation order, keeps matching deterministic

	seq uint64
	rng *rand.Rand

	pf     *Portfolio
	stats  Stats
	equity []EquityPoint
}

// NewSimulator builds a simulator.
func NewSimulator(cfg Config) *Simulator {
	if cfg.MaxLevels <= 0 {
		cfg.MaxLevels = DefaultConfig().MaxLevels
	}
	return &Simulator{
		cfg:    cfg,
		books:  make(map[string]*depthBook),
		orders: make(map[uint64]*simOrder),
		rng:    rand.New(rand.NewSource(int64(cfg.Seed))),
		pf:     NewPortfolio(cfg.InitialCash),
	}
}

// Now implements strategy.Context.
func (s *Simulator) Now() int64 { return s.now }

// SetNow advances the replay clock without applying an event.
func (s *Simulator) SetNow(ts int64) {
	if ts > s.now {
		s.now = ts
	}
}

// Portfolio exposes the simulated portfolio for reporting.
func (s *Simulator) Portfolio() *Portfolio { return s.pf }

// Stats returns a copy of the order counters.
func (s *Simulator) Stats() Stats { return s.stats }

// Equity returns the recorded equity curve. Callers must not modify it.
func (s *Simulator) Equity() []EquityPoint { return s.equity }

// Position implements strategy.Context.
func (s *Simulator) Position(symbol string) int64 { return s.pf.Position(symbol) }

// Book implements strategy.Context. The returned view is a copy.
func (s *Simulator) Book(symbol string) (strategy.BookView, bool) {
	b := s.books[symbol]
	if b == nil {
		return strategy.BookView{}, false
	}
	if len(b.bids) == 0 && len(b.asks) == 0 {
		return strategy.BookView{}, false
	}
	return strategy.BookView{
		Symbol: symbol,
		Bids:   append([]strategy.Level(nil), b.bids...),
		Asks:   append([]strategy.Level(nil), b.asks...),
		TS:     s.now,
	}, true
}

// OpenOrders implements strategy.Context. Orders are returned in creation order.
func (s *Simulator) OpenOrders(symbol string) []uint64 {
	var ids []uint64
	for _, id := range s.openIDs {
		o := s.orders[id]
		if o != nil && o.open && o.symbol == symbol {
			ids = append(ids, id)
		}
	}
	return ids
}

// ApplyBook replaces the book for symbol and fills resting orders that the new
// book has made marketable.
func (s *Simulator) ApplyBook(symbol string, bids, asks []strategy.Level, ts int64) {
	s.SetNow(ts)

	b := s.books[symbol]
	if b == nil {
		b = &depthBook{}
		s.books[symbol] = b
	}
	b.set(bids, asks)

	if mid, ok := b.mid(); ok {
		s.pf.Mark(symbol, mid)
	}

	s.matchRestingAgainstBook(symbol, b)
	s.recordEquity()
}

// ApplyTrade applies a public print: it marks the position and may fill resting
// orders that the print crossed or touched.
func (s *Simulator) ApplyTrade(t strategy.TradeView) {
	s.SetNow(t.TS)
	if t.Price > 0 {
		s.pf.Mark(t.Symbol, t.Price)
	}

	for _, id := range s.openIDs {
		o := s.orders[id]
		if o == nil || !o.open || o.symbol != t.Symbol {
			continue
		}
		s.fillFromTrade(o, t)
	}

	s.recordEquity()
}

func (s *Simulator) fillFromTrade(o *simOrder, t strategy.TradeView) {
	var crossed, touch bool
	if o.side == engine.SideBuy {
		crossed = t.Price < o.price
	} else {
		crossed = t.Price > o.price
	}
	touch = t.Price == o.price
	if !crossed && !touch {
		return
	}

	qty := s.marketQty(t.Qty)
	if qty <= 0 {
		return
	}
	if qty > o.leaves {
		qty = o.leaves
	}

	if touch && !s.touchFills() {
		return
	}

	s.fillResting(o, o.price, qty)
}

// touchFills decides whether a print exactly at the resting price reaches this
// order. It is a deterministic function of the seeded generator, which is why
// replaying the same data with the same seed reproduces the same fills.
func (s *Simulator) touchFills() bool {
	part := s.cfg.Fill.ParticipationBps
	queue := s.cfg.Fill.QueuePositionBps
	if part <= 0 || queue >= 10000 {
		return false
	}
	if queue < 0 {
		queue = 0
	}
	prob := part * (10000 - queue) / 10000
	if prob <= 0 {
		return false
	}
	if prob > 10000 {
		prob = 10000
	}
	return s.rng.Int63n(10000) < prob
}

func (s *Simulator) marketQty(tradeQty int64) int64 {
	if s.cfg.Fill.ParticipationBps <= 0 {
		return tradeQty
	}
	return tradeQty * s.cfg.Fill.ParticipationBps / 10000
}

func (s *Simulator) fillResting(o *simOrder, price, qty int64) {
	fee := s.cfg.Fee.Fee(price, qty, true)
	f := strategy.Fill{
		OrderID: o.id,
		Symbol:  o.symbol,
		Side:    o.side,
		Price:   price,
		Qty:     qty,
		Maker:   true,
		Fee:     fee,
		TS:      s.now,
	}
	s.pf.ApplyFill(f)
	s.stats.Fills++

	o.filled += qty
	o.leaves -= qty
	if o.leaves <= 0 {
		o.leaves = 0
		o.open = false
		o.status = engine.OrderStatusFilled
		s.stats.FilledOrders++
	}
}

// matchRestingAgainstBook fills resting orders that cross the new book at the
// opposite best price, which is how a stale quote gets lifted after a jump.
func (s *Simulator) matchRestingAgainstBook(symbol string, b *depthBook) {
	for _, id := range s.openIDs {
		o := s.orders[id]
		if o == nil || !o.open || o.symbol != symbol {
			continue
		}

		for o.leaves > 0 {
			var level strategy.Level
			var ok bool
			if o.side == engine.SideBuy {
				if level, ok = b.bestAsk(); !ok || o.price < level.Price {
					break
				}
			} else {
				if level, ok = b.bestBid(); !ok || o.price > level.Price {
					break
				}
			}

			qty := level.Qty
			if s.cfg.Fill.ParticipationBps > 0 {
				qty = qty * s.cfg.Fill.ParticipationBps / 10000
			}
			if qty <= 0 {
				break
			}
			if qty > o.leaves {
				qty = o.leaves
			}

			if o.side == engine.SideBuy {
				b.reduceAsk(level.Price, qty)
			} else {
				b.reduceBid(level.Price, qty)
			}
			s.fillResting(o, level.Price, qty)
		}
	}
}

// Submit implements strategy.Context.
func (s *Simulator) Submit(req strategy.OrderRequest) (strategy.SubmitResult, error) {
	if err := validateOrder(req); err != nil {
		s.stats.Rejected++
		return strategy.SubmitResult{}, err
	}

	s.stats.Submitted++
	o := &simOrder{
		id:     s.nextID(),
		symbol: req.Symbol,
		side:   req.Side,
		typ:    req.Type,
		tif:    req.TIF,
		price:  req.Price,
		leaves: req.Qty,
		status: engine.OrderStatusNew,
	}
	if o.typ == engine.OrderTypeMarket {
		o.price = 0
	}
	s.orders[o.id] = o

	// A market order without a book has nothing to trade against; a limit order
	// can still rest and wait for data.
	b := s.books[o.symbol]
	var planned []planFill
	var remaining int64 = req.Qty
	if b != nil {
		planned, remaining = b.plan(o.side, o.price, req.Qty,
			o.typ == engine.OrderTypeMarket, s.cfg.MaxLevels, s.cfg.Fill.ParticipationBps)
	}

	if o.tif == engine.TimeInForceFOK && remaining > 0 {
		o.status = engine.OrderStatusRejected
		o.leaves = 0
		s.stats.Rejected++
		return strategy.SubmitResult{OrderID: o.id, Status: o.status}, ErrFOKUnfilled
	}

	fills := make([]strategy.Fill, 0, len(planned))
	for _, p := range planned {
		price := s.fillPrice(o.side, o.price, p.price, o.typ == engine.OrderTypeMarket)
		fee := s.cfg.Fee.Fee(price, p.qty, false)
		f := strategy.Fill{
			OrderID: o.id,
			Symbol:  o.symbol,
			Side:    o.side,
			Price:   price,
			Qty:     p.qty,
			Maker:   false,
			Fee:     fee,
			TS:      s.now,
		}
		s.pf.ApplyFill(f)
		fills = append(fills, f)
		o.filled += p.qty
		o.leaves -= p.qty
		s.stats.Fills++
	}

	if len(planned) > 0 && b != nil {
		b.take(o.side, planned)
	}

	result := strategy.SubmitResult{
		OrderID: o.id,
		Filled:  o.filled,
		Status:  o.status,
		Fills:   fills,
	}

	if o.leaves == 0 {
		o.status = engine.OrderStatusFilled
		s.stats.FilledOrders++
		result.Status = o.status
		s.recordEquity()
		return result, nil
	}

	switch {
	case o.typ == engine.OrderTypeMarket || o.tif == engine.TimeInForceIOC:
		o.status = engine.OrderStatusCanceled
		if o.filled > 0 {
			o.status = engine.OrderStatusPartiallyFilled
		}
		s.stats.CanceledOrders++
		s.stats.CanceledQty += o.leaves
		result.Canceled = o.leaves
		o.leaves = 0

	default:
		o.open = true
		o.status = engine.OrderStatusNew
		if o.filled > 0 {
			o.status = engine.OrderStatusPartiallyFilled
		}
		s.openIDs = append(s.openIDs, o.id)
		result.Leaves = o.leaves
	}

	result.Status = o.status
	s.recordEquity()
	return result, nil
}

// Cancel implements strategy.Context.
func (s *Simulator) Cancel(orderID uint64) error {
	o := s.orders[orderID]
	if o == nil || !o.open {
		return ErrUnknownOrder
	}

	o.open = false
	o.status = engine.OrderStatusCanceled
	s.stats.CanceledOrders++
	s.stats.CanceledQty += o.leaves
	o.leaves = 0
	s.recordEquity()
	return nil
}

// fillPrice applies slippage and clamps the result to the limit price.
func (s *Simulator) fillPrice(side engine.Side, limit, level int64, market bool) int64 {
	bps := s.cfg.SlippageBps
	if bps <= 0 {
		return level
	}

	slip := level * bps / 10000
	if slip < 0 {
		slip = 0
	}

	price := level
	if side == engine.SideBuy {
		price = addSat(level, slip)
		if !market && limit > 0 && price > limit {
			price = limit
		}
	} else {
		price = subSat(level, slip)
		if !market && limit > 0 && price < limit {
			price = limit
		}
	}
	if price <= 0 {
		price = 1
	}
	return price
}

func (s *Simulator) nextID() uint64 {
	s.seq++
	return s.seq
}

func (s *Simulator) recordEquity() {
	eq := s.pf.Equity()
	if n := len(s.equity); n > 0 && s.equity[n-1].TS == s.now {
		s.equity[n-1].Equity = eq
		return
	}
	s.equity = append(s.equity, EquityPoint{TS: s.now, Equity: eq})
}

func validateOrder(req strategy.OrderRequest) error {
	switch {
	case req.Symbol == "":
		return fmt.Errorf("%w: symbol is required", ErrInvalidOrder)
	case req.Side != engine.SideBuy && req.Side != engine.SideSell:
		return fmt.Errorf("%w: invalid side", ErrInvalidOrder)
	case req.Type != engine.OrderTypeLimit && req.Type != engine.OrderTypeMarket:
		return fmt.Errorf("%w: invalid order type", ErrInvalidOrder)
	case req.TIF != engine.TimeInForceGTC &&
		req.TIF != engine.TimeInForceIOC &&
		req.TIF != engine.TimeInForceFOK:
		return fmt.Errorf("%w: invalid time in force", ErrInvalidOrder)
	case req.Qty <= 0:
		return fmt.Errorf("%w: qty must be positive", ErrInvalidOrder)
	case req.Type == engine.OrderTypeLimit && req.Price <= 0:
		return fmt.Errorf("%w: limit price must be positive", ErrInvalidOrder)
	}
	return nil
}

// depthBook is a mutable price-level book used only for simulation.
type depthBook struct {
	bids []strategy.Level // descending
	asks []strategy.Level // ascending
}

type planFill struct {
	idx   int
	price int64
	qty   int64
}

func (b *depthBook) set(bids, asks []strategy.Level) {
	b.bids = filterLevels(bids)
	b.asks = filterLevels(asks)
}

func filterLevels(in []strategy.Level) []strategy.Level {
	out := make([]strategy.Level, 0, len(in))
	for _, lv := range in {
		if lv.Price <= 0 || lv.Qty <= 0 {
			continue
		}
		out = append(out, lv)
	}
	return out
}

func (b *depthBook) bestBid() (strategy.Level, bool) {
	if len(b.bids) == 0 {
		return strategy.Level{}, false
	}
	return b.bids[0], true
}

func (b *depthBook) bestAsk() (strategy.Level, bool) {
	if len(b.asks) == 0 {
		return strategy.Level{}, false
	}
	return b.asks[0], true
}

func (b *depthBook) mid() (int64, bool) {
	bid, ok := b.bestBid()
	if !ok {
		return 0, false
	}
	ask, ok := b.bestAsk()
	if !ok || ask.Price < bid.Price {
		return 0, false
	}
	return bid.Price + (ask.Price-bid.Price)/2, true
}

// plan computes the aggressive fills without mutating the book.
func (b *depthBook) plan(side engine.Side, limit int64, qty int64, market bool, maxLevels int, participationBps int64) ([]planFill, int64) {
	levels := b.asks
	if side == engine.SideSell {
		levels = b.bids
	}

	remaining := qty
	fills := make([]planFill, 0, 4)

	for i, lv := range levels {
		if maxLevels > 0 && i >= maxLevels {
			break
		}
		if !market {
			if side == engine.SideBuy && lv.Price > limit {
				break
			}
			if side == engine.SideSell && lv.Price < limit {
				break
			}
		}

		avail := lv.Qty
		if participationBps > 0 {
			avail = lv.Qty * participationBps / 10000
		}
		if avail <= 0 {
			continue
		}
		if avail > remaining {
			avail = remaining
		}
		fills = append(fills, planFill{idx: i, price: lv.Price, qty: avail})
		remaining -= avail
		if remaining == 0 {
			break
		}
	}
	return fills, remaining
}

// take removes the planned quantity from the book.
func (b *depthBook) take(side engine.Side, fills []planFill) {
	touched := make(map[int]int64, len(fills))
	for _, f := range fills {
		touched[f.idx] += f.qty
	}

	levels := &b.asks
	if side == engine.SideSell {
		levels = &b.bids
	}

	out := (*levels)[:0]
	for i, lv := range *levels {
		if q, ok := touched[i]; ok {
			lv.Qty -= q
		}
		if lv.Qty > 0 {
			out = append(out, lv)
		}
	}
	*levels = out
}

func (b *depthBook) reduceAsk(price, qty int64) { b.asks = reduce(b.asks, price, qty) }

func (b *depthBook) reduceBid(price, qty int64) { b.bids = reduce(b.bids, price, qty) }

func reduce(levels []strategy.Level, price, qty int64) []strategy.Level {
	out := levels[:0]
	for _, lv := range levels {
		if lv.Price == price {
			lv.Qty -= qty
		}
		if lv.Qty > 0 {
			out = append(out, lv)
		}
	}
	return out
}

var _ strategy.Context = (*Simulator)(nil)
