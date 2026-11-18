// Package clickhouse provides a stdlib-only writer for trade ticks.
//
// The writer talks to the ClickHouse HTTP interface: Ping issues GET /ping and
// InsertTrades posts newline-delimited JSON via FORMAT JSONEachRow. Batching,
// retries and buffering are deliberately left to the caller.
package clickhouse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultURL is the endpoint used when Config.URL is empty.
	DefaultURL = "http://127.0.0.1:8123"
	// DefaultDatabase is used when Config.Database is empty.
	DefaultDatabase = "market"
	// DefaultTable is used when Config.Table is empty.
	DefaultTable = "trade_ticks"
	// DefaultTimeout bounds one request when Config.Timeout is not positive.
	DefaultTimeout = 5 * time.Second

	// errSnippetLimit caps how many response bytes end up in an error.
	errSnippetLimit = 512
	// drainLimit caps how much of a response body is drained for reuse.
	drainLimit = 4 << 10

	// dateTimeInputFormat is sent with inserts so ISO 8601 timestamps parse.
	// The default ClickHouse parser accepts only YYYY-MM-DD HH:MM:SS.
	dateTimeInputFormat = "best_effort"
)

// identPattern accepts unquoted ClickHouse identifiers: a leading letter or
// underscore followed by letters, digits or underscores.
var identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config configures a Writer. Zero values fall back to defaults.
type Config struct {
	// URL is the ClickHouse HTTP endpoint, default DefaultURL.
	URL string
	// Database qualifies the target table, default DefaultDatabase.
	Database string
	// Table is the target table, default DefaultTable.
	Table string
	// Username enables basic auth when set.
	Username string
	// Password is ignored unless Username is set.
	Password string
	// HTTPClient overrides the internal client. Timeout still applies per call.
	HTTPClient *http.Client
	// Timeout bounds each request, default DefaultTimeout.
	Timeout time.Duration
}

// TradeTick is one executed trade to persist. TS must be a valid instant.
type TradeTick struct {
	TS          time.Time
	Symbol      string
	TradeID     uint64
	Price       int64
	Quantity    int64
	BuyOrderID  uint64
	SellOrderID uint64
}

// Writer inserts trade ticks into ClickHouse. It starts no goroutines and owns
// no resources, so it has no Close method. A Writer is immutable after
// construction and safe for concurrent use.
type Writer struct {
	client    *http.Client
	timeout   time.Duration
	username  string
	password  string
	pingURL   string
	insertURL string
}

// NewWriter validates cfg and builds a Writer.
func NewWriter(cfg Config) (*Writer, error) {
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

	table := cfg.Table
	if table == "" {
		table = DefaultTable
	}
	if !identPattern.MatchString(table) {
		return nil, fmt.Errorf("clickhouse: invalid table %q: want letters, digits or underscore, not starting with a digit", table)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	statement := fmt.Sprintf("INSERT INTO %s.%s FORMAT JSONEachRow", database, table)

	return &Writer{
		client:   client,
		timeout:  timeout,
		username: cfg.Username,
		password: cfg.Password,
		pingURL:  base + "/ping",
		insertURL: base + "/?query=" + encodeQuery(statement) +
			"&date_time_input_format=" + dateTimeInputFormat,
	}, nil
}

// Ping checks connectivity with GET /ping and requires HTTP 200.
func (w *Writer) Ping(ctx context.Context) error {
	req, cancel, err := w.newRequest(ctx, http.MethodGet, w.pingURL, nil)
	if err != nil {
		return err
	}
	defer cancel()

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("clickhouse: ping: %w", err)
	}
	defer closeBody(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clickhouse: ping failed: %w", statusError(resp))
	}
	return nil
}

