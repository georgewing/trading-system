package engine

// Paper matcher: LIMIT / MARKET with GTC / IOC / FOK.
// Resting GTC orders are tracked in per-price FIFO queues so every fill is
// attributed to the actual maker order and account. The aggregate order book
// still provides best prices and price levels; the queues order the resting
// orders inside a level (time priority).
//
// Matcher.mu serializes all book and queue mutations.

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"trading-system/internal/infrastructure/datastructures"
)

var (
	// ErrFOKNotFillable is returned when an FOK order cannot be filled in full.
	ErrFOKNotFillable = errors.New("FOK: cannot fully fill")
	// ErrOrderNotFound reports a cancel for an order the book does not hold.
	ErrOrderNotFound = errors.New("order not found")
	// ErrOrderNotCancelable reports a resting order with no leaves left.
	ErrOrderNotCancelable = errors.New("order is not cancelable")
)

// Matcher is the in-process matching engine for one symbol.
type Matcher struct {
	ob           *OrderBook
	mu           sync.Mutex         // protects activeOrders, queues and book mutations
	activeOrders map[uint64]*Order  // resting orders by ID
	bidQueues    map[int64][]uint64 // resting bid IDs, FIFO per price
	askQueues    map[int64][]uint64 // resting ask IDs, FIFO per price
	tradeSeq     uint64             // trade ID generator
}

// SubmitResult表示一次提交后的最终执行结果（供 execution report 使用）。
type SubmitResult struct {
	Trades      []Trade
	ExecutedQty int64
	OpenLeaves  int64
	CanceledQty int64 // 被策略取消的量（IOC/MARKET 可能 >0）
	FinalStatus OrderStatus

	// MakerReports carries one execution report per resting order that this
	// order filled. Without them the position ledger would only ever see the
	// taker side, so a user whose bid gets hit would never show a position.
	MakerReports []ExecReport
}

func NewMatcher(ob *OrderBook) *Matcher {
	return &Matcher{
		ob:           ob,
		activeOrders: make(map[uint64]*Order),
		bidQueues:    make(map[int64][]uint64),
		askQueues:    make(map[int64][]uint64),
	}
}

// Cancel removes a resting order from the book and returns the canceled copy.
//
// It shares the matcher mutex with Match, so it is safe to call from an HTTP
// handler while the pipeline is matching. Removing the aggregate quantity with
// the order's own leaves leaves other orders at the same price untouched.
func (m *Matcher) Cancel(ctx context.Context, orderID uint64) (Order, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Order{}, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	order, ok := m.activeOrders[orderID]
	if !ok {
		return Order{}, ErrOrderNotFound
	}
	if order.Leaves <= 0 {
		delete(m.activeOrders, orderID)
		m.dropQueueEntryLocked(order.Side, order.Price, orderID)
		return Order{}, ErrOrderNotCancelable
	}

	m.removeAggregateLocked(order.Side, order.Price, order.Leaves)
	m.dropQueueEntryLocked(order.Side, order.Price, orderID)

	canceled := *order
	canceled.Status = OrderStatusCanceled
	canceled.Leaves = 0
	delete(m.activeOrders, orderID)

	return canceled, nil
}

// RestingOrderCount reports how many orders are currently resting in the book.
func (m *Matcher) RestingOrderCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.activeOrders)
}

// Match executes the taker order against the opposite side and returns the
// generated trades plus the taker quantity that could not be filled.
// FOK orders are rejected up front when the book cannot fill them in full.
func (m *Matcher) Match(ctx context.Context, order Order) ([]Trade, int64, error) {
	trades, remaining, _, err := m.match(ctx, order)
	return trades, remaining, err
}

