package backtest

import (
	"errors"
	"math"
	"testing"

	"trading-system/internal/hft/engine"
	"trading-system/internal/strategy"
)

func testSim(cfg Config) *Simulator {
	if cfg.Seed == 0 {
		cfg.Seed = 7
	}
	if cfg.MaxLevels == 0 {
		cfg.MaxLevels = 10
	}
	return NewSimulator(cfg)
}

func book(sim *Simulator, symbol string, bids, asks []strategy.Level, ts int64) {
	sim.ApplyBook(symbol, bids, asks, ts)
}

func TestFeeModel(t *testing.T) {
	m := FeeModel{MakerBps: 2, TakerBps: 5}

	if got, want := m.Fee(10000, 100, false), int64(500); got != want {
		t.Fatalf("taker fee = %d, want %d", got, want)
	}
	if got, want := m.Fee(10000, 100, true), int64(200); got != want {
		t.Fatalf("maker fee = %d, want %d", got, want)
	}
	if got := (FeeModel{}).Fee(100, 100, false); got != 0 {
		t.Fatalf("zero bps fee = %d, want 0", got)
	}
	if got := m.Fee(math.MaxInt64, 2, false); got != math.MaxInt64 {
		t.Fatalf("overflow fee = %d, want saturate", got)
	}
}

