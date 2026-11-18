package clickhouse

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// capturedRequest is a request snapshot taken by a test server handler.
type capturedRequest struct {
	method      string
	path        string
	query       string
	dtFormat    string
	contentType string
	user        string
	pass        string
	hasAuth     bool
	body        []byte
}

func capture(r *http.Request) (capturedRequest, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return capturedRequest{}, err
	}
	user, pass, ok := r.BasicAuth()
	values := r.URL.Query()
	return capturedRequest{
		method:      r.Method,
		path:        r.URL.Path,
		query:       values.Get("query"),
		dtFormat:    values.Get("date_time_input_format"),
		contentType: r.Header.Get("Content-Type"),
		user:        user,
		pass:        pass,
		hasAuth:     ok,
		body:        body,
	}, nil
}

// requestLog records requests served by a test server. Handlers must never
// block: a blocking channel send parks the handler goroutine as soon as the
// client sends more requests than the test expects (for example an idempotent
// GET replayed by the transport on a stale keep-alive connection).
// httptest.Server.Close waits for active connections, so a parked handler
// wedges the whole test binary instead of failing the test.
type requestLog struct {
	mu    sync.Mutex
	reqs  []capturedRequest
	ready chan struct{}
}

func newRequestLog() *requestLog {
	return &requestLog{ready: make(chan struct{}, 1)}
}

// handle captures r and reports whether the caller should still respond.
func (log *requestLog) handle(rw http.ResponseWriter, r *http.Request) bool {
	c, err := capture(r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return false
	}

	log.mu.Lock()
	log.reqs = append(log.reqs, c)
	log.mu.Unlock()

	select {
	case log.ready <- struct{}{}:
	default:
	}
	return true
}

// wait returns the recorded requests once at least n have arrived. It fails the
// test on timeout so a missing request never hangs the run.
func (log *requestLog) wait(t *testing.T, n int) []capturedRequest {
	t.Helper()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		log.mu.Lock()
		got := append([]capturedRequest(nil), log.reqs...)
		log.mu.Unlock()
		if len(got) >= n {
			return got
		}

		select {
		case <-log.ready:
		case <-timer.C:
			t.Fatalf("timed out waiting for %d request(s), got %d", n, len(got))
		}
	}
}

func TestNewWriterDefaults(t *testing.T) {
	w, err := NewWriter(Config{})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	if got, want := w.pingURL, "http://127.0.0.1:8123/ping"; got != want {
		t.Errorf("pingURL = %q, want %q", got, want)
	}

	u, err := url.Parse(w.insertURL)
	if err != nil {
		t.Fatalf("parse insertURL %q: %v", w.insertURL, err)
	}
	if got, want := u.Path, "/"; got != want {
		t.Errorf("insert path = %q, want %q", got, want)
	}
	if got, want := u.Query().Get("query"), "INSERT INTO market.trade_ticks FORMAT JSONEachRow"; got != want {
		t.Errorf("insert query = %q, want %q", got, want)
	}
	if got, want := u.Query().Get("date_time_input_format"), "best_effort"; got != want {
		t.Errorf("date_time_input_format = %q, want %q", got, want)
	}

	if w.timeout != DefaultTimeout {
		t.Errorf("timeout = %s, want %s", w.timeout, DefaultTimeout)
	}
	if w.client == nil {
		t.Fatal("client = nil, want default client")
	}
	if w.client.Timeout != DefaultTimeout {
		t.Errorf("client timeout = %s, want %s", w.client.Timeout, DefaultTimeout)
	}
}

func TestNewWriterCustomTarget(t *testing.T) {
	w, err := NewWriter(Config{
		URL:      " http://127.0.0.1:8123/clickhouse/ ",
		Database: "market_v2",
		Table:    "trade_ticks_v2",
	})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	if got, want := w.pingURL, "http://127.0.0.1:8123/clickhouse/ping"; got != want {
		t.Errorf("pingURL = %q, want %q", got, want)
	}

	u, err := url.Parse(w.insertURL)
	if err != nil {
		t.Fatalf("parse insertURL %q: %v", w.insertURL, err)
	}
	if got, want := u.Path, "/clickhouse/"; got != want {
		t.Errorf("insert path = %q, want %q", got, want)
	}
	if got, want := u.Query().Get("query"), "INSERT INTO market_v2.trade_ticks_v2 FORMAT JSONEachRow"; got != want {
		t.Errorf("insert query = %q, want %q", got, want)
	}
	if got, want := u.Query().Get("date_time_input_format"), "best_effort"; got != want {
		t.Errorf("date_time_input_format = %q, want %q", got, want)
	}
}

