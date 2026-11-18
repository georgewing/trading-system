package http

// Read-only market-data and order-query endpoints consumed by the trading
// terminal. Writes still go through POST /api/v1/order.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"trading-system/internal/hft/domain"
	"trading-system/internal/orders"
	"trading-system/internal/transport/wire"
)

const (
	defaultDepthLimit  = 20
	maxDepthLimit      = 200
	defaultKlineLimit  = 300
	maxKlineLimit      = 1000
	defaultOrdersLimit = 100
	maxOrdersLimit     = 500
)

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

// depthResponse mirrors the WebSocket depth payload so both transports can
// share one client-side type.
type depthResponse struct {
	Symbol       string      `json:"symbol"`
	LastUpdateID uint64      `json:"last_update_id"`
	Bids         [][2]string `json:"bids"`
	Asks         [][2]string `json:"asks"`
	Ts           int64       `json:"ts"`
}

type marketTradeResponse struct {
	ID     uint64 `json:"id"`
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
	Qty    string `json:"qty"`
	Side   string `json:"side"`
	Ts     int64  `json:"ts"`
}

type klineResponse struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	OpenTime int64  `json:"open_time"`
	Open     string `json:"open"`
	High     string `json:"high"`
	Low      string `json:"low"`
	Close    string `json:"close"`
	Volume   string `json:"volume"`
	Closed   bool   `json:"closed"`
	Ts       int64  `json:"ts"`
}

// MakeDepthEndpoint serves GET /api/v1/market/depth.
func MakeDepthEndpoint(snap domain.Snapshotter, defaultSymbol string, pricePrecision, qtyPrecision int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if snap == nil {
			writeError(w, http.StatusServiceUnavailable, "market data unavailable")
			return
		}

		symbol := querySymbol(r, defaultSymbol)
		depth, ok := snap.Depth(symbol)
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

		resp := depthResponse{
			Symbol:       symbol,
			LastUpdateID: depth.UpdateID,
			Bids:         make([][2]string, 0, len(depth.Bids)),
			Asks:         make([][2]string, 0, len(depth.Asks)),
			Ts:           time.Now().UnixMilli(),
		}
		for _, lv := range depth.Bids {
			resp.Bids = append(resp.Bids, wire.Level(lv.Price, lv.Qty, pricePrecision, qtyPrecision))
		}
		for _, lv := range depth.Asks {
			resp.Asks = append(resp.Asks, wire.Level(lv.Price, lv.Qty, pricePrecision, qtyPrecision))
		}

		writeJSON(w, http.StatusOK, resp)
	})
}

// MakeTradesEndpoint serves GET /api/v1/market/trades.
func MakeTradesEndpoint(snap domain.Snapshotter, defaultSymbol string, pricePrecision, qtyPrecision int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if snap == nil {
			writeError(w, http.StatusServiceUnavailable, "market data unavailable")
			return
		}

		symbol := querySymbol(r, defaultSymbol)
		limit := queryLimit(r, 100, 500)

		trades := snap.Trades(symbol, limit)
		out := make([]marketTradeResponse, 0, len(trades))
		for _, t := range trades {
			side := "BUY"
			if t.BuyerIsMaker {
				side = "SELL"
			}
			out = append(out, marketTradeResponse{
				ID:     t.ID,
				Symbol: symbol,
				Price:  wire.Decimal(t.Price, pricePrecision),
				Qty:    wire.Decimal(t.Qty, qtyPrecision),
				Side:   side,
				Ts:     t.TS,
			})
		}

		writeJSON(w, http.StatusOK, map[string]any{"symbol": symbol, "trades": out})
	})
}

// MakeKlineEndpoint serves GET /api/v1/market/kline.
func MakeKlineEndpoint(snap domain.Snapshotter, defaultSymbol string, pricePrecision, qtyPrecision int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if snap == nil {
			writeError(w, http.StatusServiceUnavailable, "market data unavailable")
			return
		}

		symbol := querySymbol(r, defaultSymbol)
		interval := strings.TrimSpace(r.URL.Query().Get("interval"))
		if interval == "" {
			interval = "1m"
		}
		limit := queryLimit(r, defaultKlineLimit, maxKlineLimit)

		klines := snap.Klines(symbol, interval, limit)
		out := make([]klineResponse, 0, len(klines))
		for _, k := range klines {
			out = append(out, klineResponse{
				Symbol:   symbol,
				Interval: k.Interval,
				OpenTime: k.OpenTime,
				Open:     wire.Decimal(k.Open, pricePrecision),
				High:     wire.Decimal(k.High, pricePrecision),
				Low:      wire.Decimal(k.Low, pricePrecision),
				Close:    wire.Decimal(k.Close, pricePrecision),
				Volume:   wire.Decimal(k.Volume, qtyPrecision),
				Closed:   k.Closed,
				Ts:       k.OpenTime,
			})
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"symbol":   symbol,
			"interval": interval,
			"klines":   out,
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
