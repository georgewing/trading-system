package clickhouse

// Read side of the ClickHouse adapter: historical 1-minute candles.
//
// The reader talks to the same HTTP interface as the writer and queries the
// market.kline_1m view created by 000001_init.sql. It issues one GET per query;
// caching and paging are left to the caller.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"trading-system/internal/hft/domain"
)

const (
	// DefaultView is the 1-minute candle view defined by 000001_init.sql.
	DefaultView = "kline_1m"
	// KlineInterval is the bucket width of DefaultView.
	KlineInterval = "1m"

	// DefaultKlineLimit is used when QueryKlines receives a non-positive limit.
	DefaultKlineLimit = 1000
	// MaxKlineLimit caps one query so a bad request cannot scan the view.
	MaxKlineLimit = 10000

	// maxKlineResponseBytes bounds the response body read into memory.
	maxKlineResponseBytes = 16 << 20
)

// klineBucketMillis is the width of one KlineInterval bucket.
const klineBucketMillis = int64(time.Minute / time.Millisecond)

// ReaderConfig configures a Reader. Zero values fall back to defaults.
type ReaderConfig struct {
	// URL is the ClickHouse HTTP endpoint, default DefaultURL.
	URL string
	// Database qualifies the kline view, default DefaultDatabase.
	Database string
	// View is the kline view name, default DefaultView. It must expose one row
	// per (symbol, 1-minute bucket) with the columns DefaultView provides.
	View string
	// Username enables basic auth when set.
	Username string
	// Password is ignored unless Username is set.
	Password string
	// HTTPClient overrides the internal client. Timeout still applies per call.
	HTTPClient *http.Client
	// Timeout bounds each request, default DefaultTimeout.
	Timeout time.Duration
}

// Reader queries historical candles from ClickHouse. It starts no goroutines
// and owns no resources. A Reader is immutable after construction and safe for
// concurrent use.
type Reader struct {
	client   *http.Client
	timeout  time.Duration
	username string
	password string
	baseURL  string
	database string
	view     string
}

// NewReader validates cfg and builds a Reader.
func NewReader(cfg ReaderConfig) (*Reader, error) {
	base, err := normalizeURL(cfg.URL)
	if err != nil {
		return nil, err
	}

	database := cfg.Database
	if database == "" {
		database = DefaultDatabase
	}
	if !identPattern.MatchString(database) {
		return nil, fmt.Errorf("clickhouse: invalid database %q: want letters, digits or underscore, not starting with a digit", database)
	}

	view := cfg.View
	if view == "" {
		view = DefaultView
	}
	if !identPattern.MatchString(view) {
		return nil, fmt.Errorf("clickhouse: invalid view %q: want letters, digits or underscore, not starting with a digit", view)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	return &Reader{
		client:   client,
		timeout:  timeout,
		username: cfg.Username,
		password: cfg.Password,
		baseURL:  base,
		database: database,
		view:     view,
	}, nil
}

// QueryKlines returns candles for symbol whose bucket start lies in
// [fromMillis, toMillis], in ascending open_time order.
//
// fromMillis and toMillis are unix milliseconds; a non-positive bound means
// "unbounded". When limit truncates the window, the most recent limit candles
// are kept, matching the in-memory /api/v1/market/kline semantics. A
// non-positive limit falls back to DefaultKlineLimit and values above
// MaxKlineLimit are clamped.
//
// Prices and volume are returned as integers in the instrument's minimal unit;
// no float conversion happens. Non-2xx responses and undecodable rows become
// errors carrying a bounded snippet of the ClickHouse response.
func (r *Reader) QueryKlines(ctx context.Context, symbol string, fromMillis, toMillis int64, limit int) ([]domain.Kline, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, errors.New("clickhouse: query klines: symbol is required")
	}
	if fromMillis < 0 || toMillis < 0 {
		return nil, errors.New("clickhouse: query klines: time bounds must not be negative")
	}
	if fromMillis > 0 && toMillis > 0 && fromMillis > toMillis {
		return nil, fmt.Errorf("clickhouse: query klines: from %d is after to %d", fromMillis, toMillis)
	}
	if limit <= 0 {
		limit = DefaultKlineLimit
	}
	if limit > MaxKlineLimit {
		limit = MaxKlineLimit
	}

	statement := buildKlineStatement(r.database, r.view, fromMillis > 0, toMillis > 0)

	params := url.Values{}
	params.Set("query", statement)
	params.Set("param_symbol", symbol)
	params.Set("param_limit", strconv.Itoa(limit))
	if fromMillis > 0 {
		params.Set("param_from", strconv.FormatInt(fromMillis, 10))
	}
	if toMillis > 0 {
		params.Set("param_to", strconv.FormatInt(toMillis, 10))
	}

	// Spaces use %20 rather than + so the statement decodes the same regardless
	// of form semantics, matching the writer.
	target := r.baseURL + "/?" + strings.ReplaceAll(params.Encode(), "+", "%20")

	req, cancel, err := r.newRequest(ctx, target)
	if err != nil {
		return nil, err
	}
	defer cancel()

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: query klines for %q: %w", symbol, err)
	}
	defer closeBody(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("clickhouse: query klines for %q failed: %w", symbol, statusError(resp))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxKlineResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("clickhouse: read klines for %q: %w", symbol, err)
	}
	if len(data) > maxKlineResponseBytes {
		return nil, fmt.Errorf("clickhouse: read klines for %q: response exceeds %d bytes", symbol, maxKlineResponseBytes)
	}

	klines, err := decodeKlines(data)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: decode klines for %q: %w", symbol, err)
	}
	return klines, nil
}