// match is the internal form of Match. It also returns the execution reports
// generated for the resting orders this order consumed.
func (m *Matcher) match(ctx context.Context, order Order) ([]Trade, int64, []ExecReport, error) {
	if ctx.Err() != nil {
		return nil, 0, nil, ctx.Err()
	}

	// order.Price is already an integer price unit.
	priceInt := order.Price

	qtyInt, err := ParseQuantity(order.Quantity, m.ob.quantityPrecision)
	if err != nil {
		return nil, 0, nil, err
	}
	if qtyInt <= 0 {
		return nil, 0, nil, errors.New("quantity must be positive")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// FOK is all-or-nothing: reject before touching book or queues.
	if order.TIF == TimeInForceFOK && !m.hasLiquidityLocked(order, priceInt, qtyInt) {
		return nil, 0, nil, ErrFOKNotFillable
	}

	var trades []Trade
	var makerReports []ExecReport
	remainingQty := qtyInt

	for remainingQty > 0 {
		best, ok := m.bestOpposite(order.Side)
		if !ok {
			break // no resting orders on the opposite side
		}

		// MARKET orders cross at any price.
		if order.Type != OrderTypeMarket && !isCrossing(order.Side, priceInt, best.Price) {
			break
		}

		price := best.Price
		var filled int64
		if len(m.restingQueue(order.Side)[price]) > 0 {
			filled = m.matchQueueLocked(order, price, remainingQty, &trades, &makerReports)
		}
		if filled == 0 {
			// Aggregate fallback for levels without FIFO entries, for example a
			// book seeded directly by tests. No maker attribution is possible.
			fillQty := min(remainingQty, best.Qty)
			if fillQty <= 0 {
				break // defensive: never spin on an empty level
			}
			trades = append(trades, m.newTradeLocked(order, nil, price, fillQty))
			m.removeAggregateLocked(oppositeSide(order.Side), price, fillQty)
			filled = fillQty
		}
		remainingQty -= filled
	}

	return trades, remainingQty, makerReports, nil
}

// bestOpposite returns the best level on the side the taker consumes.
func (m *Matcher) bestOpposite(takerSide Side) (*datastructures.PriceLevel, bool) {
	if takerSide == SideBuy {
		return m.ob.GetBestAsk()
	}
	return m.ob.GetBestBid()
}

// restingQueue returns the FIFO queues of the side the taker consumes.
func (m *Matcher) restingQueue(takerSide Side) map[int64][]uint64 {
	if takerSide == SideBuy {
		return m.askQueues
	}
	return m.bidQueues
}

// matchQueueLocked fills the taker against the FIFO queue at price and returns
// the filled quantity. A partially filled maker stays at the head; a fully
// filled maker is popped and dropped from activeOrders.
func (m *Matcher) matchQueueLocked(taker Order, price, remaining int64, trades *[]Trade, makerReports *[]ExecReport) int64 {
	queues := m.restingQueue(taker.Side)
	queue := queues[price]

	var filled int64
	for len(queue) > 0 && remaining > 0 {
		makerID := queue[0]
		maker, ok := m.activeOrders[makerID]
		if !ok {
			queue = queue[1:] // stale entry
			continue
		}
		if maker.Leaves <= 0 {
			delete(m.activeOrders, makerID)
			queue = queue[1:]
			continue
		}

		fillQty := min(remaining, maker.Leaves)
		maker.Leaves -= fillQty
		maker.Filled += fillQty
		if maker.Leaves == 0 {
			maker.Status = OrderStatusFilled
			delete(m.activeOrders, makerID)
			queue = queue[1:] // fully filled maker leaves the head
		} else {
			maker.Status = OrderStatusPartiallyFilled
		}

		trade := m.newTradeLocked(taker, maker, price, fillQty)
		*trades = append(*trades, trade)
		*makerReports = append(*makerReports, execReportForMaker(maker, trade, price, fillQty))
		m.removeAggregateLocked(oppositeSide(taker.Side), price, fillQty)
		filled += fillQty
		remaining -= fillQty
	}

	if len(queue) == 0 {
		delete(queues, price)
	} else {
		queues[price] = queue
	}
	return filled
}

// execReportForMaker renders the execution report for a resting order that a
// taker just filled. Maker.Filled/Leaves already reflect the fill.
func execReportForMaker(maker *Order, trade Trade, price, fillQty int64) ExecReport {
	tradeCopy := trade
	return ExecReport{
		OrderID:       maker.ID,
		ClientOrderID: maker.ClientOrderID,
		AccountID:     maker.AccountID,
		Symbol:        maker.Symbol,
		Side:          maker.Side,
		Status:        maker.Status,
		Filled:        maker.Filled,
		Leaves:        maker.Leaves,
		LastPx:        price,
		LastQty:       fillQty,
		CumQty:        maker.Filled,
		LeavesQty:     maker.Leaves,
		OrderPx:       maker.Price,
		OrderType:     maker.Type,
		TIF:           maker.TIF,
		Quantity:      maker.Quantity,
		Trade:         &tradeCopy,
		Timestamp:     time.Now().UnixNano(),
	}
}

// newTradeLocked builds a trade. A nil maker means aggregate matching, where
// the resting order is unknown.
func (m *Matcher) newTradeLocked(taker Order, maker *Order, price, qty int64) Trade {
	tradeID, _ := strconv.ParseUint(generateTradeID(&m.tradeSeq)[2:], 10, 64)
	trade := Trade{
		ID:        tradeID,
		Symbol:    m.ob.symbol,
		Price:     price,
		Quantity:  qty,
		Timestamp: time.Now().UnixNano(),
	}

	if maker == nil {
		if taker.Side == SideBuy {
			trade.BuyOrderID = taker.ID
		} else {
			trade.SellOrderID = taker.ID
		}
		return trade
	}

	trade.MakerOrder = maker.ID
	trade.MakerAccountID = maker.AccountID
	trade.MakerSide = maker.Side
	if taker.Side == SideBuy {
		trade.BuyOrderID = taker.ID
		trade.SellOrderID = maker.ID
	} else {
		trade.BuyOrderID = maker.ID
		trade.SellOrderID = taker.ID
	}
	return trade
}

// hasLiquidityLocked reports whether the opposite side can fill need lots at
// crossing prices. Aggregate level quantity is fully reachable (FIFO first,
// then the aggregate fallback), so summing it is a safe FOK pre-check.
func (m *Matcher) hasLiquidityLocked(order Order, limit, need int64) bool {
	var it datastructures.Iterator
	if order.Side == SideBuy {
		it = m.ob.asks.Iterate() // ascending: best ask first
	} else {
		it = m.ob.bids.Iterate() // ascending: best bid last
	}

	var total int64
	for it.Next() {
		level := it.Current()
		if order.Type != OrderTypeMarket {
			if order.Side == SideBuy && level.Price > limit {
				break
			}
			if order.Side == SideSell && level.Price < limit {
				continue
			}
		}
		total += level.Qty
		if total >= need {
			return true
		}
	}
	return false
}

// restOrderLocked stores the unfilled remainder as a resting order, adds it to
// the aggregate book and appends it to the FIFO queue at its price.
func (m *Matcher) restOrderLocked(order Order, leaves, filled int64) {
	resting := order
	resting.Leaves = leaves
	resting.Filled = filled
	resting.Status = OrderStatusNew
	if filled > 0 {
		resting.Status = OrderStatusPartiallyFilled
	}

	if order.Side == SideBuy {
		m.ob.bids.Add(order.Price, leaves)
		m.bidQueues[order.Price] = append(m.bidQueues[order.Price], order.ID)
	} else {
		m.ob.asks.Add(order.Price, leaves)
		m.askQueues[order.Price] = append(m.askQueues[order.Price], order.ID)
	}
	m.activeOrders[order.ID] = &resting
}

// dropQueueEntryLocked removes orderID from the FIFO queue at price and deletes
// the price key when the queue becomes empty.
func (m *Matcher) dropQueueEntryLocked(side Side, price int64, orderID uint64) {
	queues := m.bidQueues
	if side == SideSell {
		queues = m.askQueues
	}

	queue := queues[price]
	for i, id := range queue {
		if id != orderID {
			continue
		}
		queue = append(queue[:i], queue[i+1:]...)
		break
	}

	if len(queue) == 0 {
		delete(queues, price)
		return
	}
	queues[price] = queue
}

// removeAggregateLocked deducts filled quantity from the given resting side.
func (m *Matcher) removeAggregateLocked(side Side, price, qty int64) {
	if side == SideBuy {
		m.ob.bids.Remove(price, qty)
		return
	}
	m.ob.asks.Remove(price, qty)
}

func oppositeSide(side Side) Side {
	if side == SideBuy {
		return SideSell
	}
	return SideBuy
}

func sumExecutedQty(trades []Trade) int64 {
	var n int64
	for _, trade := range trades {
		n += trade.Quantity
	}
	return n
}

// ReferencePrice returns the current best bid, or the best ask when no bid
// exists. It returns 0 when the book is empty.
func (m *Matcher) ReferencePrice() int64 {
	if best, ok := m.ob.GetBestBid(); ok {
		return best.Price
	}
	if best, ok := m.ob.GetBestAsk(); ok {
		return best.Price
	}
	return 0
}

// SubmitOrder 提交订单并执行撮合（生产主入口）
func (m *Matcher) SubmitOrder(ctx context.Context, order Order) ([]Trade, error) {
	result, err := m.SubmitOrderDetailed(ctx, order)
	if err != nil {
		return nil, err
	}
	return result.Trades, nil
}

// SubmitOrderDetailed 返回生产执行回报所需的完整语义。
func (m *Matcher) SubmitOrderDetailed(ctx context.Context, order Order) (SubmitResult, error) {
	trades, remainingQty, makerReports, err := m.match(ctx, order)
	if err != nil {
		return SubmitResult{}, err
	}

	executed := sumExecutedQty(trades)
	result := SubmitResult{
		Trades:       trades,
		ExecutedQty:  executed,
		MakerReports: makerReports,
	}

	// 处理剩余订单
	if remainingQty > 0 {
		switch {
		case order.Type == OrderTypeMarket || order.TIF == TimeInForceIOC:
			// MARKET / IOC：剩余全部取消，不挂单
			// 可在这里发布 Cancel 事件
			result.CanceledQty = remainingQty
			if executed > 0 {
				result.FinalStatus = OrderStatusPartiallyFilled
			} else {
				result.FinalStatus = OrderStatusCanceled
			}
			return result, nil
		case order.TIF == TimeInForceFOK:
			// Match pre-checks FOK liquidity, so this path is unreachable unless
			// the book changed between the check and the fill.
			return SubmitResult{}, ErrFOKNotFillable
		default: // LIMIT + GTC -> rest the remainder
			m.mu.Lock()
			m.restOrderLocked(order, remainingQty, executed)
			m.mu.Unlock()

			result.OpenLeaves = remainingQty
			if executed > 0 {
				result.FinalStatus = OrderStatusPartiallyFilled
			} else {
				result.FinalStatus = OrderStatusNew
			}

			return result, nil
		}
	}

	if executed > 0 {
		result.FinalStatus = OrderStatusFilled
	} else {
		result.FinalStatus = OrderStatusNew
	}

	return result, nil
}

// isCrossing 判断是否价格交叉
func isCrossing(takerSide Side, takerPrice, bestOpposite int64) bool {
	if takerSide == SideBuy {
		return takerPrice >= bestOpposite // 买单 >= 最佳卖价
	}
	return takerPrice <= bestOpposite // 卖单 <= 最佳买价
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func generateTradeID(seq *uint64) string {
	id := atomic.AddUint64(seq, 1)
	return "T-" + strconv.FormatUint(id, 10)
}