// InsertTrades posts ticks as newline-delimited JSON. An empty slice is a no-op.
// Non-2xx responses become errors carrying the status line and a bounded body
// snippet. InsertTrades never retries; callers decide whether to resend.
//
// The request sets date_time_input_format=best_effort so the RFC3339Nano ts
// strings are parsed as ISO 8601 instead of the basic YYYY-MM-DD hh:mm:ss form.
func (w *Writer) InsertTrades(ctx context.Context, ticks []TradeTick) error {
	if len(ticks) == 0 {
		return nil
	}

	body, err := encodeTicks(ticks)
	if err != nil {
		return err
	}

	req, cancel, err := w.newRequest(ctx, http.MethodPost, w.insertURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer cancel()
	req.Header.Set("Content-Type", "application/x-ndjson")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("clickhouse: insert %d ticks: %w", len(ticks), err)
	}
	defer closeBody(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("clickhouse: insert %d ticks failed: %w", len(ticks), statusError(resp))
	}
	return nil
}

// newRequest builds a request with basic auth (when configured) and a deadline
// derived from the writer timeout. The caller must invoke cancel.
func (w *Writer) newRequest(ctx context.Context, method, target string, body io.Reader) (*http.Request, context.CancelFunc, error) {
	reqCtx, cancel := context.WithTimeout(ctx, w.timeout)
	req, err := http.NewRequestWithContext(reqCtx, method, target, body)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("clickhouse: build request: %w", err)
	}
	if w.username != "" {
		req.SetBasicAuth(w.username, w.password)
	}
	return req, cancel, nil
}

// tickJSON is the JSONEachRow shape. ts is an RFC3339Nano UTC string with
// millisecond precision, matching DateTime64(3). Inserts request
// date_time_input_format=best_effort so the value parses as ISO 8601, and
// ClickHouse converts it to the column time zone (Asia/Shanghai) on insert.
type tickJSON struct {
	TS          string `json:"ts"`
	Symbol      string `json:"symbol"`
	TradeID     uint64 `json:"trade_id"`
	Price       int64  `json:"price"`
	Quantity    int64  `json:"quantity"`
	BuyOrderID  uint64 `json:"buy_order_id"`
	SellOrderID uint64 `json:"sell_order_id"`
}

func newTickJSON(t TradeTick) tickJSON {
	return tickJSON{
		TS:          formatTimestamp(t.TS),
		Symbol:      t.Symbol,
		TradeID:     t.TradeID,
		Price:       t.Price,
		Quantity:    t.Quantity,
		BuyOrderID:  t.BuyOrderID,
		SellOrderID: t.SellOrderID,
	}
}

// formatTimestamp renders ts as RFC3339Nano in UTC, truncated to the
// millisecond precision of the column.
func formatTimestamp(ts time.Time) string {
	return ts.Truncate(time.Millisecond).UTC().Format(time.RFC3339Nano)
}

// encodeTicks returns newline-terminated JSON lines, one per tick.
func encodeTicks(ticks []TradeTick) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, len(ticks)*160))
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)

	for i := range ticks {
		if err := enc.Encode(newTickJSON(ticks[i])); err != nil {
			return nil, fmt.Errorf("clickhouse: encode tick %d: %w", i, err)
		}
	}
	return buf.Bytes(), nil
}

// normalizeURL validates the endpoint and strips trailing slashes. Only plain
// http/https URLs without query or fragment are accepted.
func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultURL
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("clickhouse: invalid url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("clickhouse: invalid url %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("clickhouse: invalid url %q: host is empty", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("clickhouse: invalid url %q: query and fragment are not supported", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// encodeQuery escapes a statement for the query URL parameter. Spaces use %20
// rather than + so the value decodes the same regardless of form semantics.
func encodeQuery(statement string) string {
	return strings.ReplaceAll(url.QueryEscape(statement), "+", "%20")
}

// statusError turns a non-2xx response into an error with the status line and a
// bounded body snippet.
func statusError(resp *http.Response) error {
	snippet := readSnippet(resp.Body)
	if snippet == "" {
		return fmt.Errorf("status %s", resp.Status)
	}
	return fmt.Errorf("status %s: %s", resp.Status, snippet)
}

// readSnippet reads at most errSnippetLimit bytes and trims whitespace.
func readSnippet(body io.Reader) string {
	buf, _ := io.ReadAll(io.LimitReader(body, errSnippetLimit))
	return strings.TrimSpace(string(buf))
}

// closeBody drains a bounded amount before closing so the transport can reuse
// the underlying connection.
func closeBody(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, drainLimit))
	_ = body.Close()
}
