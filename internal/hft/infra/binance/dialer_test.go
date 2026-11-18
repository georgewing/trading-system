package binance

import (
	"strings"
	"testing"
)

func TestStreamsExpandsSymbols(t *testing.T) {
	cfg := StreamConfig{
		DepthLevels:   20,
		DepthSpeed:    "100ms",
		KlineInterval: "1m",
	}

	got, err := cfg.streams([]string{"BTCUSDT"})
	if err != nil {
		t.Fatalf("streams() error = %v", err)
	}

	want := []string{
		"btcusdt@depth20@100ms",
		"btcusdt@trade",
		"btcusdt@kline_1m",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("streams() = %v, want %v", got, want)
	}
}

func TestStreamsOmitsKlineWhenDisabled(t *testing.T) {
	cfg := StreamConfig{DepthLevels: 10, DepthSpeed: "1000ms"}

	got, err := cfg.streams([]string{"ETHUSDT"})
	if err != nil {
		t.Fatalf("streams() error = %v", err)
	}
	if len(got) != 2 || got[0] != "ethusdt@depth10@1000ms" {
		t.Fatalf("streams() = %v", got)
	}
}

func TestStreamsRejectsEmptySymbol(t *testing.T) {
	cfg := StreamConfig{DepthLevels: 20, DepthSpeed: "100ms"}
	if _, err := cfg.streams(nil); err == nil {
		t.Fatal("empty symbol list: error = nil, want error")
	}
	if _, err := cfg.streams([]string{" "}); err == nil {
		t.Fatal("blank symbol: error = nil, want error")
	}
}

func TestURLJoinsStreams(t *testing.T) {
	cfg := StreamConfig{
		BaseURL:       "wss://example.test/",
		DepthLevels:   20,
		DepthSpeed:    "100ms",
		KlineInterval: "", // disabled by streams(), see withDefaults
	}

	url, err := cfg.URL([]string{"BTCUSDT"})
	if err != nil {
		t.Fatalf("URL() error = %v", err)
	}

	want := "wss://example.test/stream?streams=btcusdt@depth20@100ms/btcusdt@trade"
	if url != want {
		t.Fatalf("URL() = %q, want %q", url, want)
	}
}

func TestNewWebsocketDialerValidates(t *testing.T) {
	if _, err := NewWebsocketDialer(StreamConfig{DepthLevels: 7}); err == nil {
		t.Fatal("depth levels 7: error = nil, want error")
	}
	if _, err := NewWebsocketDialer(StreamConfig{DepthLevels: 20, DepthSpeed: "250ms"}); err == nil {
		t.Fatal("depth speed 250ms: error = nil, want error")
	}
	if _, err := NewWebsocketDialer(StreamConfig{DepthLevels: 20, DepthSpeed: "100ms"}); err != nil {
		t.Fatalf("valid config: error = %v", err)
	}
}
