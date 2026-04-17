package app

import (
	"context"
	"log"
	"trading-system/internal/engine"
	"trading-system/pkg/ringbuffer"
)

// Run 启动最小骨架：演示 SPSC RingBuffer + OrderBook 跑通后退出（可替换为真实 gateway）。
func Run(ctx context.Context) error {
	rb := ringbuffer.New[engine.OrderEvent](1 << 20)

	// 演示：生产者协程往 RingBuffer 发送事件
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// 消费事件（生产中替换为真实 handler）
				if event, ok := rb.Dequeue(); ok {
					log.Printf("consumed event: orderID=%d side=%d", event.OrderID, event.Side)
				}
			}
		}
	}()

	<-ctx.Done()
	return nil
}
