package binance

import (
	"testing"

	"trading-system/internal/market"
)

// Binance mixes lower and upper case keys in the kline payload: "l" is the low
// price while "L" is the last trade id, and "v" is volume while "V" is taker
// buy volume. encoding/json falls back to a case-insensitive match when a key
// is missing, which silently decodes the wrong field. These tests pin the
// behavior so that regression cannot come back unnoticed.
func TestHandleKlineKeepsLowAndVolume(t *testing.T) {
	client, store := newTestClient(t)

	raw := []byte(`{
		"stream": "btcusdt@kline_1m",
		"data": {
			"e": "kline",
			"E": 1700000000123,
			"k": {
				"t": 1700000000000,
				"T": 1700000059999,
				"s": "BTCUSDT",
				"i": "1m",
				"f": 100,
				"L": 999,
				"o": "100.0",
				"c": "101.0",
				"h": "102.0",
				"l": "99.0",
				"v": "12.5",
				"n": 42,
				"x": false,
				"q": "1250.0",
				"V": "7.0",
				"Q": "700.0"
			}
		}
	}`)

	if err := client.handle(raw); err != nil {
		t.Fatalf("handle: %v", err)
	}

	klines := store.Klines("BTCUSDT", "1m", 0)
	if len(klines) != 1 {
		t.Fatalf("klines = %d, want 1", len(klines))
	}

	got := klines[0]
	if got.Low != "99.0" {
		t.Errorf("Low = %q, want %q (regression: \"L\" leaked into \"l\")", got.Low, "99.0")
	}
	if got.Volume != "12.5" {
		t.Errorf("Volume = %q, want %q (regression: \"V\" leaked into \"v\")", got.Volume, "12.5")
	}
	if got.OpenTime != 1700000000000 || got.High != "102.0" || got.Close != "101.0" {
		t.Errorf("unexpected candle: %+v", got)
	}
}

func TestHandleTradeSideAndFields(t *testing.T) {
	client, store := newTestClient(t)

	// m=true means the buyer was the maker, so the taker sold.
	raw := []byte(`{
		"stream": "btcusdt@trade",
		"data": {
			"e": "trade",
			"E": 1700000000456,
			"s": "BTCUSDT",
			"t": 12345,
			"p": "62518.01",
			"q": "0.001",
			"T": 1700000000400,
			"m": true
		}
	}`)

	if err := client.handle(raw); err != nil {
		t.Fatalf("handle: %v", err)
	}

	trades := store.Trades("BTCUSDT", 0)
	if len(trades) != 1 {
		t.Fatalf("trades = %d, want 1", len(trades))
	}

	got := trades[0]
	if got.Side != market.SideSell {
		t.Errorf("Side = %q, want %q", got.Side, market.SideSell)
	}
	if got.ID != 12345 || got.Price != "62518.01" || got.Qty != "0.001" {
		t.Errorf("unexpected trade: %+v", got)
	}
}

func TestHandleDepthSnapshot(t *testing.T) {
	client, store := newTestClient(t)

	raw := []byte(`{
		"stream": "btcusdt@depth20@100ms",
		"data": {
			"lastUpdateId": 77,
			"bids": [["100.0","1.5"]],
			"asks": [["101.0","2.5"]]
		}
	}`)

	if err := client.handle(raw); err != nil {
		t.Fatalf("handle: %v", err)
	}

	depth, ok := store.Depth("BTCUSDT")
	if !ok {
		t.Fatal("depth not stored")
	}
	if depth.LastUpdateID != 77 || len(depth.Bids) != 1 || depth.Bids[0][0] != "100.0" {
		t.Errorf("unexpected depth: %+v", depth)
	}
}

func TestHandleIgnoresUnrelatedStream(t *testing.T) {
	client, store := newTestClient(t)

	raw := []byte(`{"stream":"ethusdt@trade","data":{"e":"trade","E":1,"s":"ETHUSDT","t":1,"p":"1","q":"1","T":1}}`)
	if err := client.handle(raw); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(store.Trades("ETHUSDT", 0)) != 0 {
		t.Fatal("unrelated stream must be ignored")
	}
}

// newTestClient builds a client wired to a fresh store without dialing out.
func newTestClient(t *testing.T) (*Client, *market.Store) {
	t.Helper()

	store := market.NewStore(market.DefaultStoreConfig())
	client, err := NewClient(DefaultConfig(), store, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, store
}
