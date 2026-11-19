package risk

import (
	"context"
	"testing"
	"time"

	"trading-system/internal/hft/engine"
)

func TestPositionsAttributePnLPerStrategy(t *testing.T) {
	pos := NewPositions()

	pos.ApplyFillFor(7, "mm", "X", engine.SideBuy, 100, 10)
	pos.ApplyFillFor(7, "arb", "Y", engine.SideBuy, 200, 5)
	pos.Mark("X", 120)
	pos.Mark("Y", 180)

	if got := pos.StrategyEquity(7, "mm"); got != 200 {
		t.Fatalf("mm equity = %d, want 200", got)
	}
	if got := pos.StrategyEquity(7, "arb"); got != -100 {
		t.Fatalf("arb equity = %d, want -100", got)
	}
	if got := pos.AccountEquity(7); got != 100 {
		t.Fatalf("account equity = %d, want 100", got)
	}

	// Untagged fills belong to the default strategy.
	pos.ApplyFill(7, "Z", engine.SideBuy, 50, 2)
	pos.Mark("Z", 60)
	if got := pos.StrategyEquity(7, DefaultStrategy); got != 20 {
		t.Fatalf("default equity = %d, want 20", got)
	}
}

func TestMonitorBreakersAreScopedPerStrategy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewMonitor(IntradayConfig{MaxOpenOrders: 1}, nil)

	mm := Subject{AccountID: 7, Strategy: "mm"}
	arb := Subject{AccountID: 7, Strategy: "arb"}

	m.OnOrderAccepted(mm, now)

	if got, _ := m.Check(mm, now); got.Code != CodeMaxOpenOrders {
		t.Fatalf("mm decision = %+v, want %s", got, CodeMaxOpenOrders)
	}
	if got, _ := m.Check(arb, now); !got.Approved {
		t.Fatalf("arb should be unaffected: %+v", got)
	}

	if state, _ := m.State(mm); state != ActionAllow {
		t.Fatalf("mm state = %s, want allow (max open orders only rejects)", state)
	}
}

func TestEngineDailyLossHaltsOnlyTheLosingStrategy(t *testing.T) {
	pos := NewPositions()
	e := NewEngine(EngineConfig{Intraday: IntradayConfig{MaxDailyLoss: 50}}, pos)

	fill := func(strategy string, side engine.Side, price, qty int64) {
		t.Helper()
		order := engine.Order{
			ID:        1,
			AccountID: 7,
			Symbol:    "X",
			Strategy:  strategy,
			Side:      side,
		}
		e.OnExecReport(order, engine.ExecReport{
			OrderID: 1,
			Status:  engine.OrderStatusFilled,
			Trade:   &engine.Trade{Price: price, Quantity: qty},
		})
	}

	// mm loses 100; arb makes 100.
	fill("mm", engine.SideBuy, 100, 10)
	fill("mm", engine.SideSell, 90, 10)
	fill("arb", engine.SideBuy, 100, 5)
	fill("arb", engine.SideSell, 120, 5)

	mmReq := engineRequest()
	mmReq.Strategy = "mm"
	if got := e.Check(context.Background(), mmReq); got.Code != CodeStrategyHalted {
		t.Fatalf("mm decision = %+v, want %s", got, CodeStrategyHalted)
	}

	arbReq := engineRequest()
	arbReq.Strategy = "arb"
	arbReq.ClientOrderID = "c-2"
	if got := e.Check(context.Background(), arbReq); !got.Approved {
		t.Fatalf("arb decision = %+v, want approved", got)
	}
}

func TestEngineNormalizesEmptyStrategy(t *testing.T) {
	pos := NewPositions()
	pos.ApplyFillFor(7, DefaultStrategy, "X", engine.SideBuy, 100, 10)
	e := NewEngine(EngineConfig{}, pos)

	req := engineRequest()
	if got := e.Check(context.Background(), req); !got.Approved {
		t.Fatalf("untagged order: %+v", got)
	}
	if got := pos.StrategyEquity(7, DefaultStrategy); got != 0 {
		t.Fatalf("default equity = %d, want 0 (marked at cost)", got)
	}
}
