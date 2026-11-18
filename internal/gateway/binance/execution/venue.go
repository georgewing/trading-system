// Package binanceexec implements a Binance Spot execution venue: signed REST
// order entry and cancel, a user data stream that emits engine.ExecReport
// values, and a pure parser for execution report frames.
//
// Defaults point at the Spot testnet. Live endpoints must be configured
// explicitly through Config.BaseURL and Config.WSURL.
//
// The package is intentionally not wired into the application. Construct a
// Venue, register it with internal/routing under VenueName, and pump the
// reports produced by RunUserStream into pipeline.EventBus.PublishReport.
package binanceexec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"trading-system/internal/execution"
	"trading-system/internal/hft/engine"
)

const (
	apiKeyHeader = "X-MBX-APIKEY"

	orderPath          = "/api/v3/order"
	timePath           = "/api/v3/time"
	userDataStreamPath = "/api/v3/userDataStream"

	// apiCodeUnknownOrder is Binance's "Unknown order sent." error code.
	apiCodeUnknownOrder = -2011
)

// Errors returned by this package. Errors produced by the venue itself always
// wrap one of these where a caller can sensibly branch on the cause.
var (
	// ErrInvalidConfig reports an unusable Config.
	ErrInvalidConfig = errors.New("binance: invalid config")
	// ErrInvalidOrder reports an order that cannot be represented on Binance.
	ErrInvalidOrder = errors.New("binance: invalid order")
	// ErrUnsupportedOrder reports an order type or time in force this venue
	// does not support.
	ErrUnsupportedOrder = errors.New("binance: unsupported order")
	// ErrInvalidClientOrderID reports a client order id outside the Binance
	// charset or length limit.
	ErrInvalidClientOrderID = errors.New("binance: invalid client order id")
	// ErrInvalidCancel reports a cancel request without a symbol or without
	// any order identifier.
	ErrInvalidCancel = errors.New("binance: invalid cancel request")
	// ErrUnknownOrder is wrapped when Binance answers -2011, for example when
	// cancelling an order that is already gone.
	ErrUnknownOrder = errors.New("binance: unknown order")
	// ErrNilReportChannel reports RunUserStream called without an output.
	ErrNilReportChannel = errors.New("binance: nil report channel")
	// ErrNotExecutionReport reports a user stream frame that is not an
	// executionReport event (account updates, subscription acks).
	ErrNotExecutionReport = errors.New("binance: not an execution report")
	// ErrListenKeyExpired reports Binance's listenKeyExpired event. The stream
	// loop uses it to refresh the listen key and reconnect.
	ErrListenKeyExpired = errors.New("binance: listen key expired")
)

// APIError is a structured error response from the Binance REST API.
type APIError struct {
	HTTPStatus int
	Code       int
	Msg        string
}

func (e *APIError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("binance: api error: http %d code %d", e.HTTPStatus, e.Code)
	}
	return fmt.Sprintf("binance: api error: http %d code %d: %s", e.HTTPStatus, e.Code, e.Msg)
}

// Unwrap exposes ErrUnknownOrder for the -2011 case so callers can branch on
// it with errors.Is.
func (e *APIError) Unwrap() error {
	if e.Code == apiCodeUnknownOrder {
		return ErrUnknownOrder
	}
	return nil
}

// Venue is a Binance Spot execution gateway. It is safe for concurrent use.
type Venue struct {
	cfg Config

	// clockOff is the estimate of serverTime-localTime in nanoseconds and
	// clockSynced records whether a sample was ever taken successfully.
	clockOff    atomic.Int64
	clockSynced atomic.Bool
	clockMu     sync.Mutex
	clockLast   time.Time

	reconnects  atomic.Uint64
	parseErrors atomic.Uint64
}

var _ execution.Venue = (*Venue)(nil)

// New validates cfg and returns a venue. It performs no network access: the
// clock offset is probed lazily on the first signed request (or eagerly by
// calling SyncClock from the application at startup).
func New(cfg Config) (*Venue, error) {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Venue{cfg: normalized}, nil
}

// Name returns VenueName.
func (v *Venue) Name() string { return VenueName }

// Reconnects returns how many established user stream sessions were lost.
func (v *Venue) Reconnects() uint64 { return v.reconnects.Load() }

// ParseErrors returns how many user stream frames were skipped because they
// could not be decoded into an execution report.
func (v *Venue) ParseErrors() uint64 { return v.parseErrors.Load() }

