package pipeline

import (
	"context"
	"errors"
	"testing"

	"trading-system/internal/hft/engine"
)

func newCancelTestBus(t *testing.T) (*EventBus, *engine.Matcher) {
	t.Helper()

	ob := engine.NewOrderBook("BTCUSDT", 100, 1)
	matcher := engine.NewMatcher(ob)
	bus := NewEventBusWithConfig(matcher, DefaultEventBusConfig())

	return bus, matcher
}

func TestCancelOrderPublishesCanceledReport(t *testing.T) {
	bus, matcher := newCancelTestBus(t)
	ctx := context.Background()

	order := engine.Order{
		ID:       42,
		Side:     engine.SideBuy,
		Price:    100,
		Quantity: "1",
		Type:     engine.OrderTypeLimit,
		TIF:      engine.TimeInForceGTC,
	}
	if _, err := matcher.SubmitOrderDetailed(ctx, order); err != nil {
		t.Fatalf("submit: %v", err)
	}

	report, err := bus.CancelOrder(ctx, 42, "unit test")
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if report.Status != engine.OrderStatusCanceled {
		t.Fatalf("status = %v, want CANCELED", report.Status)
	}
	if report.Reason != "unit test" {
		t.Fatalf("reason = %q", report.Reason)
	}

	// The report must reach the outbound sinks so the order list updates.
	got, ok := bus.Outbound().Dequeue()
	if !ok {
		t.Fatal("no execution report enqueued")
	}
	if got.OrderID != 42 || got.Status != engine.OrderStatusCanceled {
		t.Fatalf("enqueued report = %+v", got)
	}
}

func TestCancelOrderUnknownID(t *testing.T) {
	bus, _ := newCancelTestBus(t)

	if _, err := bus.CancelOrder(context.Background(), 999, ""); !errors.Is(err, engine.ErrOrderNotFound) {
		t.Fatalf("error = %v, want ErrOrderNotFound", err)
	}
}

func TestCancelOrderRejectedWhenShuttingDown(t *testing.T) {
	bus, matcher := newCancelTestBus(t)
	ctx := context.Background()

	order := engine.Order{
		ID: 7, Side: engine.SideSell, Price: 100, Quantity: "1",
		Type: engine.OrderTypeLimit, TIF: engine.TimeInForceGTC,
	}
	if _, err := matcher.SubmitOrderDetailed(ctx, order); err != nil {
		t.Fatalf("submit: %v", err)
	}

	bus.StopAccepting()
	if _, err := bus.CancelOrder(ctx, 7, ""); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("error = %v, want ErrShuttingDown", err)
	}
}
