package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"trading-system/internal/hft/domain"
)

type fakeKlineHistory struct {
	klines []domain.Kline
	err    error

	calls  int
	symbol string
	from   int64
	to     int64
	limit  int
}

func (f *fakeKlineHistory) QueryKlines(_ context.Context, symbol string, from, to int64, limit int) ([]domain.Kline, error) {
	f.calls++
	f.symbol = symbol
	f.from = from
	f.to = to
	f.limit = limit
	return f.klines, f.err
}

type staticSnapshotter struct {
	klines []domain.Kline
}

func (s staticSnapshotter) Depth(string) (domain.Depth, bool)         { return domain.Depth{}, false }
func (s staticSnapshotter) Trades(string, int) []domain.Trade         { return nil }
func (s staticSnapshotter) Klines(string, string, int) []domain.Kline { return s.klines }

func serveHistory(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestKlineHistorySuccess(t *testing.T) {
	fake := &fakeKlineHistory{klines: []domain.Kline{{
		Interval: "1m",
		OpenTime: 1700000000000,
		Open:     6251801000000,
		High:     6252801000000,
		Low:      6250801000000,
		Close:    6252001000000,
		Volume:   123450000,
		Closed:   true,
	}}}

	h := MakeKlineHistoryEndpoint(fake, "BTCUSDT", 8, 8)
	rec := serveHistory(t, h, "/api/v1/market/kline/history?symbol=BTCUSDT&from=1700000000000&to=1700000300000&limit=2")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Symbol   string          `json:"symbol"`
		Interval string          `json:"interval"`
		Klines   []klineResponse `json:"klines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Symbol != "BTCUSDT" || resp.Interval != "1m" {
		t.Errorf("envelope = (%q, %q), want BTCUSDT/1m", resp.Symbol, resp.Interval)
	}
	if len(resp.Klines) != 1 {
		t.Fatalf("len(klines) = %d, want 1", len(resp.Klines))
	}

	k := resp.Klines[0]
	want := klineResponse{
		Symbol:   "BTCUSDT",
		Interval: "1m",
		OpenTime: 1700000000000,
		Open:     "62518.01000000",
		High:     "62528.01000000",
		Low:      "62508.01000000",
		Close:    "62520.01000000",
		Volume:   "1.23450000",
		Closed:   true,
		Ts:       1700000000000,
	}
	if k != want {
		t.Errorf("kline = %+v, want %+v", k, want)
	}

	if fake.calls != 1 || fake.symbol != "BTCUSDT" || fake.from != 1700000000000 ||
		fake.to != 1700000300000 || fake.limit != 2 {
		t.Errorf("reader call = (%d, %q, %d, %d, %d), want one call with parsed args",
			fake.calls, fake.symbol, fake.from, fake.to, fake.limit)
	}
}

// TestKlineHistoryMatchesLiveShape pins the requirement that history returns
// the same JSON as the live endpoint for the same candle.
func TestKlineHistoryMatchesLiveShape(t *testing.T) {
	klines := []domain.Kline{{
		Interval: "1m",
		OpenTime: 1700000000000,
		Open:     6251801000000,
		High:     6252801000000,
		Low:      6250801000000,
		Close:    6252001000000,
		Volume:   123450000,
		Closed:   true,
	}}

	live := serveHistory(t, MakeKlineEndpoint(staticSnapshotter{klines: klines}, "BTCUSDT", 8, 8),
		"/api/v1/market/kline")
	history := serveHistory(t, MakeKlineHistoryEndpoint(&fakeKlineHistory{klines: klines}, "BTCUSDT", 8, 8),
		"/api/v1/market/kline/history")

	if live.Code != http.StatusOK || history.Code != http.StatusOK {
		t.Fatalf("status = (%d, %d), want both 200", live.Code, history.Code)
	}
	if got, want := history.Body.String(), live.Body.String(); got != want {
		t.Errorf("history body = %s\nwant live body = %s", got, want)
	}
}

func TestKlineHistoryDefaultsSymbolAndLimit(t *testing.T) {
	fake := &fakeKlineHistory{}
	h := MakeKlineHistoryEndpoint(fake, "BTCUSDT", 8, 8)

	rec := serveHistory(t, h, "/api/v1/market/kline/history")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fake.symbol != "BTCUSDT" {
		t.Errorf("symbol = %q, want default BTCUSDT", fake.symbol)
	}
	if fake.from != 0 || fake.to != 0 {
		t.Errorf("bounds = (%d, %d), want open range", fake.from, fake.to)
	}
	if fake.limit != defaultKlineLimit {
		t.Errorf("limit = %d, want %d", fake.limit, defaultKlineLimit)
	}
	if !strings.Contains(rec.Body.String(), `"klines":[]`) {
		t.Errorf("body = %s, want empty klines array", rec.Body.String())
	}
}

func TestKlineHistoryDisabled(t *testing.T) {
	rec := serveHistory(t, MakeKlineHistoryEndpoint(nil, "BTCUSDT", 8, 8),
		"/api/v1/market/kline/history?symbol=BTCUSDT")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp.Message, "clickhouse is not enabled") {
		t.Errorf("message = %q, want clickhouse-disabled explanation", resp.Message)
	}
}

func TestKlineHistoryQueryFailure(t *testing.T) {
	fake := &fakeKlineHistory{
		err: errors.New(`clickhouse: query klines for "BTCUSDT" failed: status 500 Code: 60. DB::Exception`),
	}
	rec := serveHistory(t, MakeKlineHistoryEndpoint(fake, "BTCUSDT", 8, 8),
		"/api/v1/market/kline/history?symbol=BTCUSDT")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp.Message, "kline history query failed") ||
		!strings.Contains(resp.Message, "DB::Exception") {
		t.Errorf("message = %q, want failure context and CH snippet", resp.Message)
	}
}

func TestKlineHistoryBadRequest(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   string
	}{
		{"invalid from", "/api/v1/market/kline/history?from=abc", "invalid from"},
		{"negative to", "/api/v1/market/kline/history?to=-5", "invalid to"},
		{"from after to", "/api/v1/market/kline/history?from=2000&to=1000", "from is after to"},
		{"unsupported interval", "/api/v1/market/kline/history?interval=5m", "only supports interval=1m"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeKlineHistory{}
			rec := serveHistory(t, MakeKlineHistoryEndpoint(fake, "BTCUSDT", 8, 8), tc.target)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			var resp ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if !strings.Contains(resp.Message, tc.want) {
				t.Errorf("message = %q, want %q", resp.Message, tc.want)
			}
			if fake.calls != 0 {
				t.Errorf("reader calls = %d, want 0 for a rejected request", fake.calls)
			}
		})
	}
}

func TestKlineHistoryMethodNotAllowed(t *testing.T) {
	h := MakeKlineHistoryEndpoint(&fakeKlineHistory{}, "BTCUSDT", 8, 8)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/market/kline/history", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
