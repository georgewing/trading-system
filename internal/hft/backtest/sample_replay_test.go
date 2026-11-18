package backtest

import (
	"context"
	"math"
	"testing"

	"trading-system/internal/hft/strategy"
)

// TestSampleReplayAccountingIdentity replays the bundled sample through the
// market maker and checks that cash, realized PnL, unrealized PnL and fees stay
// internally consistent.
func TestSampleReplayAccountingIdentity(t *testing.T) {
	src, err := OpenJSONL("testdata/sample.jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	sim := NewSimulator(Config{
		InitialCash: 10_000_000_000,
		Fee:         FeeModel{MakerBps: 2, TakerBps: 5},
		SlippageBps: 1,
		Fill:        FillModel{ParticipationBps: 5000, QueuePositionBps: 5000},
		MaxLevels:   10,
		Seed:        42,
	})

	runner := strategy.NewRunner(strategy.NewMarketMaker(), sim, strategy.RunnerConfig{
		MaxOpenOrders: 4,
	})
	if err := runner.Init(map[string]string{
		"symbol":          "BTCUSDT",
		"half_spread_bps": "2",
		"order_qty":       "100000000",
		"inventory_limit": "0",
		"min_requote_bps": "1",
	}); err != nil {
		t.Fatalf("init strategy: %v", err)
	}

	ctx := context.Background()
	var ev Event
	var lastTimer int64
	for {
		ok, err := src.Next(ctx, &ev)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if !ok {
			break
		}

		switch ev.Kind {
		case KindBook:
			sim.ApplyBook(ev.Symbol, ev.Book.Bids, ev.Book.Asks, ev.TS)
			if view, ok := sim.Book(ev.Symbol); ok {
				if err := runner.OnBook(view); err != nil {
					t.Fatalf("on book: %v", err)
				}
			}
		case KindTrade:
			view := strategy.TradeView{Symbol: ev.Symbol, Price: ev.Trade.Price, Qty: ev.Trade.Qty, TS: ev.TS}
			sim.ApplyTrade(view)
			if err := runner.OnTrade(view); err != nil {
				t.Fatalf("on trade: %v", err)
			}
		}

		if ev.TS-lastTimer >= 60_000 {
			lastTimer = ev.TS
			if err := runner.OnTimer(ev.TS); err != nil {
				t.Fatalf("on timer: %v", err)
			}
		}
	}
	if err := runner.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	pf := sim.Portfolio()
	want := pf.InitialCash() + pf.Realized() + pf.Unrealized() - pf.Fees()
	got := pf.Equity()
	if diff := got - want; diff > 1_000 || diff < -1_000 {
		t.Fatalf("accounting identity broken: equity=%d want=%d diff=%d (pos=%d mark? realized=%d unrealized=%d fees=%d)",
			got, want, diff, pf.Position("BTCUSDT"), pf.Realized(), pf.Unrealized(), pf.Fees())
	}

	// The strategy must not be able to end with a runaway position: with a
	// symmetric quote and partial fills, exposure should stay bounded by the
	// order size.
	if pos := pf.Position("BTCUSDT"); math.Abs(float64(pos)) > 2*100_000_000 {
		t.Fatalf("runaway position %d", pos)
	}
}
