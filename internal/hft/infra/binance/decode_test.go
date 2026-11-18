package binance

import (
	"testing"
	"time"

	"trading-system/internal/hft/domain"
)

const testScale = int64(100_000_000)

func fixedNow() time.Time { return time.UnixMilli(1_700_000_000_123) }

func testDecoder() Decoder {
	return Decoder{PriceScale: testScale, QtyScale: testScale, Now: fixedNow}
}

func TestDecodeTradeFromCombinedStream(t *testing.T) {
	raw := []byte(`{"stream":"btcusdt@trade","data":{"e":"trade","E":1700000000000,"s":"BTCUSDT","t":123456,"p":"25000.12000000","q":"0.50000000","T":1700000000456,"m":true}}`)

	ev, ok, err := testDecoder().Decode(raw)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !ok {
		t.Fatal("Decode() ok = false, want true")
	}
	if ev.Kind != domain.KindTrade {
		t.Fatalf("Kind = %v, want %v", ev.Kind, domain.KindTrade)
	}
	if ev.Exchange != Exchange || ev.Symbol != "BTCUSDT" {
		t.Fatalf("identity = %q/%q, want %q/BTCUSDT", ev.Exchange, ev.Symbol, Exchange)
	}
	if ev.Seq != 123456 {
		t.Fatalf("Seq = %d, want 123456", ev.Seq)
	}
	if ev.TS != 1_700_000_000_456 {
		t.Fatalf("TS = %d, want exchange trade time", ev.TS)
	}
	if ev.RecvTS != 1_700_000_000_123 {
		t.Fatalf("RecvTS = %d, want receive time", ev.RecvTS)
	}
	if ev.Trade == nil {
		t.Fatal("Trade = nil, want trade payload")
	}
	if ev.Tick != nil {
		t.Fatal("Tick != nil for a trade event")
	}
	if ev.Trade.Price != 2_500_012_000_000 {
		t.Fatalf("price = %d, want 2500012000000", ev.Trade.Price)
	}
	if ev.Trade.Qty != 50_000_000 {
		t.Fatalf("qty = %d, want 50000000", ev.Trade.Qty)
	}
	if ev.Trade.ID != 123456 {
		t.Fatalf("trade id = %d, want 123456", ev.Trade.ID)
	}
	if !ev.Trade.BuyerIsMaker {
		t.Fatal("BuyerIsMaker = false, want true")
	}
}

func TestDecodeBookTickerFromCombinedStream(t *testing.T) {
	raw := []byte(`{"stream":"btcusdt@bookTicker","data":{"u":400900217,"s":"BTCUSDT","b":"25.35190000","B":"31.21000000","a":"25.36520000","A":"40.66000000"}}`)

	ev, ok, err := testDecoder().Decode(raw)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !ok {
		t.Fatal("Decode() ok = false, want true")
	}
	if ev.Kind != domain.KindTick {
		t.Fatalf("Kind = %v, want tick", ev.Kind)
	}
	if ev.Trade != nil {
		t.Fatal("Trade != nil for a tick event")
	}
	if ev.Tick == nil {
		t.Fatal("Tick = nil, want BBO payload")
	}
	if ev.Seq != 400900217 || ev.Tick.UpdateID != 400900217 {
		t.Fatalf("seq = %d/%d, want 400900217", ev.Seq, ev.Tick.UpdateID)
	}
	if ev.Tick.BidPrice != 2_535_190_000 {
		t.Fatalf("bid price = %d, want 2535190000", ev.Tick.BidPrice)
	}
	if ev.Tick.BidQty != 3_121_000_000 {
		t.Fatalf("bid qty = %d, want 3121000000", ev.Tick.BidQty)
	}
	if ev.Tick.AskPrice != 2_536_520_000 {
		t.Fatalf("ask price = %d, want 2536520000", ev.Tick.AskPrice)
	}
	if ev.Tick.AskQty != 4_066_000_000 {
		t.Fatalf("ask qty = %d, want 4066000000", ev.Tick.AskQty)
	}
	// BBO has no exchange timestamp, so TS mirrors RecvTS.
	if ev.TS != ev.RecvTS || ev.TS != 1_700_000_000_123 {
		t.Fatalf("TS/RecvTS = %d/%d, want receive time twice", ev.TS, ev.RecvTS)
	}
}

func TestDecodeSingleStreamFrameWithoutEnvelope(t *testing.T) {
	raw := []byte(`{"e":"trade","s":"ETHUSDT","t":7,"p":"1.0","q":"2.0","T":1700000000001,"m":false}`)

	ev, ok, err := testDecoder().Decode(raw)
	if err != nil || !ok {
		t.Fatalf("Decode() ok=%v err=%v", ok, err)
	}
	if ev.Symbol != "ETHUSDT" || ev.Kind != domain.KindTrade {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Trade.Price != 100_000_000 || ev.Trade.Qty != 200_000_000 {
		t.Fatalf("price/qty = %d/%d", ev.Trade.Price, ev.Trade.Qty)
	}
	if ev.Trade.BuyerIsMaker {
		t.Fatal("BuyerIsMaker = true, want false")
	}
}

func TestDecodeIgnoresFramesWithoutMarketData(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"subscription ack", `{"result":null,"id":1}`},
		{"pong", `{"pong":1700000000000}`},
		{"depth update", `{"e":"depthUpdate","E":1700000000000,"s":"BTCUSDT","u":1,"b":[["1.0","1.0"]],"a":[["2.0","1.0"]]}`},
	}

	for _, tc := range cases {
		ev, ok, err := testDecoder().Decode([]byte(tc.raw))
		if err != nil {
			t.Fatalf("%s: Decode() error = %v", tc.name, err)
		}
		if ok {
			t.Fatalf("%s: Decode() ok = true, want false (%+v)", tc.name, ev)
		}
	}
}

func TestDecodeRejectsMalformedFrames(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"not json", `nope`},
		{"truncated envelope", `{"stream":"x@trade","data":`},
		{"trade missing symbol", `{"e":"trade","t":1,"p":"1.0","q":"1.0","T":1700000000001}`},
		{"excess precision", `{"e":"trade","s":"BTCUSDT","t":1,"p":"1.0","q":"0.123456789","T":1700000000001}`},
		{"empty bid price", `{"u":1,"s":"BTCUSDT","b":"","B":"1.0","a":"1.0","A":"1.0"}`},
		{"negative price", `{"e":"trade","s":"BTCUSDT","t":1,"p":"-1.0","q":"1.0","T":1700000000001}`},
	}

	for _, tc := range cases {
		if _, _, err := testDecoder().Decode([]byte(tc.raw)); err == nil {
			t.Fatalf("%s: Decode() error = nil, want error", tc.name)
		}
	}
}
