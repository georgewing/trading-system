package backtest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// chJSONLines is a realistic JSONEachRow payload with a blank line thrown in to
// prove that empty lines are skipped.
const chJSONLines = `{"ts_ms":1700000000000,"symbol":"BTCUSDT","trade_id":1,"price":10000,"quantity":5}` + "\n" +
	"\n" +
	`{"ts_ms":1700000000001,"symbol":"BTCUSDT","trade_id":2,"price":10001,"quantity":7}` + "\n"

func TestOpenClickHouseSourceReplaysTrades(t *testing.T) {
	var (
		rawQuery string
		path     string
		user     string
		pass     string
		method   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		rawQuery = r.URL.RawQuery
		method = r.Method
		user, pass, _ = r.BasicAuth()

		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, chJSONLines)
	}))
	defer srv.Close()

	from := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	to := time.Date(2023, 11, 14, 23, 0, 0, 0, time.UTC)

	src, err := OpenClickHouseSource(context.Background(), ClickHouseConfig{
		URL:      srv.URL,
		Database: "market",
		Table:    "trade_ticks",
		Username: "reader",
		Password: "secret",
		Symbol:   "BTCUSDT",
		From:     from,
		To:       to,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	if method != http.MethodGet {
		t.Fatalf("method = %q, want GET", method)
	}
	if path != "/" {
		t.Fatalf("path = %q, want /", path)
	}
	if user != "reader" || pass != "secret" {
		t.Fatalf("basic auth = %q/%q", user, pass)
	}

	query := decodeQuery(t, rawQuery)
	for _, want := range []string{
		"SELECT toUnixTimestamp64Milli(ts) AS ts_ms, symbol, trade_id, price, quantity FROM market.trade_ticks",
		"WHERE symbol = 'BTCUSDT'",
		"AND ts >= toDateTime64('2023-11-14 22:13:20.000', 3)",
		"AND ts <= toDateTime64('2023-11-14 23:00:00.000', 3)",
		"ORDER BY ts, trade_id",
		"LIMIT 2",
		"FORMAT JSONEachRow",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	if strings.Contains(rawQuery, "+") {
		t.Fatalf("raw query uses '+', want %%20 escapes: %s", rawQuery)
	}

	var ev Event
	ok, err := src.Next(context.Background(), &ev)
	if err != nil {
		t.Fatalf("next 1: %v", err)
	}
	if !ok {
		t.Fatal("next 1: stream ended early")
	}
	if ev.TS != 1700000000000 || ev.Symbol != "BTCUSDT" || ev.Kind != KindTrade {
		t.Fatalf("event 1 = %+v", ev)
	}
	if ev.Trade == nil || ev.Trade.ID != 1 || ev.Trade.Price != 10000 || ev.Trade.Qty != 5 {
		t.Fatalf("trade 1 = %+v", ev.Trade)
	}
	if ev.Book != nil {
		t.Fatalf("trade event carried a book: %+v", ev.Book)
	}

	ev = Event{}
	ok, err = src.Next(context.Background(), &ev)
	if err != nil {
		t.Fatalf("next 2: %v", err)
	}
	if !ok {
		t.Fatal("next 2: stream ended early")
	}
	if ev.TS != 1700000000001 || ev.Symbol != "BTCUSDT" || ev.Kind != KindTrade {
		t.Fatalf("event 2 = %+v", ev)
	}
	if ev.Trade == nil || ev.Trade.ID != 2 || ev.Trade.Price != 10001 || ev.Trade.Qty != 7 {
		t.Fatalf("trade 2 = %+v", ev.Trade)
	}

	ok, err = src.Next(context.Background(), &ev)
	if err != nil {
		t.Fatalf("next 3: %v", err)
	}
	if ok {
		t.Fatalf("next 3 returned an event past the end: %+v", ev)
	}
}

func TestOpenClickHouseSourceOmitsBoundsAndLimit(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
	}))
	defer srv.Close()

	src, err := OpenClickHouseSource(context.Background(), ClickHouseConfig{
		URL:    srv.URL,
		Symbol: "ETHUSDT",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	query := decodeQuery(t, rawQuery)
	for _, unwanted := range []string{"toDateTime64", "LIMIT"} {
		if strings.Contains(query, unwanted) {
			t.Fatalf("query unexpectedly contains %q:\n%s", unwanted, query)
		}
	}
	if !strings.Contains(query, "FROM market.trade_ticks WHERE symbol = 'ETHUSDT'") {
		t.Fatalf("query = %s", query)
	}
}

func TestOpenClickHouseSourceNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Code: 62. DB::Exception: Syntax error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	src, err := OpenClickHouseSource(context.Background(), ClickHouseConfig{
		URL:    srv.URL,
		Symbol: "BTCUSDT",
	})
	if err == nil {
		if src != nil {
			_ = src.Close()
		}
		t.Fatal("expected an error for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want it to mention status 500", err)
	}
	if !strings.Contains(err.Error(), "Syntax error") {
		t.Fatalf("error = %v, want it to include the body snippet", err)
	}
}

func TestOpenClickHouseSourceEmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	src, err := OpenClickHouseSource(context.Background(), ClickHouseConfig{
		URL:    srv.URL,
		Symbol: "BTCUSDT",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	var ev Event
	ok, err := src.Next(context.Background(), &ev)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if ok {
		t.Fatalf("expected no events, got %+v", ev)
	}
}

func TestOpenClickHouseSourceInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  ClickHouseConfig
	}{
		{name: "empty symbol", cfg: ClickHouseConfig{}},
		{name: "blank symbol", cfg: ClickHouseConfig{Symbol: "   "}},
		{name: "quote in symbol", cfg: ClickHouseConfig{Symbol: "BTC'USDT"}},
		{name: "backslash in symbol", cfg: ClickHouseConfig{Symbol: `BTC\USDT`}},
		{name: "control char in symbol", cfg: ClickHouseConfig{Symbol: "BTC\nUSDT"}},
		{name: "invalid url scheme", cfg: ClickHouseConfig{URL: "ftp://127.0.0.1:8123", Symbol: "BTCUSDT"}},
		{name: "url with query", cfg: ClickHouseConfig{URL: "http://127.0.0.1:8123/?query=1", Symbol: "BTCUSDT"}},
		{name: "invalid database", cfg: ClickHouseConfig{Database: "1market", Symbol: "BTCUSDT"}},
		{name: "invalid table", cfg: ClickHouseConfig{Table: "trade-ticks", Symbol: "BTCUSDT"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, err := OpenClickHouseSource(context.Background(), tc.cfg)
			if err == nil {
				if src != nil {
					_ = src.Close()
				}
				t.Fatalf("expected an error for %+v", tc.cfg)
			}
			if src != nil {
				t.Fatalf("source = %v, want nil on error", src)
			}
		})
	}
}

func TestClickHouseSourceCloseNilSafe(t *testing.T) {
	var src *ClickHouseSource
	if err := src.Close(); err != nil {
		t.Fatalf("close nil source: %v", err)
	}
	if err := (&ClickHouseSource{}).Close(); err != nil {
		t.Fatalf("close empty source: %v", err)
	}
}

func TestClickHouseSourceRejectsMalformedRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{not json}\n")
	}))
	defer srv.Close()

	src, err := OpenClickHouseSource(context.Background(), ClickHouseConfig{
		URL:    srv.URL,
		Symbol: "BTCUSDT",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	var ev Event
	if _, err := src.Next(context.Background(), &ev); err == nil {
		t.Fatal("expected a parse error")
	}
}

func decodeQuery(t *testing.T, rawQuery string) string {
	t.Helper()
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("parse raw query %q: %v", rawQuery, err)
	}
	return values.Get("query")
}
