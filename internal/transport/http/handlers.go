package http

// HTTP Handler 工厂（Match 撮合端点）

import (
	"encoding/json"
	"net/http"
	"time"

	"trading-system/internal/engine"
	"trading-system/internal/observability"
)

// 请求/响应结构体（OpenAPI 友好）
type MatchRequest struct {
	OrderID     string  `json:"order_id" validate:"required"`
	Side        string  `json:"side" validate:"required,oneof=BUY SELL"`
	Price       float64 `json:"price" validate:"required,gt=0"`
	Quantity    string  `json:"quantity" validate:"required"`
	Type        string  `json:"type" validate:"required,oneof=LIMIT MARKET"`
	TimeInForce string  `json:"time_in_force" validate:"required,oneof=FOK GTC IOC"`
	// 未来扩展：ClientOrderID、AccountID、PostOnly 等
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

// MakeMatchEndpoint 创建撮合端点 Handler
func MakeMatchEndpoint(matcher *engine.Matcher, metrics *observability.Metrics) http.Handler {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			if metrics != nil {
				metrics.ObserveMatchLatency("http.match", time.Since(start))
			}
		}()

		// 解析请求
		var req MatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}

		// 校验
		if req.OrderID == "" || (req.Side != "BUY" && req.Side != "SELL") {
			writeError(w, http.StatusBadRequest, "invalid_params", "invalid side or order_id")
			return
		}

		// 构造领域 Order
		order := engine.Order{
			ID:          req.OrderID,
			Side:        req.Side,
			Price:       req.Price,
			Quantity:    req.Quantity,
			Type:        req.Type,
			TimeInForce: req.TimeInForce,
		}

		// 执行撮合
		trades, err := matcher.SubmitOrder(r.Context(), order)
		if err != nil {
			// 生产中可根据错误类型映射不同 HTTP 码
			code := http.StatusInternalServerError
			if err.Error() == "FOK: cannot fully fill" {
				code = http.StatusUnprocessableEntity
			}
			writeError(w, code, "match_failed", err.Error())
			return
		}

		// 计算已成交数量
		var executed int64
		for _, t := range trades {
			executed += t.Qty
		}

		// 返回响应
		resp := MatchResponse{
			Trades:     trades,
			Remaining:  0, // SubmitOrder 内部已处理挂单，剩余 qty 不在这里返回（可扩展）
			OrderID:    req.OrderID,
			ExecuteQty: executed,
			Timestamp:  time.Now().UnixNano(),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)

		// TODO: 生产中在这里异步推送 WS / ClickHouse
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{
		Code:    code,
		Message: msg,
	})
}