// SyncClock samples /api/v3/time and stores the offset applied to signed
// request timestamps. Signed requests probe lazily when this was never called;
// calling it during startup removes that latency from the first order.
func (v *Venue) SyncClock(ctx context.Context) error {
	var resp struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := v.do(ctx, http.MethodGet, timePath, nil, false, &resp); err != nil {
		return fmt.Errorf("binance: sync clock: %w", err)
	}
	if resp.ServerTime <= 0 {
		return errors.New("binance: sync clock: invalid serverTime")
	}
	v.setClockOffset(time.Duration(resp.ServerTime-time.Now().UnixMilli()) * time.Millisecond)
	return nil
}

// setClockOffset records a clock sample. Tests use it to pin the clock.
func (v *Venue) setClockOffset(d time.Duration) {
	v.clockOff.Store(int64(d))
	v.clockSynced.Store(true)
}

// ensureClock probes the server clock at most once per clockSyncRetry until a
// sample succeeds. A failed probe is not fatal: the request proceeds with the
// local clock, which is what a testnet or a partially reachable host needs.
func (v *Venue) ensureClock(ctx context.Context) {
	if v.clockSynced.Load() {
		return
	}

	v.clockMu.Lock()
	defer v.clockMu.Unlock()
	if v.clockSynced.Load() {
		return
	}
	now := time.Now()
	if !v.clockLast.IsZero() && now.Sub(v.clockLast) < clockSyncRetry {
		return
	}
	v.clockLast = now

	syncCtx, cancel := context.WithTimeout(ctx, clockSyncTimeout)
	defer cancel()
	if err := v.SyncClock(syncCtx); err != nil {
		log.Printf("binance: clock sync failed, using local time: %v", err)
	}
}

func (v *Venue) nowMillis() int64 {
	now := time.Now()
	if off := v.clockOff.Load(); off != 0 {
		now = now.Add(time.Duration(off))
	}
	return now.UnixMilli()
}

// Submit places a signed POST /api/v3/order and maps the response.
//
// The response is requested with newOrderRespType=FULL so fills[] is present;
// each fill becomes an engine.Trade. Trades are keyed by Binance trade ids and
// carry MakerOrder=0 because the REST response does not expose the maker side.
//
// OpenLeaves is the requested quantity minus the venue-executed quantity. For
// terminal CANCELED/EXPIRED statuses the unfilled remainder is also reported as
// CanceledQty, mirroring the paper venue's IOC semantics.
func (v *Venue) Submit(ctx context.Context, order engine.Order) (engine.SubmitResult, error) {
	requested, err := engine.ParseQuantity(order.Quantity, v.cfg.QuantityScale)
	if err != nil {
		return engine.SubmitResult{}, fmt.Errorf("binance: submit order %d: quantity: %w", order.ID, err)
	}
	if requested <= 0 {
		return engine.SubmitResult{}, fmt.Errorf("%w: quantity must be positive", ErrInvalidOrder)
	}

	params, err := v.orderParams(order, requested)
	if err != nil {
		return engine.SubmitResult{}, fmt.Errorf("binance: submit order %d: %w", order.ID, err)
	}

	var resp orderResponse
	if err := v.doSigned(ctx, http.MethodPost, orderPath, params, &resp); err != nil {
		return engine.SubmitResult{}, fmt.Errorf("binance: submit order %d: %w", order.ID, err)
	}

	return v.submitResult(order, requested, resp)
}

