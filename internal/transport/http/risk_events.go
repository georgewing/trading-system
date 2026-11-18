package http

// Risk event endpoint: exposes the persisted risk audit trail as JSON.
//
// The handler owns its wire type so the storage package can evolve without
// changing the API shape. TS is emitted as unix milliseconds, matching the risk
// event that produced the row.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultRiskEventLimit = 50
	maxRiskEventLimit     = 500
)

// RiskEventRecord is the wire form of one risk event.
type RiskEventRecord struct {
	ID        int64  `json:"id"`
	TS        int64  `json:"ts"`
	AccountID uint64 `json:"account_id"`
	Symbol    string `json:"symbol"`
	Strategy  string `json:"strategy"`
	OrderID   uint64 `json:"order_id"`
	Code      string `json:"code"`
	Action    string `json:"action"`
	Message   string `json:"message"`
}

// RiskEventFilter is the parsed query. Zero values mean "no filter":
// AccountID 0 and BeforeID 0 match every row, empty Symbol and Code match every
// row.
type RiskEventFilter struct {
	Limit     int
	AccountID uint64
	Symbol    string
	Code      string
	BeforeID  int64
}

// RiskEventLister reads one page of risk events, newest first. A nil lister
// returns an empty page instead of an error.
type RiskEventLister func(ctx context.Context, f RiskEventFilter) ([]RiskEventRecord, error)

// MakeRiskEventsEndpoint serves GET /api/v1/risk/events.
//
// Query parameters: limit (default 50, max 500), account_id, symbol, code and
// before_id (exclusive upper bound for paging). An invalid account_id or
// before_id is rejected with 400 before the lister is called.
func MakeRiskEventsEndpoint(list RiskEventLister) http.Handler {
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

		beforeID, err := queryBeforeID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid before_id")
			return
		}

		filter := RiskEventFilter{
			Limit:     queryLimit(r, defaultRiskEventLimit, maxRiskEventLimit),
			AccountID: accountID,
			Symbol:    strings.TrimSpace(r.URL.Query().Get("symbol")),
			Code:      strings.TrimSpace(r.URL.Query().Get("code")),
			BeforeID:  beforeID,
		}

		events := []RiskEventRecord{}
		if list != nil {
			page, err := list(r.Context(), filter)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "risk events query failed: "+err.Error())
				return
			}
			events = append(events, page...)
		}

		writeJSON(w, http.StatusOK, map[string]any{"events": events})
	})
}

// queryBeforeID parses the exclusive paging bound. An empty value means "start
// from the newest row".
func queryBeforeID(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("before_id"))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("invalid before_id %q", raw)
	}
	return id, nil
}
