package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func newFIFOTestMatcher() *Matcher {
	return NewMatcher(NewOrderBook("BTCUSDT", 1, 1))
}

func fifoTestOrder(id, account uint64, side Side, price int64, qty string, tif TimeInForce) Order {
	return Order{
		ID:        id,
		AccountID: account,
		Symbol:    "BTCUSDT",
		Side:      side,
		Type:      OrderTypeLimit,
		TIF:       tif,
		Price:     price,
		Quantity:  qty,
	}
}

func mustSubmitFIFO(t *testing.T, m *Matcher, order Order) SubmitResult {
	t.Helper()

	result, err := m.SubmitOrderDetailed(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitOrderDetailed(order %d) error = %v", order.ID, err)
	}
	return result
}

func requireBestAskQty(t *testing.T, m *Matcher, want int64) {
	t.Helper()

	best, ok := m.ob.GetBestAsk()
	if !ok {
		t.Fatalf("best ask missing, want qty %d", want)
	}
	if best.Qty != want {
		t.Fatalf("best ask qty = %d, want %d", best.Qty, want)
	}
}

func TestMatcherFIFOAtSamePrice(t *testing.T) {
	m := newFIFOTestMatcher()

	mustSubmitFIFO(t, m, fifoTestOrder(1, 101, SideSell, 100, "2", TimeInForceGTC))
	mustSubmitFIFO(t, m, fifoTestOrder(2, 102, SideSell, 100, "3", TimeInForceGTC))

	result := mustSubmitFIFO(t, m, fifoTestOrder(3, 103, SideBuy, 100, "4", TimeInForceGTC))

	if result.ExecutedQty != 4 || result.OpenLeaves != 0 {
		t.Fatalf("executed/open = %d/%d, want 4/0", result.ExecutedQty, result.OpenLeaves)
	}
	if len(result.Trades) != 2 {
		t.Fatalf("trades = %d, want 2", len(result.Trades))
	}

	first := result.Trades[0]
	if first.MakerOrder != 1 || first.Quantity != 2 || first.Price != 100 {
		t.Fatalf("first trade = maker %d qty %d px %d, want 1/2/100",
			first.MakerOrder, first.Quantity, first.Price)
	}
	second := result.Trades[1]
	if second.MakerOrder != 2 || second.Quantity != 2 || second.Price != 100 {
		t.Fatalf("second trade = maker %d qty %d px %d, want 2/2/100",
			second.MakerOrder, second.Quantity, second.Price)
	}

	// The second maker is only partially filled and still rests.
	maker := m.activeOrders[2]
	if maker == nil {
		t.Fatal("maker 2 missing from activeOrders")
	}
	if maker.Leaves != 1 || maker.Filled != 2 {
		t.Fatalf("maker 2 leaves/filled = %d/%d, want 1/2", maker.Leaves, maker.Filled)
	}
	if _, ok := m.activeOrders[1]; ok {
		t.Fatal("maker 1 still registered after being fully filled")
	}
	if queue := m.askQueues[100]; len(queue) != 1 || queue[0] != 2 {
		t.Fatalf("ask queue at 100 = %v, want [2]", queue)
	}
	requireBestAskQty(t, m, 1)
}

func TestMatcherMakerAttributionBuyTaker(t *testing.T) {
	m := newFIFOTestMatcher()

	mustSubmitFIFO(t, m, fifoTestOrder(10, 500, SideSell, 105, "2", TimeInForceGTC))
	result := mustSubmitFIFO(t, m, fifoTestOrder(20, 600, SideBuy, 105, "2", TimeInForceGTC))

	if len(result.Trades) != 1 {
		t.Fatalf("trades = %d, want 1", len(result.Trades))
	}
	trade := result.Trades[0]
	if trade.BuyOrderID != 20 || trade.SellOrderID != 10 {
		t.Fatalf("buy/sell order ids = %d/%d, want 20/10", trade.BuyOrderID, trade.SellOrderID)
	}
	if trade.MakerOrder != 10 {
		t.Fatalf("maker order = %d, want 10", trade.MakerOrder)
	}
	if trade.MakerAccountID != 500 {
		t.Fatalf("maker account = %d, want 500", trade.MakerAccountID)
	}
	if trade.MakerSide != SideSell {
		t.Fatalf("maker side = %d, want %d", trade.MakerSide, SideSell)
	}
}

