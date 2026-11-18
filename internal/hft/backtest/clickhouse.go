package backtest

import (
	"bufio"
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

// ClickHouseSource replays historical trade ticks from the ClickHouse HTTP
// interface as a monotonic Source. One query is issued at Open time and the
// response body is scanned line by line with FORMAT JSONEachRow, so a long
// replay never buffers the full result set.
//
// Only trade prints are replayed: the trade_ticks table has no book snapshots,
// so Next always emits KindTrade.
type ClickHouseSource struct {
	body    io.ReadCloser
	cancel  context.CancelFunc
	scanner *bufio.Scanner
	line    int
}

var _ Source = (*ClickHouseSource)(nil)

const (
	chDefaultURL      = "http://127.0.0.1:8123"
	chDefaultDatabase = "market"
	chDefaultTable    = "trade_ticks"
	chDefaultTimeout  = 10 * time.Second

	// chErrSnippetLimit caps how many response bytes end up in an error.
	chErrSnippetLimit = 512
	// chDrainLimit caps how much of a response body is drained for reuse.
	chDrainLimit = 4 << 10

	// chScanInitialBuf/chScanMaxBuf keep the scanner from failing on wide rows.
	chScanInitialBuf = 1 << 20
	chScanMaxBuf     = 8 << 20

	// chDateTimeLayout matches DateTime64(3) second precision plus millis. The
	// value is rendered in UTC and passed as a bare literal, so it is read in the
	// server time zone like the stored column.
	chDateTimeLayout = "2006-01-02 15:04:05.000"
)

// chIdentPattern accepts unquoted ClickHouse identifiers: a leading letter or
// underscore followed by letters, digits or underscores. It mirrors the writer
// rule so configuration behaves the same on both sides of the pipeline.
var chIdentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ClickHouseConfig configures a ClickHouseSource. Zero values fall back to
// defaults; only Symbol is mandatory.
type ClickHouseConfig struct {
	// URL is the ClickHouse HTTP endpoint, default http://127.0.0.1:8123.
	URL string
	// Database qualifies the source table, default market.
	Database string
	// Table is the source table, default trade_ticks.
	Table string
	// Username enables basic auth when set.
	Username string
	// Password is ignored unless Username is set.
	Password string
	// Symbol selects the only instrument that is replayed. Required.
	Symbol string
	// From is an inclusive lower bound; the zero value means no lower bound.
	From time.Time
	// To is an inclusive upper bound; the zero value means no upper bound.
	To time.Time
	// Limit caps the number of rows; a non-positive value means no limit.
	Limit int
	// HTTPClient overrides the internal client. When set, Timeout is not
	// applied and the client's own configuration governs the exchange.
	HTTPClient *http.Client
	// Timeout bounds the wait for response headers on the internal client. It
	// does not cap the streaming body, which is bounded by the Next context.
	Timeout time.Duration
}

// OpenClickHouseSource validates cfg, issues one query and returns a Source that
// streams the matching trades in (ts, trade_id) order. The response body stays
// open until Close is called.
func OpenClickHouseSource(ctx context.Context, cfg ClickHouseConfig) (Source, error) {
	base, err := normalizeCHURL(cfg.URL)
	if err != nil {
		return nil, err
	}

	database := cfg.Database
	if database == "" {
		database = chDefaultDatabase
	}
	if !chIdentPattern.MatchString(database) {
		return nil, fmt.Errorf("backtest: clickhouse: invalid database %q: want letters, digits or underscore, not starting with a digit", database)
	}

	table := cfg.Table
	if table == "" {
		table = chDefaultTable
	}
	if !chIdentPattern.MatchString(table) {
		return nil, fmt.Errorf("backtest: clickhouse: invalid table %q: want letters, digits or underscore, not starting with a digit", table)
	}

	symbol := strings.TrimSpace(cfg.Symbol)
	if symbol == "" {
		return nil, fmt.Errorf("backtest: clickhouse: symbol is required")
	}
	if err := validateCHSymbol(symbol); err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = chDefaultTimeout
	}

	client := cfg.HTTPClient
	if client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = timeout
		client = &http.Client{Transport: tr}
	}

	statement := buildCHQuery(database, table, symbol, cfg.From, cfg.To, cfg.Limit)
	target := base + "/?query=" + encodeCHQuery(statement)

	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("backtest: clickhouse: build request: %w", err)
	}
	if cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("backtest: clickhouse: query %s.%s: %w", database, table, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		statusErr := chStatusError(resp)
		chCloseBody(resp.Body)
		cancel()
		return nil, fmt.Errorf("backtest: clickhouse: query %s.%s failed: %w", database, table, statusErr)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, chScanInitialBuf), chScanMaxBuf)

	return &ClickHouseSource{body: resp.Body, cancel: cancel, scanner: sc}, nil
}

