package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// fakeRiskEventLister records the parsed filter and returns a canned page.
type fakeRiskEventLister struct {
	events []RiskEventRecord
	err    error

	calls  int
	filter RiskEventFilter
}

func (f *fakeRiskEventLister) list(_ context.Context, filter RiskEventFilter) ([]RiskEventRecord, error) {
	f.calls++
	f.filter = filter
	return f.events, f.err
}

func serveRiskEvents(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func riskEventRequest(method, target string) *http.Request {
	return httptest.NewRequest(method, target, nil)
}

func TestRiskEventsMethodNotAllowed(t *testing.T) {
	fake := &fakeRiskEventLister{}
	h := MakeRiskEventsEndpoint(fake.list)

	rec := serveRiskEvents(t, h, riskEventRequest(http.MethodPost, "/api/v1/risk/events"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405: %s", rec.Code, rec.Body.String())
	}
	if fake.calls != 0 {
		t.Fatalf("lister calls = %d, want 0", fake.calls)
	}
}

func TestRiskEventsDefaultFilter(t *testing.T) {
	fake := &fakeRiskEventLister{}
	h := MakeRiskEventsEndpoint(fake.list)

	rec := serveRiskEvents(t, h, riskEventRequest(http.MethodGet, "/api/v1/risk/events"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("lister calls = %d, want 1", fake.calls)
	}

	want := RiskEventFilter{Limit: defaultRiskEventLimit}
	if fake.filter != want {
		t.Fatalf("filter = %+v, want %+v", fake.filter, want)
	}
	if !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Errorf("body = %s, want empty events array", rec.Body.String())
	}
}

func TestRiskEventsLimitClamping(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   int
	}{
		{"default", "/api/v1/risk/events", defaultRiskEventLimit},
		{"in range", "/api/v1/risk/events?limit=10", 10},
		{"zero falls back", "/api/v1/risk/events?limit=0", defaultRiskEventLimit},
		{"negative falls back", "/api/v1/risk/events?limit=-1", defaultRiskEventLimit},
		{"malformed falls back", "/api/v1/risk/events?limit=abc", defaultRiskEventLimit},
		{"at maximum", "/api/v1/risk/events?limit=500", maxRiskEventLimit},
		{"above maximum clamps", "/api/v1/risk/events?limit=5000", maxRiskEventLimit},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRiskEventLister{}
			rec := serveRiskEvents(t, MakeRiskEventsEndpoint(fake.list), riskEventRequest(http.MethodGet, tc.target))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if fake.filter.Limit != tc.want {
				t.Fatalf("limit = %d, want %d", fake.filter.Limit, tc.want)
			}
		})
	}
}

func TestRiskEventsInvalidParams(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   string
	}{
		{"account id not a number", "/api/v1/risk/events?account_id=abc", "invalid account_id"},
		{"account id negative", "/api/v1/risk/events?account_id=-1", "invalid account_id"},
		{"before id not a number", "/api/v1/risk/events?before_id=abc", "invalid before_id"},
		{"before id negative", "/api/v1/risk/events?before_id=-5", "invalid before_id"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRiskEventLister{}
			rec := serveRiskEvents(t, MakeRiskEventsEndpoint(fake.list), riskEventRequest(http.MethodGet, tc.target))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}

			var resp ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if !strings.Contains(resp.Message, tc.want) {
				t.Errorf("message = %q, want %q", resp.Message, tc.want)
			}
			if fake.calls != 0 {
				t.Errorf("lister calls = %d, want 0 for a rejected request", fake.calls)
			}
		})
	}
}

func TestRiskEventsFilterPassthrough(t *testing.T) {
	fake := &fakeRiskEventLister{}
	h := MakeRiskEventsEndpoint(fake.list)

	rec := serveRiskEvents(t, h, riskEventRequest(http.MethodGet,
		"/api/v1/risk/events?account_id=42&symbol=BTC-USDT&code=MAX_LEVERAGE&before_id=900&limit=7"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	want := RiskEventFilter{
		Limit:     7,
		AccountID: 42,
		Symbol:    "BTC-USDT",
		Code:      "MAX_LEVERAGE",
		BeforeID:  900,
	}
	if fake.filter != want {
		t.Fatalf("filter = %+v, want %+v", fake.filter, want)
	}
}

func TestRiskEventsSuccess(t *testing.T) {
	want := []RiskEventRecord{{
		ID:        2,
		TS:        1763553600000,
		AccountID: 42,
		Symbol:    "BTC-USDT",
		Strategy:  "mm",
		OrderID:   9001,
		Code:      "MAX_LEVERAGE",
		Action:    "REJECT",
		Message:   "leverage 12x > 10x",
	}}
	fake := &fakeRiskEventLister{events: want}
	h := MakeRiskEventsEndpoint(fake.list)

	rec := serveRiskEvents(t, h, riskEventRequest(http.MethodGet, "/api/v1/risk/events?account_id=42"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var resp struct {
		Events []RiskEventRecord `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !reflect.DeepEqual(resp.Events, want) {
		t.Fatalf("events = %+v, want %+v", resp.Events, want)
	}
}

func TestRiskEventsEmptyListMarshalsAsArray(t *testing.T) {
	fake := &fakeRiskEventLister{events: nil}
	h := MakeRiskEventsEndpoint(fake.list)

	rec := serveRiskEvents(t, h, riskEventRequest(http.MethodGet, "/api/v1/risk/events"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, `"events":null`) {
		t.Fatalf("body = %s, want empty array instead of null", body)
	}
	if !strings.Contains(body, `"events":[]`) {
		t.Fatalf("body = %s, want empty events array", body)
	}
}

func TestRiskEventsNilLister(t *testing.T) {
	rec := serveRiskEvents(t, MakeRiskEventsEndpoint(nil), riskEventRequest(http.MethodGet, "/api/v1/risk/events"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Fatalf("body = %s, want empty events array", rec.Body.String())
	}
}

func TestRiskEventsListerError(t *testing.T) {
	fake := &fakeRiskEventLister{
		err: errors.New(`postgres: list risk events: query failed: relation "risk_events" does not exist`),
	}
	h := MakeRiskEventsEndpoint(fake.list)

	rec := serveRiskEvents(t, h, riskEventRequest(http.MethodGet, "/api/v1/risk/events"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", resp.Code)
	}
	if !strings.Contains(resp.Message, "risk events query failed") ||
		!strings.Contains(resp.Message, "does not exist") {
		t.Errorf("message = %q, want failure context and underlying error", resp.Message)
	}
}