func TestMatcherMakerAttributionSellTaker(t *testing.T) {
	m := newFIFOTestMatcher()

	mustSubmitFIFO(t, m, fifoTestOrder(11, 501, SideBuy, 95, "2", TimeInForceGTC))
	result := mustSubmitFIFO(t, m, fifoTestOrder(21, 601, SideSell, 95, "2", TimeInForceGTC))

	if len(result.Trades) != 1 {
		t.Fatalf("trades = %d, want 1", len(result.Trades))
	}
	trade := result.Trades[0]
	if trade.BuyOrderID != 11 || trade.SellOrderID != 21 {
		t.Fatalf("buy/sell order ids = %d/%d, want 11/21", trade.BuyOrderID, trade.SellOrderID)
	}
	if trade.MakerOrder != 11 {
		t.Fatalf("maker order = %d, want 11", trade.MakerOrder)
	}
	if trade.MakerAccountID != 501 {
		t.Fatalf("maker account = %d, want 501", trade.MakerAccountID)
	}
	if trade.MakerSide != SideBuy {
		t.Fatalf("maker side = %d, want %d", trade.MakerSide, SideBuy)
	}
}

func TestMatcherPartialMakerFillKeepsHead(t *testing.T) {
	m := newFIFOTestMatcher()

	mustSubmitFIFO(t, m, fifoTestOrder(1, 11, SideSell, 100, "5", TimeInForceGTC))
	mustSubmitFIFO(t, m, fifoTestOrder(2, 22, SideSell, 100, "5", TimeInForceGTC))

	result := mustSubmitFIFO(t, m, fifoTestOrder(3, 33, SideBuy, 100, "2", TimeInForceGTC))
	if len(result.Trades) != 1 || result.Trades[0].MakerOrder != 1 || result.Trades[0].Quantity != 2 {
		t.Fatalf("trades = %+v, want one fill of 2 against maker 1", result.Trades)
	}

	maker := m.activeOrders[1]
	if maker == nil {
		t.Fatal("maker 1 removed after a partial fill")
	}
	if maker.Leaves != 3 || maker.Filled != 2 {
		t.Fatalf("maker 1 leaves/filled = %d/%d, want 3/2", maker.Leaves, maker.Filled)
	}
	if queue := m.askQueues[100]; len(queue) != 2 || queue[0] != 1 || queue[1] != 2 {
		t.Fatalf("ask queue at 100 = %v, want [1 2]", queue)
	}
	requireBestAskQty(t, m, 8)

	// The next taker finishes maker 1 exactly; maker 2 becomes the head.
	result = mustSubmitFIFO(t, m, fifoTestOrder(4, 44, SideBuy, 100, "3", TimeInForceGTC))
	if len(result.Trades) != 1 || result.Trades[0].MakerOrder != 1 || result.Trades[0].Quantity != 3 {
		t.Fatalf("trades = %+v, want one fill of 3 against maker 1", result.Trades)
	}
	if _, ok := m.activeOrders[1]; ok {
		t.Fatal("fully filled maker 1 still registered")
	}
	if queue := m.askQueues[100]; len(queue) != 1 || queue[0] != 2 {
		t.Fatalf("ask queue at 100 = %v, want [2]", queue)
	}
	requireBestAskQty(t, m, 5)
}

