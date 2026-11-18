package clickhouse

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// klineRequestSnapshot is a query request recorded by a test server handler.
type klineRequestSnapshot struct {
	method  string
	path    string
	query   string
	params  url.Values
	user    string
	pass    string
	hasAuth bool
}

func snapshotKlineRequest(r *http.Request) klineRequestSnapshot {
	user, pass, ok := r.BasicAuth()
	return klineRequestSnapshot{
		method:  r.Method,
		path:    r.URL.Path,
		query:   r.URL.Query().Get("query"),
		params:  r.URL.Query(),
		user:    user,
		pass:    pass,
		hasAuth: ok,
	}
}

func newKlineServer(t *testing.T, status int, body string, got *klineRequestSnapshot) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			*got = snapshotKlineRequest(r)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestNewReaderDefaults(t *testing.T) {
	r, err := NewReader(ReaderConfig{})
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}

	if r.baseURL != DefaultURL {
		t.Errorf("baseURL = %q, want %q", r.baseURL, DefaultURL)
	}
	if r.database != DefaultDatabase {
		t.Errorf("database = %q, want %q", r.database, DefaultDatabase)
	}
	if r.view != DefaultView {
		t.Errorf("view = %q, want %q", r.view, DefaultView)
	}
	if r.timeout != DefaultTimeout {
		t.Errorf("timeout = %s, want %s", r.timeout, DefaultTimeout)
	}
	if r.client == nil || r.client.Timeout != DefaultTimeout {
		t.Errorf("client timeout = %v, want %s", r.client, DefaultTimeout)
	}
}

func TestNewReaderCustomTarget(t *testing.T) {
	r, err := NewReader(ReaderConfig{
		URL:      " http://127.0.0.1:8123/clickhouse/ ",
		Database: "market_v2",
		View:     "kline_1m_v2",
	})
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}

	if got, want := r.baseURL, "http://127.0.0.1:8123/clickhouse"; got != want {
		t.Errorf("baseURL = %q, want %q", got, want)
	}
	if r.database != "market_v2" {
		t.Errorf("database = %q, want market_v2", r.database)
	}
	if r.view != "kline_1m_v2" {
		t.Errorf("view = %q, want kline_1m_v2", r.view)
	}
}

func TestNewReaderInvalid(t *testing.T) {
	cases := []struct {
		name string
		cfg  ReaderConfig
	}{
		{"view with statement", ReaderConfig{View: "kline_1m; DROP VIEW market.kline_1m"}},
		{"database with dash", ReaderConfig{Database: "market-v2"}},
		{"unsupported scheme", ReaderConfig{URL: "ftp://127.0.0.1:8123"}},
		{"url with query", ReaderConfig{URL: "http://127.0.0.1:8123?x=1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewReader(tc.cfg); err == nil {
				t.Fatal("NewReader() error = nil, want validation error")
			}
		})
	}
}

