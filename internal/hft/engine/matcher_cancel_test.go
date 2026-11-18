package engine

import (
	"context"
	"errors"
	"testing"
)

func restableOrder(id uint64, side Side, price int64, qty string) Order {
	return Order{
		ID:       id,
		Side:     side,
		Price:    price,
		Quantity: qty,
		Type:     OrderTypeLimit,
		TIF:      TimeInForceGTC,
	}
}

func TestCancelRemovesRestingOrder(t *testing.T) {
	ob := NewOrderBook("BTCUSDT", 100, 1)
	m := NewMatcher(ob)
	ctx := context.Background()

	if _, err := m.SubmitOrderDetailed(ctx, restableOrder(1, SideBuy, 100, "1")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, ok := ob.GetBestBid(); !ok {
		t.Fatal("order should be resting")
	}

	canceled, err := m.Cancel(ctx, 1)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != OrderStatusCanceled || canceled.Leaves != 0 {
		t.Fatalf("canceled = %+v, want status CANCELED and no leaves", canceled)
	}

	// The aggregate level must disappear with the only order at that price.
	if _, ok := ob.GetBestBid(); ok {
		t.Fatal("book still has a bid after cancel")
	}
	if m.RestingOrderCount() != 0 {
		t.Fatalf("resting orders = %d, want 0", m.RestingOrderCount())
	}

	// A second cancel must report a missing order, not a stale success.
	if _, err := m.Cancel(ctx, 1); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("second cancel error = %v, want ErrOrderNotFound", err)
	}
}

// A partially filled resting order must report the executed quantity when
// canceled; otherwise the order read model would show a filled amount of zero.
func TestCancelKeepsFilledQuantity(t *testing.T) {
	// 10 means one decimal place, so "0.5" is five lots of 0.1.
	ob := NewOrderBook("BTCUSDT", 100, 10)
	m := NewMatcher(ob)
	ctx := context.Background()

	// Maker sells 0.5, taker buys 1: 0.5 fills, 0.5 rests with Filled set.
	if _, err := m.SubmitOrderDetailed(ctx, restableOrder(1, SideSell, 100, "0.5")); err != nil {
		t.Fatalf("maker submit: %v", err)
	}
	result, err := m.SubmitOrderDetailed(ctx, restableOrder(2, SideBuy, 100, "1"))
	if err != nil {
		t.Fatalf("taker submit: %v", err)
	}
	if result.OpenLeaves != 5 || result.ExecutedQty != 5 {
		t.Fatalf("result = %+v, want 5 filled and 5 resting", result)
	}

	canceled, err := m.Cancel(ctx, 2)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Filled != 5 {
		t.Fatalf("canceled.Filled = %d, want 5", canceled.Filled)
	}
	if canceled.Leaves != 0 {
		t.Fatalf("canceled.Leaves = %d, want 0", canceled.Leaves)
	}
}

func TestCancelOnlyTouchesTheCancelledOrder(t *testing.T) {
	ob := NewOrderBook("BTCUSDT", 100, 1)
	m := NewMatcher(ob)
	ctx := context.Background()

	if _, err := m.SubmitOrderDetailed(ctx, restableOrder(1, SideBuy, 100, "1")); err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	if _, err := m.SubmitOrderDetailed(ctx, restableOrder(2, SideBuy, 100, "2")); err != nil {
		t.Fatalf("submit 2: %v", err)
	}

	if _, err := m.Cancel(ctx, 2); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	level, ok := ob.GetBestBid()
	if !ok {
		t.Fatal("remaining order disappeared")
	}
	if level.Qty != 1 {
		t.Fatalf("aggregate qty = %d, want 1 (only the canceled order removed)", level.Qty)
	}
	if m.RestingOrderCount() != 1 {
		t.Fatalf("resting orders = %d, want 1", m.RestingOrderCount())
	}
}