// Next implements Source. Blank lines are skipped and a clean end of stream
// yields (false, nil).
func (s *ClickHouseSource) Next(ctx context.Context, ev *Event) (bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	if s == nil || s.scanner == nil {
		return false, nil
	}

	for s.scanner.Scan() {
		s.line++
		line := strings.TrimSpace(s.scanner.Text())
		if line == "" {
			continue
		}

		var row chTradeRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return false, fmt.Errorf("backtest: clickhouse line %d: %w", s.line, err)
		}
		if row.Symbol == "" {
			return false, fmt.Errorf("backtest: clickhouse line %d: symbol is empty", s.line)
		}

		*ev = Event{
			TS:     row.TSMillis,
			Symbol: row.Symbol,
			Kind:   KindTrade,
			Trade: &Trade{
				ID:    row.TradeID,
				Price: row.Price,
				Qty:   row.Quantity,
			},
		}
		return true, nil
	}
	if err := s.scanner.Err(); err != nil {
		return false, fmt.Errorf("backtest: clickhouse: %w", err)
	}
	return false, nil
}

// Close implements Source. It is nil-safe and idempotent so a source that never
// started can be closed unconditionally.
func (s *ClickHouseSource) Close() error {
	if s == nil {
		return nil
	}
	body, cancel := s.body, s.cancel
	s.body, s.cancel, s.scanner = nil, nil, nil
	if cancel != nil {
		cancel()
	}
	if body == nil {
		return nil
	}
	return body.Close()
}

// chTradeRow is the JSONEachRow shape produced by buildCHQuery.
type chTradeRow struct {
	TSMillis int64  `json:"ts_ms"`
	Symbol   string `json:"symbol"`
	TradeID  uint64 `json:"trade_id"`
	Price    int64  `json:"price"`
	Quantity int64  `json:"quantity"`
}

// buildCHQuery renders the read statement. The symbol is validated by
// validateCHSymbol before it reaches here; quoteSymbolLiteral is defense in
// depth for callers that bypass OpenClickHouseSource.
func buildCHQuery(database, table, symbol string, from, to time.Time, limit int) string {
	var b strings.Builder
	b.Grow(256)
	b.WriteString("SELECT toUnixTimestamp64Milli(ts) AS ts_ms, symbol, trade_id, price, quantity FROM ")
	b.WriteString(database)
	b.WriteString(".")
	b.WriteString(table)
	b.WriteString(" WHERE symbol = ")
	b.WriteString(quoteSymbolLiteral(symbol))
	if !from.IsZero() {
		b.WriteString(" AND ts >= ")
		b.WriteString(formatCHDateTime(from))
	}
	if !to.IsZero() {
		b.WriteString(" AND ts <= ")
		b.WriteString(formatCHDateTime(to))
	}
	b.WriteString(" ORDER BY ts, trade_id")
	if limit > 0 {
		fmt.Fprintf(&b, " LIMIT %d", limit)
	}
	b.WriteString(" FORMAT JSONEachRow")
	return b.String()
}

// formatCHDateTime renders ts as a DateTime64(3) literal in UTC.
func formatCHDateTime(ts time.Time) string {
	return "toDateTime64('" + ts.UTC().Format(chDateTimeLayout) + "', 3)"
}

// quoteSymbolLiteral escapes a symbol for use inside a single-quoted literal.
func quoteSymbolLiteral(symbol string) string {
	symbol = strings.ReplaceAll(symbol, `\`, `\\`)
	symbol = strings.ReplaceAll(symbol, `'`, `''`)
	return "'" + symbol + "'"
}

// validateCHSymbol rejects characters that could break or extend the SQL
// literal. Backslash and quote are refused outright, as are control characters.
func validateCHSymbol(symbol string) error {
	for _, r := range symbol {
		switch {
		case r == '\'' || r == '\\':
			return fmt.Errorf("backtest: clickhouse: invalid symbol %q: quote and backslash are not allowed", symbol)
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("backtest: clickhouse: invalid symbol %q: control characters are not allowed", symbol)
		}
	}
	return nil
}

// normalizeCHURL validates the endpoint and strips trailing slashes. Only plain
// http/https URLs without query or fragment are accepted.
func normalizeCHURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = chDefaultURL
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("backtest: clickhouse: invalid url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("backtest: clickhouse: invalid url %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("backtest: clickhouse: invalid url %q: host is empty", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("backtest: clickhouse: invalid url %q: query and fragment are not supported", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// encodeCHQuery escapes a statement for the query URL parameter. Spaces use %20
// rather than + so the value decodes the same regardless of form semantics.
func encodeCHQuery(statement string) string {
	return strings.ReplaceAll(url.QueryEscape(statement), "+", "%20")
}

// chStatusError turns a non-2xx response into an error with the status line and
// a bounded body snippet.
func chStatusError(resp *http.Response) error {
	snippet := chReadSnippet(resp.Body)
	if snippet == "" {
		return fmt.Errorf("status %s", resp.Status)
	}
	return fmt.Errorf("status %s: %s", resp.Status, snippet)
}

// chReadSnippet reads at most chErrSnippetLimit bytes and trims whitespace.
func chReadSnippet(body io.Reader) string {
	buf, _ := io.ReadAll(io.LimitReader(body, chErrSnippetLimit))
	return strings.TrimSpace(string(buf))
}

// chCloseBody drains a bounded amount before closing so the transport can reuse
// the underlying connection.
func chCloseBody(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, chDrainLimit))
	_ = body.Close()
}