func TestMatcherFullyFilledMakerRemoved(t *testing.T) {
	m := newFIFOTestMatcher()

	mustSubmitFIFO(t, m, fifoTestOrder(1, 11, SideSell, 100, "2", TimeInForceGTC))
	result := mustSubmitFIFO(t, m, fifoTestOrder(2, 22, SideBuy, 100, "2", TimeInForceGTC))

	if result.ExecutedQty != 2 || result.FinalStatus != OrderStatusFilled {
		t.Fatalf("executed/status = %d/%d, want 2/%d",
			result.ExecutedQty, result.FinalStatus, OrderStatusFilled)
	}
	if len(m.activeOrders) != 0 {
		t.Fatalf("activeOrders = %d, want 0", len(m.activeOrders))
	}
	if len(m.askQueues) != 0 {
		t.Fatalf("askQueues = %v, want empty", m.askQueues)
	}
	best, ok := m.ob.GetBestAsk()
	if ok {
		t.Fatalf("best ask still present = %d/%d, want empty book", best.Price, best.Qty)
	}
}

func TestMatcherFOKInsufficientLiquidityLeavesStateUnchanged(t *testing.T) {
	m := newFIFOTestMatcher()

	mustSubmitFIFO(t, m, fifoTestOrder(1, 11, SideSell, 100, "2", TimeInForceGTC))
	mustSubmitFIFO(t, m, fifoTestOrder(2, 12, SideSell, 101, "1", TimeInForceGTC))

	result, err := m.SubmitOrderDetailed(
		context.Background(),
		fifoTestOrder(3, 13, SideBuy, 101, "4", TimeInForceFOK),
	)
	if !errors.Is(err, ErrFOKNotFillable) {
		t.Fatalf("FOK error = %v, want ErrFOKNotFillable", err)
	}
	if len(result.Trades) != 0 || result.ExecutedQty != 0 {
		t.Fatalf("FOK result = %+v, want zero result", result)
	}

	// Book and queues must be untouched.
	best, ok := m.ob.GetBestAsk()
	if !ok || best.Price != 100 || best.Qty != 2 {
		t.Fatalf("best ask = %+v, %v; want price 100 qty 2", best, ok)
	}
	if queue := m.askQueues[100]; len(queue) != 1 || queue[0] != 1 {
		t.Fatalf("ask queue at 100 = %v, want [1]", queue)
	}
	if queue := m.askQueues[101]; len(queue) != 1 || queue[0] != 2 {
		t.Fatalf("ask queue at 101 = %v, want [2]", queue)
	}
	for _, id := range []uint64{1, 2} {
		maker := m.activeOrders[id]
		if maker == nil || maker.Leaves != mustQty(t, maker.Quantity) || maker.Filled != 0 {
			t.Fatalf("maker %d = %+v, want untouched resting order", id, maker)
		}
	}

	// A FOK that can be filled across two levels still executes in full.
	result = mustSubmitFIFO(t, m, fifoTestOrder(4, 14, SideBuy, 101, "3", TimeInForceFOK))
	if result.ExecutedQty != 3 || result.FinalStatus != OrderStatusFilled {
		t.Fatalf("executed/status = %d/%d, want 3/%d",
			result.ExecutedQty, result.FinalStatus, OrderStatusFilled)
	}
	if len(result.Trades) != 2 {
		t.Fatalf("trades = %d, want 2", len(result.Trades))
	}
	if len(m.activeOrders) != 0 || len(m.askQueues) != 0 {
		t.Fatalf("book state after fill = %d orders / %v queues, want empty",
			len(m.activeOrders), m.askQueues)
	}
}

func mustQty(t *testing.T, quantity string) int64 {
	t.Helper()

	qty, err := ParseQuantity(quantity, 1)
	if err != nil {
		t.Fatalf("ParseQuantity(%q) error = %v", quantity, err)
	}
	return qty
}

