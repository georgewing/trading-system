package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"trading-system/internal/hft/engine"
	"trading-system/internal/hft/risk"
)

// fakeRiskGuard records the requests it evaluates and returns a fixed decision.
type fakeRiskGuard struct {
	decision risk.Decision

	mu    sync.Mutex
	calls []risk.Request
}

func (g *fakeRiskGuard) Check(_ context.Context, req risk.Request) risk.Decision {
	g.mu.Lock()
	g.calls = append(g.calls, req)
	g.mu.Unlock()
	return g.decision
}

func (g *fakeRiskGuard) Calls() []risk.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]risk.Request, len(g.calls))
	copy(out, g.calls)
	return out
}

// riskMetricsRecorder captures the risk-related observations for assertions.
type riskMetricsRecorder struct {
	mu        sync.Mutex
	arrivals  []string
	decisions []string
}

func (m *riskMetricsRecorder) IncOrderArrival(result string) {
	m.mu.Lock()
	m.arrivals = append(m.arrivals, result)
	m.mu.Unlock()
}

func (m *riskMetricsRecorder) ObserveMatchLatency(string, time.Duration) {}
func (m *riskMetricsRecorder) SetQueueBacklog(string, float64)           {}
func (m *riskMetricsRecorder) IncQueueDrop(string)                       {}
func (m *riskMetricsRecorder) IncSinkFailure(string)                     {}
func (m *riskMetricsRecorder) IncWSDrop()                                {}

func (m *riskMetricsRecorder) IncRiskDecision(code, result string) {
	m.mu.Lock()
	m.decisions = append(m.decisions, code+"/"+result)
	m.mu.Unlock()
}

func (m *riskMetricsRecorder) hasArrival(want string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, got := range m.arrivals {
		if got == want {
			return true
		}
	}
	return false
}

func (m *riskMetricsRecorder) hasDecision(want string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, got := range m.decisions {
		if got == want {
			return true
		}
	}
	return false
}

func (m *riskMetricsRecorder) arrivalsSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.arrivals...)
}

func (m *riskMetricsRecorder) decisionsSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.decisions...)
}

func newStartedBus(t *testing.T, matcher *engine.Matcher, setup func(*EventBus)) *EventBus {
	t.Helper()

	bus := NewEventBus(matcher, 16)
	if setup != nil {
		setup(bus)
	}

	ctx, cancel := context.WithCancel(context.Background())
	bus.Start(ctx)
	t.Cleanup(func() {
		bus.StopAccepting()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
		defer drainCancel()
		if err := bus.Drain(drainCtx); err != nil {
			t.Errorf("Drain() during cleanup error = %v", err)
		}
		cancel()
	})
	return bus
}

func submitAndDrain(t *testing.T, bus *EventBus, order engine.Order) {
	t.Helper()

	if err := bus.SubmitWithContext(context.Background(), OrderCommand{Order: order}); err != nil {
		t.Fatalf("SubmitWithContext() error = %v", err)
	}
	bus.StopAccepting()

	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := bus.Drain(drainCtx); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
}

func singleReport(t *testing.T, bus *EventBus) engine.ExecReport {
	t.Helper()

	// Maker reports are enqueued after the taker's own report, so a fill against
	// resting orders legitimately produces more than one entry. The first one is
	// always the order under test.
	if got := bus.Outbound().Len(); got < 1 {
		t.Fatalf("outbound queue length = %d, want at least 1", got)
	}
	report, ok := bus.Outbound().Dequeue()
	if !ok {
		t.Fatal("outbound queue dequeue failed")
	}
	return report
}

