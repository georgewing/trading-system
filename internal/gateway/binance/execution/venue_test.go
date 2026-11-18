package binanceexec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"trading-system/internal/execution"
	"trading-system/internal/hft/engine"
)

const (
	testAPIKey    = "test-api-key"
	testAPISecret = "test-api-secret"
	testScale     = int64(100_000_000)
	testTimestamp = int64(1_700_000_000_123)
)

func newTestConfig(baseURL string) Config {
	return Config{
		BaseURL:       baseURL,
		WSURL:         "ws://127.0.0.1:1",
		APIKey:        testAPIKey,
		APISecret:     testAPISecret,
		AccountID:     7,
		RecvWindow:    5 * time.Second,
		PriceScale:    testScale,
		QuantityScale: testScale,
		ReconnectMin:  time.Millisecond,
		ReconnectMax:  5 * time.Millisecond,
	}
}

func newTestVenue(t *testing.T, handler http.Handler) *Venue {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	venue, err := New(newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// Signed requests would otherwise probe /api/v3/time first.
	venue.setClockOffset(0)
	return venue
}

func validOrder() engine.Order {
	return engine.Order{
		ID:       1001,
		Symbol:   "BTCUSDT",
		Side:     engine.SideBuy,
		Type:     engine.OrderTypeLimit,
		TIF:      engine.TimeInForceGTC,
		Price:    2_500_012_000_000, // 25000.12 at 1e-8
		Quantity: "1.5",
	}
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

// requireSignedQuery verifies the HMAC signature and returns the signed
// parameters without the signature itself.
func requireSignedQuery(t *testing.T, r *http.Request) url.Values {
	t.Helper()

	if got := r.Header.Get(apiKeyHeader); got != testAPIKey {
		t.Errorf("%s = %q, want %q", apiKeyHeader, got, testAPIKey)
	}

	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		t.Errorf("parse query: %v", err)
		return nil
	}
	signature := query.Get("signature")
	if signature == "" {
		t.Error("missing signature")
		return query
	}
	query.Del("signature")

	mac := hmac.New(sha256.New, []byte(testAPISecret))
	mac.Write([]byte(query.Encode()))
	if want := hex.EncodeToString(mac.Sum(nil)); signature != want {
		t.Errorf("signature = %q, want %q", signature, want)
	}
	if query.Get("timestamp") == "" {
		t.Error("missing timestamp")
	}
	return query
}

func TestSubmitNewLimitOrder(t *testing.T) {
	var got url.Values
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != orderPath {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, orderPath)
		}
		got = requireSignedQuery(t, r)
		writeJSON(w, `{
			"symbol":"BTCUSDT","orderId":42,"clientOrderId":"1001",
			"transactTime":1700000000123,"price":"25000.12000000",
			"origQty":"1.50000000","executedQty":"0.00000000",
			"cummulativeQuoteQty":"0.00000000","status":"NEW",
			"timeInForce":"GTC","type":"LIMIT","side":"BUY","fills":[]
		}`)
	}))

	result, err := venue.Submit(context.Background(), validOrder())
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	want := engine.SubmitResult{
		OpenLeaves:  150_000_000,
		FinalStatus: engine.OrderStatusNew,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("Submit() = %+v, want %+v", result, want)
	}

	if got == nil {
		t.Fatal("handler did not capture the request")
	}
	for key, want := range map[string]string{
		"symbol":           "BTCUSDT",
		"side":             "BUY",
		"type":             "LIMIT",
		"timeInForce":      "GTC",
		"quantity":         "1.5",
		"price":            "25000.12",
		"newClientOrderId": "1001",
		"newOrderRespType": "FULL",
		"recvWindow":       "5000",
	} {
		if value := got.Get(key); value != want {
			t.Errorf("%s = %q, want %q", key, value, want)
		}
	}
}