func TestNewWriterInvalid(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"table with statement", Config{Table: "trade_ticks; DROP TABLE market.trade_ticks"}},
		{"table with quote", Config{Table: "trade`ticks"}},
		{"table leading digit", Config{Table: "1ticks"}},
		{"table with space", Config{Table: "trade ticks"}},
		{"database with space", Config{Database: "mar ket"}},
		{"database with dash", Config{Database: "market-2"}},
		{"missing scheme", Config{URL: "localhost:8123"}},
		{"unsupported scheme", Config{URL: "ftp://127.0.0.1:8123"}},
		{"unparsable url", Config{URL: "://"}},
		{"empty host", Config{URL: "http:///path"}},
		{"url with query", Config{URL: "http://127.0.0.1:8123/?database=other"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWriter(tc.cfg); err == nil {
				t.Fatalf("NewWriter(%+v) error = nil, want error", tc.cfg)
			}
		})
	}
}

func TestPingSuccess(t *testing.T) {
	log := newRequestLog()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !log.handle(rw, r) {
			return
		}
		_, _ = io.WriteString(rw, "Ok.\n")
	}))
	defer srv.Close()

	w, err := NewWriter(Config{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	if err := w.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	got := log.wait(t, 1)[0]
	if got.method != http.MethodGet {
		t.Errorf("method = %q, want %q", got.method, http.MethodGet)
	}
	if got.path != "/ping" {
		t.Errorf("path = %q, want /ping", got.path)
	}
	if got.hasAuth {
		t.Errorf("basic auth = %v, want none when username is empty", got.hasAuth)
	}
}

func TestPingNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(rw, "Code: 999. DB::Exception: service unavailable")
	}))
	defer srv.Close()

	w, err := NewWriter(Config{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	err = w.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping() error = nil, want error")
	}
	if msg := err.Error(); !strings.Contains(msg, "503") {
		t.Errorf("error %q does not contain status 503", msg)
	}
	if msg := err.Error(); !strings.Contains(msg, "service unavailable") {
		t.Errorf("error %q does not contain body snippet", msg)
	}
}

func TestInsertTrades(t *testing.T) {
	log := newRequestLog()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !log.handle(rw, r) {
			return
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w, err := NewWriter(Config{URL: srv.URL, Username: "hft", Password: "s3cret"})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	cst := time.FixedZone("CST", 8*60*60)
	ticks := []TradeTick{
		{
			TS:          time.Date(2024, 5, 6, 7, 8, 9, 123456789, cst),
			Symbol:      "BTCUSDT",
			TradeID:     9007199254740993, // above 2^53, catches float64 drift
			Price:       6800012,
			Quantity:    -3,
			BuyOrderID:  11,
			SellOrderID: 12,
		},
		{
			TS:          time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC),
			Symbol:      "ETHUSDT",
			TradeID:     2,
			Price:       350000,
			Quantity:    100,
			BuyOrderID:  21,
			SellOrderID: 22,
		},
	}

	if err := w.InsertTrades(context.Background(), ticks); err != nil {
		t.Fatalf("InsertTrades() error = %v", err)
	}

	got := log.wait(t, 1)[0]
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want %q", got.method, http.MethodPost)
	}
	if got.path != "/" {
		t.Errorf("path = %q, want /", got.path)
	}
	if want := "INSERT INTO market.trade_ticks FORMAT JSONEachRow"; got.query != want {
		t.Errorf("query = %q, want %q", got.query, want)
	}
	if want := "best_effort"; got.dtFormat != want {
		t.Errorf("date_time_input_format = %q, want %q", got.dtFormat, want)
	}
	if want := "application/x-ndjson"; got.contentType != want {
		t.Errorf("content type = %q, want %q", got.contentType, want)
	}
	if !got.hasAuth || got.user != "hft" || got.pass != "s3cret" {
		t.Errorf("basic auth = (%q, %q, %v), want (hft, s3cret, true)", got.user, got.pass, got.hasAuth)
	}

	if len(got.body) == 0 || got.body[len(got.body)-1] != '\n' {
		t.Fatalf("body is not newline terminated: %q", got.body)
	}
	lines := bytes.Split(bytes.TrimSuffix(got.body, []byte("\n")), []byte("\n"))
	if len(lines) != len(ticks) {
		t.Fatalf("body has %d JSON lines, want %d: %q", len(lines), len(ticks), got.body)
	}

	decoded := make([]tickJSON, len(lines))
	for i, line := range lines {
		if err := json.Unmarshal(line, &decoded[i]); err != nil {
			t.Fatalf("line %d is not valid JSON: %v (%q)", i, err, line)
		}
	}
	want := []tickJSON{
		{
			TS:          "2024-05-05T23:08:09.123Z",
			Symbol:      "BTCUSDT",
			TradeID:     9007199254740993,
			Price:       6800012,
			Quantity:    -3,
			BuyOrderID:  11,
			SellOrderID: 12,
		},
		{
			TS:          "2024-05-06T00:00:00Z",
			Symbol:      "ETHUSDT",
			TradeID:     2,
			Price:       350000,
			Quantity:    100,
			BuyOrderID:  21,
			SellOrderID: 22,
		},
	}
	for i := range want {
		if decoded[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, decoded[i], want[i])
		}
	}
}

