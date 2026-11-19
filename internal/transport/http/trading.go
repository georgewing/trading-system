package http

// Portfolio and order-lifecycle endpoints: positions, account summary and
// cancel. They read the in-memory ledgers maintained by the risk stage.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"trading-system/internal/hft/engine"
	"trading-system/internal/hft/risk"
	"trading-system/internal/transport/wire"
)

// PositionReader is the read surface backed by risk.Positions.
type PositionReader interface {
	List(accountID uint64) []risk.PositionSnapshot
	Realized(accountID uint64) int64
	Unrealized(accountID uint64) int64
	OpenPositions(accountID uint64) int
	ClosedTrades() (wins, total int)
}

// CancelRequester cancels a resting order. Implemented by *pipeline.EventBus.
type CancelRequester interface {
	CancelOrder(ctx context.Context, orderID uint64, reason string) (engine.ExecReport, error)
}

type positionDTO struct {
	AccountID  uint64 `json:"account_id"`
	Symbol     string `json:"symbol"`
	Qty        string `json:"qty"`
	AvgCost    string `json:"avg_cost"`
	Mark       string `json:"mark"`
	Realized   string `json:"realized"`
	Unrealized string `json:"unrealized"`
}

type accountDTO struct {
	AccountID     uint64 `json:"account_id"`
	Enabled       bool   `json:"enabled"`
	Realized      string `json:"realized"`
	Unrealized    string `json:"unrealized"`
	Equity        string `json:"equity"`
	OpenPositions int    `json:"open_positions"`
	ClosedTrades  int    `json:"closed_trades"`
	Wins          int    `json:"wins"`
}

// MakePositionsEndpoint serves GET /api/v1/positions.
//
// account_id=0 (the default) returns every account. Prices are decimal strings
// at the configured price precision; quantities at the quantity precision.
// realized/unrealized are notional amounts, i.e. price ticks times lots, so
// their precision is the sum of both.
func MakePositionsEndpoint(
	reader PositionReader,
	enabled bool,
	pricePrecision, qtyPrecision int,
) http.Handler {
	notionalPrecision := pricePrecision + qtyPrecision

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		accountID, err := queryAccountID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid account_id")
			return
		}

		out := make([]positionDTO, 0, 8)
		if reader != nil {
			for _, p := range reader.List(accountID) {
				out = append(out, positionDTO{
					AccountID:  p.AccountID,
					Symbol:     p.Symbol,
					Qty:        wire.Decimal(p.Qty, qtyPrecision),
					AvgCost:    wire.Decimal(p.AvgCost, pricePrecision),
					Mark:       wire.Decimal(p.Mark, pricePrecision),
					Realized:   wire.Decimal(p.Realized, notionalPrecision),
					Unrealized: wire.Decimal(p.Unrealized, notionalPrecision),
				})
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":   enabled,
			"positions": out,
		})
	})
}

// MakeAccountEndpoint serves GET /api/v1/account.
//
// The summary is derived purely from the position ledger: realized plus
// unrealized PnL. It deliberately does not model cash, margin or an initial
// balance; those need a product decision before they can be reported honestly.
func MakeAccountEndpoint(
	reader PositionReader,
	enabled bool,
	pricePrecision, qtyPrecision int,
) http.Handler {
	notionalPrecision := pricePrecision + qtyPrecision

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		accountID, err := queryAccountID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid account_id")
			return
		}

		dto := accountDTO{AccountID: accountID, Enabled: enabled}
		if reader != nil {
			realized := reader.Realized(accountID)
			unrealized := reader.Unrealized(accountID)
			wins, closed := reader.ClosedTrades()

			dto.Realized = wire.Decimal(realized, notionalPrecision)
			dto.Unrealized = wire.Decimal(unrealized, notionalPrecision)
			dto.Equity = wire.Decimal(realized+unrealized, notionalPrecision)
			dto.OpenPositions = reader.OpenPositions(accountID)
			dto.ClosedTrades = closed
			dto.Wins = wins
		} else {
			dto.Realized = wire.Decimal(0, notionalPrecision)
			dto.Unrealized = wire.Decimal(0, notionalPrecision)
			dto.Equity = wire.Decimal(0, notionalPrecision)
		}

		writeJSON(w, http.StatusOK, dto)
	})
}