func TestSubmitFilledOrder(t *testing.T) {
	tests := []struct {
		name     string
		side     engine.Side
		wantBuy  uint64
		wantSell uint64
	}{
		{name: "buy", side: engine.SideBuy, wantBuy: 1001},
		{name: "sell", side: engine.SideSell, wantSell: 1001},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got url.Values
			venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = requireSignedQuery(t, r)
				writeJSON(w, `{
					"symbol":"BTCUSDT","orderId":42,"clientOrderId":"client-7",
					"transactTime":1700000000123,"price":"25000.12000000",
					"origQty":"1.50000000","executedQty":"1.50000000",
					"cummulativeQuoteQty":"37501.53000000","status":"FILLED",
					"timeInForce":"GTC","type":"LIMIT","side":"BUY","fills":[
						{"price":"25000.12000000","qty":"1.00000000","commission":"0.001","commissionAsset":"BNB","tradeId":7001},
						{"price":"25001.50000000","qty":"0.50000000","commission":"0.0005","commissionAsset":"BNB","tradeId":7002}
					]
				}`)
			}))

			order := validOrder()
			order.ClientOrderID = "client-7"
			order.Side = tt.side

			result, err := venue.Submit(context.Background(), order)
			if err != nil {
				t.Fatalf("Submit() error = %v", err)
			}

			if got.Get("newClientOrderId") != "client-7" {
				t.Fatalf("newClientOrderId = %q, want client-7", got.Get("newClientOrderId"))
			}
			if result.ExecutedQty != 150_000_000 {
				t.Fatalf("ExecutedQty = %d, want 150000000", result.ExecutedQty)
			}
			if result.OpenLeaves != 0 || result.CanceledQty != 0 {
				t.Fatalf("OpenLeaves/CanceledQty = %d/%d, want 0/0", result.OpenLeaves, result.CanceledQty)
			}
			if result.FinalStatus != engine.OrderStatusFilled {
				t.Fatalf("FinalStatus = %v, want Filled", result.FinalStatus)
			}

			if len(result.Trades) != 2 {
				t.Fatalf("Trades = %d, want 2", len(result.Trades))
			}
			wantTimestamp := testTimestamp * int64(time.Millisecond)
			wantTrades := []engine.Trade{
				{
					ID:          7001,
					Symbol:      "BTCUSDT",
					Price:       2_500_012_000_000,
					Quantity:    100_000_000,
					BuyOrderID:  tt.wantBuy,
					SellOrderID: tt.wantSell,
					Timestamp:   wantTimestamp,
				},
				{
					ID:          7002,
					Symbol:      "BTCUSDT",
					Price:       2_500_150_000_000,
					Quantity:    50_000_000,
					BuyOrderID:  tt.wantBuy,
					SellOrderID: tt.wantSell,
					Timestamp:   wantTimestamp,
				},
			}
			if !reflect.DeepEqual(result.Trades, wantTrades) {
				t.Fatalf("Trades = %+v, want %+v", result.Trades, wantTrades)
			}
		})
	}
}

func TestSubmitMarketOrderOmitsPriceAndTIF(t *testing.T) {
	var got url.Values
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = requireSignedQuery(t, r)
		writeJSON(w, `{
			"symbol":"BTCUSDT","orderId":43,"clientOrderId":"1001",
			"transactTime":1700000000123,"price":"0.00000000",
			"origQty":"0.50000000","executedQty":"0.50000000",
			"cummulativeQuoteQty":"12500.00000000","status":"FILLED",
			"timeInForce":"GTC","type":"MARKET","side":"BUY","fills":[
				{"price":"25000.00000000","qty":"0.50000000","tradeId":7003}
			]
		}`)
	}))

	order := validOrder()
	order.Type = engine.OrderTypeMarket
	order.TIF = engine.TimeInForceGTC
	order.Price = 0
	order.Quantity = "0.5"

	result, err := venue.Submit(context.Background(), order)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	if got.Get("type") != "MARKET" {
		t.Errorf("type = %q, want MARKET", got.Get("type"))
	}
	if _, ok := got["timeInForce"]; ok {
		t.Errorf("timeInForce = %q, want omitted", got.Get("timeInForce"))
	}
	if _, ok := got["price"]; ok {
		t.Errorf("price = %q, want omitted", got.Get("price"))
	}
	if result.FinalStatus != engine.OrderStatusFilled || len(result.Trades) != 1 {
		t.Fatalf("result = %+v, want one filled trade", result)
	}
}

