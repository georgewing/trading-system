package orders

import (
	"testing"

	"trading-system/internal/hft/engine"
)

func TestBookAddAndApply(t *testing.T) {
	b := NewBook(10)
	b.AddOrder(engine.Order{
		ID: 1, ClientOrderID: "c1", Symbol: "BTCUSDT",
		Side: engine.SideBuy, Type: engine.OrderTypeLimit, TIF: engine.TimeInForceGTC,
		Price: 100, Quantity: "1",
	})

	b.Apply(engine.ExecReport{OrderID: 1, Status: engine.OrderStatusFilled, Filled: 1}, 200)

	rec, ok := b.Get(1)
	if !ok {
		t.Fatal("order missing")
	}
	if rec.Status != engine.OrderStatusFilled || rec.Filled != 1 {
		t.Fatalf("record = %+v, want filled", rec)
	}
	if rec.ClientOrderID != "c1" || rec.Price != 100 {
		t.Fatalf("submission fields lost: %+v", rec)
	}
}

func TestBookAppliesReportForUnknownOrder(t *testing.T) {
	b := NewBook(10)
	b.Apply(engine.ExecReport{OrderID: 7, Status: engine.OrderStatusRejected, Reason: "MAX_QTY"}, 100)

	rec, ok := b.Get(7)
	if !ok {
		t.Fatal("placeholder record missing")
	}
	if rec.Status != engine.OrderStatusRejected || rec.Reason != "MAX_QTY" {
		t.Fatalf("record = %+v", rec)
	}
}

func TestBookIgnoresDuplicateAdd(t *testing.T) {
	b := NewBook(10)
	b.AddOrder(engine.Order{ID: 1, ClientOrderID: "first"})
	b.AddOrder(engine.Order{ID: 1, ClientOrderID: "second"})

	rec, _ := b.Get(1)
	if rec.ClientOrderID != "first" || b.Len() != 1 {
		t.Fatalf("duplicate add must be ignored, got %+v len=%d", rec, b.Len())
	}
}

func TestBookRecentIsNewestFirstAndBounded(t *testing.T) {
	b := NewBook(2)
	for i := uint64(1); i <= 3; i++ {
		b.AddOrder(engine.Order{ID: i})
	}

	got := b.Recent(10)
	if len(got) != 2 {
		t.Fatalf("records = %d, want capacity 2", len(got))
	}
	if got[0].OrderID != 3 {
		t.Fatalf("newest first expected, got %+v", got)
	}
}

func TestStatusName(t *testing.T) {
	cases := map[engine.OrderStatus]string{
		engine.OrderStatusNew:             "NEW",
		engine.OrderStatusPartiallyFilled: "PARTIALLY_FILLED",
		engine.OrderStatusFilled:          "FILLED",
		engine.OrderStatusCanceled:        "CANCELED",
		engine.OrderStatusRejected:        "REJECTED",
		engine.OrderStatus(0):             "UNKNOWN",
	}
	for status, want := range cases {
		if got := StatusName(status); got != want {
			t.Errorf("StatusName(%d) = %q, want %q", status, got, want)
		}
	}
}
