package ws

import (
	"sync"
	"sync/atomic"

	"trading-system/internal/hft/risk"
)

// RiskFeed is the risk-event surface the WS server reads from.
type RiskFeed interface {
	Subscribe() (uint64, <-chan risk.Event, func())
	Recent(limit int) []risk.Event
}

// RiskHub fans risk events out to WebSocket clients and keeps a small recent
// history for late subscribers. Record is non-blocking so the risk engine can
// call it from the matching goroutine.
type RiskHub struct {
	mu      sync.RWMutex
	nextID  uint64
	bufSize int
	subs    map[uint64]chan risk.Event

	recent    []risk.Event
	recentCap int

	dropped atomic.Uint64
}

// NewRiskHub builds a hub. buffer is the per-subscriber channel size and
// recentCap bounds the replay window for new subscribers.
func NewRiskHub(buffer, recentCap int) *RiskHub {
	if buffer <= 0 {
		buffer = 1024
	}
	if recentCap <= 0 {
		recentCap = 100
	}
	return &RiskHub{
		bufSize:   buffer,
		subs:      make(map[uint64]chan risk.Event),
		recentCap: recentCap,
	}
}

// Record implements risk.Sink.
func (h *RiskHub) Record(ev risk.Event) {
	if h == nil {
		return
	}

	h.mu.Lock()
	h.recent = append(h.recent, ev)
	if len(h.recent) > h.recentCap {
		h.recent = h.recent[len(h.recent)-h.recentCap:]
	}
	for _, ch := range h.subs {
		select {
		case ch <- ev:
		default:
			h.dropped.Add(1)
		}
	}
	h.mu.Unlock()
}

// Subscribe registers a subscriber. The returned cancel function closes the
// channel and is idempotent.
func (h *RiskHub) Subscribe() (uint64, <-chan risk.Event, func()) {
	if h == nil {
		return 0, nil, func() {}
	}

	h.mu.Lock()
	h.nextID++
	id := h.nextID
	ch := make(chan risk.Event, h.bufSize)
	h.subs[id] = ch
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if c, ok := h.subs[id]; ok {
				delete(h.subs, id)
				close(c)
			}
			h.mu.Unlock()
		})
	}
	return id, ch, cancel
}

// Recent returns up to limit most recent events, oldest first.
func (h *RiskHub) Recent(limit int) []risk.Event {
	if h == nil {
		return nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	buf := h.recent
	if limit > 0 && len(buf) > limit {
		buf = buf[len(buf)-limit:]
	}
	out := make([]risk.Event, len(buf))
	copy(out, buf)
	return out
}

// Dropped reports how many deliveries were dropped for slow subscribers.
func (h *RiskHub) Dropped() uint64 {
	if h == nil {
		return 0
	}
	return h.dropped.Load()
}

var _ risk.Sink = (*RiskHub)(nil)
var _ RiskFeed = (*RiskHub)(nil)