func TestSubmitPostOnlyMapsToLimitMaker(t *testing.T) {
	var got url.Values
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = requireSignedQuery(t, r)
		writeJSON(w, `{
			"symbol":"BTCUSDT","orderId":44,"clientOrderId":"1001",
			"transactTime":1700000000123,"price":"25000.12000000",
			"origQty":"1.50000000","executedQty":"0.00000000",
			"cummulativeQuoteQty":"0.00000000","status":"NEW",
			"timeInForce":"GTC","type":"LIMIT_MAKER","side":"BUY","fills":[]
		}`)
	}))

	order := validOrder()
	order.PostOnly = true

	if _, err := venue.Submit(context.Background(), order); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	if got.Get("type") != "LIMIT_MAKER" {
		t.Fatalf("type = %q, want LIMIT_MAKER", got.Get("type"))
	}
	if _, ok := got["timeInForce"]; ok {
		t.Fatalf("timeInForce = %q, want omitted", got.Get("timeInForce"))
	}
}

func TestSubmitRejectsInvalidOrder(t *testing.T) {
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))

	tests := []struct {
		name   string
		mutate func(*engine.Order)
		want   error
	}{
		{name: "empty quantity", mutate: func(o *engine.Order) { o.Quantity = "" }, want: engine.ErrInvalidQuantity},
		{name: "zero quantity", mutate: func(o *engine.Order) { o.Quantity = "0" }, want: ErrInvalidOrder},
		{name: "excess precision", mutate: func(o *engine.Order) { o.Quantity = "1.000000001" }, want: engine.ErrQuantityPrecision},
		{name: "missing symbol", mutate: func(o *engine.Order) { o.Symbol = "" }, want: ErrInvalidOrder},
		{name: "unknown side", mutate: func(o *engine.Order) { o.Side = 0 }, want: ErrInvalidOrder},
		{name: "limit without price", mutate: func(o *engine.Order) { o.Price = 0 }, want: ErrInvalidOrder},
		{name: "unknown tif", mutate: func(o *engine.Order) { o.TIF = "DAY" }, want: ErrUnsupportedOrder},
		{name: "unknown type", mutate: func(o *engine.Order) { o.Type = "STOP" }, want: ErrUnsupportedOrder},
		{name: "market post only", mutate: func(o *engine.Order) {
			o.Type = engine.OrderTypeMarket
			o.Price = 0
			o.PostOnly = true
		}, want: ErrUnsupportedOrder},
		{name: "invalid client id", mutate: func(o *engine.Order) { o.ClientOrderID = "bad id" }, want: ErrInvalidClientOrderID},
		{name: "missing ids", mutate: func(o *engine.Order) { o.ID = 0 }, want: ErrInvalidOrder},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order := validOrder()
			tt.mutate(&order)

			result, err := venue.Submit(context.Background(), order)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Submit() error = %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(result, engine.SubmitResult{}) {
				t.Fatalf("Submit() result = %+v, want zero value", result)
			}
		})
	}
}

func TestSubmitAPIError(t *testing.T) {
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireSignedQuery(t, r)
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, `{"code":-2010,"msg":"Account has insufficient balance for requested action."}`)
	}))

	result, err := venue.Submit(context.Background(), validOrder())
	if err == nil {
		t.Fatal("Submit() error = nil, want API error")
	}
	if !reflect.DeepEqual(result, engine.SubmitResult{}) {
		t.Fatalf("Submit() result = %+v, want zero value", result)
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Submit() error = %v, want *APIError", err)
	}
	if apiErr.HTTPStatus != http.StatusBadRequest || apiErr.Code != -2010 {
		t.Fatalf("APIError = %+v, want http 400 code -2010", apiErr)
	}
	if !strings.Contains(apiErr.Msg, "insufficient balance") {
		t.Fatalf("APIError.Msg = %q, want the venue message", apiErr.Msg)
	}
	requireNoSecrets(t, err)
}

func TestSubmitTransportErrorHidesSignedQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	venue, err := New(newTestConfig(closedURL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	venue.setClockOffset(0)

	_, err = venue.Submit(context.Background(), validOrder())
	if err == nil {
		t.Fatal("Submit() error = nil, want transport error")
	}
	requireNoSecrets(t, err)
}

func requireNoSecrets(t *testing.T, err error) {
	t.Helper()
	for _, forbidden := range []string{testAPIKey, testAPISecret, "signature=", "X-MBX-APIKEY"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: %v", forbidden, err)
		}
	}
}

func TestSubmitContextCanceled(t *testing.T) {
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request with a canceled context")
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := venue.Submit(ctx, validOrder())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit() error = %v, want context.Canceled", err)
	}
}

