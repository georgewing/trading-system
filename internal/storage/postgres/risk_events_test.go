package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var _ DB = (*riskEventDB)(nil)

// riskEventDB reuses fakeDB's exec recording and static result-set driver, but
// serves a nine-column result set: fakeDB.QueryContext is pinned to the seven
// position columns, which would make Scan of a risk event row fail.
type riskEventDB struct {
	*fakeDB
	rows [][]driver.Value
}

func newRiskEventDB(rows [][]driver.Value) *riskEventDB {
	return &riskEventDB{fakeDB: &fakeDB{}, rows: rows}
}

func (f *riskEventDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.queryCalls = append(f.queryCalls, execCall{
		query: query,
		args:  append([]any(nil), args...),
	})
	queryErr := f.queryErr
	if f.queryDB == nil {
		f.queryDB = sql.OpenDB(staticConnector{driver: &staticDriver{
			columns: riskEventColumns,
			rows:    f.rows,
		}})
	}
	db := f.queryDB
	f.mu.Unlock()

	if queryErr != nil {
		return nil, queryErr
	}
	return db.QueryContext(ctx, query, args...)
}

var riskEventColumns = []string{
	"id", "ts", "account_id", "symbol", "strategy", "order_id", "code", "action", "message",
}

func riskEventFixture() (RiskEvent, RiskEvent) {
	first := RiskEvent{
		ID:        1,
		TS:        time.Date(2026, 11, 19, 11, 59, 0, 0, time.UTC),
		AccountID: 42,
		Symbol:    "BTC-USDT",
		Strategy:  "mm",
		OrderID:   9001,
		Code:      "MAX_LEVERAGE",
		Action:    "REJECT",
		Message:   "leverage 12x > 10x",
	}
	second := RiskEvent{
		ID:        2,
		TS:        time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
		AccountID: 7,
		Symbol:    "ETH-USDT",
		Strategy:  "arb",
		Code:      "DAILY_LOSS",
		Action:    "HALT",
		Message:   "daily loss limit hit",
	}
	return first, second
}

func TestInsertRiskEvent(t *testing.T) {
	first, _ := riskEventFixture()
	db := &fakeDB{}
	w := NewWriter(db)

	if err := w.InsertRiskEvent(context.Background(), first); err != nil {
		t.Fatalf("InsertRiskEvent() error = %v", err)
	}

	call := singleCall(t, db)
	requireContains(t, call.query,
		"INSERT INTO risk_events",
		"ts",
		"account_id",
		"symbol",
		"strategy",
		"order_id",
		"code",
		"action",
		"message",
		"VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
	)
	requireNotContains(t, call.query, "ON CONFLICT")

	assertArgs(t, call.args, []any{
		first.TS,
		first.AccountID,
		first.Symbol,
		first.Strategy,
		first.OrderID,
		first.Code,
		first.Action,
		first.Message,
	})
}

func TestInsertRiskEventError(t *testing.T) {
	sentinel := errors.New("write failed")
	w := NewWriter(&fakeDB{execErr: sentinel})

	err := w.InsertRiskEvent(context.Background(), RiskEvent{Code: "MAX_LEVERAGE", Action: "REJECT"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InsertRiskEvent() error = %v, want wrapped %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "insert risk event") {
		t.Fatalf("InsertRiskEvent() error = %q, want insert context", err)
	}
}

func TestRiskEventNilWriter(t *testing.T) {
	var w *Writer

	if err := w.InsertRiskEvent(context.Background(), RiskEvent{}); !errors.Is(err, ErrNilWriter) {
		t.Fatalf("InsertRiskEvent() error = %v, want ErrNilWriter", err)
	}
	if _, err := w.ListRiskEvents(context.Background(), RiskEventQuery{}); !errors.Is(err, ErrNilWriter) {
		t.Fatalf("ListRiskEvents() error = %v, want ErrNilWriter", err)
	}
}

func TestRiskEventNilDB(t *testing.T) {
	w := NewWriter(nil)

	if err := w.InsertRiskEvent(context.Background(), RiskEvent{}); !errors.Is(err, ErrNilDB) {
		t.Fatalf("InsertRiskEvent() error = %v, want ErrNilDB", err)
	}
	if _, err := w.ListRiskEvents(context.Background(), RiskEventQuery{}); !errors.Is(err, ErrNilDB) {
		t.Fatalf("ListRiskEvents() error = %v, want ErrNilDB", err)
	}
}

func TestRiskEventClosedWriter(t *testing.T) {
	w := NewWriter(&fakeDB{})
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := w.InsertRiskEvent(context.Background(), RiskEvent{}); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("InsertRiskEvent() error = %v, want ErrWriterClosed", err)
	}
	if _, err := w.ListRiskEvents(context.Background(), RiskEventQuery{}); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("ListRiskEvents() error = %v, want ErrWriterClosed", err)
	}
}