// orderParams builds the signed-order parameters. Validation happens here so a
// malformed order never reaches the venue.
func (v *Venue) orderParams(order engine.Order, requestedLots int64) (url.Values, error) {
	symbol := strings.ToUpper(strings.TrimSpace(order.Symbol))
	if symbol == "" {
		return nil, fmt.Errorf("%w: symbol is required", ErrInvalidOrder)
	}

	var side string
	switch order.Side {
	case engine.SideBuy:
		side = "BUY"
	case engine.SideSell:
		side = "SELL"
	default:
		return nil, fmt.Errorf("%w: unsupported side %d", ErrInvalidOrder, order.Side)
	}

	clientOrderID := order.ClientOrderID
	if clientOrderID == "" {
		if order.ID == 0 {
			return nil, fmt.Errorf("%w: order ID and ClientOrderID are both empty", ErrInvalidOrder)
		}
		clientOrderID = strconv.FormatUint(order.ID, 10)
	}
	if !validClientOrderID(clientOrderID) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidClientOrderID, clientOrderID)
	}

	params := url.Values{
		"symbol":           {symbol},
		"side":             {side},
		"type":             {string(order.Type)},
		"quantity":         {formatUnits(requestedLots, v.cfg.QuantityScale)},
		"newClientOrderId": {clientOrderID},
		"newOrderRespType": {"FULL"},
	}

	switch order.Type {
	case engine.OrderTypeLimit:
		if order.Price <= 0 {
			return nil, fmt.Errorf("%w: limit order requires a positive price", ErrInvalidOrder)
		}
		if order.PostOnly {
			// LIMIT_MAKER is Binance's post-only limit order; it rejects
			// instead of taking liquidity. It carries no timeInForce.
			params.Set("type", "LIMIT_MAKER")
			break
		}
		tif := order.TIF
		if tif == "" {
			tif = engine.TimeInForceGTC
		}
		switch tif {
		case engine.TimeInForceGTC, engine.TimeInForceIOC, engine.TimeInForceFOK:
		default:
			return nil, fmt.Errorf("%w: time in force %q", ErrUnsupportedOrder, tif)
		}
		params.Set("price", formatUnits(order.Price, v.cfg.PriceScale))
		params.Set("timeInForce", string(tif))
	case engine.OrderTypeMarket:
		if order.PostOnly {
			return nil, fmt.Errorf("%w: post-only requires a limit order", ErrUnsupportedOrder)
		}
		// Binance rejects price and timeInForce for MARKET orders; any TIF on
		// the engine order is intentionally dropped.
	default:
		return nil, fmt.Errorf("%w: type %q", ErrUnsupportedOrder, order.Type)
	}

	return params, nil
}

// orderResponse is the newOrderRespType=FULL response.
type orderResponse struct {
	Symbol              string         `json:"symbol"`
	OrderID             uint64         `json:"orderId"`
	ClientOrderID       string         `json:"clientOrderId"`
	TransactTime        int64          `json:"transactTime"`
	Price               string         `json:"price"`
	OrigQty             string         `json:"origQty"`
	ExecutedQty         string         `json:"executedQty"`
	CummulativeQuoteQty string         `json:"cummulativeQuoteQty"`
	Status              string         `json:"status"`
	TimeInForce         string         `json:"timeInForce"`
	Type                string         `json:"type"`
	Side                string         `json:"side"`
	Fills               []fillResponse `json:"fills"`
}

type fillResponse struct {
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	TradeID         int64  `json:"tradeId"`
}

func (v *Venue) submitResult(order engine.Order, requested int64, resp orderResponse) (engine.SubmitResult, error) {
	status, err := mapOrderStatus(resp.Status)
	if err != nil {
		return engine.SubmitResult{}, err
	}

	executed, err := parseDecimal("executedQty", resp.ExecutedQty, v.cfg.QuantityScale)
	if err != nil {
		return engine.SubmitResult{}, err
	}

	openLeaves := requested - executed
	if openLeaves < 0 {
		openLeaves = 0
	}

	result := engine.SubmitResult{
		ExecutedQty: executed,
		OpenLeaves:  openLeaves,
		FinalStatus: status,
	}
	if status == engine.OrderStatusCanceled && openLeaves > 0 {
		result.CanceledQty = openLeaves
	}

	timestamp := resp.TransactTime * int64(time.Millisecond)
	if timestamp <= 0 {
		timestamp = time.Now().UnixNano()
	}
	symbol := resp.Symbol
	if symbol == "" {
		symbol = strings.ToUpper(strings.TrimSpace(order.Symbol))
	}

	for _, fill := range resp.Fills {
		price, err := parseDecimal("fill price", fill.Price, v.cfg.PriceScale)
		if err != nil {
			return engine.SubmitResult{}, err
		}
		quantity, err := parseDecimal("fill quantity", fill.Qty, v.cfg.QuantityScale)
		if err != nil {
			return engine.SubmitResult{}, err
		}
		if quantity <= 0 {
			continue
		}

		trade := engine.Trade{
			Symbol:    symbol,
			Price:     price,
			Quantity:  quantity,
			Timestamp: timestamp,
		}
		if fill.TradeID > 0 {
			trade.ID = uint64(fill.TradeID)
		}
		if order.Side == engine.SideBuy {
			trade.BuyOrderID = order.ID
		} else {
			trade.SellOrderID = order.ID
		}
		result.Trades = append(result.Trades, trade)
	}

	return result, nil
}

