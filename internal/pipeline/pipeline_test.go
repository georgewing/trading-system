package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"trading-system/internal/engine"
	"trading-system/pkg/ringbuffer"
)

type countingSink struct {
	mu    sync.Mutex
	count int
}

func (s *countingSink) Name() string { return "counting" }

func (s *countingSink) HandleExecReport(_ context.Context, _ engine.ExecReport) error {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	return nil
}

func (s *countingSink) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func TestOutboundWorkerStopDrainsQueue(t *testing.T) {
	queue := ringbuffer.New[engine.ExecReport](128)
	sink := &countingSink{}
	worker := NewOutboundWorker(
		queue,
		OutboundWorkerConfig{
			DispatchIdleSleep: 50 * time.Microsecond,
			DispatchTimeout:   100 * time.Millisecond,
			SinkQueueSize:     64,
			RetryMaxAttempts:  1,
		},
		nil,
		sink,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)

	const total = 100
	for i := 0; i < total; i++ {
		if !queue.Enqueue(engine.ExecReport{OrderID: uint64(i + 1)}) {
			t.Fatalf("enqueue report %d failed", i)
		}
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if got := sink.Count(); got != total {
		t.Fatalf("sink count = %d, want %d", got, total)
	}
}

func TestEventBusDrainProcessesAcceptedOrders(t *testing.T) {
	ob := engine.NewOrderBook("BTCUSDT", 100, 100)
	bus := NewEventBus(engine.NewMatcher(ob), 16)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)

	order := engine.Order{
		ID:       1,
		Side:     engine.SideBuy,
		Type:     engine.OrderTypeLimit,
		TIF:      engine.TimeInForceGTC,
		Price:    100,
		Quantity: "1",
	}
	if err := bus.SubmitWithContext(ctx, OrderCommand{Order: order}); err != nil {
		t.Fatalf("SubmitWithContext() error = %v", err)
	}

	bus.StopAccepting()
	if err := bus.SubmitWithContext(ctx, OrderCommand{Order: order}); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("SubmitWithContext() after stop error = %v, want ErrShuttingDown", err)
	}

	drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
	defer drainCancel()
	if err := bus.Drain(drainCtx); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	if got := bus.Outbound().Len(); got == 0 {
		t.Fatal("outbound queue is empty after drain, want exec report")
	}
}