type cancelRequest struct {
	OrderID string `json:"order_id"`
	Reason  string `json:"reason"`
}

// MakeCancelEndpoint serves POST /api/v1/order/cancel.
func MakeCancelEndpoint(requester CancelRequester) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if requester == nil {
			writeError(w, http.StatusServiceUnavailable, "matching bus unavailable")
			return
		}

		var req cancelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}

		orderID, err := strconv.ParseUint(strings.TrimSpace(req.OrderID), 10, 64)
		if err != nil || orderID == 0 {
			writeError(w, http.StatusBadRequest, "invalid order_id")
			return
		}

		report, err := requester.CancelOrder(r.Context(), orderID, req.Reason)
		if err != nil {
			status, msg := cancelErrorStatus(err)
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "CANCELED",
			"order_id": strconv.FormatUint(report.OrderID, 10),
			"filled":   report.Filled,
		})
	})
}

// maxBatchCancelOrders caps one batch request so a single caller cannot
// monopolize the matcher queue with unbounded work.
const maxBatchCancelOrders = 100

type batchCancelRequest struct {
	OrderIDs []string `json:"order_ids"`
	Reason   string   `json:"reason"`
}

type batchCancelSuccessDTO struct {
	OrderID string `json:"order_id"`
	Filled  int64  `json:"filled"`
}

type batchCancelFailureDTO struct {
	OrderID string `json:"order_id"`
	Error   string `json:"error"`
}

type batchCancelResponse struct {
	Canceled []batchCancelSuccessDTO `json:"canceled"`
	Failed   []batchCancelFailureDTO `json:"failed"`
}

// MakeBatchCancelEndpoint serves POST /api/v1/order/cancel/batch.
//
// Orders are canceled one by one in request order. A batch that cancels some
// orders but not others still returns 200: per-order failures go into
// "failed", because the caller cannot roll back cancels that already reached
// the matcher. Only a malformed request (bad JSON, empty or oversized
// order_ids) is rejected as a whole.
func MakeBatchCancelEndpoint(requester CancelRequester) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if requester == nil {
			writeError(w, http.StatusServiceUnavailable, "matching bus unavailable")
			return
		}

		var req batchCancelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if len(req.OrderIDs) == 0 {
			writeError(w, http.StatusBadRequest, "order_ids is empty")
			return
		}
		if len(req.OrderIDs) > maxBatchCancelOrders {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"order_ids exceeds %d entries", maxBatchCancelOrders))
			return
		}

		resp := batchCancelResponse{
			Canceled: make([]batchCancelSuccessDTO, 0, len(req.OrderIDs)),
			Failed:   make([]batchCancelFailureDTO, 0),
		}

		for _, raw := range req.OrderIDs {
			orderID, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
			if err != nil || orderID == 0 {
				resp.Failed = append(resp.Failed, batchCancelFailureDTO{
					OrderID: strings.TrimSpace(raw),
					Error:   "invalid order id",
				})
				continue
			}

			report, err := requester.CancelOrder(r.Context(), orderID, req.Reason)
			if err != nil {
				_, msg := cancelErrorStatus(err)
				resp.Failed = append(resp.Failed, batchCancelFailureDTO{
					OrderID: strconv.FormatUint(orderID, 10),
					Error:   msg,
				})
				continue
			}

			resp.Canceled = append(resp.Canceled, batchCancelSuccessDTO{
				OrderID: strconv.FormatUint(report.OrderID, 10),
				Filled:  report.Filled,
			})
		}

		writeJSON(w, http.StatusOK, resp)
	})
}

// cancelErrorStatus maps a cancel failure onto the shared HTTP status and
// message used by both the single and batch endpoints.
func cancelErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, engine.ErrOrderNotFound):
		return http.StatusNotFound, "order not found"
	case errors.Is(err, engine.ErrOrderNotCancelable):
		return http.StatusConflict, "order is not cancelable"
	default:
		return http.StatusServiceUnavailable, "cancel failed: " + err.Error()
	}
}

func queryAccountID(r *http.Request) (uint64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("account_id"))
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseUint(raw, 10, 64)
}