// Cancel submits a signed DELETE /api/v3/order.
//
// ClientOrderID takes precedence over OrderID: it is the identifier this
// gateway sets at submit time (Submit defaults newClientOrderId to the decimal
// engine order id). When only OrderID is set it is forwarded as Binance's
// numeric orderId and must therefore already be a venue order id, not the
// engine order id. CancelRequest.Reason has no Binance equivalent and is
// ignored.
func (v *Venue) Cancel(ctx context.Context, req execution.CancelRequest) error {
	symbol := strings.ToUpper(strings.TrimSpace(req.Symbol))
	if symbol == "" {
		return fmt.Errorf("%w: symbol is required", ErrInvalidCancel)
	}

	params := url.Values{"symbol": {symbol}}
	switch {
	case req.ClientOrderID != "":
		params.Set("origClientOrderId", req.ClientOrderID)
	case req.OrderID != 0:
		params.Set("orderId", strconv.FormatUint(req.OrderID, 10))
	default:
		return fmt.Errorf("%w: OrderID or ClientOrderID is required", ErrInvalidCancel)
	}

	target := "clientID=" + req.ClientOrderID
	if req.ClientOrderID == "" {
		target = "orderId=" + strconv.FormatUint(req.OrderID, 10)
	}

	// The cancel response body is not needed; treating an empty 200 as success
	// keeps the gateway tolerant of proxies that strip the body.
	if err := v.doSigned(ctx, http.MethodDelete, orderPath, params, nil); err != nil {
		return fmt.Errorf("binance: cancel %s: %w", target, err)
	}
	return nil
}

// doSigned adds recvWindow, a clock-corrected timestamp and the HMAC-SHA256
// signature, then executes the request with the API key header.
func (v *Venue) doSigned(ctx context.Context, method, path string, params url.Values, out any) error {
	v.ensureClock(ctx)

	query := make(url.Values, len(params)+3)
	for key, values := range params {
		query[key] = append([]string(nil), values...)
	}
	query.Set("recvWindow", strconv.FormatInt(v.cfg.RecvWindow.Milliseconds(), 10))
	query.Set("timestamp", strconv.FormatInt(v.nowMillis(), 10))
	query.Set("signature", signQuery(query.Encode(), v.cfg.APISecret))

	return v.do(ctx, method, path, query, true, out)
}

// do executes one REST call. Errors never carry the query string, so the API
// key (header) and the signature (query) cannot leak into logs.
func (v *Venue) do(ctx context.Context, method, path string, params url.Values, withAPIKey bool, out any) error {
	endpoint := v.cfg.BaseURL + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return fmt.Errorf("binance: build %s %s request: %w", method, path, err)
	}
	if withAPIKey {
		req.Header.Set(apiKeyHeader, v.cfg.APIKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("binance: %s %s: %w", method, path, sanitizeTransportError(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("binance: read %s %s response: %w", method, path, err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return parseAPIError(resp.StatusCode, body)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("binance: decode %s %s response: %w", method, path, err)
	}
	return nil
}

func parseAPIError(status int, body []byte) error {
	apiErr := &APIError{HTTPStatus: status}

	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		apiErr.Code = payload.Code
		apiErr.Msg = payload.Msg
	}
	return apiErr
}

// sanitizeTransportError strips the request URL from *url.Error so a signed
// query string is never embedded in an error message.
func sanitizeTransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

func signQuery(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// parseDecimal converts a Binance decimal string into minimal units. An empty
// string means zero. It reuses the engine parser so price and quantity
// conversion share one decimal implementation and the same reject-never-round
// policy.
func parseDecimal(field, raw string, scale int64) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := engine.ParseQuantity(raw, scale)
	if err != nil {
		return 0, fmt.Errorf("binance: %s %q: %w", field, raw, err)
	}
	return value, nil
}

// mapOrderStatus maps a Binance order status onto the engine enum. Binance's
// EXPIRED and EXPIRED_IN_MATCH have no engine counterpart and are reported as
// Canceled; callers that need the venue wording keep the raw frame.
func mapOrderStatus(status string) (engine.OrderStatus, error) {
	switch status {
	case "NEW":
		return engine.OrderStatusNew, nil
	case "PARTIALLY_FILLED":
		return engine.OrderStatusPartiallyFilled, nil
	case "FILLED":
		return engine.OrderStatusFilled, nil
	case "CANCELED", "EXPIRED", "EXPIRED_IN_MATCH":
		return engine.OrderStatusCanceled, nil
	case "REJECTED":
		return engine.OrderStatusRejected, nil
	default:
		return 0, fmt.Errorf("binance: unsupported order status %q", status)
	}
}
