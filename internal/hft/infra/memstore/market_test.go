package memstore

import (
	"testing"

	"trading-system/internal/hft/domain"
)

func TestApplyDepthReplacesSnapshot(t *testing.T) {
	m := New(Config{})

	m.Apply(domain.Event{
		Kind: domain.KindDepth, Symbol: "BTCUSDT",
		Depth: &domain.Depth{UpdateID: 1, Bids: []domain.Level{{Price: 100, Qty: 1}}},
	})
	m.Apply(domain.Event{
		Kind: domain.KindDepth, Symbol: "BTCUSDT",
		Depth: &domain.Depth{UpdateID: 2, Bids: []domain.Level{{Price: 101, Qty: 2}}},
	})

	got, ok := m.Depth("BTCUSDT")
	if !ok {
		t.Fatal("depth missing")
	}
	if got.UpdateID != 2 || len(got.Bids) != 1 || got.Bids[0].Price != 101 {
		t.Fatalf("depth = %+v, want the latest snapshot only", got)
	}
}

func TestApplyKlineMergesSameBucket(t *testing.T) {
	m := New(Config{})

	m.Apply(domain.Event{Kind: domain.KindKline, Symbol: "BTCUSDT",
		Kline: &domain.Kline{Interval: "1m", OpenTime: 1000, Close: 1}})
	m.Apply(domain.Event{Kind: domain.KindKline, Symbol: "BTCUSDT",
		Kline: &domain.Kline{Interval: "1m", OpenTime: 1000, Close: 2}})
	m.Apply(domain.Event{Kind: domain.KindKline, Symbol: "BTCUSDT",
		Kline: &domain.Kline{Interval: "1m", OpenTime: 2000, Close: 3}})

	got := m.Klines("BTCUSDT", "1m", 0)
	if len(got) != 2 {
		t.Fatalf("klines = %d, want 2", len(got))
	}
	if got[0].Close != 2 || got[1].Close != 3 {
		t.Fatalf("unexpected klines: %+v", got)
	}
}

func TestTradeRingRetainsNewest(t *testing.T) {
	m := New(Config{TradeCap: 3})

	for i := uint64(1); i <= 5; i++ {
		m.Apply(domain.Event{Kind: domain.KindTrade, Symbol: "BTCUSDT",
			Trade: &domain.Trade{ID: i}})
	}

	got := m.Trades("BTCUSDT", 0)
	if len(got) != 3 {
		t.Fatalf("trades = %d, want 3", len(got))
	}
	if got[0].ID != 3 || got[2].ID != 5 {
		t.Fatalf("unexpected trades: %+v", got)
	}
}

func TestTradesReturnsCopy(t *testing.T) {
	m := New(Config{})
	m.Apply(domain.Event{Kind: domain.KindTrade, Symbol: "BTCUSDT",
		Trade: &domain.Trade{ID: 1, Price: 100}})

	got := m.Trades("BTCUSDT", 0)
	got[0].Price = 999

	again := m.Trades("BTCUSDT", 0)
	if again[0].Price != 100 {
		t.Fatalf("stored trade mutated through returned slice: %+v", again[0])
	}
}

func TestApplyIgnoresMismatchedPayload(t *testing.T) {
	m := New(Config{})

	// Kind says trade but the payload is missing; this must not panic.
	m.Apply(domain.Event{Kind: domain.KindTrade, Symbol: "BTCUSDT"})
	m.Apply(domain.Event{Kind: domain.KindKline, Symbol: "BTCUSDT", Kline: &domain.Kline{}})

	if len(m.Trades("BTCUSDT", 0)) != 0 {
		t.Fatal("trade without payload must be ignored")
	}
	if len(m.Klines("BTCUSDT", "", 0)) != 0 {
		t.Fatal("kline without interval must be ignored")
	}
}
