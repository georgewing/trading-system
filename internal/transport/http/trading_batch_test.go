package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"trading-system/internal/hft/engine"
)

// stubCancelRequester records the ids it was asked to cancel and answers each
// one from fn, which keeps the batch tests free of a real matcher.
type stubCancelRequester struct {
	fn      func(orderID uint64) (engine.ExecReport, error)
	calls   []uint64
	reasons []string
}

func (s *stubCancelRequester) CancelOrder(
	_ context.Context,
	orderID uint64,
	reason string,
) (engine.ExecReport, error) {
	s.calls = append(s.calls, orderID)
	s.reasons = append(s.reasons, reason)
	if s.fn != nil {
		return s.fn(orderID)
	}
	return engine.ExecReport{OrderID: orderID}, nil
}

func postBatchCancel(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/order/cancel/batch", strings.NewReader(body))
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeBatchCancel(t *testing.T, rec *httptest.ResponseRecorder) batchCancelResponse {
	t.Helper()
	var resp batchCancelResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode batch cancel response: %v (body %s)", err, rec.Body.String())
	}
	return resp
}

func TestBatchCancelAllSucceed(t *testing.T) {
	stub := &stubCancelRequester{
		fn: func(orderID uint64) (engine.ExecReport, error) {
			return engine.ExecReport{OrderID: orderID, Filled: 3}, nil
		},
	}

	rec := postBatchCancel(t, MakeBatchCancelEndpoint(stub),
		`{"order_ids":["123"," 456 "],"reason":"batch"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := decodeBatchCancel(t, rec)
	if len(resp.Canceled) != 2 {
		t.Fatalf("canceled = %+v, want 2 entries", resp.Canceled)
	}
	if len(resp.Failed) != 0 {
		t.Fatalf("failed = %+v, want 0 entries", resp.Failed)
	}
	if resp.Canceled[0].OrderID != "123" || resp.Canceled[0].Filled != 3 {
		t.Fatalf("canceled[0] = %+v, want 123/3", resp.Canceled[0])
	}
	if resp.Canceled[1].OrderID != "456" || resp.Canceled[1].Filled != 3 {
		t.Fatalf("canceled[1] = %+v, want 456/3", resp.Canceled[1])
	}
	if len(stub.calls) != 2 || stub.calls[0] != 123 || stub.calls[1] != 456 {
		t.Fatalf("cancel calls = %v, want [123 456]", stub.calls)
	}
	for _, reason := range stub.reasons {
		if reason != "batch" {
			t.Fatalf("reason = %q, want batch", reason)
		}
	}
}

// TestBatchCancelPartialFailureReturnsOK pins the core semantic: a batch that
// cancels at least one order but fails on another is still a 200 with per-order
// results, because the successful cancels cannot be rolled back.
func TestBatchCancelPartialFailureReturnsOK(t *testing.T) {
	stub := &stubCancelRequester{
		fn: func(orderID uint64) (engine.ExecReport, error) {
			switch orderID {
			case 123:
				return engine.ExecReport{OrderID: orderID, Filled: 7}, nil
			case 456:
				return engine.ExecReport{}, engine.ErrOrderNotFound
			default:
				return engine.ExecReport{}, engine.ErrOrderNotCancelable
			}
		},
	}

	rec := postBatchCancel(t, MakeBatchCancelEndpoint(stub),
		`{"order_ids":["123","456","789"],"reason":"batch"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d for a partially successful batch (body %s)",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := decodeBatchCancel(t, rec)
	if len(resp.Canceled) != 1 {
		t.Fatalf("canceled = %+v, want 1 entry", resp.Canceled)
	}
	if resp.Canceled[0].OrderID != "123" || resp.Canceled[0].Filled != 7 {
		t.Fatalf("canceled[0] = %+v, want 123/7", resp.Canceled[0])
	}

	if len(resp.Failed) != 2 {
		t.Fatalf("failed = %+v, want 2 entries", resp.Failed)
	}
	if resp.Failed[0].OrderID != "456" || resp.Failed[0].Error != "order not found" {
		t.Fatalf("failed[0] = %+v, want 456/order not found", resp.Failed[0])
	}
	if resp.Failed[1].OrderID != "789" || resp.Failed[1].Error != "order is not cancelable" {
		t.Fatalf("failed[1] = %+v, want 789/order is not cancelable", resp.Failed[1])
	}
	if len(stub.calls) != 3 {
		t.Fatalf("cancel calls = %v, want 3 entries", stub.calls)
	}
}

// Even when every order fails the request is well formed, so the response is a
// 200 with an empty canceled list.
func TestBatchCancelAllFailReturnsOK(t *testing.T) {
	stub := &stubCancelRequester{
		fn: func(uint64) (engine.ExecReport, error) {
			return engine.ExecReport{}, engine.ErrOrderNotFound
		},
	}

	rec := postBatchCancel(t, MakeBatchCancelEndpoint(stub), `{"order_ids":["1","2"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := decodeBatchCancel(t, rec)
	if len(resp.Canceled) != 0 || len(resp.Failed) != 2 {
		t.Fatalf("canceled/failed = %d/%d, want 0/2", len(resp.Canceled), len(resp.Failed))
	}
	if !strings.Contains(rec.Body.String(), `"canceled":[]`) {
		t.Fatalf("body = %s, want an empty canceled array rather than null", rec.Body.String())
	}
}

func TestBatchCancelEmptyList(t *testing.T) {
	stub := &stubCancelRequester{}

	for _, body := range []string{`{"order_ids":[],"reason":"batch"}`, `{}`} {
		rec := postBatchCancel(t, MakeBatchCancelEndpoint(stub), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
	}
	if len(stub.calls) != 0 {
		t.Fatalf("cancel calls = %v, want none", stub.calls)
	}
}

func TestBatchCancelTooManyOrders(t *testing.T) {
	ids := make([]string, maxBatchCancelOrders+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("%d", i+1)
	}
	body, err := json.Marshal(batchCancelRequest{OrderIDs: ids, Reason: "batch"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	stub := &stubCancelRequester{}
	rec := postBatchCancel(t, MakeBatchCancelEndpoint(stub), string(body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d for %d ids", rec.Code, http.StatusBadRequest, len(ids))
	}
	if len(stub.calls) != 0 {
		t.Fatalf("cancel calls = %d, want none when the batch is rejected", len(stub.calls))
	}

	// Exactly the limit is allowed.
	atLimit := ids[:maxBatchCancelOrders]
	body, err = json.Marshal(batchCancelRequest{OrderIDs: atLimit})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if rec := postBatchCancel(t, MakeBatchCancelEndpoint(stub), string(body)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d at the limit", rec.Code, http.StatusOK)
	}
	if len(stub.calls) != maxBatchCancelOrders {
		t.Fatalf("cancel calls = %d, want %d", len(stub.calls), maxBatchCancelOrders)
	}
}

func TestBatchCancelInvalidIDIsPerOrderFailure(t *testing.T) {
	stub := &stubCancelRequester{
		fn: func(orderID uint64) (engine.ExecReport, error) {
			return engine.ExecReport{OrderID: orderID}, nil
		},
	}

	rec := postBatchCancel(t, MakeBatchCancelEndpoint(stub),
		`{"order_ids":["abc","0","12",""]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (invalid ids must not fail the batch)", rec.Code, http.StatusOK)
	}

	resp := decodeBatchCancel(t, rec)
	if len(resp.Canceled) != 1 || resp.Canceled[0].OrderID != "12" {
		t.Fatalf("canceled = %+v, want only 12", resp.Canceled)
	}
	if len(resp.Failed) != 3 {
		t.Fatalf("failed = %+v, want 3 entries", resp.Failed)
	}
	for _, failure := range resp.Failed {
		if failure.Error != "invalid order id" {
			t.Fatalf("failed error = %q, want invalid order id", failure.Error)
		}
	}
	if len(stub.calls) != 1 || stub.calls[0] != 12 {
		t.Fatalf("cancel calls = %v, want [12]", stub.calls)
	}
}

func TestBatchCancelMethodNotAllowed(t *testing.T) {
	stub := &stubCancelRequester{}
	handler := MakeBatchCancelEndpoint(stub)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/order/cancel/batch", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
	if len(stub.calls) != 0 {
		t.Fatalf("cancel calls = %v, want none", stub.calls)
	}
}

func TestBatchCancelRequesterUnavailable(t *testing.T) {
	rec := postBatchCancel(t, MakeBatchCancelEndpoint(nil), `{"order_ids":["1"]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestBatchCancelInvalidJSON(t *testing.T) {
	stub := &stubCancelRequester{}
	rec := postBatchCancel(t, MakeBatchCancelEndpoint(stub), `{"order_ids":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("cancel calls = %v, want none", stub.calls)
	}
}

// A transport error on one order must not abort the remaining cancels.
func TestBatchCancelKeepsGoingAfterUnexpectedError(t *testing.T) {
	boom := errors.New("bus closed")
	stub := &stubCancelRequester{
		fn: func(orderID uint64) (engine.ExecReport, error) {
			if orderID == 1 {
				return engine.ExecReport{}, boom
			}
			return engine.ExecReport{OrderID: orderID}, nil
		},
	}

	rec := postBatchCancel(t, MakeBatchCancelEndpoint(stub), `{"order_ids":["1","2"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	resp := decodeBatchCancel(t, rec)
	if len(resp.Canceled) != 1 || resp.Canceled[0].OrderID != "2" {
		t.Fatalf("canceled = %+v, want only 2", resp.Canceled)
	}
	if len(resp.Failed) != 1 || !strings.Contains(resp.Failed[0].Error, "bus closed") {
		t.Fatalf("failed = %+v, want one entry mentioning bus closed", resp.Failed)
	}
}