func TestSignedRequestsUseServerClock(t *testing.T) {
	const skew = 5 * time.Minute

	var (
		timeCalls int
		timestamp int64
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case timePath:
			timeCalls++
			writeJSON(w, fmt.Sprintf(`{"serverTime":%d}`, time.Now().Add(skew).UnixMilli()))
		case orderPath:
			query := requireSignedQuery(t, r)
			value, err := strconv.ParseInt(query.Get("timestamp"), 10, 64)
			if err != nil {
				t.Errorf("parse timestamp %q: %v", query.Get("timestamp"), err)
			}
			timestamp = value
			writeJSON(w, `{
				"symbol":"BTCUSDT","orderId":42,"clientOrderId":"1001",
				"transactTime":1700000000123,"status":"NEW","fills":[]
			}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	venue, err := New(newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// The first request probes /api/v3/time; the second must reuse the offset.
	if _, err := venue.Submit(context.Background(), validOrder()); err != nil {
		t.Fatalf("first Submit() error = %v", err)
	}
	if _, err := venue.Submit(context.Background(), validOrder()); err != nil {
		t.Fatalf("second Submit() error = %v", err)
	}

	if timeCalls != 1 {
		t.Fatalf("time endpoint calls = %d, want 1", timeCalls)
	}
	drift := time.Duration(timestamp-time.Now().Add(skew).UnixMilli()) * time.Millisecond
	if drift < -5*time.Second || drift > 5*time.Second {
		t.Fatalf("signed timestamp is off by %s from the server clock", drift)
	}
}

func TestCancelByClientOrderID(t *testing.T) {
	var got url.Values
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != orderPath {
			t.Errorf("request = %s %s, want DELETE %s", r.Method, r.URL.Path, orderPath)
		}
		got = requireSignedQuery(t, r)
		writeJSON(w, `{"symbol":"BTCUSDT","orderId":42,"clientOrderId":"client-7","status":"CANCELED"}`)
	}))

	err := venue.Cancel(context.Background(), execution.CancelRequest{
		OrderID:       1001,
		ClientOrderID: "client-7",
		Symbol:        "btcusdt",
		Reason:        "user",
	})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	if got.Get("symbol") != "BTCUSDT" {
		t.Errorf("symbol = %q, want BTCUSDT", got.Get("symbol"))
	}
	if got.Get("origClientOrderId") != "client-7" {
		t.Errorf("origClientOrderId = %q, want client-7", got.Get("origClientOrderId"))
	}
	if _, ok := got["orderId"]; ok {
		t.Errorf("orderId = %q, want omitted when a client order id is set", got.Get("orderId"))
	}
}

func TestCancelByOrderID(t *testing.T) {
	var got url.Values
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = requireSignedQuery(t, r)
		writeJSON(w, `{"symbol":"BTCUSDT","orderId":42,"status":"CANCELED"}`)
	}))

	err := venue.Cancel(context.Background(), execution.CancelRequest{OrderID: 42, Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if got.Get("orderId") != "42" {
		t.Fatalf("orderId = %q, want 42", got.Get("orderId"))
	}
	if _, ok := got["origClientOrderId"]; ok {
		t.Fatalf("origClientOrderId = %q, want omitted", got.Get("origClientOrderId"))
	}
}

func TestCancelUnknownOrder(t *testing.T) {
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, `{"code":-2011,"msg":"Unknown order sent."}`)
	}))

	err := venue.Cancel(context.Background(), execution.CancelRequest{OrderID: 42, Symbol: "BTCUSDT"})
	if err == nil {
		t.Fatal("Cancel() error = nil, want unknown order error")
	}
	if !errors.Is(err, ErrUnknownOrder) {
		t.Fatalf("Cancel() error = %v, want ErrUnknownOrder", err)
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != -2011 {
		t.Fatalf("Cancel() error = %v, want *APIError with code -2011", err)
	}
	requireNoSecrets(t, err)
}

func TestCancelRejectsInvalidRequest(t *testing.T) {
	venue := newTestVenue(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))

	tests := []struct {
		name string
		req  execution.CancelRequest
	}{
		{name: "missing symbol", req: execution.CancelRequest{OrderID: 42}},
		{name: "missing identifiers", req: execution.CancelRequest{Symbol: "BTCUSDT"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := venue.Cancel(context.Background(), tt.req); !errors.Is(err, ErrInvalidCancel) {
				t.Fatalf("Cancel() error = %v, want ErrInvalidCancel", err)
			}
		})
	}
}
