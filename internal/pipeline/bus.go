package pipeline

import (
	"context"
	"time"
	"trading-system/internal/engine"
	"trading-system/pkg/ringbuffer"
)

// OrderCommand 是入站命令壳：承载引擎订单 + 可选结果回传 channel。
type OrderCommand struct {
	Order engine.Order
	// 可选：利用 channel 将异步撮合结果带回给 HTTP (仅限于同步请求响应模式，实盘通常靠 WS 异步推送)
	ResultCh chan<- MatchResult
}

// MatchResult 是单次 Submit 的撮合结果，供同步等待的调用方接收。
type MatchResult struct {
	Trades []engine.Trade
	Err    error
}

type EventBus struct {
	queue    *ringbuffer.RingBuffer[OrderCommand]
	matcher  *engine.Matcher
	outbound *ringbuffer.RingBuffer[engine.ExecReport]
}

func NewEventBus(matcher *engine.Matcher, capacity uint64) *EventBus {
	return &EventBus{
		queue:    ringbuffer.New[OrderCommand](capacity),
		matcher:  matcher,
		outbound: ringbuffer.New[engine.ExecReport](capacity),
	}
}

// Submit 供 HTTP/WS handler 调用，将订单压入无锁队列
func (b *EventBus) Submit(cmd OrderCommand) bool {
	return b.queue.Enqueue(cmd)
}

// Outbound 返回执行回报出向队列，供 WS/存储 worker 消费
func (b *EventBus) Outbound() *ringbuffer.RingBuffer[engine.ExecReport] {
	return b.outbound
}

// Start 开启单线程消费 (撮合必须是单线程以避免锁竞争)
func (b *EventBus) Start(ctx context.Context) {
	go func() {

		for {
			select {
			case <-ctx.Done():
				return
			default:
				// 非阻塞队列
				if cmd, ok := b.queue.Dequeue(); ok {
					// 单线程无锁调用引擎
					trades, err := b.matcher.SubmitOrder(ctx, cmd.Order)

					// 如果 HTTP 在等结果，就通过 channel 发回去
					if cmd.ResultCh != nil {
						cmd.ResultCh <- MatchResult{Trades: trades, Err: err}
					}

					if b.outbound != nil {
						//撮合失败：回报 REJECTED
						if err != nil {
							_ = b.outbound.Enqueue(engine.ExecReport{
								OrderID:   cmd.Order.ID,
								Status:    engine.OrderStatusRejected,
								Reason:    err.Error(),
								Timestamp: time.Now().UnixNano(),
							})
							continue
						}

						// 无成交：默认不发成交回报（可按业务改为 NEW/CANCELED）
						if len(trades) == 0 {
							continue
						}

						// 有成交：逐笔回报
						var cumFilled int64
						for i := range trades {
							tr := trades[i]
							cumFilled += tr.Quantity

							status := engine.OrderStatusPartiallyFilled
							if i == len(trades)-1 {
								status = engine.OrderStatusFilled // 若有剩余挂单，后续可改为 PartiallyFilled
							}

							_ = b.outbound.Enqueue(engine.ExecReport{
								OrderID:   cmd.Order.ID,
								Status:    status,
								Filled:    cumFilled,
								Leaves:    0, // 需 matcher 返回 remainingQty 才能精确填
								Trade:     &tr,
								Timestamp: tr.Timestamp,
							})
						}
					}
					continue

				}
			}
		}
	}()
}
