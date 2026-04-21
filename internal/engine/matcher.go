package engine

// 生产级撮合引擎（Matcher）
// 特点：
//   - 支持 LIMIT / MARKET + GTC / IOC / FOK
//   - 部分成交、剩余挂单
//   - 零拷贝 PriceLevel 读取
//   - 与 OrderBook 完全解耦（仅通过接口调用）
//   - 并发安全 + ctx 支持
//   - 未来升级：per-price order queue（时间优先完美版）

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"trading-system/internal/infrastructure/datastructures"
)

// Matcher 撮合引擎（聚合根之一）
type Matcher struct {
	ob           *OrderBook
	mu           sync.Mutex        // 保护 activeOrders + 撮合原子性
	activeOrders map[uint64]*Order // resting 订单（ID → Order）
	tradeSeq     uint64            // trade ID 生成器
	// metrics   *observability.Metrics // 未来注入 Prometheus
}

// SubmitResult表示一次提交后的最终执行结果（供 execution report 使用）。
type SubmitResult struct {
	Trades      []Trade
	ExecutedQty int64
	OpenLeaves  int64
	CanceledQty int64 // 被策略取消的量（IOC/MARKET 可能 >0）
	FinalStatus OrderStatus
}

func NewMatcher(ob *OrderBook) *Matcher {
	return &Matcher{
		ob:           ob,
		activeOrders: make(map[uint64]*Order),
	}
}

// Match 撮合算法
func (m *Matcher) Match(ctx context.Context, order Order) ([]Trade, int64, error) {
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}

	// order.Price 已经是 int64（以最小价格单位表示），直接使用
	priceInt := order.Price

	qtyFloat, err := strconv.ParseFloat(order.Quantity, 64)
	if err != nil {
		return nil, 0, errors.New("invalid quantity")
	}
	qtyInt := int64(math.Round(qtyFloat * float64(m.ob.quantityPrecision)))
	if qtyInt <= 0 {
		return nil, 0, errors.New("quantity must be positive")
	}

	// 加锁
	m.mu.Lock()
	defer m.mu.Unlock()

	var trades []Trade
	remainingQty := qtyInt

	for remainingQty > 0 {
		// 获取对侧最佳价格档位（零拷贝）
		var best *datastructures.PriceLevel
		var ok bool
		if order.Side == SideBuy {
			best, ok = m.ob.GetBestAsk()
		} else {
			best, ok = m.ob.GetBestBid()
		}
		if !ok {
			break // 对侧无订单
		}

		// 是否价格交叉（MARKET 单永远成交）
		if order.Type == OrderTypeMarket || isCrossing(order.Side, priceInt, best.Price) {
			fillQty := min(remainingQty, best.Qty)

			// 生成成交记录
			tradeID, _ := strconv.ParseUint(generateTradeID(&m.tradeSeq)[2:], 10, 64)
			trade := Trade{
				ID:       tradeID,
				Symbol:   m.ob.symbol,
				Price:    best.Price,
				Quantity: fillQty,
				BuyOrderID: func() uint64 {
					if order.Side == SideBuy {
						return order.ID
					}
					return 0
				}(),
				SellOrderID: func() uint64 {
					if order.Side == SideSell {
						return order.ID
					}
					return 0
				}(),
				Timestamp:  time.Now().UnixNano(),
				MakerOrder: 0, // 当前聚合模式无法精确对应 maker 订单ID，升级后填充
			}
			trades = append(trades, trade)

			// 扣减对侧订单簿（SkipList 自动清理 qty <= 0）
			if order.Side == SideBuy {
				m.ob.asks.Remove(best.Price, fillQty)
			} else {
				m.ob.bids.Remove(best.Price, fillQty)
			}

			remainingQty -= fillQty
		} else {
			break // 不交叉，结束撮合
		}
	}

	return trades, remainingQty, nil
}

func sumExecutedQty(trades []Trade) int64 {
	var n int64
	for _, trade := range trades {
		n += trade.Quantity
	}
	return n
}

// SubmitOrder 提交订单并执行撮合（生产主入口）
func (m *Matcher) SubmitOrder(ctx context.Context, order Order) ([]Trade, error) {
	trades, remainingQty, err := m.Match(ctx, order)
	if err != nil {
		return nil, err
	}

	executed := sumExecutedQty(trades)
	result := &SubmitResult{
		Trades:      trades,
		ExecutedQty: executed,
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
			return result.Trades, nil
		case order.TIF == TimeInForceFOK:
			// FOK: 必须全部成交，否则整单拒绝并回滚已扣减的对侧订单簿
			for _, t := range trades {
				// 回滚：将已扣减的数量加回对侧
				if order.Side == SideBuy {
					m.ob.asks.Add(t.Price, t.Quantity)
				} else {
					m.ob.bids.Add(t.Price, t.Quantity)
				}
			}
			return SubmitResult{}.Trades, errors.New("FOK: cannot fully fill")
		default: // LIMIT + GTC → 剩余挂单
			priceInt := order.Price
			qtyInt := remainingQty

			if order.Side == SideBuy {
				m.ob.bids.Add(priceInt, qtyInt)
			} else {
				m.ob.asks.Add(priceInt, qtyInt)
			}
			// 保存 resting order（保留原始信息）
			m.activeOrders[order.ID] = &order // 注意：实际生产建议深拷贝或只存必要字段 //nolint
			result.OpenLeaves = remainingQty
			if executed > 0 {
				result.FinalStatus = OrderStatusPartiallyFilled
			} else {
				result.FinalStatus = OrderStatusNew
			}

			return result.Trades, nil
		}
	}

	if executed > 0 {
		result.FinalStatus = OrderStatusFilled
	} else {
		result.FinalStatus = OrderStatusNew
	}

	return result.Trades, nil
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