func TestInsertTradesEmpty(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w, err := NewWriter(Config{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	if err := w.InsertTrades(context.Background(), nil); err != nil {
		t.Errorf("InsertTrades(nil) error = %v", err)
	}
	if err := w.InsertTrades(context.Background(), []TradeTick{}); err != nil {
		t.Errorf("InsertTrades(empty) error = %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("server received %d requests, want 0 for empty input", got)
	}
}

func TestInsertTradesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		rw.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(rw, "Code: 62. DB::Exception: Syntax error near 'ts'")
	}))
	defer srv.Close()

	w, err := NewWriter(Config{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	err = w.InsertTrades(context.Background(), []TradeTick{{
		TS:      time.UnixMilli(1700000000123).UTC(),
		Symbol:  "BTCUSDT",
		TradeID: 1,
	}})
	if err == nil {
		t.Fatal("InsertTrades() error = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "500") {
		t.Errorf("error %q does not contain status 500", msg)
	}
	if !strings.Contains(msg, "Syntax error near 'ts'") {
		t.Errorf("error %q does not contain body snippet", msg)
	}
}

func TestInsertTradesErrorSnippetBounded(t *testing.T) {
	long := strings.Repeat("x", errSnippetLimit*4)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		rw.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(rw, long)
	}))
	defer srv.Close()

	w, err := NewWriter(Config{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	err = w.InsertTrades(context.Background(), []TradeTick{{
		TS:      time.UnixMilli(1700000000123).UTC(),
		Symbol:  "BTCUSDT",
		TradeID: 1,
	}})
	if err == nil {
		t.Fatal("InsertTrades() error = nil, want error")
	}
	if got := len(err.Error()); got > errSnippetLimit+100 {
		t.Errorf("error length = %d, want <= %d", got, errSnippetLimit+100)
	}
}

func TestInsertTradesTimeout(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(url string) Config
	}{
		{
			name: "default client",
			cfg:  func(u string) Config { return Config{URL: u, Timeout: 30 * time.Millisecond} },
		},
		{
			name: "custom client",
			cfg: func(u string) Config {
				return Config{URL: u, Timeout: 30 * time.Millisecond, HTTPClient: &http.Client{}}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				// Return as soon as the client gives up so Close does not have to
				// wait out the full delay on an active connection.
				select {
				case <-time.After(200 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
				rw.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			w, err := NewWriter(tc.cfg(srv.URL))
			if err != nil {
				t.Fatalf("NewWriter() error = %v", err)
			}

			start := time.Now()
			err = w.InsertTrades(context.Background(), []TradeTick{{
				TS:      time.UnixMilli(1700000000123).UTC(),
				Symbol:  "BTCUSDT",
				TradeID: 1,
			}})
			if err == nil {
				t.Fatal("InsertTrades() error = nil, want timeout error")
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("InsertTrades() took %s, want timeout near 30ms", elapsed)
			}
		})
	}
}

func TestInsertTradesContextCanceled(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w, err := NewWriter(Config{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = w.InsertTrades(ctx, []TradeTick{{
		TS:      time.UnixMilli(1700000000123).UTC(),
		Symbol:  "BTCUSDT",
		TradeID: 1,
	}})
	if err == nil {
		t.Fatal("InsertTrades() error = nil, want context error")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("server received %d requests, want 0 for a canceled context", got)
	}
}

func TestBasePathPrefix(t *testing.T) {
	log := newRequestLog()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !log.handle(rw, r) {
			return
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w, err := NewWriter(Config{URL: srv.URL + "/clickhouse/"})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	if err := w.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if err := w.InsertTrades(context.Background(), []TradeTick{{
		TS:      time.UnixMilli(1700000000123).UTC(),
		Symbol:  "BTCUSDT",
		TradeID: 7,
	}}); err != nil {
		t.Fatalf("InsertTrades() error = %v", err)
	}

	got := log.wait(t, 2)
	ping, insert := got[0], got[1]
	if want := "/clickhouse/ping"; ping.path != want {
		t.Errorf("ping path = %q, want %q", ping.path, want)
	}
	if want := "/clickhouse/"; insert.path != want {
		t.Errorf("insert path = %q, want %q", insert.path, want)
	}
}

func TestNoGoroutineLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/ping" {
			_, _ = io.WriteString(rw, "Ok.\n")
			return
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w, err := NewWriter(Config{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}

	ctx := context.Background()
	tick := TradeTick{
		TS:       time.UnixMilli(1700000000123).UTC(),
		Symbol:   "BTCUSDT",
		TradeID:  1,
		Price:    1,
		Quantity: 1,
	}
	roundTrip := func() error {
		if err := w.Ping(ctx); err != nil {
			return err
		}
		return w.InsertTrades(ctx, []TradeTick{tick})
	}

	// Warm up so idle connections owned by the transport are already counted.
	for i := 0; i < 5; i++ {
		if err := roundTrip(); err != nil {
			t.Fatalf("warmup %d: %v", i, err)
		}
	}
	base := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		if err := roundTrip(); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines grew from %d to %d", base, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