func TestQueryKlinesSuccess(t *testing.T) {
	var got klineRequestSnapshot
	// The first row uses quoted integers (ClickHouse's default for 64-bit
	// columns), the second uses bare numbers. Both must decode without floats.
	body := `{"open_time_ms":"1700000000000","open":"6251801000000","high":"6252801000000","low":"6250801000000","close":"6252001000000","volume":"123450000"}
{"open_time_ms":1700000060000,"open":6251802000000,"high":6252802000000,"low":6250802000000,"close":6252002000000,"volume":123450001}
`
	srv := newKlineServer(t, http.StatusOK, body, &got)
	defer srv.Close()

	r, err := NewReader(ReaderConfig{
		URL:      srv.URL,
		Username: "reader",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}

	klines, err := r.QueryKlines(context.Background(), " BTCUSDT ", 1700000000000, 1700000300000, 2)
	if err != nil {
		t.Fatalf("QueryKlines() error = %v", err)
	}

	if got.method != http.MethodGet {
		t.Errorf("method = %q, want GET", got.method)
	}
	if got.path != "/" {
		t.Errorf("path = %q, want /", got.path)
	}
	if !got.hasAuth || got.user != "reader" || got.pass != "secret" {
		t.Errorf("basic auth = (%q, %q, %v), want reader/secret", got.user, got.pass, got.hasAuth)
	}

	if want := "BTCUSDT"; got.params.Get("param_symbol") != want {
		t.Errorf("param_symbol = %q, want %q", got.params.Get("param_symbol"), want)
	}
	if want := "1700000000000"; got.params.Get("param_from") != want {
		t.Errorf("param_from = %q, want %q", got.params.Get("param_from"), want)
	}
	if want := "1700000300000"; got.params.Get("param_to") != want {
		t.Errorf("param_to = %q, want %q", got.params.Get("param_to"), want)
	}
	if want := "2"; got.params.Get("param_limit") != want {
		t.Errorf("param_limit = %q, want %q", got.params.Get("param_limit"), want)
	}

	for _, fragment := range []string{
		"FROM market.kline_1m",
		"symbol = {symbol:String}",
		"open_time >= fromUnixTimestamp64Milli({from:Int64})",
		"open_time <= fromUnixTimestamp64Milli({to:Int64})",
		"LIMIT {limit:UInt32}",
		"ORDER BY open_time_ms ASC",
		"FORMAT JSONEachRow",
	} {
		if !strings.Contains(got.query, fragment) {
			t.Errorf("query missing %q:\n%s", fragment, got.query)
		}
	}

	if len(klines) != 2 {
		t.Fatalf("len(klines) = %d, want 2", len(klines))
	}

	first := klines[0]
	if first.OpenTime != 1700000000000 {
		t.Errorf("OpenTime = %d, want 1700000000000", first.OpenTime)
	}
	if first.Open != 6251801000000 || first.High != 6252801000000 ||
		first.Low != 6250801000000 || first.Close != 6252001000000 {
		t.Errorf("OHLC = (%d, %d, %d, %d), want quoted values",
			first.Open, first.High, first.Low, first.Close)
	}
	if first.Volume != 123450000 {
		t.Errorf("Volume = %d, want 123450000", first.Volume)
	}
	if first.Interval != KlineInterval {
		t.Errorf("Interval = %q, want %q", first.Interval, KlineInterval)
	}
	if !first.Closed {
		t.Error("Closed = false, want true for a past bucket")
	}

	second := klines[1]
	if second.OpenTime != 1700000060000 || second.Open != 6251802000000 ||
		second.High != 6252802000000 || second.Low != 6250802000000 ||
		second.Close != 6252002000000 || second.Volume != 123450001 {
		t.Errorf("second row = %+v, want bare-number values", second)
	}
}

func TestQueryKlinesOpenBucketIsNotClosed(t *testing.T) {
	openTime := time.Now().Add(5 * time.Minute).Truncate(time.Minute).UnixMilli()
	body := `{"open_time_ms":` + strconv.FormatInt(openTime, 10) + `,"open":1,"high":2,"low":1,"close":2,"volume":3}`
	srv := newKlineServer(t, http.StatusOK, body, nil)
	defer srv.Close()

	r, err := NewReader(ReaderConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}

	klines, err := r.QueryKlines(context.Background(), "BTCUSDT", 0, 0, 1)
	if err != nil {
		t.Fatalf("QueryKlines() error = %v", err)
	}
	if len(klines) != 1 {
		t.Fatalf("len(klines) = %d, want 1", len(klines))
	}
	if klines[0].Closed {
		t.Error("Closed = true for a bucket that has not ended yet")
	}
}

func TestQueryKlinesEmptyResult(t *testing.T) {
	var got klineRequestSnapshot
	srv := newKlineServer(t, http.StatusOK, "", &got)
	defer srv.Close()

	r, err := NewReader(ReaderConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}

	klines, err := r.QueryKlines(context.Background(), "BTCUSDT", 0, 0, 0)
	if err != nil {
		t.Fatalf("QueryKlines() error = %v", err)
	}
	if klines == nil {
		t.Fatal("klines = nil, want empty slice")
	}
	if len(klines) != 0 {
		t.Fatalf("len(klines) = %d, want 0", len(klines))
	}

	if _, ok := got.params["param_from"]; ok {
		t.Error("param_from present, want omitted for an open lower bound")
	}
	if _, ok := got.params["param_to"]; ok {
		t.Error("param_to present, want omitted for an open upper bound")
	}
	if strings.Contains(got.query, "fromUnixTimestamp64Milli") {
		t.Errorf("query should not filter by time:\n%s", got.query)
	}
	if want := strconv.Itoa(DefaultKlineLimit); got.params.Get("param_limit") != want {
		t.Errorf("param_limit = %q, want %q", got.params.Get("param_limit"), want)
	}
}

func TestQueryKlinesClampsLimit(t *testing.T) {
	var got klineRequestSnapshot
	srv := newKlineServer(t, http.StatusOK, "", &got)
	defer srv.Close()

	r, err := NewReader(ReaderConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}

	if _, err := r.QueryKlines(context.Background(), "BTCUSDT", 0, 0, MaxKlineLimit*2); err != nil {
		t.Fatalf("QueryKlines() error = %v", err)
	}
	if want := strconv.Itoa(MaxKlineLimit); got.params.Get("param_limit") != want {
		t.Errorf("param_limit = %q, want %q", got.params.Get("param_limit"), want)
	}
}

func TestQueryKlinesClickHouseError(t *testing.T) {
	const body = "Code: 60. DB::Exception: Table market.kline_1m does not exist. (UNKNOWN_TABLE)"
	srv := newKlineServer(t, http.StatusInternalServerError, body, nil)
	defer srv.Close()

	r, err := NewReader(ReaderConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}

	_, err = r.QueryKlines(context.Background(), "BTCUSDT", 0, 0, 10)
	if err == nil {
		t.Fatal("QueryKlines() error = nil, want failure")
	}
	for _, fragment := range []string{"500", "DB::Exception", "UNKNOWN_TABLE", "BTCUSDT"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q missing %q", err, fragment)
		}
	}
}

func TestQueryKlinesMalformedRow(t *testing.T) {
	srv := newKlineServer(t, http.StatusOK, `{"open_time_ms":`, nil)
	defer srv.Close()

	r, err := NewReader(ReaderConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}

	_, err = r.QueryKlines(context.Background(), "BTCUSDT", 0, 0, 10)
	if err == nil {
		t.Fatal("QueryKlines() error = nil, want decode error")
	}
	if !strings.Contains(err.Error(), "decode klines") {
		t.Errorf("error = %q, want decode context", err)
	}
}

func TestQueryKlinesRejectsInvalidArguments(t *testing.T) {
	r, err := NewReader(ReaderConfig{})
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}

	cases := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "empty symbol",
			call: func() error {
				_, err := r.QueryKlines(context.Background(), "  ", 0, 0, 10)
				return err
			},
			want: "symbol is required",
		},
		{
			name: "negative from",
			call: func() error {
				_, err := r.QueryKlines(context.Background(), "BTCUSDT", -1, 0, 10)
				return err
			},
			want: "must not be negative",
		},
		{
			name: "from after to",
			call: func() error {
				_, err := r.QueryKlines(context.Background(), "BTCUSDT", 2000, 1000, 10)
				return err
			},
			want: "is after",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want %q", err, tc.want)
			}
		})
	}
}
