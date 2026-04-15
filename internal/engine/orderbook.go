package engine

import (
	"context"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"trading-system/internal/infrastructure/datastructures"
	"trading-system/pkg/ringbuffer"
)

type Order struct {
	ID       string
	Side     string
	Price    float64
	Quantity string
	// 可根据业务扩展：ClientID, OrderType, TimeInForce 等
}

type OrderBook struct {
	symbol            string
	pricePrecision    int64 // 价格精度倍数（如 BTC/USDT 用 100000000 表示 8 位小数）
	quantityPrecision int64 // 数量精度倍数（如 100000000 表示 8 位小数）

	bids     *datastructures.SkipList
	asks     *datastructures.SkipList
	eventBus *ringbuffer.RingBuffer[OrderEvent]
	seq      uint64
}

type OrderEvent struct {
	OrderID   string
	Side      string
	Price     float64
	Quantity  string
	TimeStamp int64
	EventType string
}

// NewOrderBook 创建订单簿（必须传入精度参数，不同交易对精度不同）
func NewOrderBook(symbol string, pricePrecision, quantityPrecision int64) *OrderBook {
	if pricePrecision <= 0 || quantityPrecision <= 0 {
		panic("pricePrecision and quantityPrecision must be positive") // 生产中可替换为错误返回
	}
	return &OrderBook{
		symbol:            symbol,
		pricePrecision:    pricePrecision,
		quantityPrecision: quantityPrecision,
		bids:              datastructures.NewSkipList(),
		asks:              datastructures.NewSkipList(),
		eventBus:          ringbuffer.New[OrderEvent](1 << 20), // 1M 事件缓冲
	}
}

// AddOrder 订单进入主流程
func (ob *OrderBook) AddOrder(ctx context.Context, order Order) {
	atomic.AddUint64(&ob.seq, 1)

	event := &OrderEvent{
		OrderID:   order.ID,
		Side:      order.Side,
		Price:     order.Price,
		Quantity:  order.Quantity,
		TimeStamp: time.Now().UnixNano(),
		EventType: "ADD",
	}
	ob.eventBus.Enqueue(*event)

	priceInt := int64(math.Round(order.Price * float64(ob.pricePrecision)))

	qtyFloat, err := strconv.ParseFloat(order.Quantity, 64)
	if err != nil {
		// 生产环境建议：记录监控日志 + 拒绝订单，这里简化直接返回
		// 可扩展为返回 error 或通过事件总线发布错误事件
		return
	}
	qtyInt := int64(math.Round(qtyFloat * float64(ob.quantityPrecision)))

	// 实际订单簿更新逻辑（零拷贝、高性能）
	if order.Side == "BUY" {
		ob.bids.Add(priceInt, qtyInt)
	} else {
		ob.asks.Add(priceInt, qtyInt)
	}
}

// GetBestBid 返回最佳买价档位（零拷贝指针，可直接读取 Price / Qty）
func (ob *OrderBook) GetBestBid() (*datastructures.PriceLevel, bool) {
	return ob.bids.Best(true) // true = Bid 侧最高价
}

// GetBestAsk 返回最佳卖价档位（零拷贝指针）
func (ob *OrderBook) GetBestAsk() (*datastructures.PriceLevel, bool) {
	return ob.asks.Best(false) // false = Ask 侧最低价
}

// GetMarketDepth 示例：生成深度行情（Ask 侧升序，最佳 Ask 在前）
func (ob *OrderBook) GetMarketDepth(side string, limit int) []*datastructures.PriceLevel {
	var it *datastructures.Iterator
	if side == "BUY" || side == "BID" {
		it = ob.bids.Iterate() // Bid 侧降序（最高价在前），实际深度通常 Ask 升序
	} else {
		it = ob.asks.Iterate() // Ask 侧升序
	}

	depth := make([]*datastructures.PriceLevel, 0, limit)
	for it.Next() && len(depth) < limit {
		depth = append(depth, it.Current()) // 零拷贝
	}
	return depth
}

// GetQuantityAtPrice 查询指定价格档位剩余数量（用于风控 / 撮合判断）
func (ob *OrderBook) GetQuantityAtPrice(side string, price float64) int64 {
	priceInt := int64(math.Round(price * float64(ob.pricePrecision)))
	if side == "BUY" || side == "BID" {
		return ob.bids.Get(priceInt)
	}
	return ob.asks.Get(priceInt)
}
