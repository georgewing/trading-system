// These tests exercise the REST surface and the user stream against local
// httptest servers, including the websocket handshake, malformed frames and a
// listenKeyExpired reconnect. The live Binance testnet path (TLS, real listen
// keys, the 30 minute keepalive interval) is not exercised here; keepalive
// requests are covered by calling keepAliveListenKey directly.
package binanceexec

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"trading-system/internal/hft/engine"
)

const streamExecutionReport = `{
	"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":"1001",
	"S":"BUY","o":"LIMIT","f":"GTC","q":"2.00000000","p":"25000.00000000",
	"x":"TRADE","X":"FILLED","r":"NONE","i":42,
	"l":"2.00000000","z":"2.00000000","L":"25000.00000000",
	"T":1700000000150,"t":9001,"m":false
}`

// newStreamVenue starts a local server and points both REST and websocket
// endpoints at it. No live Binance host is contacted.
func newStreamVenue(t *testing.T, handler http.Handler) *Venue {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := newTestConfig(srv.URL)
	cfg.WSURL = "ws" + strings.TrimPrefix(srv.URL, "http")

	venue, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	venue.setClockOffset(0)
	return venue
}

func requireListenKeyHeaders(t *testing.T, r *http.Request) {
	t.Helper()

	if got := r.Header.Get(apiKeyHeader); got != testAPIKey {
		t.Errorf("%s = %q, want %q", apiKeyHeader, got, testAPIKey)
	}
	if signature := r.URL.Query().Get("signature"); signature != "" {
		t.Errorf("listen key request is signed, want an unsigned request")
	}
}

