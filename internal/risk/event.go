package risk

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
)

// Event is one audit record for a risk decision or state change. The same
// structure feeds the log, the WebSocket hub and (later) Postgres.
type Event struct {
	TS        int64  `json:"ts"`
	AccountID uint64 `json:"account_id"`
	Symbol    string `json:"symbol"`
	Strategy  string `json:"strategy"`
	OrderID   uint64 `json:"order_id"`
	Code      string `json:"code"`
	Action    Action `json:"action"`
	Message   string `json:"message"`
}

// Sink consumes risk events. Implementations must not block the matching
// goroutine; use AsyncSink when the destination can be slow.
type Sink interface {
	Record(ev Event)
}

// MultiSink fans one event out to several sinks in order.
type MultiSink []Sink

// Record implements Sink.
func (m MultiSink) Record(ev Event) {
	for _, s := range m {
		if s != nil {
			s.Record(ev)
		}
	}
}

// LogSink writes risk events to the standard logger.
type LogSink struct {
	logf func(string, ...any)
}

// NewLogSink builds a log-backed sink.
func NewLogSink() *LogSink {
	return &LogSink{logf: log.Printf}
}

// Record implements Sink.
func (s *LogSink) Record(ev Event) {
	if s == nil {
		return
	}
	s.logf("risk event: account=%d symbol=%s order=%d code=%s action=%s msg=%q",
		ev.AccountID, ev.Symbol, ev.OrderID, ev.Code, ev.Action, ev.Message)
}

// AsyncSink decouples risk recording from the matching goroutine. Record never
// blocks: when the buffer is full the event is dropped and counted.
type AsyncSink struct {
	dst Sink
	ch  chan Event
	met Metrics

	mu     sync.RWMutex
	closed bool

	wg      sync.WaitGroup
	dropped atomic.Uint64
}

// NewAsyncSink starts a worker that forwards events to dst. A nil dst discards
// events while still recovering the goroutine.
func NewAsyncSink(dst Sink, buffer int, met Metrics) *AsyncSink {
	if buffer <= 0 {
		buffer = 1024
	}
	s := &AsyncSink{
		dst: dst,
		ch:  make(chan Event, buffer),
		met: met,
	}
	s.wg.Add(1)
	go s.run()
	return s
}

func (s *AsyncSink) run() {
	defer s.wg.Done()
	for ev := range s.ch {
		if s.dst != nil {
			s.dst.Record(ev)
		}
	}
}

// Record implements Sink.
func (s *AsyncSink) Record(ev Event) {
	if s == nil {
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}

	select {
	case s.ch <- ev:
	default:
		s.dropped.Add(1)
		if s.met != nil {
			s.met.IncRiskEventDrop()
		}
	}
}

// Close drains the buffer and waits for the worker. It is idempotent.
func (s *AsyncSink) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Dropped reports how many events were dropped because the buffer was full.
func (s *AsyncSink) Dropped() uint64 { return s.dropped.Load() }
