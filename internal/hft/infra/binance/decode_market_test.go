package binance

import (
	"testing"

	"trading-system/internal/hft/domain"
)

func TestDecodeDepthSnapshotFromCombinedStream(t *testing.T) {
	raw := []byte(`{"stream":"btcusdt@depth20@100ms","data":{"lastUpdateId":103141898974,"bids":[["62659.78000000","5.98905000"]],"asks":[["62659.79000000","0.04160000"]]}}`)

	ev, ok, err := testDecoder().Decode(raw)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !ok {
		t.Fatal("Decode() ok = false, want true")
	}
	if ev.Kind != domain.KindDepth {
		t.Fatalf("Kind = %v, want %v", ev.Kind, domain.KindDepth)
	}
	// The frame carries no symbol; it must come from the stream name.
	if ev.Symbol != "BTCUSDT" {
		t.Fatalf("Symbol = %q, want BTCUSDT", ev.Symbol)
	}
	if ev.Depth == nil || ev.Depth.UpdateID != 103141898974 {
		t.Fatalf("Depth = %+v", ev.Depth)
	}
	if len(ev.Depth.Bids) != 1 || ev.Depth.Bids[0].Price != 6_265_978_000_000 {
		t.Fatalf("bids = %+v", ev.Depth.Bids)
	}
	if len(ev.Depth.Asks) != 1 || ev.Depth.Asks[0].Qty != 4_160_000 {
		t.Fatalf("asks = %+v", ev.Depth.Asks)
	}
	if ev.Topic() != "depth:BTCUSDT" {
		t.Fatalf("Topic() = %q", ev.Topic())
	}
}

// Binance mixes cases inside the kline object: "l" is the low price while "L"
// is the last trade id, and "v" is volume while "V" is taker-buy volume.
// encoding/json falls back to a case-insensitive match for undeclared keys,
// which silently decodes the wrong value. This test pins the correct fields.
func TestDecodeKlineKeepsLowAndVolume(t *testing.T) {
	raw := []byte(`{"stream":"btcusdt@kline_1m","data":{"e":"kline","E":1700000000123,"s":"BTCUSDT","k":{"t":1700000000000,"T":1700000059999,"s":"BTCUSDT","i":"1m","f":100,"L":999,"o":"100.0","c":"101.0","h":"102.0","l":"99.0","v":"12.5","n":42,"x":false,"q":"1250.0","V":"7.0","Q":"700.0"}}}`)

	ev, ok, err := testDecoder().Decode(raw)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !ok {
		t.Fatal("Decode() ok = false, want true")
	}
	if ev.Kind != domain.KindKline {
		t.Fatalf("Kind = %v, want %v", ev.Kind, domain.KindKline)
	}
	if ev.Kline == nil {
		t.Fatal("Kline = nil")
	}

	got := ev.Kline
	if got.Low != 9_900_000_000 {
		t.Errorf("Low = %d, want 9900000000 (regression: \"L\" leaked into \"l\")", got.Low)
	}
	if got.Volume != 1_250_000_000 {
		t.Errorf("Volume = %d, want 1250000000 (regression: \"V\" leaked into \"v\")", got.Volume)
	}
	if got.OpenTime != 1700000000000 || got.Interval != "1m" {
		t.Errorf("unexpected candle: %+v", got)
	}
	if got.Closed {
		t.Error("Closed = true, want false")
	}
	// Kline topics carry the interval so several candle streams can coexist.
	if ev.Topic() != "kline:BTCUSDT:1m" {
		t.Fatalf("Topic() = %q, want kline:BTCUSDT:1m", ev.Topic())
	}
}

func TestDecodeDepthSniffsPayloadWithoutStreamName(t *testing.T) {
	dec := testDecoder()
	dec.Symbol = "ETHUSDT"

	raw := []byte(`{"lastUpdateId":7,"bids":[["1.00000000","2.00000000"]],"asks":[]}`)

	ev, ok, err := dec.Decode(raw)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !ok {
		t.Fatal("Decode() ok = false, want true")
	}
	// Without a stream name the configured symbol is the fallback.
	if ev.Symbol != "ETHUSDT" {
		t.Fatalf("Symbol = %q, want ETHUSDT", ev.Symbol)
	}
}