func TestUserStreamURL(t *testing.T) {
	tests := []struct {
		name  string
		wsURL string
		key   string
		want  string
	}{
		{
			name:  "default root",
			wsURL: "wss://stream.testnet.binance.vision",
			key:   "lk-1",
			want:  "wss://stream.testnet.binance.vision/ws/lk-1",
		},
		{
			name:  "trailing slash",
			wsURL: "wss://stream.testnet.binance.vision/",
			key:   "lk-1",
			want:  "wss://stream.testnet.binance.vision/ws/lk-1",
		},
		{
			name:  "existing path and query",
			wsURL: "wss://stream.example/base/?foo=bar",
			key:   "lk-1",
			want:  "wss://stream.example/base/ws/lk-1?foo=bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.WSURL = tt.wsURL

			venue, err := New(cfg)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			got, err := venue.userStreamURL(tt.key)
			if err != nil {
				t.Fatalf("userStreamURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("userStreamURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListenKeyLifecycle(t *testing.T) {
	keepalived := make(chan string, 1)
	closed := make(chan string, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(userDataStreamPath, func(w http.ResponseWriter, r *http.Request) {
		requireListenKeyHeaders(t, r)

		switch r.Method {
		case http.MethodPost:
			writeJSON(w, `{"listenKey":"lk-1"}`)
		case http.MethodPut:
			keepalived <- r.URL.Query().Get("listenKey")
			_, _ = w.Write([]byte(`{}`))
		case http.MethodDelete:
			closed <- r.URL.Query().Get("listenKey")
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})

	venue := newStreamVenue(t, mux)
	ctx := context.Background()

	key, err := venue.createListenKey(ctx)
	if err != nil {
		t.Fatalf("createListenKey() error = %v", err)
	}
	if key != "lk-1" {
		t.Fatalf("listen key = %q, want lk-1", key)
	}

	if err := venue.keepAliveListenKey(ctx, key); err != nil {
		t.Fatalf("keepAliveListenKey() error = %v", err)
	}
	select {
	case got := <-keepalived:
		if got != key {
			t.Fatalf("keepalive key = %q, want %q", got, key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("keepalive PUT was not observed")
	}

	venue.closeListenKey(key)
	select {
	case got := <-closed:
		if got != key {
			t.Fatalf("closed key = %q, want %q", got, key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close DELETE was not observed")
	}
}

func TestRunUserStreamDeliversReports(t *testing.T) {
	var keyCreates atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc(userDataStreamPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			keyCreates.Add(1)
			writeJSON(w, `{"listenKey":"lk-1"}`)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/lk-1" {
			t.Errorf("websocket path = %q, want /ws/lk-1", r.URL.Path)
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		writeStreamFrame(t, conn, `not json`)
		writeStreamFrame(t, conn, `{"e":"outboundAccountPosition","E":1,"B":[]}`)
		writeStreamFrame(t, conn, streamExecutionReport)

		// Hold the session open until the client closes it.
		_, _, _ = conn.Read(context.Background())
	})

	venue := newStreamVenue(t, mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan engine.ExecReport, 8)
	done := make(chan error, 1)
	go func() { done <- venue.RunUserStream(ctx, out) }()

	var report engine.ExecReport
	select {
	case report = <-out:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for an execution report")
	}

	if report.OrderID != 1001 || report.ClientOrderID != "1001" {
		t.Fatalf("order ids = %d/%q, want 1001/1001", report.OrderID, report.ClientOrderID)
	}
	if report.AccountID != 7 {
		t.Fatalf("AccountID = %d, want 7 from config", report.AccountID)
	}
	if report.ExecType != engine.ExecTypeTrade || report.Status != engine.OrderStatusFilled {
		t.Fatalf("exec/status = %s/%v", report.ExecType, report.Status)
	}
	if report.LastPx != 2_500_000_000_000 || report.CumQty != 200_000_000 || report.LeavesQty != 0 {
		t.Fatalf("fill fields = %d/%d/%d", report.LastPx, report.CumQty, report.LeavesQty)
	}
	if report.Liquidity != engine.LiquidityTaker {
		t.Fatalf("Liquidity = %q, want TAKER", report.Liquidity)
	}
	if report.Trade == nil || report.Trade.ID != 9001 {
		t.Fatalf("Trade = %+v, want trade id 9001", report.Trade)
	}
	if report.Venue != VenueName {
		t.Fatalf("Venue = %q, want %q", report.Venue, VenueName)
	}

	if got := keyCreates.Load(); got != 1 {
		t.Fatalf("listen key creates = %d, want 1", got)
	}

	cancel()
	waitForStreamStop(t, done)
}

func TestRunUserStreamReconnectsAfterListenKeyExpired(t *testing.T) {
	var (
		keyCreates  atomic.Int32
		connections atomic.Int32
	)

	mux := http.NewServeMux()
	mux.HandleFunc(userDataStreamPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			keyCreates.Add(1)
			writeJSON(w, `{"listenKey":"lk-1"}`)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		if connections.Add(1) == 1 {
			writeStreamFrame(t, conn, `{"e":"listenKeyExpired","E":1700000000000}`)
			return
		}

		writeStreamFrame(t, conn, streamExecutionReport)
		_, _, _ = conn.Read(context.Background())
	})

	venue := newStreamVenue(t, mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan engine.ExecReport, 8)
	done := make(chan error, 1)
	go func() { done <- venue.RunUserStream(ctx, out) }()

	select {
	case report := <-out:
		if report.OrderID != 1001 {
			t.Fatalf("OrderID = %d, want 1001", report.OrderID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a report after reconnect")
	}

	if got := connections.Load(); got < 2 {
		t.Fatalf("websocket connections = %d, want at least 2", got)
	}
	if got := keyCreates.Load(); got < 2 {
		t.Fatalf("listen key creates = %d, want at least 2", got)
	}
	if got := venue.Reconnects(); got == 0 {
		t.Fatal("Reconnects() = 0, want a recorded reconnect")
	}

	cancel()
	waitForStreamStop(t, done)
}

func TestRunUserStreamStopsOnCanceledContext(t *testing.T) {
	venue := newStreamVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := venue.RunUserStream(ctx, make(chan engine.ExecReport)); err != nil {
		t.Fatalf("RunUserStream() error = %v, want nil", err)
	}
}

func TestRunUserStreamRejectsNilArguments(t *testing.T) {
	venue := newStreamVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))

	if err := venue.RunUserStream(context.Background(), nil); !errors.Is(err, ErrNilReportChannel) {
		t.Fatalf("RunUserStream(nil channel) error = %v, want ErrNilReportChannel", err)
	}
	var nilCtx context.Context
	if err := venue.RunUserStream(nilCtx, make(chan engine.ExecReport)); err == nil {
		t.Fatal("RunUserStream(nil ctx) error = nil, want an error")
	}
}

func TestBackoffBounds(t *testing.T) {
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	venue.cfg.ReconnectMin = 10 * time.Millisecond
	venue.cfg.ReconnectMax = 40 * time.Millisecond

	for _, attempt := range []int{1, 2, 3, 20} {
		delay := venue.backoff(attempt)
		if delay < 8*time.Millisecond || delay > 48*time.Millisecond {
			t.Fatalf("backoff(%d) = %s, want within [8ms, 48ms]", attempt, delay)
		}
	}
}

func writeStreamFrame(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(payload)); err != nil {
		t.Errorf("write frame: %v", err)
	}
}

func waitForStreamStop(t *testing.T, done <-chan error) {
	t.Helper()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunUserStream() error = %v, want nil on shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunUserStream did not stop after cancellation")
	}
}