func TestMatcherAggregateFallbackWithoutFIFOEntries(t *testing.T) {
	m := newFIFOTestMatcher()

	// Seed the aggregate book directly, bypassing the matcher and its queues.
	m.ob.asks.Add(100, 5)

	result := mustSubmitFIFO(t, m, fifoTestOrder(1, 11, SideBuy, 100, "3", TimeInForceGTC))
	if len(result.Trades) != 1 {
		t.Fatalf("trades = %d, want 1", len(result.Trades))
	}

	trade := result.Trades[0]
	if trade.Price != 100 || trade.Quantity != 3 {
		t.Fatalf("trade px/qty = %d/%d, want 100/3", trade.Price, trade.Quantity)
	}
	if trade.BuyOrderID != 1 || trade.SellOrderID != 0 {
		t.Fatalf("buy/sell ids = %d/%d, want 1/0", trade.BuyOrderID, trade.SellOrderID)
	}
	if trade.MakerOrder != 0 || trade.MakerAccountID != 0 || trade.MakerSide != 0 {
		t.Fatalf("aggregate trade attribution = %d/%d/%d, want 0/0/0",
			trade.MakerOrder, trade.MakerAccountID, trade.MakerSide)
	}

	best, ok := m.ob.GetBestAsk()
	if !ok || best.Qty != 2 {
		t.Fatalf("best ask = %+v, %v; want qty 2", best, ok)
	}
}

func TestMatcherTakerRemainderRestsWithAttribution(t *testing.T) {
	m := newFIFOTestMatcher()

	mustSubmitFIFO(t, m, fifoTestOrder(1, 11, SideSell, 100, "2", TimeInForceGTC))
	result := mustSubmitFIFO(t, m, fifoTestOrder(2, 22, SideBuy, 100, "5", TimeInForceGTC))

	if result.ExecutedQty != 2 || result.OpenLeaves != 3 {
		t.Fatalf("executed/open = %d/%d, want 2/3", result.ExecutedQty, result.OpenLeaves)
	}
	if result.FinalStatus != OrderStatusPartiallyFilled {
		t.Fatalf("status = %d, want partially filled", result.FinalStatus)
	}

	resting := m.activeOrders[2]
	if resting == nil {
		t.Fatal("taker remainder was not registered as resting order")
	}
	if resting.Leaves != 3 || resting.Filled != 2 {
		t.Fatalf("resting leaves/filled = %d/%d, want 3/2", resting.Leaves, resting.Filled)
	}
	if resting.AccountID != 22 || resting.Side != SideBuy {
		t.Fatalf("resting identity = account %d side %d, want 22/%d",
			resting.AccountID, resting.Side, SideBuy)
	}
	if resting.Status != OrderStatusPartiallyFilled {
		t.Fatalf("resting status = %d, want partially filled", resting.Status)
	}
	if queue := m.bidQueues[100]; len(queue) != 1 || queue[0] != 2 {
		t.Fatalf("bid queue at 100 = %v, want [2]", queue)
	}

	// A later sell taker fills the resting buy and sees its attribution.
	result = mustSubmitFIFO(t, m, fifoTestOrder(3, 33, SideSell, 100, "3", TimeInForceGTC))
	if len(result.Trades) != 1 {
		t.Fatalf("trades = %d, want 1", len(result.Trades))
	}
	trade := result.Trades[0]
	if trade.BuyOrderID != 2 || trade.SellOrderID != 3 {
		t.Fatalf("buy/sell ids = %d/%d, want 2/3", trade.BuyOrderID, trade.SellOrderID)
	}
	if trade.MakerOrder != 2 || trade.MakerAccountID != 22 || trade.MakerSide != SideBuy {
		t.Fatalf("maker attribution = %d/%d/%d, want 2/22/%d",
			trade.MakerOrder, trade.MakerAccountID, trade.MakerSide, SideBuy)
	}
	if len(m.activeOrders) != 0 || len(m.bidQueues) != 0 {
		t.Fatalf("book state after fill = %d orders / %v queues, want empty",
			len(m.activeOrders), m.bidQueues)
	}
	if _, ok := m.ob.GetBestBid(); ok {
		t.Fatal("best bid still present, want empty book")
	}
}

