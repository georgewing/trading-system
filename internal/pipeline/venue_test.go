package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"trading-system/internal/hft/engine"
	"trading-system/internal/execution"
	"trading-system/internal/routing"
)

// fakeVenue records the orders it submits and returns a fixed result.
type fakeVenue struct {
	name   string
	result engine.SubmitResult
	err    error

	mu    sync.Mutex
	calls []engine.Order
}

func (v *fakeVenue) Name() string { return v.name }

func (v *fakeVenue) Submit(_ context.Context, order engine.Order) (engine.SubmitResult, error) {
	v.mu.Lock()
	v.calls = append(v.calls, order)
	v.mu.Unlock()
	return v.result, v.err
}

func (v *fakeVenue) Cancel(_ context.Context, _ execution.CancelRequest) error { return nil }

func (v *fakeVenue) Calls() []engine.Order {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]engine.Order, len(v.calls))
	copy(out, v.calls)
	return out
}

// fakeResolver returns a fixed venue/error and records routed symbols.
type fakeResolver struct {
	venue execution.Venue
	err   error

	mu      sync.Mutex
	symbols []string
}

func (r *fakeResolver) Route(symbol string) (execution.Venue, error) {
	r.mu.Lock()
	r.symbols = append(r.symbols, symbol)
	r.mu.Unlock()
	return r.venue, r.err
}

func (r *fakeResolver) Symbols() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.symbols...)
}

func TestEventBusVenueSubmitDrivesExecReport(t *testing.T) {
	ob := engine.NewOrderBook("BTCUSDT", 100, 1)
	matcher := engine.NewMatcher(ob)

	// A resting ask that the matcher would consume if it ran.
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

	trade := engine.Trade{
		ID:          99,
		Symbol:      "BTCUSDT",
		Price:       100,
		Quantity:    2,
		BuyOrderID:  1,
		SellOrderID: 10,
		Timestamp:   123,
	}
	venue := &fakeVenue{
		name: "fake",
		result: engine.SubmitResult{
			Trades:      []engine.Trade{trade},
			ExecutedQty: 2,
			FinalStatus: engine.OrderStatusFilled,
		},
	}
	resolver := &fakeResolver{venue: venue}
	bus := newStartedBus(t, matcher, func(b *EventBus) {
		b.SetVenueResolver(resolver)
	})

	order := engine.Order{
		ID:       1,
		Symbol:   "BTCUSDT",
		Side:     engine.SideBuy,
		Type:     engine.OrderTypeLimit,
		TIF:      engine.TimeInForceGTC,
		Price:    100,
		Quantity: "2",
	}
	submitAndDrain(t, bus, order)

	report := singleReport(t, bus)
	if report.Status != engine.OrderStatusFilled {
		t.Fatalf("report status = %v, want filled", report.Status)
	}
	if report.Filled != 2 || report.Leaves != 0 {
		t.Fatalf("report filled/leaves = %d/%d, want 2/0", report.Filled, report.Leaves)
	}
	if report.Trade == nil || report.Trade.ID != trade.ID {
		t.Fatalf("report trade = %+v, want trade id %d", report.Trade, trade.ID)
	}

	calls := venue.Calls()
	if len(calls) != 1 {
		t.Fatalf("venue submit calls = %d, want 1", len(calls))
	}
	if calls[0].ID != order.ID || calls[0].Symbol != order.Symbol {
		t.Fatalf("venue order = %d/%q, want %d/%q", calls[0].ID, calls[0].Symbol, order.ID, order.Symbol)
	}

	routed := resolver.Symbols()
	if len(routed) != 1 || routed[0] != order.Symbol {
		t.Fatalf("resolver symbols = %v, want [%s]", routed, order.Symbol)
	}

	best, ok := ob.GetBestAsk()
	if !ok {
		t.Fatal("resting ask was consumed: order reached the matcher instead of the venue")
	}
	if best.Price != 100 || best.Qty != 2 {
		t.Fatalf("best ask = %d/%d, want 100/2", best.Price, best.Qty)
	}
}

func TestEventBusRoutingFailureRejectsWithoutVenue(t *testing.T) {
	cases := []struct {
		name     string
		routeErr error
		venueNil bool
		wantText string
	}{
		{
			name:     "unknown symbol",
			routeErr: fmt.Errorf("routing: symbol %q: %w", "ETHUSDT", routing.ErrUnknownSymbol),
			wantText: routing.ErrUnknownSymbol.Error(),
		},
		{
			name:     "nil venue",
			venueNil: true,
			wantText: errNilVenue.Error(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ob := engine.NewOrderBook("BTCUSDT", 100, 1)
			matcher := engine.NewMatcher(ob)

			venue := &fakeVenue{name: "fake"}
			resolver := &fakeResolver{err: tc.routeErr}
			if !tc.venueNil {
				resolver.venue = venue
			}

			bus := newStartedBus(t, matcher, func(b *EventBus) {
				b.SetVenueResolver(resolver)
			})

			order := engine.Order{
				ID:       2,
				Symbol:   "ETHUSDT",
				Side:     engine.SideBuy,
				Type:     engine.OrderTypeLimit,
				TIF:      engine.TimeInForceGTC,
				Price:    100,
				Quantity: "1",
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
			if !strings.HasPrefix(report.Reason, routingRejectPrefix) {
				t.Fatalf("report reason = %q, want prefix %q", report.Reason, routingRejectPrefix)
			}
			if !strings.Contains(report.Reason, tc.wantText) {
				t.Fatalf("report reason = %q, want it to contain %q", report.Reason, tc.wantText)
			}

			if calls := venue.Calls(); len(calls) != 0 {
				t.Fatalf("venue submit calls = %d, want 0 on routing failure", len(calls))
			}
			if routed := resolver.Symbols(); len(routed) != 1 || routed[0] != order.Symbol {
				t.Fatalf("resolver symbols = %v, want [%s]", routed, order.Symbol)
			}
		})
	}
}

func TestEventBusVenueErrorProducesRejectedReport(t *testing.T) {
	ob := engine.NewOrderBook("BTCUSDT", 100, 1)
	matcher := engine.NewMatcher(ob)

	submitErr := errors.New("venue unavailable")
	venue := &fakeVenue{name: "fake", err: submitErr}
	bus := newStartedBus(t, matcher, func(b *EventBus) {
		b.SetVenueResolver(&fakeResolver{venue: venue})
	})

	order := engine.Order{
		ID:       3,
		Symbol:   "BTCUSDT",
		Side:     engine.SideBuy,
		Type:     engine.OrderTypeLimit,
		TIF:      engine.TimeInForceGTC,
		Price:    100,
		Quantity: "1",
	}
	submitAndDrain(t, bus, order)

	report := singleReport(t, bus)
	if report.Status != engine.OrderStatusRejected {
		t.Fatalf("report status = %v, want rejected", report.Status)
	}
	if !strings.Contains(report.Reason, submitErr.Error()) {
		t.Fatalf("report reason = %q, want it to contain %q", report.Reason, submitErr.Error())
	}
	if calls := venue.Calls(); len(calls) != 1 {
		t.Fatalf("venue submit calls = %d, want 1", len(calls))
	}
}
