package execution

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"trading-system/internal/engine"
)

type fakeMatcher struct {
	result engine.SubmitResult
	err    error

	calls int
	last  engine.Order
}

func (f *fakeMatcher) SubmitOrderDetailed(_ context.Context, order engine.Order) (engine.SubmitResult, error) {
	f.calls++
	f.last = order
	return f.result, f.err
}

func fakeOrder() engine.Order {
	return engine.Order{
		ID:       1001,
		Symbol:   "BTCUSDT",
		Side:     engine.SideBuy,
		Type:     engine.OrderTypeLimit,
		TIF:      engine.TimeInForceGTC,
		Price:    100,
		Quantity: "2",
		Leaves:   2,
	}
}

func TestPaperVenueNameDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty falls back", in: "", want: DefaultPaperVenueName},
		{name: "explicit name kept", in: "paper-sim", want: "paper-sim"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			venue := NewPaperVenue(tt.in, &fakeMatcher{})
			if got := venue.Name(); got != tt.want {
				t.Fatalf("Name() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPaperVenueSubmitResultPassthrough(t *testing.T) {
	order := fakeOrder()
	want := engine.SubmitResult{
		Trades: []engine.Trade{
			{ID: 11, Symbol: "BTCUSDT", Price: 100, Quantity: 1, BuyOrderID: 1001, SellOrderID: 2001, Timestamp: 111},
			{ID: 12, Symbol: "BTCUSDT", Price: 101, Quantity: 1, BuyOrderID: 1001, SellOrderID: 2002, Timestamp: 112},
		},
		ExecutedQty: 2,
		OpenLeaves:  4,
		CanceledQty: 3,
		FinalStatus: engine.OrderStatusPartiallyFilled,
	}
	fm := &fakeMatcher{result: want}
	venue := NewPaperVenue("paper", fm)

	got, err := venue.Submit(context.Background(), order)
	if err != nil {
		t.Fatalf("Submit() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got.Trades, want.Trades) {
		t.Fatalf("Submit() Trades = %+v, want %+v", got.Trades, want.Trades)
	}
	if got.ExecutedQty != want.ExecutedQty {
		t.Fatalf("Submit() ExecutedQty = %d, want %d", got.ExecutedQty, want.ExecutedQty)
	}
	if got.OpenLeaves != want.OpenLeaves {
		t.Fatalf("Submit() OpenLeaves = %d, want %d", got.OpenLeaves, want.OpenLeaves)
	}
	if got.CanceledQty != want.CanceledQty {
		t.Fatalf("Submit() CanceledQty = %d, want %d", got.CanceledQty, want.CanceledQty)
	}
	if got.FinalStatus != want.FinalStatus {
		t.Fatalf("Submit() FinalStatus = %v, want %v", got.FinalStatus, want.FinalStatus)
	}
	if fm.calls != 1 {
		t.Fatalf("matcher calls = %d, want 1", fm.calls)
	}
	if fm.last.ID != order.ID {
		t.Fatalf("matcher order ID = %d, want %d", fm.last.ID, order.ID)
	}
}

func TestPaperVenueSubmitMatcherError(t *testing.T) {
	submitErr := errors.New("matching failed")
	fm := &fakeMatcher{err: submitErr}
	venue := NewPaperVenue("", fm)

	got, err := venue.Submit(context.Background(), fakeOrder())

	if !errors.Is(err, submitErr) {
		t.Fatalf("Submit() error = %v, want wrapping %v", err, submitErr)
	}
	if !reflect.DeepEqual(got, engine.SubmitResult{}) {
		t.Fatalf("Submit() result = %+v, want zero value on error", got)
	}
	if fm.calls != 1 {
		t.Fatalf("matcher calls = %d, want 1", fm.calls)
	}
}

func TestPaperVenueSubmitNilMatcher(t *testing.T) {
	venue := NewPaperVenue("paper", nil)

	got, err := venue.Submit(context.Background(), fakeOrder())

	if !errors.Is(err, ErrNilMatcher) {
		t.Fatalf("Submit() error = %v, want %v", err, ErrNilMatcher)
	}
	if !reflect.DeepEqual(got, engine.SubmitResult{}) {
		t.Fatalf("Submit() result = %+v, want zero value", got)
	}
}

func TestPaperVenueCancelNotSupported(t *testing.T) {
	venue := NewPaperVenue("paper", &fakeMatcher{})

	err := venue.Cancel(context.Background(), CancelRequest{
		OrderID:       7,
		ClientOrderID: "client-7",
		Symbol:        "BTCUSDT",
		Reason:        "user",
	})

	if !errors.Is(err, ErrCancelNotSupported) {
		t.Fatalf("Cancel() error = %v, want %v", err, ErrCancelNotSupported)
	}
}
