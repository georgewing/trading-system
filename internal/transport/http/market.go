package http

// Read-only market-data and order-query endpoints consumed by the trading
// terminal. Writes still go through POST /api/v1/order.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"trading-system/internal/market"
	"trading-system/internal/orders"
)

const (
	defaultDepthLimit  = 20
	maxDepthLimit      = 200
	defaultKlineLimit  = 300
	maxKlineLimit      = 1000
	defaultOrdersLimit = 100
	maxOrdersLimit     = 500
)

// MarketReader is the snapshot surface backed by market.Store.
type MarketReader interface {
	Depth(symbol string) (market.Depth, bool)
	Trades(symbol string, limit int) []market.Trade
	Klines(symbol, interval string, limit int) []market.Kline
}

// OrderReader is the query surface backed by orders.Book.
type OrderReader interface {
	Get(orderID uint64) (orders.Record, bool)
	Recent(limit int) []orders.Record
}

// MetaResponse describes the instrument the gateway is configured for.
// The terminal reads it before opening the WebSocket so it can build topic
// names and convert prices without guessing.
type MetaResponse struct {
	Symbol            string `json:"symbol"`
	PricePrecision    int    `json:"price_precision"`
	QuantityPrecision int    `json:"quantity_precision"`
}

// MakeMetaEndpoint serves GET /api/v1/meta.
func MakeMetaEndpoint(symbol string, pricePrecision, quantityPrecision int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, MetaResponse{
			Symbol:            symbol,
			PricePrecision:    pricePrecision,
			QuantityPrecision: quantityPrecision,
		})
	})
}

// MakeDepthEndpoint serves GET /api/v1/market/depth.
func MakeDepthEndpoint(feed MarketReader, defaultSymbol string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if feed == nil {
			writeError(w, http.StatusServiceUnavailable, "market data unavailable")
			return
		}

		symbol := querySymbol(r, defaultSymbol)
		depth, ok := feed.Depth(symbol)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "market data not ready for "+symbol)
			return
		}

		limit := queryLimit(r, defaultDepthLimit, maxDepthLimit)
		if len(depth.Bids) > limit {
			depth.Bids = depth.Bids[:limit]
		}
		if len(depth.Asks) > limit {
			depth.Asks = depth.Asks[:limit]
		}

		writeJSON(w, http.StatusOK, depth)
	})
}

// MakeTradesEndpoint serves GET /api/v1/market/trades.
func MakeTradesEndpoint(feed MarketReader, defaultSymbol string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if feed == nil {
			writeError(w, http.StatusServiceUnavailable, "market data unavailable")
			return
		}

		symbol := querySymbol(r, defaultSymbol)
		limit := queryLimit(r, 100, 500)

		writeJSON(w, http.StatusOK, map[string]any{
			"symbol": symbol,
			"trades": feed.Trades(symbol, limit),
		})
	})
}

// MakeKlineEndpoint serves GET /api/v1/market/kline.
func MakeKlineEndpoint(feed MarketReader, defaultSymbol string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if feed == nil {
			writeError(w, http.StatusServiceUnavailable, "market data unavailable")
			return
		}

		symbol := querySymbol(r, defaultSymbol)
		interval := strings.TrimSpace(r.URL.Query().Get("interval"))
		if interval == "" {
			interval = "1m"
		}
		limit := queryLimit(r, defaultKlineLimit, maxKlineLimit)

		writeJSON(w, http.StatusOK, map[string]any{
			"symbol":   symbol,
			"interval": interval,
			"klines":   feed.Klines(symbol, interval, limit),
		})
	})
}

// MakeOrdersEndpoint serves GET /api/v1/orders and GET /api/v1/orders/{id}.
func MakeOrdersEndpoint(book OrderReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if book == nil {
			writeError(w, http.StatusServiceUnavailable, "order book unavailable")
			return
		}

		// /api/v1/orders/{order_id}
		if rawID := strings.TrimPrefix(r.URL.Path, "/api/v1/orders/"); rawID != "" && rawID != r.URL.Path {
			id, err := strconv.ParseUint(rawID, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid order id")
				return
			}
			rec, ok := book.Get(id)
			if !ok {
				writeError(w, http.StatusNotFound, "order not found")
				return
			}
			writeJSON(w, http.StatusOK, orderDTOFromRecord(rec))
			return
		}

		limit := queryLimit(r, defaultOrdersLimit, maxOrdersLimit)
		records := book.Recent(limit)

		out := make([]orderDTO, 0, len(records))
		for _, rec := range records {
			out = append(out, orderDTOFromRecord(rec))
		}

		writeJSON(w, http.StatusOK, map[string]any{"orders": out})
	})
}

// orderDTO is the wire form of an order. OrderID is a string because 64-bit
// snowflake ids do not survive a round-trip through a JS number.
type orderDTO struct {
	OrderID       string `json:"order_id"`
	ClientOrderID string `json:"client_order_id,omitempty"`
	AccountID     uint64 `json:"account_id"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	TIF           string `json:"time_in_force"`
	Price         int64  `json:"price"`
	Quantity      string `json:"quantity"`
	Status        string `json:"status"`
	Filled        int64  `json:"filled"`
	Leaves        int64  `json:"leaves"`
	Reason        string `json:"reason,omitempty"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

func orderDTOFromRecord(rec orders.Record) orderDTO {
	return orderDTO{
		OrderID:       strconv.FormatUint(rec.OrderID, 10),
		ClientOrderID: rec.ClientOrderID,
		AccountID:     rec.AccountID,
		Symbol:        rec.Symbol,
		Side:          orders.SideName(rec.Side),
		Type:          string(rec.Type),
		TIF:           string(rec.TIF),
		Price:         rec.Price,
		Quantity:      rec.Quantity,
		Status:        orders.StatusName(rec.Status),
		Filled:        rec.Filled,
		Leaves:        rec.Leaves,
		Reason:        rec.Reason,
		CreatedAt:     rec.CreatedAt,
		UpdatedAt:     rec.UpdatedAt,
	}
}

func querySymbol(r *http.Request, fallback string) string {
	if s := strings.TrimSpace(r.URL.Query().Get("symbol")); s != "" {
		return s
	}
	return fallback
}

func queryLimit(r *http.Request, def, max int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