// buildKlineStatement renders the JSONEachRow query. symbol, from, to and limit
// travel as HTTP query parameters ({name:Type}), never interpolated into SQL.
//
// The inner query keeps the most recent rows; the outer one restores ascending
// order for the wire. FORMAT is appended because the view is read through the
// HTTP interface, whose default output format is TabSeparated.
func buildKlineStatement(database, view string, hasFrom, hasTo bool) string {
	var conditions strings.Builder
	if hasFrom {
		conditions.WriteString("\n      AND open_time >= fromUnixTimestamp64Milli({from:Int64})")
	}
	if hasTo {
		conditions.WriteString("\n      AND open_time <= fromUnixTimestamp64Milli({to:Int64})")
	}

	return fmt.Sprintf(`SELECT open_time_ms, open, high, low, close, volume
FROM (
    SELECT
        toInt64(toUnixTimestamp(open_time)) * 1000 AS open_time_ms,
        open,
        high,
        low,
        close,
        volume
    FROM %s.%s
    WHERE symbol = {symbol:String}%s
    ORDER BY open_time DESC
    LIMIT {limit:UInt32}
)
ORDER BY open_time_ms ASC
FORMAT JSONEachRow`, database, view, conditions.String())
}

// klineRow is one JSONEachRow result. Numbers use json.Number because
// ClickHouse quotes 64-bit integers by default, and because decoding into
// json.Number never passes through float64.
type klineRow struct {
	OpenTime json.Number `json:"open_time_ms"`
	Open     json.Number `json:"open"`
	High     json.Number `json:"high"`
	Low      json.Number `json:"low"`
	Close    json.Number `json:"close"`
	Volume   json.Number `json:"volume"`
}

// decodeKlines parses the response body. An empty body is a valid empty result.
// now decides whether a bucket is still open; it is captured once per query.
func decodeKlines(data []byte) ([]domain.Kline, error) {
	now := time.Now()
	dec := json.NewDecoder(bytes.NewReader(data))
	klines := make([]domain.Kline, 0, 16)

	for {
		var row klineRow
		err := dec.Decode(&row)
		if errors.Is(err, io.EOF) {
			return klines, nil
		}
		if err != nil {
			return nil, fmt.Errorf("row %d: %w: %s", len(klines), err, readSnippet(bytes.NewReader(data)))
		}

		k, err := row.toKline(now)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", len(klines), err)
		}
		klines = append(klines, k)
	}
}

func (row klineRow) toKline(now time.Time) (domain.Kline, error) {
	var k domain.Kline
	var err error

	if k.OpenTime, err = row.OpenTime.Int64(); err != nil {
		return domain.Kline{}, fmt.Errorf("open_time_ms: %w", err)
	}
	if k.Open, err = row.Open.Int64(); err != nil {
		return domain.Kline{}, fmt.Errorf("open: %w", err)
	}
	if k.High, err = row.High.Int64(); err != nil {
		return domain.Kline{}, fmt.Errorf("high: %w", err)
	}
	if k.Low, err = row.Low.Int64(); err != nil {
		return domain.Kline{}, fmt.Errorf("low: %w", err)
	}
	if k.Close, err = row.Close.Int64(); err != nil {
		return domain.Kline{}, fmt.Errorf("close: %w", err)
	}
	if k.Volume, err = row.Volume.Int64(); err != nil {
		return domain.Kline{}, fmt.Errorf("volume: %w", err)
	}

	k.Interval = KlineInterval
	// The view materializes the current minute as soon as the first trade
	// arrives, so only buckets whose end has passed are final.
	k.Closed = k.OpenTime+klineBucketMillis <= now.UnixMilli()
	return k, nil
}

// newRequest builds a GET request with basic auth (when configured) and a
// deadline derived from the reader timeout. The caller must invoke cancel.
func (r *Reader) newRequest(ctx context.Context, target string) (*http.Request, context.CancelFunc, error) {
	reqCtx, cancel := context.WithTimeout(ctx, r.timeout)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("clickhouse: build request: %w", err)
	}
	if r.username != "" {
		req.SetBasicAuth(r.username, r.password)
	}
	return req, cancel, nil
}