func TestMatcherIOCRemainderIsCanceled(t *testing.T) {
	m := newFIFOTestMatcher()

	mustSubmitFIFO(t, m, fifoTestOrder(1, 11, SideSell, 100, "1", TimeInForceGTC))
	result := mustSubmitFIFO(t, m, fifoTestOrder(2, 22, SideBuy, 100, "3", TimeInForceIOC))

	if result.ExecutedQty != 1 || result.CanceledQty != 2 || result.OpenLeaves != 0 {
		t.Fatalf("executed/canceled/open = %d/%d/%d, want 1/2/0",
			result.ExecutedQty, result.CanceledQty, result.OpenLeaves)
	}
	if result.FinalStatus != OrderStatusPartiallyFilled {
		t.Fatalf("status = %d, want partially filled", result.FinalStatus)
	}
	if _, ok := m.activeOrders[2]; ok {
		t.Fatal("IOC remainder rested as an active order")
	}
	if len(m.bidQueues) != 0 {
		t.Fatalf("bid queues = %v, want empty", m.bidQueues)
	}
}

func TestMatcherCancelRemovesQueueEntry(t *testing.T) {
	m := newFIFOTestMatcher()

	mustSubmitFIFO(t, m, fifoTestOrder(1, 11, SideSell, 100, "2", TimeInForceGTC))
	mustSubmitFIFO(t, m, fifoTestOrder(2, 12, SideSell, 100, "3", TimeInForceGTC))

	canceled, err := m.Cancel(context.Background(), 1)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if canceled.Status != OrderStatusCanceled || canceled.Leaves != 0 {
		t.Fatalf("canceled order = %+v, want canceled with zero leaves", canceled)
	}
	if queue := m.askQueues[100]; len(queue) != 1 || queue[0] != 2 {
		t.Fatalf("ask queue at 100 = %v, want [2]", queue)
	}
	requireBestAskQty(t, m, 3)

	// The remaining order still matches normally.
	result := mustSubmitFIFO(t, m, fifoTestOrder(3, 13, SideBuy, 100, "3", TimeInForceGTC))
	if len(result.Trades) != 1 || result.Trades[0].MakerOrder != 2 {
		t.Fatalf("trades = %+v, want one fill against maker 2", result.Trades)
	}
	if _, err := m.Cancel(context.Background(), 1); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("second Cancel() error = %v, want ErrOrderNotFound", err)
	}
}

// Concurrent submits and cancels must keep the aggregate book and the FIFO
// queues consistent. Run with -race.
func TestMatcherConcurrentSubmitAndCancel(t *testing.T) {
	m := newFIFOTestMatcher()
	ctx := context.Background()

	const orders = 8

	var wg sync.WaitGroup
	for i := 0; i < orders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			order := fifoTestOrder(uint64(100+i), uint64(i+1), SideBuy, 90, "1", TimeInForceGTC)
			if _, err := m.SubmitOrderDetailed(ctx, order); err != nil {
				t.Errorf("submit %d: %v", order.ID, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < orders; i += 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := m.Cancel(ctx, uint64(100+i)); err != nil {
				t.Errorf("cancel %d: %v", 100+i, err)
			}
		}(i)
	}
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	queue := m.bidQueues[90]
	if len(queue) != orders/2 {
		t.Fatalf("bid queue length = %d, want %d", len(queue), orders/2)
	}
	if got := len(m.activeOrders); got != orders/2 {
		t.Fatalf("active orders = %d, want %d", got, orders/2)
	}

	var queuedQty int64
	for _, id := range queue {
		order := m.activeOrders[id]
		if order == nil {
			t.Fatalf("queue references missing order %d", id)
		}
		queuedQty += order.Leaves
	}
	if got := m.ob.bids.Get(90); got != queuedQty {
		t.Fatalf("aggregate qty at 90 = %d, want %d", got, queuedQty)
	}
}
