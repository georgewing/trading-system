package app

import (
	"testing"
	"time"

	"trading-system/internal/hft/engine"
	"trading-system/internal/storage/clickhouse"
)

func sampleExecReport() engine.ExecReport {
	ts := time.Now().UnixNano()
	return engine.ExecReport{
		ExecID:        101,
		Seq:           2,
		OrderID:       5001,
		ClientOrderID: "client-5001",
		AccountID:     42,
		Symbol:        "BTCUSDT",
		Side:          engine.SideBuy,
		ExecType:      engine.ExecTypeTrade,
		Status:        engine.OrderStatusPartiallyFilled,
		Filled:        5,
		Leaves:        5,
		LastPx:        100,
		LastQty:       5,
		CumQty:        5,
		LeavesQty:     5,
		Liquidity:     engine.LiquidityTaker,
		OrderPx:       100,
		OrderType:     engine.OrderTypeLimit,
		TIF:           engine.TimeInForceGTC,
		Quantity:      "10",
		Venue:         "paper",
		Timestamp:     ts,
		Trade: &engine.Trade{
			ID:          9001,
			Symbol:      "BTCUSDT",
			Price:       100,
			Quantity:    5,
			BuyOrderID:  5001,
			SellOrderID: 6001,
			MakerOrder:  6001,
			Timestamp:   ts,
		},
	}
}

func TestToPGExecEvent(t *testing.T) {
	report := sampleExecReport()
	got := toPGExecEvent(report)

	if got.ExecID != report.ExecID || got.Seq != report.Seq || got.OrderID != report.OrderID {
		t.Fatalf("identity = %d/%d/%d", got.ExecID, got.Seq, got.OrderID)
	}
	if got.AccountID != report.AccountID || got.Symbol != report.Symbol || got.Side != int8(report.Side) {
		t.Fatalf("scope = %d/%q/%d", got.AccountID, got.Symbol, got.Side)
	}
	if got.ExecType != string(report.ExecType) || got.Status != int8(report.Status) {
		t.Fatalf("exec/status = %q/%d", got.ExecType, got.Status)
	}
	if got.LastPx != report.LastPx || got.LastQty != report.LastQty || got.CumQty != report.CumQty || got.LeavesQty != report.LeavesQty {
		t.Fatalf("fill fields = %d/%d/%d/%d", got.LastPx, got.LastQty, got.CumQty, got.LeavesQty)
	}
	if got.Liquidity != string(report.Liquidity) || got.TradeID != report.Trade.ID || got.Venue != report.Venue {
		t.Fatalf("liquidity/trade/venue = %q/%d/%q", got.Liquidity, got.TradeID, got.Venue)
	}
	if got.ReportedAt.UnixNano() != report.Timestamp {
		t.Fatalf("reported_at = %d, want %d", got.ReportedAt.UnixNano(), report.Timestamp)
	}
}

func TestToPGOrderAndTrade(t *testing.T) {
	report := sampleExecReport()
	order := toPGOrder(report)
	if order.ID != report.OrderID || order.ClientOrderID != report.ClientOrderID {
		t.Fatalf("order identity = %d/%q", order.ID, order.ClientOrderID)
	}
	if order.Side != int8(report.Side) || order.OrderType != string(report.OrderType) || order.TimeInForce != string(report.TIF) {
		t.Fatalf("order fields = %d/%q/%q", order.Side, order.OrderType, order.TimeInForce)
	}
	if order.Price != report.OrderPx || order.Quantity != report.Quantity {
		t.Fatalf("order price/qty = %d/%q", order.Price, order.Quantity)
	}
	if order.Filled != report.Filled || order.Leaves != report.Leaves || order.Status != int8(report.Status) {
		t.Fatalf("order state = %d/%d/%d", order.Filled, order.Leaves, order.Status)
	}

	trade := toPGTrade(report)
	if trade.ID != report.Trade.ID || trade.Symbol != report.Symbol {
		t.Fatalf("trade identity = %d/%q", trade.ID, trade.Symbol)
	}
	if trade.Price != report.Trade.Price || trade.Quantity != report.Trade.Quantity {
		t.Fatalf("trade price/qty = %d/%d", trade.Price, trade.Quantity)
	}
	if trade.BuyOrderID != report.Trade.BuyOrderID || trade.SellOrderID != report.Trade.SellOrderID || trade.MakerOrderID != report.Trade.MakerOrder {
		t.Fatalf("trade order ids = %d/%d/%d", trade.BuyOrderID, trade.SellOrderID, trade.MakerOrderID)
	}
	if trade.TradedAt.UnixNano() != report.Trade.Timestamp {
		t.Fatalf("traded_at = %d, want %d", trade.TradedAt.UnixNano(), report.Trade.Timestamp)
	}
}

func TestToCHTradeTick(t *testing.T) {
	report := sampleExecReport()
	got := toCHTradeTick(report)

	want := clickhouse.TradeTick{
		TS:          time.Unix(0, report.Trade.Timestamp),
		Symbol:      report.Symbol,
		TradeID:     report.Trade.ID,
		Price:       report.Trade.Price,
		Quantity:    report.Trade.Quantity,
		BuyOrderID:  report.Trade.BuyOrderID,
		SellOrderID: report.Trade.SellOrderID,
	}
	if got.TS.UnixNano() != want.TS.UnixNano() {
		t.Fatalf("ts = %d, want %d", got.TS.UnixNano(), want.TS.UnixNano())
	}
	got.TS = want.TS
	if got != want {
		t.Fatalf("tick = %+v, want %+v", got, want)
	}
}
