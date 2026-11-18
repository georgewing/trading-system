package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"trading-system/internal/engine"
	"trading-system/internal/pipeline"
	"trading-system/internal/risk"
	"trading-system/pkg/idgen"
)

// capturingGuard records the risk requests produced by the pipeline. Because
// the pipeline builds them from engine.Order, the recorded values prove the
// HTTP boundary mapped account_id and symbol onto the order.
type capturingGuard struct {
	mu    sync.Mutex
	calls []risk.Request
}

func (g *capturingGuard) Check(_ context.Context, req risk.Request) risk.Decision {
	g.mu.Lock()
	g.calls = append(g.calls, req)
	g.mu.Unlock()
	return risk.Decision{Approved: true, Code: risk.CodeOK}
}

func (g *capturingGuard) Calls() []risk.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]risk.Request(nil), g.calls...)
}

func waitGuardCalls(t *testing.T, g *capturingGuard, n int) []risk.Request {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		calls := g.Calls()
		if len(calls) >= n {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("risk guard calls = %d, want %d", len(calls), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNewOrderFromRequest(t *testing.T) {
	order, err := newOrderFromRequest(MatchRequest{
		OrderID:     "client-9",
		AccountID:   42,
		Symbol:      "BTCUSDT",
		Side:        "SELL",
		Price:       123,
		Quantity:    "1.5",
		Type:        "LIMIT",
		TimeInForce: "IOC",
	}, "BTCUSDT", 99)
	if err != nil {
		t.Fatalf("newOrderFromRequest() error = %v", err)
	}

	if order.ID != 99 || order.ClientOrderID != "client-9" {
		t.Fatalf("order id/client id = %d/%q, want 99/client-9", order.ID, order.ClientOrderID)
	}
	if order.AccountID != 42 || order.Symbol != "BTCUSDT" {
		t.Fatalf("order account/symbol = %d/%q, want 42/BTCUSDT", order.AccountID, order.Symbol)
	}
	if order.Side != engine.SideSell {
		t.Fatalf("order side = %v, want sell", order.Side)
	}
	if order.Price != 123 || order.Quantity != "1.5" {
		t.Fatalf("order price/qty = %d/%q, want 123/1.5", order.Price, order.Quantity)
	}
	if order.Type != engine.OrderTypeLimit || order.TIF != engine.TimeInForceIOC {
		t.Fatalf("order type/tif = %v/%v, want LIMIT/IOC", order.Type, order.TIF)
	}

	fallback, err := newOrderFromRequest(MatchRequest{Side: "BUY"}, "BTCUSDT", 100)
	if err != nil {
		t.Fatalf("fallback newOrderFromRequest() error = %v", err)
	}
	if fallback.Symbol != "BTCUSDT" {
		t.Fatalf("fallback symbol = %q, want BTCUSDT", fallback.Symbol)
	}
	if fallback.Side != engine.SideBuy {
		t.Fatalf("fallback side = %v, want buy", fallback.Side)
	}

	if _, err := newOrderFromRequest(MatchRequest{Symbol: "ETHUSDT", Side: "BUY"}, "BTCUSDT", 101); err == nil {
		t.Fatal("mismatched symbol error = nil, want rejection")
	}
}

func TestMatchEndpointPassesAccountAndSymbol(t *testing.T) {
	if err := idgen.Init(1); err != nil {
		t.Fatalf("idgen.Init() error = %v", err)
	}

	ob := engine.NewOrderBook("BTCUSDT", 100, 100)
	bus := pipeline.NewEventBus(engine.NewMatcher(ob), 16)

	guard := &capturingGuard{}
	bus.SetRiskGuard(guard, 1)

	ctx, cancel := context.WithCancel(context.Background())
	bus.Start(ctx)
	t.Cleanup(func() {
		bus.StopAccepting()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
		defer drainCancel()
		_ = bus.Drain(drainCtx)
		cancel()
	})

	handler := MakeMatchEndpoint(bus, "BTCUSDT")

	post := func(body string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/order", strings.NewReader(body))
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// New client: explicit account and symbol.
	first := `{"order_id":"client-1","account_id":42,"symbol":"BTCUSDT",
		"side":"BUY","price":100,"quantity":"1","type":"LIMIT","time_in_force":"GTC"}`
	if code := post(first); code != http.StatusAccepted {
		t.Fatalf("first order status = %d, want %d", code, http.StatusAccepted)
	}

	// Legacy client: no account_id and no symbol.
	second := `{"order_id":"client-2","side":"SELL","price":101,"quantity":"1",
		"type":"LIMIT","time_in_force":"GTC"}`
	if code := post(second); code != http.StatusAccepted {
		t.Fatalf("legacy order status = %d, want %d (missing account_id must not be rejected)", code, http.StatusAccepted)
	}

	// Unsupported symbol must be rejected before it reaches the order book.
	third := `{"order_id":"client-3","account_id":42,"symbol":"ETHUSDT",
		"side":"BUY","price":100,"quantity":"1","type":"LIMIT","time_in_force":"GTC"}`
	if code := post(third); code != http.StatusBadRequest {
		t.Fatalf("unsupported symbol status = %d, want %d", code, http.StatusBadRequest)
	}

	calls := waitGuardCalls(t, guard, 2)

	if got := calls[0]; got.AccountID != 42 || got.Symbol != "BTCUSDT" || got.ClientOrderID != "client-1" {
		t.Fatalf("first risk request = %d/%q/%q, want 42/BTCUSDT/client-1",
			got.AccountID, got.Symbol, got.ClientOrderID)
	}
	if got := calls[1]; got.AccountID != 0 || got.Symbol != "BTCUSDT" || got.ClientOrderID != "client-2" {
		t.Fatalf("legacy risk request = %d/%q/%q, want 0/BTCUSDT/client-2",
			got.AccountID, got.Symbol, got.ClientOrderID)
	}
}
