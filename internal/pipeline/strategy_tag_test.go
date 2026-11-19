package pipeline

import (
	"sync"
	"testing"

	"trading-system/internal/hft/engine"
)

type tagObserver struct {
	mu       sync.Mutex
	accepted []engine.Order
	reports  []engine.ExecReport
}

func (o *tagObserver) OnOrderAccepted(order engine.Order) {
	o.mu.Lock()
	o.accepted = append(o.accepted, order)
	o.mu.Unlock()
}

func (o *tagObserver) OnExecReport(_ engine.Order, report engine.ExecReport) {
	o.mu.Lock()
	o.reports = append(o.reports, report)
	o.mu.Unlock()
}

func (o *tagObserver) OnCancel(engine.Order) {}

func (o *tagObserver) snapshot() ([]engine.Order, []engine.ExecReport) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]engine.Order(nil), o.accepted...), append([]engine.ExecReport(nil), o.reports...)
}

func TestPipelinePropagatesStrategyTag(t *testing.T) {
	ob := engine.NewOrderBook("BTCUSDT", 1, 1)
	matcher := engine.NewMatcher(ob)

	observer := &tagObserver{}
	bus := newStartedBus(t, matcher, func(b *EventBus) {
		b.SetRiskObserver(observer)
	})

	order := observerTestOrder()
	order.Strategy = "market_maker"
	submitAndDrain(t, bus, order)

	accepted, reports := observer.snapshot()
	if len(accepted) != 1 || accepted[0].Strategy != "market_maker" {
		t.Fatalf("accepted = %+v", accepted)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	if reports[0].Strategy != "market_maker" {
		t.Fatalf("report strategy = %q, want market_maker", reports[0].Strategy)
	}
}
