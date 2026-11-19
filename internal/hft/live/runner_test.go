package live

import (
	"context"
	"os"
	"testing"
	"time"

	"trading-system/internal/hft/domain"
	"trading-system/internal/hft/engine"
	"trading-system/internal/hft/risk"
	"trading-system/internal/hft/strategy"
	"trading-system/internal/pipeline"
	"trading-system/pkg/idgen"
)

func TestMain(m *testing.M) {
	if err := idgen.Init(1); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func testBus(t *testing.T) (*pipeline.EventBus, *engine.Matcher, context.CancelFunc) {
	t.Helper()

	ob := engine.NewOrderBook("BTCUSDT", 10, 10)
	matcher := engine.NewMatcher(ob)
	bus := pipeline.NewEventBus(matcher, 16)

	ctx, cancel := context.WithCancel(context.Background())
	bus.Start(ctx)

	t.Cleanup(func() {
		bus.StopAccepting()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
		defer drainCancel()
		_ = bus.Drain(drainCtx)
		cancel()
	})
	return bus, matcher, cancel
}

func TestRunnerContextConvertsDepthAndTick(t *testing.T) {
	ctx := newRunnerContext(Deps{Symbol: "BTCUSDT"}, "mm")

	ctx.setDepth("BTCUSDT", domain.Depth{
		Bids: []domain.Level{{Price: 100, Qty: 5}, {Price: 99, Qty: 7}},
		Asks: []domain.Level{{Price: 101, Qty: 3}},
	}, 1_700_000_000_000)

	book, ok := ctx.Book("BTCUSDT")
	if !ok {
		t.Fatal("book missing")
	}
	if len(book.Bids) != 2 || book.Bids[0].Price != 100 || book.Bids[1].Qty != 7 {
		t.Fatalf("bids = %+v", book.Bids)
	}
	if mid, ok := book.Mid(); !ok || mid != 100 {
		t.Fatalf("mid = %d, ok=%v, want 100", mid, ok)
	}

	ctx.setTick("BTCUSDT", domain.Tick{
		BidPrice: 110, BidQty: 2,
		AskPrice: 111, AskQty: 4,
	}, 1_700_000_001_000)

	book, _ = ctx.Book("BTCUSDT")
	if len(book.Bids) != 1 || book.Bids[0].Price != 110 || len(book.Asks) != 1 || book.Asks[0].Price != 111 {
		t.Fatalf("tick book = %+v", book)
	}
}

func TestRunnerContextSubmitsTaggedOrder(t *testing.T) {
	bus, matcher, _ := testBus(t)

	ctx := newRunnerContext(Deps{
		Bus:               bus,
		Matcher:           matcher,
		Positions:         risk.NewPositions(),
		AccountID:         7,
		Symbol:            "BTCUSDT",
		QuantityPrecision: 1,
		SubmitTimeout:     time.Second,
	}, "market_maker")

	res, err := ctx.Submit(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   engine.SideBuy,
		Type:   engine.OrderTypeLimit,
		TIF:    engine.TimeInForceGTC,
		Price:  100,
		Qty:    5,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.OrderID == 0 {
		t.Fatal("order id is zero")
	}
	if res.Status != engine.OrderStatusNew || res.Leaves != 5 {
		t.Fatalf("result = %+v, want resting 5", res)
	}

	open := ctx.OpenOrders("BTCUSDT")
	if len(open) != 1 || open[0] != res.OrderID {
		t.Fatalf("open orders = %v, want [%d]", open, res.OrderID)
	}
	if got := matcher.OpenOrders(7, "other", "BTCUSDT"); len(got) != 0 {
		t.Fatalf("other strategy sees orders: %v", got)
	}
}

func TestRunnerContextRejectsUnsupportedSymbol(t *testing.T) {
	ctx := newRunnerContext(Deps{
		Positions: risk.NewPositions(),
		AccountID: 7,
		Symbol:    "BTCUSDT",
	}, "mm")

	_, err := ctx.Submit(strategy.OrderRequest{
		Symbol: "ETHUSDT",
		Side:   engine.SideBuy,
		Type:   engine.OrderTypeLimit,
		TIF:    engine.TimeInForceGTC,
		Price:  100,
		Qty:    1,
	})
	if err == nil {
		t.Fatal("expected unsupported symbol error")
	}
}

func TestStartStopNoopStrategy(t *testing.T) {
	bus, matcher, _ := testBus(t)
	fanout := domain.NewMemFanout(16)

	runner, err := Start(context.Background(), Deps{
		Bus:       bus,
		Matcher:   matcher,
		Positions: risk.NewPositions(),
		Stream:    fanout,
		AccountID: 7,
		Symbol:    "BTCUSDT",
		Timer:     10 * time.Millisecond,
	}, Config{Strategy: strategy.NewNoop()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	fanout.Publish(domain.Event{
		Kind:   domain.KindDepth,
		Symbol: "BTCUSDT",
		TS:     time.Now().UnixMilli(),
		Depth:  &domain.Depth{Bids: []domain.Level{{Price: 100, Qty: 1}}, Asks: []domain.Level{{Price: 101, Qty: 1}}},
	})

	time.Sleep(30 * time.Millisecond)
	runner.Stop()
	runner.Stop() // idempotent
}