func TestListRiskEvents(t *testing.T) {
	first, second := riskEventFixture()
	db := newRiskEventDB([][]driver.Value{
		{second.ID, second.TS, int64(second.AccountID), second.Symbol, second.Strategy, int64(second.OrderID), second.Code, second.Action, second.Message},
		{first.ID, first.TS, int64(first.AccountID), first.Symbol, first.Strategy, int64(first.OrderID), first.Code, first.Action, first.Message},
	})
	w := NewWriter(db)

	got, err := w.ListRiskEvents(context.Background(), RiskEventQuery{})
	if err != nil {
		t.Fatalf("ListRiskEvents() error = %v", err)
	}

	want := []RiskEvent{second, first}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRiskEvents() = %+v, want %+v", got, want)
	}

	queries := db.queries()
	if len(queries) != 1 {
		t.Fatalf("query calls = %d, want 1", len(queries))
	}
	requireContains(t, queries[0].query,
		"FROM risk_events",
		"($1 = 0 OR account_id = $1)",
		"($2 = '' OR symbol = $2)",
		"($3 = '' OR code = $3)",
		"($4 = 0 OR id < $4)",
		"ORDER BY id DESC",
		"LIMIT $5",
	)
	assertArgs(t, queries[0].args, []any{uint64(0), "", "", int64(0), defaultRiskEventLimit})
}

func TestListRiskEventsFilters(t *testing.T) {
	db := newRiskEventDB(nil)
	w := NewWriter(db)

	_, err := w.ListRiskEvents(context.Background(), RiskEventQuery{
		Limit:     7,
		AccountID: 42,
		Symbol:    "BTC-USDT",
		Code:      "MAX_LEVERAGE",
		BeforeID:  900,
	})
	if err != nil {
		t.Fatalf("ListRiskEvents() error = %v", err)
	}

	queries := db.queries()
	if len(queries) != 1 {
		t.Fatalf("query calls = %d, want 1", len(queries))
	}
	assertArgs(t, queries[0].args, []any{uint64(42), "BTC-USDT", "MAX_LEVERAGE", int64(900), 7})
}

func TestListRiskEventsClampsLimit(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero falls back to default", 0, defaultRiskEventLimit},
		{"negative falls back to default", -3, defaultRiskEventLimit},
		{"in range is preserved", 25, 25},
		{"at maximum is preserved", maxRiskEventLimit, maxRiskEventLimit},
		{"above maximum is clamped", maxRiskEventLimit + 1, maxRiskEventLimit},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newRiskEventDB(nil)
			w := NewWriter(db)

			if _, err := w.ListRiskEvents(context.Background(), RiskEventQuery{Limit: tc.limit}); err != nil {
				t.Fatalf("ListRiskEvents() error = %v", err)
			}

			queries := db.queries()
			if len(queries) != 1 {
				t.Fatalf("query calls = %d, want 1", len(queries))
			}
			if got, ok := queries[0].args[4].(int); !ok || got != tc.want {
				t.Fatalf("limit arg = %#v, want %d", queries[0].args[4], tc.want)
			}
		})
	}
}

func TestListRiskEventsEmpty(t *testing.T) {
	w := NewWriter(newRiskEventDB(nil))

	got, err := w.ListRiskEvents(context.Background(), RiskEventQuery{})
	if err != nil {
		t.Fatalf("ListRiskEvents() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListRiskEvents() = %+v, want empty", got)
	}
}

func TestListRiskEventsQueryError(t *testing.T) {
	sentinel := errors.New("query failed")
	db := newRiskEventDB(nil)
	db.queryErr = sentinel
	w := NewWriter(db)

	_, err := w.ListRiskEvents(context.Background(), RiskEventQuery{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("ListRiskEvents() error = %v, want wrapped %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "list risk events") {
		t.Fatalf("ListRiskEvents() error = %q, want list context", err)
	}
}

func TestScanRiskEventsErrors(t *testing.T) {
	scanErr := errors.New("scan failed")
	if _, err := scanRiskEvents(&fakeRows{remaining: 1, scanErr: scanErr}); !errors.Is(err, scanErr) {
		t.Fatalf("scanRiskEvents() scan error = %v, want wrapped %v", err, scanErr)
	}

	iterErr := errors.New("iteration failed")
	if _, err := scanRiskEvents(&fakeRows{remaining: 1, iterErr: iterErr}); !errors.Is(err, iterErr) {
		t.Fatalf("scanRiskEvents() iteration error = %v, want wrapped %v", err, iterErr)
	}
}
