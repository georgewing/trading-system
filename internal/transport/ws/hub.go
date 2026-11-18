package ws

import (
	"context"
	"sync"
	"sync/atomic"
	"trading-system/internal/hft/engine"
	"trading-system/internal/observability"
)

// Hub 维护执行回报订阅者，供 WS 连接层复用。
type Hub struct {
	mu      sync.RWMutex
	nextID  uint64
	bufSize int
	subs    map[uint64]chan engine.ExecReport
	met     observability.Recorder
}

func NewHub(bufferSize int) *Hub {
	if bufferSize <= 0 {
		bufferSize = 1024
	}
	return &Hub{
		bufSize: bufferSize,
		subs:    make(map[uint64]chan engine.ExecReport),
		met:     observability.NoopRecorder(),
	}
}

// Subscribe 注册一个订阅者并返回退订函数。
func (h *Hub) Subscribe() (uint64, <-chan engine.ExecReport, func()) {
	if h == nil {
		return 0, nil, func() {}
	}

	id := atomic.AddUint64(&h.nextID, 1)
	ch := make(chan engine.ExecReport, h.bufSize)

	h.mu.Lock()
	h.subs[id] = ch
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
		h.mu.Unlock()
	}

	return id, ch, unsubscribe
}

// SetMetrics injects a metrics recorder, nil falls back to noop.
func (h *Hub) SetMetrics(recorder observability.Recorder) {
	if h == nil {
		return
	}
	if recorder == nil {
		h.met = observability.NoopRecorder()
		return
	}
	h.met = recorder
}

// BroadcastExecReport 向所有订阅者广播执行回报；慢消费者会被丢弃当前消息以保护主流程。
func (h *Hub) BroadcastExecReport(_ context.Context, report engine.ExecReport) error {
	if h == nil {
		return nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, ch := range h.subs {
		select {
		case ch <- report:
		default:
			// Slow consumer: drop this report to protect the matching path.
			h.met.IncWSDrop()
		}
	}
	return nil
}
