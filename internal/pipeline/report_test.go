package pipeline

import (
	"errors"
	"testing"

	"trading-system/internal/hft/engine"
)

func TestBuildExecReportsFillsV2Fields(t *testing.T) {
	order := engine.Order{
		ID:            7,
		ClientOrderID: "client-7",
		AccountID:     42,
		Symbol:        "BTCUSDT",
		Side:          engine.SideBuy,
		Type:          engine.OrderTypeLimit,
		TIF:           engine.TimeInForceGTC,
		Price:         100,
		Quantity:      "2",
	}
	trade := engine.Trade{
		ID:          11,
		Symbol:      "BTCUSDT",
		Price:       100,
		Quantity:    2,
		BuyOrderID:  7,
		SellOrderID: 10,
		Timestamp:   123456789,
	}
	result := engine.SubmitResult{
		Trades:      []engine.Trade{trade},
		ExecutedQty: 2,
		FinalStatus: engine.OrderStatusFilled,
	}

	reports := buildExecReports(order, result, nil, "paper")
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}

	got := reports[0]
	if got.ExecID == 0 {
		t.Fatal("ExecID = 0, want non-zero")
	}
	if got.Seq != 1 {
		t.Fatalf("Seq = %d, want 1", got.Seq)
	}
	if got.OrderID != order.ID || got.ClientOrderID != order.ClientOrderID {
		t.Fatalf("order identity = %d/%q, want %d/%q", got.OrderID, got.ClientOrderID, order.ID, order.ClientOrderID)
	}
	if got.AccountID != order.AccountID || got.Symbol != order.Symbol || got.Side != order.Side {
		t.Fatalf("order scope = %d/%q/%d, want %d/%q/%d", got.AccountID, got.Symbol, got.Side, order.AccountID, order.Symbol, order.Side)
	}
	if got.ExecType != engine.ExecTypeTrade || got.Status != engine.OrderStatusFilled {
		t.Fatalf("exec/status = %s/%d, want TRADE/FILLED", got.ExecType, got.Status)
	}
	if got.LastPx != trade.Price || got.LastQty != trade.Quantity {
		t.Fatalf("last = %d/%d, want %d/%d", got.LastPx, got.LastQty, trade.Price, trade.Quantity)
	}
	if got.CumQty != 2 || got.LeavesQty != 0 || got.Filled != 2 || got.Leaves != 0 {
		t.Fatalf("cum/leaves = %d/%d (filled/leaves %d/%d), want 2/0", got.CumQty, got.LeavesQty, got.Filled, got.Leaves)
	}
	if got.Liquidity != engine.LiquidityTaker {
		t.Fatalf("liquidity = %q, want TAKER", got.Liquidity)
	}
	if got.Venue != "paper" {
		t.Fatalf("venue = %q, want paper", got.Venue)
	}
	if got.OrderPx != order.Price || got.OrderType != order.Type || got.TIF != order.TIF || got.Quantity != order.Quantity {
		t.Fatalf("order metadata = %d/%s/%s/%q", got.OrderPx, got.OrderType, got.TIF, got.Quantity)
	}
	if got.Trade == nil || got.Trade.ID != trade.ID {
		t.Fatalf("trade = %+v, want id %d", got.Trade, trade.ID)
	}
}

func TestBuildExecReportsRejectedV2Fields(t *testing.T) {
	order := engine.Order{
		ID:            8,
		ClientOrderID: "client-8",
		AccountID:     43,
		Symbol:        "BTCUSDT",
		Side:          engine.SideSell,
		Type:          engine.OrderTypeLimit,
		TIF:           engine.TimeInForceIOC,
		Price:         101,
		Quantity:      "3",
	}

	reports := buildExecReports(order, engine.SubmitResult{}, errors.New("MAX_QTY: too large"), "risk")
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}

	got := reports[0]
	if got.ExecType != engine.ExecTypeRejected || got.Status != engine.OrderStatusRejected {
		t.Fatalf("exec/status = %s/%d, want REJECTED/REJECTED", got.ExecType, got.Status)
	}
	if got.Reason != "MAX_QTY: too large" {
		t.Fatalf("reason = %q", got.Reason)
	}
	if got.AccountID != order.AccountID || got.Symbol != order.Symbol || got.Side != order.Side {
		t.Fatalf("order scope = %d/%q/%d", got.AccountID, got.Symbol, got.Side)
	}
	if got.ExecID == 0 || got.Seq != 1 {
		t.Fatalf("exec identity = %d/%d, want non-zero/1", got.ExecID, got.Seq)
	}
}

func TestQuoteNotional(t *testing.T) {
	bus := &EventBus{riskPriceScale: 100_000_000, riskQuantityScale: 100_000_000}

	if got := bus.quoteNotional(100_000_000_00, 200_000_000); got != 200 {
		t.Fatalf("quoteNotional() = %d, want 200", got)
	}
	if got := bus.quoteNotional(0, 200_000_000); got != 0 {
		t.Fatalf("quoteNotional(zero price) = %d, want 0", got)
	}

	bus.riskPriceScale = 0
	if got := bus.quoteNotional(100, 200); got != 0 {
		t.Fatalf("quoteNotional(no price scale) = %d, want 0", got)
	}
}

func TestAssignReportIdentityIsPerOrder(t *testing.T) {
	bus := &EventBus{reportSeq: make(map[uint64]uint64)}

	first := engine.ExecReport{OrderID: 7}
	second := engine.ExecReport{OrderID: 7}
	other := engine.ExecReport{OrderID: 8}
	bus.assignReportIdentity(&first)
	bus.assignReportIdentity(&second)
	bus.assignReportIdentity(&other)

	if first.Seq != 1 || second.Seq != 2 || other.Seq != 1 {
		t.Fatalf("seqs = %d/%d/%d, want 1/2/1", first.Seq, second.Seq, other.Seq)
	}
	if first.ExecID == 0 || second.ExecID == 0 || other.ExecID == 0 {
		t.Fatalf("exec ids = %d/%d/%d, want non-zero", first.ExecID, second.ExecID, other.ExecID)
	}
	if first.ExecID == second.ExecID || second.ExecID == other.ExecID || first.ExecID == other.ExecID {
		t.Fatalf("exec ids collide: %d/%d/%d", first.ExecID, second.ExecID, other.ExecID)
	}
}