func TestSubmitSweepsLevelsAndChargesFees(t *testing.T) {
	sim := testSim(Config{
		InitialCash: 1_000_000,
		Fee:         FeeModel{TakerBps: 100},
		Fill:        FillModel{ParticipationBps: 0},
	})
	book(sim, "X",
		[]strategy.Level{{Price: 100, Qty: 10}},
		[]strategy.Level{{Price: 101, Qty: 5}, {Price: 102, Qty: 10}},
		1000)

	res, err := sim.Submit(strategy.OrderRequest{
		Symbol: "X",
		Side:   engine.SideBuy,
		Type:   engine.OrderTypeLimit,
		TIF:    engine.TimeInForceIOC,
		Price:  102,
		Qty:    12,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Status != engine.OrderStatusFilled {
		t.Fatalf("status = %v, want FILLED", res.Status)
	}
	if res.Filled != 12 {
		t.Fatalf("filled = %d, want 12", res.Filled)
	}
	if len(res.Fills) != 2 {
		t.Fatalf("fills = %d, want 2", len(res.Fills))
	}
	if res.Fills[0].Price != 101 || res.Fills[0].Qty != 5 {
		t.Fatalf("first fill = %+v", res.Fills[0])
	}
	if res.Fills[1].Price != 102 || res.Fills[1].Qty != 7 {
		t.Fatalf("second fill = %+v", res.Fills[1])
	}

	if got := sim.Portfolio().Position("X"); got != 12 {
		t.Fatalf("position = %d, want 12", got)
	}
	wantCash := int64(1_000_000 - 505 - 5 - 714 - 7)
	if got := sim.Portfolio().Equity(); got != wantCash+12*102 {
		t.Fatalf("equity = %d, want cash %d plus marked position %d", got, wantCash, 12*102)
	}

	// The swept levels are consumed.
	view, ok := sim.Book("X")
	if !ok {
		t.Fatal("book missing")
	}
	if len(view.Asks) != 1 || view.Asks[0].Price != 102 || view.Asks[0].Qty != 3 {
		t.Fatalf("asks after sweep = %+v", view.Asks)
	}
}

func TestPassiveFillOnCrossedTrade(t *testing.T) {
	sim := testSim(Config{
		InitialCash: 1_000_000,
		Fee:         FeeModel{MakerBps: 10},
		Fill:        FillModel{ParticipationBps: 10000, QueuePositionBps: 0},
	})
	book(sim, "X",
		[]strategy.Level{{Price: 100, Qty: 10}},
		[]strategy.Level{{Price: 101, Qty: 10}},
		1000)

	res, err := sim.Submit(strategy.OrderRequest{
		Symbol: "X", Side: engine.SideBuy, Type: engine.OrderTypeLimit,
		TIF: engine.TimeInForceGTC, Price: 100, Qty: 4,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Status != engine.OrderStatusNew || res.Leaves != 4 {
		t.Fatalf("resting result = %+v", res)
	}

	sim.ApplyTrade(strategy.TradeView{Symbol: "X", Price: 99, Qty: 10, TS: 2000})

	if got := sim.Portfolio().Position("X"); got != 4 {
		t.Fatalf("position = %d, want 4", got)
	}
	if got := sim.Stats().FilledOrders; got != 1 {
		t.Fatalf("filled orders = %d, want 1", got)
	}
	if len(sim.OpenOrders("X")) != 0 {
		t.Fatalf("open orders = %v, want none", sim.OpenOrders("X"))
	}
}

func TestTouchFillProbabilities(t *testing.T) {
	specs := []struct {
		name     string
		queuePos int64
		wantPos  int64
	}{
		{"front of queue fills", 0, 3},
		{"back of queue never fills", 10000, 0},
	}

	for _, spec := range specs {
		t.Run(spec.name, func(t *testing.T) {
			sim := testSim(Config{
				InitialCash: 1_000_000,
				Fill:        FillModel{ParticipationBps: 10000, QueuePositionBps: spec.queuePos},
			})
			book(sim, "X",
				[]strategy.Level{{Price: 100, Qty: 10}},
				[]strategy.Level{{Price: 101, Qty: 10}},
				1000)

			if _, err := sim.Submit(strategy.OrderRequest{
				Symbol: "X", Side: engine.SideBuy, Type: engine.OrderTypeLimit,
				TIF: engine.TimeInForceGTC, Price: 100, Qty: 3,
			}); err != nil {
				t.Fatalf("submit: %v", err)
			}

			sim.ApplyTrade(strategy.TradeView{Symbol: "X", Price: 100, Qty: 10, TS: 2000})

			if got := sim.Portfolio().Position("X"); got != spec.wantPos {
				t.Fatalf("position = %d, want %d", got, spec.wantPos)
			}
		})
	}
}

func TestFOKIsAllOrNothing(t *testing.T) {
	sim := testSim(Config{InitialCash: 1_000_000, Fill: FillModel{ParticipationBps: 0}})
	book(sim, "X",
		[]strategy.Level{{Price: 100, Qty: 10}},
		[]strategy.Level{{Price: 101, Qty: 5}},
		1000)

	_, err := sim.Submit(strategy.OrderRequest{
		Symbol: "X", Side: engine.SideBuy, Type: engine.OrderTypeLimit,
		TIF: engine.TimeInForceFOK, Price: 101, Qty: 10,
	})
	if !errors.Is(err, ErrFOKUnfilled) {
		t.Fatalf("err = %v, want ErrFOKUnfilled", err)
	}
	if got := sim.Portfolio().Position("X"); got != 0 {
		t.Fatalf("position after rejected FOK = %d, want 0", got)
	}

	// The book must be untouched: a later order can still take the level.
	res, err := sim.Submit(strategy.OrderRequest{
		Symbol: "X", Side: engine.SideBuy, Type: engine.OrderTypeLimit,
		TIF: engine.TimeInForceIOC, Price: 101, Qty: 5,
	})
	if err != nil {
		t.Fatalf("submit after FOK: %v", err)
	}
	if res.Filled != 5 {
		t.Fatalf("filled = %d, want 5", res.Filled)
	}
}

func TestMarketOrderWithoutBookIsCanceled(t *testing.T) {
	sim := testSim(Config{InitialCash: 1000})

	res, err := sim.Submit(strategy.OrderRequest{
		Symbol: "X", Side: engine.SideBuy, Type: engine.OrderTypeMarket,
		TIF: engine.TimeInForceIOC, Qty: 3,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Status != engine.OrderStatusCanceled || res.Canceled != 3 {
		t.Fatalf("result = %+v, want canceled 3", res)
	}
	if got := sim.Stats().CanceledQty; got != 3 {
		t.Fatalf("canceled qty = %d, want 3", got)
	}
}

func TestCancelRestingOrder(t *testing.T) {
	sim := testSim(Config{InitialCash: 1_000_000})
	book(sim, "X",
		[]strategy.Level{{Price: 100, Qty: 10}},
		[]strategy.Level{{Price: 101, Qty: 10}},
		1000)

	res, err := sim.Submit(strategy.OrderRequest{
		Symbol: "X", Side: engine.SideBuy, Type: engine.OrderTypeLimit,
		TIF: engine.TimeInForceGTC, Price: 100, Qty: 5,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := sim.OpenOrders("X"); len(got) != 1 || got[0] != res.OrderID {
		t.Fatalf("open orders = %v, want [%d]", got, res.OrderID)
	}

	if err := sim.Cancel(res.OrderID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := sim.OpenOrders("X"); len(got) != 0 {
		t.Fatalf("open orders after cancel = %v", got)
	}
	if err := sim.Cancel(res.OrderID); !errors.Is(err, ErrUnknownOrder) {
		t.Fatalf("second cancel err = %v, want ErrUnknownOrder", err)
	}
}

func TestSlippageIsClampedToLimit(t *testing.T) {
	sim := testSim(Config{
		InitialCash: 1_000_000,
		SlippageBps: 10000, // 100%
		Fill:        FillModel{ParticipationBps: 0},
	})
	book(sim, "X",
		[]strategy.Level{{Price: 100, Qty: 10}},
		[]strategy.Level{{Price: 100, Qty: 5}},
		1000)

	res, err := sim.Submit(strategy.OrderRequest{
		Symbol: "X", Side: engine.SideBuy, Type: engine.OrderTypeLimit,
		TIF: engine.TimeInForceIOC, Price: 101, Qty: 5,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(res.Fills) != 1 || res.Fills[0].Price != 101 {
		t.Fatalf("fills = %+v, want one fill at the limit 101", res.Fills)
	}
}

func TestReplayIsDeterministic(t *testing.T) {
	run := func() (int64, int, int64) {
		sim := testSim(Config{
			InitialCash: 1_000_000,
			Fee:         FeeModel{MakerBps: 5, TakerBps: 5},
			Fill:        FillModel{ParticipationBps: 5000, QueuePositionBps: 5000},
			Seed:        99,
		})
		book(sim, "X",
			[]strategy.Level{{Price: 100, Qty: 100}},
			[]strategy.Level{{Price: 101, Qty: 100}},
			1000)

		if _, err := sim.Submit(strategy.OrderRequest{
			Symbol: "X", Side: engine.SideBuy, Type: engine.OrderTypeLimit,
			TIF: engine.TimeInForceGTC, Price: 100, Qty: 50,
		}); err != nil {
			t.Fatalf("submit: %v", err)
		}

		for i := 0; i < 20; i++ {
			sim.ApplyTrade(strategy.TradeView{
				Symbol: "X", Price: 100, Qty: 10, TS: int64(2000 + i*10),
			})
		}
		return sim.Portfolio().Equity(), sim.Stats().Fills, sim.Portfolio().Realized()
	}

	eq1, fills1, realized1 := run()
	eq2, fills2, realized2 := run()

	if eq1 != eq2 || fills1 != fills2 || realized1 != realized2 {
		t.Fatalf("replay not deterministic: (%d,%d,%d) vs (%d,%d,%d)",
			eq1, fills1, realized1, eq2, fills2, realized2)
	}
}
