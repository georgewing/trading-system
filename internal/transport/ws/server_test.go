package ws

import (
	"testing"

	"trading-system/internal/hft/engine"
)

func TestDTOFromExecReportV2Fields(t *testing.T) {
	rep := engine.ExecReport{
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
		Venue:         "paper",
		Reason:        "partial fill",
		Timestamp:     123456789,
		Trade: &engine.Trade{
			ID:          9001,
			Symbol:      "BTCUSDT",
			Price:       100,
			Quantity:    5,
			BuyOrderID:  5001,
			SellOrderID: 6001,
			Timestamp:   123456789,
		},
	}

	dto := dtoFromExecReport(rep)
	if dto.ExecID != "101" || dto.Seq != 2 || dto.OrderID != "5001" {
		t.Fatalf("identity = %q/%d/%q", dto.ExecID, dto.Seq, dto.OrderID)
	}
	if dto.ClientOrderID != rep.ClientOrderID || dto.AccountID != rep.AccountID {
		t.Fatalf("owner = %q/%d", dto.ClientOrderID, dto.AccountID)
	}
	if dto.Symbol != rep.Symbol || dto.Side != "BUY" {
		t.Fatalf("symbol/side = %q/%q", dto.Symbol, dto.Side)
	}
	if dto.ExecType != "TRADE" || dto.Status != "PARTIALLY_FILLED" {
		t.Fatalf("exec/status = %q/%q", dto.ExecType, dto.Status)
	}
	if dto.LastPx != 100 || dto.LastQty != 5 || dto.CumQty != 5 || dto.LeavesQty != 5 {
		t.Fatalf("fill = %d/%d/%d/%d", dto.LastPx, dto.LastQty, dto.CumQty, dto.LeavesQty)
	}
	if dto.Liquidity != "TAKER" || dto.Venue != "paper" {
		t.Fatalf("liquidity/venue = %q/%q", dto.Liquidity, dto.Venue)
	}
	if dto.Trade == nil || dto.Trade.ID != 9001 {
		t.Fatalf("trade = %+v", dto.Trade)
	}
}
