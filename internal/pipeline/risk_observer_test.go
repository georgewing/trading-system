package pipeline

import (
	"sync"
	"testing"

	"trading-system/internal/hft/engine"
	"trading-system/internal/hft/risk"
)

type fakeRiskObserver struct {
	mu       sync.Mutex
	accepted []engine.Order
	reports  []engine.ExecReport
	canceled []engine.Order
}

func (o *fakeRiskObserver) OnOrderAccepted(order engine.Order) {
	o.mu.Lock()
	o.accepted = append(o.accepted, order)
	o.mu.Unlock()
}

func (o *fakeRiskObserver) OnExecReport(order engine.Order, report engine.ExecReport) {
	o.mu.Lock()
	o.reports = append(o.reports, report)
	o.mu.Unlock()
}

func (o *fakeRiskObserver) OnCancel(order engine.Order) {
	o.mu.Lock()
	o.canceled = append(o.canceled, order)
	o.mu.Unlock()
}

func (o *fakeRiskObserver) counts() (int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.accepted), len(o.reports)
}

func observerTestOrder() engine.Order {
	return engine.Order{
		ID:            1,
		ClientOrderID: "c-1",
		AccountID:     1,
		Symbol:        "BTCUSDT",
		Side:          engine.SideBuy,
		Type:          engine.OrderTypeLimit,
		TIF:           engine.TimeInForceGTC,
		Price:         100,
		Quantity:      "1",
	}
}

func TestRiskObserverSeesAcceptedOrderAndReport(t *testing.T) {
	ob := engine.NewOrderBook("BTCUSDT", 1, 1)
	matcher := engine.NewMatcher(ob)

	observer := &fakeRiskObserver{}
	guard := &fakeRiskGuard{decision: risk.Decision{Approved: true, Code: risk.CodeOK}}

	bus := newStartedBus(t, matcher, func(b *EventBus) {
		b.SetRiskGuard(guard, 1)
		b.SetRiskObserver(observer)
	})

	submitAndDrain(t, bus, observerTestOrder())

	accepted, reports := observer.counts()
	if accepted != 1 {
		t.Fatalf("accepted = %d, want 1", accepted)
	}
	if reports != 1 {
		t.Fatalf("reports = %d, want 1", reports)
	}

	if got := singleReport(t, bus); got.Status != engine.OrderStatusNew {
		t.Fatalf("report status = %v, want NEW", got.Status)
	}
}

func TestRiskObserverIsSilentOnRejection(t *testing.T) {
	ob := engine.NewOrderBook("BTCUSDT", 1, 1)
	matcher := engine.NewMatcher(ob)

	observer := &fakeRiskObserver{}
	guard := &fakeRiskGuard{decision: risk.Decision{
		Code:    risk.CodeMaxQty,
		Message: "too large",
	}}

	bus := newStartedBus(t, matcher, func(b *EventBus) {
		b.SetRiskGuard(guard, 1)
		b.SetRiskObserver(observer)
	})

	submitAndDrain(t, bus, observerTestOrder())

	accepted, reports := observer.counts()
	if accepted != 0 || reports != 0 {
		t.Fatalf("rejected order reached the observer: accepted=%d reports=%d", accepted, reports)
	}

	if got := singleReport(t, bus); got.Status != engine.OrderStatusRejected {
		t.Fatalf("report status = %v, want REJECTED", got.Status)
	}
}
