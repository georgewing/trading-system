package http

// HTTP Handler 工厂（Match 撮合端点）

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
	"trading-system/internal/pipeline"
	"trading-system/pkg/idgen"

	"trading-system/internal/hft/engine"
)

// 请求/响应结构体（OpenAPI 友好）
type MatchRequest struct {
	OrderID string `json:"order_id" validate:"required"`
	// AccountID / Symbol 可选：risk 未开启时可省略，Symbol 为空时回退到端点默认交易对。
	AccountID   uint64  `json:"account_id"`
	Strategy    string  `json:"strategy"`
	Symbol      string  `json:"symbol"`
	Side        string  `json:"side" validate:"required,oneof=BUY SELL"`
	Price       float64 `json:"price" validate:"required,gt=0"`
	Quantity    string  `json:"quantity" validate:"required"`
	Type        string  `json:"type" validate:"required,oneof=LIMIT MARKET"`
	TimeInForce string  `json:"time_in_force" validate:"required,oneof=FOK GTC IOC"`
}

type MatchResponse struct {
	Trades     []engine.Trade `json:"trades"`
	Remaining  int64          `json:"remaining_qty"` // 挂单剩余数量（0 表示全部成交）
	OrderID    string         `json:"order_id"`
	ExecuteQty int64          `json:"execute_qty"`
	Timestamp  int64          `json:"timestamp"`
}

// ErrorResponse
type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Option customizes the match endpoint without breaking existing callers.
type Option func(*endpointConfig)

type endpointConfig struct {
	onOrder func(engine.Order)
}

// WithOrderObserver installs a callback invoked after an order is accepted for
// processing. The trading terminal uses it to build its order read model.
func WithOrderObserver(fn func(engine.Order)) Option {
	return func(c *endpointConfig) { c.onOrder = fn }
}

// MakeMatchEndpoint 创建撮合端点 Handler。
// defaultSymbol 用于请求未携带 symbol 的旧客户端（空字符串表示不填充）。
func MakeMatchEndpoint(bus *pipeline.EventBus, defaultSymbol string, opts ...Option) http.Handler {
	var cfg endpointConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	if bus == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusInternalServerError, "matching bus unavailable")
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		_ = start // TODO: metrics.ObserveMatchLatency("http.match", time.Since(start))

		// 解析请求
		var req MatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}

		// 校验
		if req.Side != "BUY" && req.Side != "SELL" {
			writeError(w, http.StatusBadRequest, "invalid side")
			return
		}

		if req.Type != "LIMIT" && req.Type != "MARKET" {
			writeError(w, http.StatusBadRequest, "invalid type")
			return
		}

		if req.TimeInForce != "GTC" && req.TimeInForce != "IOC" && req.TimeInForce != "FOK" {
			writeError(w, http.StatusBadRequest, "invalid time_in_force")
			return
		}

		if req.Price <= 0 {
			writeError(w, http.StatusBadRequest, "invalid price")
			return
		}

		if req.Quantity == "" {
			writeError(w, http.StatusBadRequest, "invalid quantity")
			return
		}

		// 将 float64 Price 转换为 int64（调用方应传入已乘以精度的整数值，
		// 或在此处乘以精度——取决于 API 契约，当前简化取整）
		// 当前引擎 pricePrecision=1，先禁止小数，避免 silent truncation
		if req.Price != math.Trunc(req.Price) {
			writeError(w, http.StatusBadRequest, "price must be integer ticks")
			return
		}
		priceInt := int64(req.Price)
		if priceInt <= 0 {
			writeError(w, http.StatusBadRequest, "invalid price")
			return
		}

		orderID := idgen.NextID()

		// 构造领域 Order；account_id 不做强制校验，由 risk guard 在开启时拒绝。
		order, err := newOrderFromRequest(req, defaultSymbol, orderID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		cmd := pipeline.OrderCommand{
			Order:    order,
			ResultCh: nil,
		}

		if err := bus.SubmitWithContext(r.Context(), cmd); err != nil {
			writeError(w, http.StatusServiceUnavailable, "exchange overload")
			return
		}

		if cfg.onOrder != nil {
			cfg.onOrder(order)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted) // 使用 202 Accepted 表示已接收请求但尚未处理完成

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":     "accepted",
			"order_id":   strconv.FormatUint(orderID, 10),
			"client_oid": req.OrderID,
			"msg":        "Order received, awaiting execution",
		})

		// TODO: 生产中在这里异步推送 WS / ClickHouse
	})
}

// newOrderFromRequest maps an already validated MatchRequest into the engine
// order. An empty Symbol falls back to defaultSymbol. Until multi-symbol routing
// is wired, a symbol different from defaultSymbol is rejected to prevent orders
// from hitting the wrong order book.
func newOrderFromRequest(req MatchRequest, defaultSymbol string, orderID uint64) (engine.Order, error) {
	symbol := req.Symbol
	if symbol == "" {
		symbol = defaultSymbol
	}
	if symbol == "" {
		return engine.Order{}, fmt.Errorf("symbol is required")
	}
	if defaultSymbol != "" && symbol != defaultSymbol {
		return engine.Order{}, fmt.Errorf("unsupported symbol %q, expected %q", symbol, defaultSymbol)
	}

	side := engine.SideSell
	if req.Side == "BUY" {
		side = engine.SideBuy
	}

	return engine.Order{
		ID:            orderID,
		ClientOrderID: req.OrderID,
		AccountID:     req.AccountID,
		Strategy:      req.Strategy,
		Symbol:        symbol,
		Side:          side,
		Type:          engine.OrderType(req.Type),
		TIF:           engine.TimeInForce(req.TimeInForce),
		Price:         int64(req.Price),
		Quantity:      req.Quantity,
	}, nil
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{
		Code:    status,
		Message: msg,
	})
}
