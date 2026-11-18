package http

// Historical candle endpoint backed by durable storage (ClickHouse).
//
// It never falls back to the in-memory snapshot: a client that asked for
// history must get either stored candles or an explicit 503, otherwise a
// short live buffer would look like complete history.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"trading-system/internal/hft/domain"
)

// historyKlineInterval is the only interval the kline_1m view can answer.
const historyKlineInterval = "1m"

// KlineHistoryReader is the read port for stored candles. Times are unix
// milliseconds and a non-positive bound means "unbounded".
type KlineHistoryReader interface {
	QueryKlines(ctx context.Context, symbol string, fromMillis, toMillis int64, limit int) ([]domain.Kline, error)
}

// MakeKlineHistoryEndpoint serves
// GET /api/v1/market/kline/history?symbol=&from=&to=&limit=.
//
// from/to are inclusive unix milliseconds; omitting either leaves that side of
// the range open. The response has the same shape as /api/v1/market/kline.
// A nil reader (ClickHouse disabled) or a failing query returns 503 so the
// caller cannot mistake a short in-memory buffer for history.
func MakeKlineHistoryEndpoint(reader KlineHistoryReader, defaultSymbol string, pricePrecision, qtyPrecision int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if reader == nil {
			writeError(w, http.StatusServiceUnavailable, "kline history unavailable: clickhouse is not enabled")
			return
		}

		if interval := strings.TrimSpace(r.URL.Query().Get("interval")); interval != "" && interval != historyKlineInterval {
			writeError(w, http.StatusBadRequest, "kline history only supports interval=1m")
			return
		}

		symbol := querySymbol(r, defaultSymbol)

		from, err := queryMillis(r, "from")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		to, err := queryMillis(r, "to")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if from > 0 && to > 0 && from > to {
			writeError(w, http.StatusBadRequest, "invalid time range: from is after to")
			return
		}

		limit := queryLimit(r, defaultKlineLimit, maxKlineLimit)

		klines, err := reader.QueryKlines(r.Context(), symbol, from, to, limit)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "kline history query failed: "+err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"symbol":   symbol,
			"interval": historyKlineInterval,
			"klines":   klineResponses(klines, symbol, pricePrecision, qtyPrecision),
		})
	})
}

// queryMillis parses an optional unix-millisecond query parameter. An empty
// value means the bound is open and returns zero.
func queryMillis(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid %s: want unix milliseconds", name)
	}
	return v, nil
}
