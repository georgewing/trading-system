package market

import "testing"

func TestApplyDepthReplacesSnapshot(t *testing.T) {
	s := NewStore(DefaultStoreConfig())

	s.ApplyDepth(Depth{Symbol: "BTCUSDT", LastUpdateID: 1, Bids: [][2]string{{"100", "1"}}})
	s.ApplyDepth(Depth{Symbol: "BTCUSDT", LastUpdateID: 2, Bids: [][2]string{{"101", "2"}}})

	got, ok := s.Depth("BTCUSDT")
	if !ok {
		t.Fatal("depth missing")
	}
	if got.LastUpdateID != 2 || len(got.Bids) != 1 || got.Bids[0][0] != "101" {
		t.Fatalf("depth = %+v, want the latest snapshot only", got)
	}
}

func TestApplyKlineMergesSameBucket(t *testing.T) {
	s := NewStore(DefaultStoreConfig())

	s.ApplyKline(Kline{Symbol: "BTCUSDT", Interval: "1m", OpenTime: 1000, Close: "1"})
	s.ApplyKline(Kline{Symbol: "BTCUSDT", Interval: "1m", OpenTime: 1000, Close: "2"})
	s.ApplyKline(Kline{Symbol: "BTCUSDT", Interval: "1m", OpenTime: 2000, Close: "3"})

	got := s.Klines("BTCUSDT", "1m", 0)
	if len(got) != 2 {
		t.Fatalf("klines = %d, want 2", len(got))
	}
	if got[0].Close != "2" || got[1].Close != "3" {
		t.Fatalf("unexpected klines: %+v", got)
	}
}

func TestTradeRingRetainsNewest(t *testing.T) {
	cfg := DefaultStoreConfig()
	cfg.TradeCap = 3
	s := NewStore(cfg)

	for i := 1; i <= 5; i++ {
		s.ApplyTrade(Trade{Symbol: "BTCUSDT", ID: uint64(i)})
	}

	got := s.Trades("BTCUSDT", 0)
	if len(got) != 3 {
		t.Fatalf("trades = %d, want 3", len(got))
	}
	if got[0].ID != 3 || got[2].ID != 5 {
		t.Fatalf("unexpected trades: %+v", got)
	}
}

func TestSubscribeReceivesEvents(t *testing.T) {
	s := NewStore(DefaultStoreConfig())
	ch, cancel := s.Subscribe(8)
	defer cancel()

	s.ApplyDepth(Depth{Symbol: "BTCUSDT"})

	select {
	case ev := <-ch:
		if ev.Type != EventDepth || ev.Symbol != "BTCUSDT" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	default:
		t.Fatal("no event delivered")
	}
}

func TestSubscribeCancelClosesChannel(t *testing.T) {
	s := NewStore(DefaultStoreConfig())
	ch, cancel := s.Subscribe(1)
	cancel()

	if _, ok := <-ch; ok {
		t.Fatal("channel must be closed by cancel")
	}
}