func TestEventBusRiskRejectionSkipsMatcher(t *testing.T) {
	ob := engine.NewOrderBook("BTCUSDT", 100, 1)
	matcher := engine.NewMatcher(ob)

	// A resting ask that the buy order would consume if the matcher ran.
	rest := engine.Order{
		ID:       10,
		Side:     engine.SideSell,
		Type:     engine.OrderTypeLimit,
		TIF:      engine.TimeInForceGTC,
		Price:    100,
		Quantity: "2",
	}
	if _, err := matcher.SubmitOrderDetailed(context.Background(), rest); err != nil {
		t.Fatalf("rest sell order error = %v", err)
	}

	guard := &fakeRiskGuard{decision: risk.Decision{
		Code:    risk.CodeMaxQty,
		Message: "order qty 3 exceeds max 1",
	}}
	metrics := &riskMetricsRecorder{}
	bus := newStartedBus(t, matcher, func(b *EventBus) {
		b.SetMetrics(metrics)
		b.SetRiskGuard(guard, 1)
	})

	order := engine.Order{
		ID:            1,
		ClientOrderID: "client-1",
		AccountID:     7,
		Symbol:        "BTCUSDT",
		Side:          engine.SideBuy,
		Type:          engine.OrderTypeLimit,
		TIF:           engine.TimeInForceGTC,
		Price:         100,
		Quantity:      "3",
	}
	submitAndDrain(t, bus, order)

	report := singleReport(t, bus)
	if report.OrderID != order.ID {
		t.Fatalf("report order id = %d, want %d", report.OrderID, order.ID)
	}
	if report.Status != engine.OrderStatusRejected {
		t.Fatalf("report status = %v, want rejected", report.Status)
	}
	if report.Filled != 0 || report.Leaves != 0 {
		t.Fatalf("report filled/leaves = %d/%d, want 0/0", report.Filled, report.Leaves)
	}
	wantReason := risk.CodeMaxQty + ": " + guard.decision.Message
	if report.Reason != wantReason {
		t.Fatalf("report reason = %q, want %q", report.Reason, wantReason)
	}

	best, ok := ob.GetBestAsk()
	if !ok {
		t.Fatal("resting ask was consumed: matcher ran for a rejected order")
	}
	if best.Price != 100 || best.Qty != 2 {
		t.Fatalf("best ask = %d/%d, want 100/2", best.Price, best.Qty)
	}

	calls := guard.Calls()
	if len(calls) != 1 {
		t.Fatalf("risk guard calls = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.AccountID != 7 || got.Symbol != "BTCUSDT" || got.ClientOrderID != "client-1" {
		t.Fatalf("risk request identity = %d/%q/%q, want 7/BTCUSDT/client-1",
			got.AccountID, got.Symbol, got.ClientOrderID)
	}
	if got.Qty != 3 || got.Price != 100 {
		t.Fatalf("risk request qty/price = %d/%d, want 3/100", got.Qty, got.Price)
	}
	if got.ReferencePrice != 100 {
		t.Fatalf("risk request reference price = %d, want 100", got.ReferencePrice)
	}

	if !metrics.hasArrival("accepted") {
		t.Fatalf("order arrival labels = %v, want accepted", metrics.arrivalsSnapshot())
	}
	if !metrics.hasDecision(risk.CodeMaxQty + "/" + riskResultRejected) {
		t.Fatalf("risk decisions = %v, want %s/rejected", metrics.decisionsSnapshot(), risk.CodeMaxQty)
	}
}

func TestEventBusRiskApprovalMatchesOrder(t *testing.T) {
	ob := engine.NewOrderBook("BTCUSDT", 100, 1)
	matcher := engine.NewMatcher(ob)

	rest := engine.Order{
		ID:       10,
		Side:     engine.SideSell,
		Type:     engine.OrderTypeLimit,
		TIF:      engine.TimeInForceGTC,
		Price:    100,
		Quantity: "2",
	}
	if _, err := matcher.SubmitOrderDetailed(context.Background(), rest); err != nil {
		t.Fatalf("rest sell order error = %v", err)
	}

	guard := &fakeRiskGuard{decision: risk.Decision{Approved: true, Code: risk.CodeOK}}
	metrics := &riskMetricsRecorder{}
	bus := newStartedBus(t, matcher, func(b *EventBus) {
		b.SetMetrics(metrics)
		b.SetRiskGuard(guard, 1)
	})

	order := engine.Order{
		ID:            2,
		ClientOrderID: "client-2",
		AccountID:     7,
		Symbol:        "BTCUSDT",
		Side:          engine.SideBuy,
		Type:          engine.OrderTypeLimit,
		TIF:           engine.TimeInForceGTC,
		Price:         100,
		Quantity:      "2",
	}
	submitAndDrain(t, bus, order)

	// The resting sell is a maker now: it gets its own report.
	if got := bus.Outbound().Len(); got != 2 {
		t.Fatalf("outbound queue length = %d, want 2 (taker + maker)", got)
	}

	report := singleReport(t, bus)
	if report.Status != engine.OrderStatusFilled {
		t.Fatalf("report status = %v, want filled", report.Status)
	}
	if report.Filled != 2 || report.Leaves != 0 {
		t.Fatalf("report filled/leaves = %d/%d, want 2/0", report.Filled, report.Leaves)
	}
	if report.Trade == nil {
		t.Fatal("report trade = nil, want execution trade")
	}

	if _, ok := ob.GetBestAsk(); ok {
		t.Fatal("best ask still present, want fully consumed book")
	}
	if calls := guard.Calls(); len(calls) != 1 {
		t.Fatalf("risk guard calls = %d, want 1", len(calls))
	}
	if !metrics.hasDecision(risk.CodeOK + "/" + riskResultApproved) {
		t.Fatalf("risk decisions = %v, want OK/approved", metrics.decisionsSnapshot())
	}
}

func TestEventBusRiskRejectsInvalidQuantity(t *testing.T) {
	ob := engine.NewOrderBook("BTCUSDT", 100, 1)
	matcher := engine.NewMatcher(ob)

	guard := &fakeRiskGuard{decision: risk.Decision{Approved: true, Code: risk.CodeOK}}
	metrics := &riskMetricsRecorder{}
	bus := newStartedBus(t, matcher, func(b *EventBus) {
		b.SetMetrics(metrics)
		b.SetRiskGuard(guard, 100) // two decimal places
	})

	order := engine.Order{
		ID:            3,
		ClientOrderID: "client-3",
		AccountID:     7,
		Symbol:        "BTCUSDT",
		Side:          engine.SideBuy,
		Type:          engine.OrderTypeLimit,
		TIF:           engine.TimeInForceGTC,
		Price:         100,
		Quantity:      "1.234",
	}
	submitAndDrain(t, bus, order)

	report := singleReport(t, bus)
	if report.Status != engine.OrderStatusRejected {
		t.Fatalf("report status = %v, want rejected", report.Status)
	}
	if !strings.Contains(report.Reason, risk.CodeInvalidRequest) {
		t.Fatalf("report reason = %q, want code %s", report.Reason, risk.CodeInvalidRequest)
	}
	if calls := guard.Calls(); len(calls) != 0 {
		t.Fatalf("risk guard calls = %d, want 0 for unparsable quantity", len(calls))
	}
	if !metrics.hasDecision(risk.CodeInvalidRequest + "/" + riskResultRejected) {
		t.Fatalf("risk decisions = %v, want %s/rejected", metrics.decisionsSnapshot(), risk.CodeInvalidRequest)
	}
}
