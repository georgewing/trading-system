package app

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListenServersFailsWhenAPIAddrInUse(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve api port: %v", err)
	}
	defer occupied.Close()

	cfg := &Config{
		Server: ServerConfig{
			APIAddr:     occupied.Addr().String(),
			MetricsAddr: "127.0.0.1:0",
		},
	}

	apiLn, metricsLn, err := listenServers(cfg)
	if err == nil {
		if apiLn != nil {
			_ = apiLn.Close()
		}
		if metricsLn != nil {
			_ = metricsLn.Close()
		}
		t.Fatal("listenServers() error = nil, want bind error")
	}
	if apiLn != nil || metricsLn != nil {
		t.Fatalf("listeners = (%v, %v), want nil on error", apiLn, metricsLn)
	}
}

func TestListenServersFailsWhenMetricsAddrInUse(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve metrics port: %v", err)
	}
	defer occupied.Close()

	cfg := &Config{
		Server: ServerConfig{
			APIAddr:     "127.0.0.1:0",
			MetricsAddr: occupied.Addr().String(),
		},
	}

	apiLn, metricsLn, err := listenServers(cfg)
	if err == nil {
		if apiLn != nil {
			_ = apiLn.Close()
		}
		if metricsLn != nil {
			_ = metricsLn.Close()
		}
		t.Fatal("listenServers() error = nil, want bind error")
	}
	if apiLn != nil || metricsLn != nil {
		t.Fatalf("listeners = (%v, %v), want nil on error", apiLn, metricsLn)
	}
}

func TestLoadRiskConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
matcher:
  symbol: "BTCUSDT"
  price_precision: 8
  quantity_precision: 2
risk:
  enabled: true
  symbol_whitelist: ["BTCUSDT", "ETHUSDT"]
  max_order_qty: 10
  max_notional: 100000
  price_band_bps: 50
  max_orders_per_window: 5
  window_seconds: 2
  max_position: 20
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.Risk.Enabled {
		t.Fatal("risk.enabled = false, want true")
	}
	if len(cfg.Risk.SymbolWhitelist) != 2 || cfg.Risk.SymbolWhitelist[0] != "BTCUSDT" {
		t.Fatalf("risk.symbol_whitelist = %v, want [BTCUSDT ETHUSDT]", cfg.Risk.SymbolWhitelist)
	}
	if cfg.Risk.MaxOrderQty != 10 || cfg.Risk.MaxNotional != 100000 {
		t.Fatalf("risk qty/notional limits = %d/%d, want 10/100000", cfg.Risk.MaxOrderQty, cfg.Risk.MaxNotional)
	}
	if cfg.Risk.PriceBandBps != 50 || cfg.Risk.MaxPosition != 20 {
		t.Fatalf("risk band/position limits = %d/%d, want 50/20", cfg.Risk.PriceBandBps, cfg.Risk.MaxPosition)
	}
	if cfg.Risk.MaxOrdersPerWindow != 5 || cfg.Risk.WindowSeconds != 2 {
		t.Fatalf("risk rate limit = %d/%ds, want 5/2s", cfg.Risk.MaxOrdersPerWindow, cfg.Risk.WindowSeconds)
	}
}

func TestLoadRejectsUnknownRiskField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
risk:
  enabled: false
  max_order_quantity: 10
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want strict YAML error for unknown risk field")
	}
}

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "configs", "config.example.yaml"))
	if err != nil {
		t.Fatalf("Load(config.example.yaml) error = %v", err)
	}

	if cfg.Risk.Enabled {
		t.Fatal("example risk.enabled = true, want false by default")
	}
	if len(cfg.Risk.SymbolWhitelist) != 0 || cfg.Risk.MaxOrderQty != 0 || cfg.Risk.MaxNotional != 0 ||
		cfg.Risk.PriceBandBps != 0 || cfg.Risk.MaxOrdersPerWindow != 0 ||
		cfg.Risk.WindowSeconds != 0 || cfg.Risk.MaxPosition != 0 {
		t.Fatalf("example risk defaults = %+v, want zero values", cfg.Risk)
	}
}

func TestRunFailsWhenAPIAddrInUse(t *testing.T) {
	t.Setenv("SNOWFLAKE_WORKER_ID", "1")

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve api port: %v", err)
	}
	defer occupied.Close()

	cfg := &Config{
		Server: ServerConfig{
			APIAddr:     occupied.Addr().String(),
			MetricsAddr: "127.0.0.1:0",
		},
		Matcher: MatcherConfig{
			Symbol:            "BTCUSDT",
			PricePrecision:    8,
			QuantityPrecision: 8,
		},
		Pipeline: PipelineConfig{
			InboundCapacity:  16,
			OutboundCapacity: 16,
		},
		WebSocket: WebSocketConfig{BufferSize: 16},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := Run(ctx, cfg); err == nil {
		t.Fatal("Run() error = nil, want bind error")
	}
}

func TestServeServersReturnsErrorWhenAPIListenerClosed(t *testing.T) {
	apiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen api: %v", err)
	}
	metricsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = apiLn.Close()
		t.Fatalf("listen metrics: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- serveServers(ctx, &http.Server{}, &http.Server{}, apiLn, metricsLn)
	}()

	// Simulate a runtime listener failure.
	_ = apiLn.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("serveServers() error = nil, want serve error")
		}
	case <-time.After(time.Second):
		t.Fatal("serveServers() did not return after api listener close")
	}
}

func TestServeServersReturnsNilOnContextCancel(t *testing.T) {
	apiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen api: %v", err)
	}
	metricsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = apiLn.Close()
		t.Fatalf("listen metrics: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveServers(ctx, &http.Server{}, &http.Server{}, apiLn, metricsLn)
	}()

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serveServers() error = %v, want nil on shutdown", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveServers() did not return after context cancel")
	}
}
