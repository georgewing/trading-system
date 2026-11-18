package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	_ DB   = (*sql.DB)(nil)
	_ DB   = (*fakeDB)(nil)
	_ Rows = (*sql.Rows)(nil)
)

type execCall struct {
	query string
	args  []any
}

// fakeDB records ExecContext and QueryContext calls and returns configurable
// errors. It never talks to a real database.
type fakeDB struct {
	mu sync.Mutex

	calls      []execCall
	queryCalls []execCall

	queryRows [][]driver.Value
	queryErr  error
	queryDB   *sql.DB

	execErr    error
	pingErr    error
	closeErr   error
	pingCalls  int
	closeCalls int
}

func (f *fakeDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, execCall{
		query: query,
		args:  append([]any(nil), args...),
	})
	if f.execErr != nil {
		return nil, f.execErr
	}
	return driver.ResultNoRows, nil
}

// QueryRowContext is part of the narrow DB interface but Writer does not issue
// single-row read queries yet, so the fake returns nil.
func (f *fakeDB) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

// QueryContext serves the configured rows through a real *sql.Rows built on an
// in-memory driver, so LoadPositions is exercised end to end.
func (f *fakeDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
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
			columns: positionColumns,
			rows:    f.queryRows,
		}})
	}
	db := f.queryDB
	f.mu.Unlock()

	if queryErr != nil {
		return nil, queryErr
	}
	return db.QueryContext(ctx, query, args...)
}

func (f *fakeDB) PingContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.pingCalls++
	return f.pingErr
}

func (f *fakeDB) Close() error {
	f.mu.Lock()
	db := f.queryDB
	f.queryDB = nil
	f.closeCalls++
	err := f.closeErr
	f.mu.Unlock()

	if db != nil {
		_ = db.Close()
	}
	return err
}

func (f *fakeDB) execCalls() []execCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]execCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeDB) queries() []execCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]execCall, len(f.queryCalls))
	copy(out, f.queryCalls)
	return out
}

func (f *fakeDB) pingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingCalls
}

func (f *fakeDB) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls
}

func TestOpenRejectsEmptyDSN(t *testing.T) {
	for _, dsn := range []string{"", "   "} {
		if _, err := Open(context.Background(), dsn); !errors.Is(err, ErrEmptyDSN) {
			t.Fatalf("Open(%q) error = %v, want ErrEmptyDSN", dsn, err)
		}
	}
}

func TestNilWriter(t *testing.T) {
	var w *Writer
	ctx := context.Background()

	checks := []struct {
		name string
		call func() error
	}{
		{"Close", func() error { return w.Close() }},
		{"Ping", func() error { return w.Ping(ctx) }},
		{"UpsertOrder", func() error { return w.UpsertOrder(ctx, Order{}) }},
		{"InsertTrade", func() error { return w.InsertTrade(ctx, Trade{}) }},
		{"InsertExecEvent", func() error { return w.InsertExecEvent(ctx, ExecEvent{}) }},
		{"UpsertPosition", func() error { return w.UpsertPosition(ctx, Position{}) }},
		{"UpsertPositions", func() error { return w.UpsertPositions(ctx, nil) }},
		{"LoadPositions", func() error { _, err := w.LoadPositions(ctx); return err }},
	}

	for _, check := range checks {
		if err := check.call(); !errors.Is(err, ErrNilWriter) {
			t.Errorf("%s() error = %v, want ErrNilWriter", check.name, err)
		}
	}
}

func TestNilDB(t *testing.T) {
	w := NewWriter(nil)
	ctx := context.Background()

	checks := []struct {
		name string
		call func() error
	}{
		{"Ping", func() error { return w.Ping(ctx) }},
		{"UpsertOrder", func() error { return w.UpsertOrder(ctx, Order{}) }},
		{"InsertTrade", func() error { return w.InsertTrade(ctx, Trade{}) }},
		{"InsertExecEvent", func() error { return w.InsertExecEvent(ctx, ExecEvent{}) }},
		{"UpsertPosition", func() error { return w.UpsertPosition(ctx, Position{}) }},
		{"UpsertPositions", func() error { return w.UpsertPositions(ctx, nil) }},
		{"LoadPositions", func() error { _, err := w.LoadPositions(ctx); return err }},
		{"Close", func() error { return w.Close() }},
	}

	for _, check := range checks {
		if err := check.call(); !errors.Is(err, ErrNilDB) {
			t.Errorf("%s() error = %v, want ErrNilDB", check.name, err)
		}
	}
}

func TestClosedWriter(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := db.closeCount(); got != 1 {
		t.Fatalf("db.Close called %d times, want 1", got)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}
	if got := db.closeCount(); got != 1 {
		t.Fatalf("db.Close called %d times after second Close, want 1", got)
	}

	ctx := context.Background()
	checks := []struct {
		name string
		call func() error
	}{
		{"Ping", func() error { return w.Ping(ctx) }},
		{"UpsertOrder", func() error { return w.UpsertOrder(ctx, Order{}) }},
		{"InsertTrade", func() error { return w.InsertTrade(ctx, Trade{}) }},
		{"InsertExecEvent", func() error { return w.InsertExecEvent(ctx, ExecEvent{}) }},
		{"UpsertPosition", func() error { return w.UpsertPosition(ctx, Position{}) }},
		{"UpsertPositions", func() error { return w.UpsertPositions(ctx, nil) }},
		{"LoadPositions", func() error { _, err := w.LoadPositions(ctx); return err }},
	}

	for _, check := range checks {
		if err := check.call(); !errors.Is(err, ErrWriterClosed) {
			t.Errorf("%s() error = %v, want ErrWriterClosed", check.name, err)
		}
	}
}

func TestPing(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		db := &fakeDB{}
		w := NewWriter(db)
		if err := w.Ping(context.Background()); err != nil {
			t.Fatalf("Ping() error = %v", err)
		}
		if got := db.pingCount(); got != 1 {
			t.Fatalf("ping count = %d, want 1", got)
		}
	})

	t.Run("error", func(t *testing.T) {
		sentinel := errors.New("ping failed")
		db := &fakeDB{pingErr: sentinel}
		w := NewWriter(db)
		if err := w.Ping(context.Background()); !errors.Is(err, sentinel) {
			t.Fatalf("Ping() error = %v, want wrapped %v", err, sentinel)
		}
	})
}

func TestUpsertOrder(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)

	createdAt := time.Date(2026, 11, 19, 9, 30, 0, 0, time.UTC)
	updatedAt := createdAt.Add(2 * time.Second)
	order := Order{
		ID:            5001,
		ClientOrderID: "cid-1",
		AccountID:     42,
		Symbol:        "BTC-USDT",
		Side:          1,
		OrderType:     "LIMIT",
		TimeInForce:   "GTC",
		Price:         1234500,
		Quantity:      "1.500000000000000000",
		Leaves:        100,
		Filled:        0,
		Status:        1,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}

	if err := w.UpsertOrder(context.Background(), order); err != nil {
		t.Fatalf("UpsertOrder() error = %v", err)
	}

	call := singleCall(t, db)
	requireContains(t, call.query,
		"INSERT INTO orders",
		"client_order_id",
		"account_id",
		"symbol",
		"side",
		"order_type",
		"time_in_force",
		"price",
		"quantity",
		"leaves",
		"filled",
		"status",
		"created_at",
		"updated_at",
		"$14",
		"ON CONFLICT (id) DO UPDATE",
	)
	requireNotContains(t, call.query,
		"BTC-USDT",
		"cid-1",
		"1.500000000000000000",
		"created_at = EXCLUDED.created_at",
	)

	assertArgs(t, call.args, []any{
		uint64(5001),
		"cid-1",
		uint64(42),
		"BTC-USDT",
		int8(1),
		"LIMIT",
		"GTC",
		int64(1234500),
		"1.500000000000000000",
		int64(100),
		int64(0),
		int8(1),
		createdAt,
		updatedAt,
	})
}

func TestInsertTrade(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)

	tradedAt := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	trade := Trade{
		ID:           9001,
		Symbol:       "BTC-USDT",
		Price:        1234500,
		Quantity:     10,
		BuyOrderID:   5001,
		SellOrderID:  5002,
		MakerOrderID: 5001,
		TradedAt:     tradedAt,
	}

	if err := w.InsertTrade(context.Background(), trade); err != nil {
		t.Fatalf("InsertTrade() error = %v", err)
	}

	call := singleCall(t, db)
	requireContains(t, call.query,
		"INSERT INTO trades",
		"symbol",
		"price",
		"quantity",
		"buy_order_id",
		"sell_order_id",
		"maker_order_id",
		"traded_at",
		"$8",
		"ON CONFLICT (id) DO NOTHING",
	)
	requireNotContains(t, call.query, "BTC-USDT", "1234500", "9001")

	assertArgs(t, call.args, []any{
		uint64(9001),
		"BTC-USDT",
		int64(1234500),
		int64(10),
		uint64(5001),
		uint64(5002),
		uint64(5001),
		tradedAt,
	})
}

func TestInsertTradeNullMaker(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)

	trade := Trade{
		ID:           9002,
		Symbol:       "ETH-USDT",
		Price:        200000,
		Quantity:     3,
		BuyOrderID:   6001,
		SellOrderID:  6002,
		MakerOrderID: 0,
		TradedAt:     time.Date(2026, 11, 19, 10, 1, 0, 0, time.UTC),
	}
	if err := w.InsertTrade(context.Background(), trade); err != nil {
		t.Fatalf("InsertTrade() error = %v", err)
	}

	call := singleCall(t, db)
	if got := call.args[6]; got != nil {
		t.Fatalf("args[6] = %#v, want nil for zero maker order ID", got)
	}
}

func TestInsertExecEvent(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)

	reportedAt := time.Date(2026, 11, 19, 10, 30, 0, 123456000, time.UTC)
	event := ExecEvent{
		ExecID:        101,
		Seq:           7,
		OrderID:       5001,
		ClientOrderID: "cid-1",
		AccountID:     42,
		Symbol:        "BTC-USDT",
		Side:          -1,
		ExecType:      "TRADE",
		Status:        2,
		LastPx:        1234500,
		LastQty:       10,
		CumQty:        10,
		LeavesQty:     90,
		Liquidity:     "TAKER",
		TradeID:       9001,
		Reason:        "",
		Venue:         "BINANCE",
		ReportedAt:    reportedAt,
	}

	if err := w.InsertExecEvent(context.Background(), event); err != nil {
		t.Fatalf("InsertExecEvent() error = %v", err)
	}

	call := singleCall(t, db)
	requireContains(t, call.query,
		"INSERT INTO exec_events",
		"exec_id",
		"seq",
		"order_id",
		"client_order_id",
		"account_id",
		"symbol",
		"side",
		"exec_type",
		"status",
		"last_px",
		"last_qty",
		"cum_qty",
		"leaves_qty",
		"liquidity",
		"trade_id",
		"reason",
		"venue",
		"reported_at",
		"$18",
		"ON CONFLICT (exec_id) DO NOTHING",
	)
	requireNotContains(t, call.query, "BTC-USDT", "BINANCE", "cid-1", "TAKER")

	assertArgs(t, call.args, []any{
		uint64(101),
		uint64(7),
		uint64(5001),
		"cid-1",
		uint64(42),
		"BTC-USDT",
		int8(-1),
		"TRADE",
		int8(2),
		int64(1234500),
		int64(10),
		int64(10),
		int64(90),
		"TAKER",
		uint64(9001),
		"",
		"BINANCE",
		reportedAt,
	})
}

func TestInsertExecEventNullTradeID(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)

	event := ExecEvent{
		ExecID:     102,
		Seq:        1,
		OrderID:    5001,
		AccountID:  42,
		Symbol:     "BTC-USDT",
		Side:       1,
		ExecType:   "NEW",
		Status:     1,
		Liquidity:  "MAKER",
		TradeID:    0,
		ReportedAt: time.Date(2026, 11, 19, 10, 31, 0, 0, time.UTC),
	}
	if err := w.InsertExecEvent(context.Background(), event); err != nil {
		t.Fatalf("InsertExecEvent() error = %v", err)
	}

	call := singleCall(t, db)
	if got := call.args[14]; got != nil {
		t.Fatalf("args[14] = %#v, want nil for zero trade ID", got)
	}
}

func TestExecErrorPropagation(t *testing.T) {
	sentinel := errors.New("write failed")
	ctx := context.Background()

	checks := []struct {
		name string
		call func(*Writer) error
	}{
		{"UpsertOrder", func(w *Writer) error { return w.UpsertOrder(ctx, Order{}) }},
		{"InsertTrade", func(w *Writer) error { return w.InsertTrade(ctx, Trade{}) }},
		{"InsertExecEvent", func(w *Writer) error { return w.InsertExecEvent(ctx, ExecEvent{}) }},
		{"UpsertPosition", func(w *Writer) error { return w.UpsertPosition(ctx, Position{}) }},
		{"UpsertPositions", func(w *Writer) error {
			return w.UpsertPositions(ctx, []Position{{AccountID: 42, Symbol: "BTC-USDT"}})
		}},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			w := NewWriter(&fakeDB{execErr: sentinel})
			if err := check.call(w); !errors.Is(err, sentinel) {
				t.Fatalf("%s() error = %v, want wrapped %v", check.name, err, sentinel)
			}
		})
	}
}

func TestContextCanceled(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := w.Ping(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Ping() error = %v, want context.Canceled", err)
	}
	if err := w.UpsertOrder(ctx, Order{}); !errors.Is(err, context.Canceled) {
		t.Errorf("UpsertOrder() error = %v, want context.Canceled", err)
	}
	if err := w.InsertTrade(ctx, Trade{}); !errors.Is(err, context.Canceled) {
		t.Errorf("InsertTrade() error = %v, want context.Canceled", err)
	}
	if err := w.InsertExecEvent(ctx, ExecEvent{}); !errors.Is(err, context.Canceled) {
		t.Errorf("InsertExecEvent() error = %v, want context.Canceled", err)
	}
	if err := w.UpsertPosition(ctx, Position{}); !errors.Is(err, context.Canceled) {
		t.Errorf("UpsertPosition() error = %v, want context.Canceled", err)
	}
	if err := w.UpsertPositions(ctx, []Position{{AccountID: 42, Symbol: "BTC-USDT"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("UpsertPositions() error = %v, want context.Canceled", err)
	}
	if _, err := w.LoadPositions(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("LoadPositions() error = %v, want context.Canceled", err)
	}
}

func TestConcurrentInserts(t *testing.T) {
	const (
		goroutines   = 16
		perGoroutine = 25
	)

	db := &fakeDB{}
	w := NewWriter(db)
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				event := ExecEvent{
					ExecID:     uint64(g*perGoroutine + i + 1),
					Seq:        uint64(i + 1),
					OrderID:    5001,
					AccountID:  42,
					Symbol:     "BTC-USDT",
					Side:       1,
					ExecType:   "NEW",
					Status:     1,
					LastPx:     1234500,
					Liquidity:  "MAKER",
					ReportedAt: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC),
				}
				if err := w.InsertExecEvent(ctx, event); err != nil {
					t.Errorf("InsertExecEvent() error = %v", err)
				}
			}
		}(g)
	}
	wg.Wait()

	if got, want := len(db.execCalls()), goroutines*perGoroutine; got != want {
		t.Fatalf("exec calls = %d, want %d", got, want)
	}
}

func TestUpsertPosition(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)

	updatedAt := time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC)
	position := Position{
		AccountID: 42,
		Symbol:    "BTC-USDT",
		Qty:       -3,
		AvgCost:   1234500,
		Mark:      1235000,
		Realized:  -1200,
		UpdatedAt: updatedAt,
	}

	if err := w.UpsertPosition(context.Background(), position); err != nil {
		t.Fatalf("UpsertPosition() error = %v", err)
	}

	call := singleCall(t, db)
	requireContains(t, call.query,
		"INSERT INTO positions",
		"account_id",
		"symbol",
		"qty",
		"avg_cost",
		"mark",
		"realized",
		"updated_at",
		"$7",
		"ON CONFLICT (account_id, symbol) DO UPDATE",
	)
	requireNotContains(t, call.query, "BTC-USDT", "1234500", "42")

	assertArgs(t, call.args, []any{
		uint64(42),
		"BTC-USDT",
		int64(-3),
		int64(1234500),
		int64(1235000),
		int64(-1200),
		updatedAt,
	})
}

func TestUpsertPositions(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)

	updatedAt := time.Date(2026, 11, 19, 11, 5, 0, 0, time.UTC)
	batch := []Position{
		{AccountID: 42, Symbol: "BTC-USDT", Qty: 10, AvgCost: 100, Mark: 101, Realized: 5, UpdatedAt: updatedAt},
		{AccountID: 43, Symbol: "ETH-USDT", Qty: -2, AvgCost: 200, Mark: 198, Realized: -4, UpdatedAt: updatedAt.Add(time.Second)},
	}

	if err := w.UpsertPositions(context.Background(), batch); err != nil {
		t.Fatalf("UpsertPositions() error = %v", err)
	}

	calls := db.execCalls()
	if len(calls) != len(batch) {
		t.Fatalf("exec calls = %d, want %d", len(calls), len(batch))
	}
	for i, call := range calls {
		requireContains(t, call.query, "INSERT INTO positions", "ON CONFLICT (account_id, symbol) DO UPDATE")
		assertArgs(t, call.args, []any{
			batch[i].AccountID,
			batch[i].Symbol,
			batch[i].Qty,
			batch[i].AvgCost,
			batch[i].Mark,
			batch[i].Realized,
			batch[i].UpdatedAt,
		})
	}
}

func TestUpsertPositionsEmpty(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db)

	if err := w.UpsertPositions(context.Background(), nil); err != nil {
		t.Fatalf("UpsertPositions(nil) error = %v", err)
	}
	if got := len(db.execCalls()); got != 0 {
		t.Fatalf("exec calls = %d, want 0", got)
	}
}

func TestUpsertPositionsReportsFailingEntry(t *testing.T) {
	sentinel := errors.New("write failed")
	w := NewWriter(&fakeDB{execErr: sentinel})

	err := w.UpsertPositions(context.Background(), []Position{{AccountID: 42, Symbol: "BTC-USDT"}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("UpsertPositions() error = %v, want wrapped %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "positions[0]") || !strings.Contains(err.Error(), "BTC-USDT") {
		t.Fatalf("UpsertPositions() error = %q, want failing index and key", err)
	}
}

func TestLoadPositions(t *testing.T) {
	updatedAt := time.Date(2026, 11, 19, 11, 10, 0, 0, time.UTC)
	db := &fakeDB{queryRows: [][]driver.Value{
		{int64(42), "BTC-USDT", int64(10), int64(100), int64(101), int64(5), updatedAt},
		{int64(43), "ETH-USDT", int64(-2), int64(200), int64(198), int64(-4), updatedAt.Add(time.Second)},
	}}
	w := NewWriter(db)

	got, err := w.LoadPositions(context.Background())
	if err != nil {
		t.Fatalf("LoadPositions() error = %v", err)
	}

	want := []Position{
		{AccountID: 42, Symbol: "BTC-USDT", Qty: 10, AvgCost: 100, Mark: 101, Realized: 5, UpdatedAt: updatedAt},
		{AccountID: 43, Symbol: "ETH-USDT", Qty: -2, AvgCost: 200, Mark: 198, Realized: -4, UpdatedAt: updatedAt.Add(time.Second)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadPositions() = %+v, want %+v", got, want)
	}

	queries := db.queries()
	if len(queries) != 1 {
		t.Fatalf("query calls = %d, want 1", len(queries))
	}
	requireContains(t, queries[0].query,
		"FROM positions",
		"account_id",
		"symbol",
		"qty",
		"avg_cost",
		"mark",
		"realized",
		"updated_at",
		"ORDER BY account_id, symbol",
	)
}

func TestLoadPositionsEmpty(t *testing.T) {
	w := NewWriter(&fakeDB{})

	got, err := w.LoadPositions(context.Background())
	if err != nil {
		t.Fatalf("LoadPositions() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadPositions() = %+v, want empty", got)
	}
}

func TestLoadPositionsQueryError(t *testing.T) {
	sentinel := errors.New("query failed")
	w := NewWriter(&fakeDB{queryErr: sentinel})

	if _, err := w.LoadPositions(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("LoadPositions() error = %v, want wrapped %v", err, sentinel)
	}
}

func TestScanPositionsErrors(t *testing.T) {
	scanErr := errors.New("scan failed")
	if _, err := scanPositions(&fakeRows{remaining: 1, scanErr: scanErr}); !errors.Is(err, scanErr) {
		t.Fatalf("scanPositions() scan error = %v, want wrapped %v", err, scanErr)
	}

	iterErr := errors.New("iteration failed")
	if _, err := scanPositions(&fakeRows{remaining: 1, iterErr: iterErr}); !errors.Is(err, iterErr) {
		t.Fatalf("scanPositions() iteration error = %v, want wrapped %v", err, iterErr)
	}
}

// fakeRows implements the narrow Rows interface for scan and iteration errors.
// It does not assign destination values; value mapping is covered by
// TestLoadPositions through a real *sql.Rows.
type fakeRows struct {
	remaining int
	scanErr   error
	iterErr   error
}

func (r *fakeRows) Next() bool {
	if r.remaining <= 0 {
		return false
	}
	r.remaining--
	return true
}

func (r *fakeRows) Scan(...any) error { return r.scanErr }
func (r *fakeRows) Err() error        { return r.iterErr }
func (r *fakeRows) Close() error      { return nil }

var positionColumns = []string{"account_id", "symbol", "qty", "avg_cost", "mark", "realized", "updated_at"}

// staticDriver serves a fixed result set through database/sql. It is the only
// way to produce a real *sql.Rows without a database or a mock dependency.
type staticDriver struct {
	columns []string
	rows    [][]driver.Value
}

func (d *staticDriver) Open(string) (driver.Conn, error) {
	return &staticConn{driver: d}, nil
}

type staticConnector struct{ driver *staticDriver }

func (c staticConnector) Connect(context.Context) (driver.Conn, error) {
	return &staticConn{driver: c.driver}, nil
}

func (c staticConnector) Driver() driver.Driver { return c.driver }

type staticConn struct{ driver *staticDriver }

func (c *staticConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("static driver: Prepare unsupported")
}

func (c *staticConn) Close() error { return nil }

func (c *staticConn) Begin() (driver.Tx, error) {
	return nil, errors.New("static driver: Begin unsupported")
}

func (c *staticConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &staticRows{columns: c.driver.columns, rows: c.driver.rows}, nil
}

type staticRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
}

func (r *staticRows) Columns() []string { return r.columns }
func (r *staticRows) Close() error      { return nil }

func (r *staticRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

func singleCall(t *testing.T, db *fakeDB) execCall {
	t.Helper()

	calls := db.execCalls()
	if len(calls) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(calls))
	}
	return calls[0]
}

func requireContains(t *testing.T, s string, substrings ...string) {
	t.Helper()

	for _, sub := range substrings {
		if !strings.Contains(s, sub) {
			t.Errorf("query does not contain %q:\n%s", sub, s)
		}
	}
}

func requireNotContains(t *testing.T, s string, substrings ...string) {
	t.Helper()

	for _, sub := range substrings {
		if strings.Contains(s, sub) {
			t.Errorf("query unexpectedly contains %q:\n%s", sub, s)
		}
	}
}

func assertArgs(t *testing.T, got, want []any) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("len(args) = %d, want %d (args: %#v)", len(got), len(want), got)
	}
	for i := range want {
		if !argEqual(got[i], want[i]) {
			t.Errorf("args[%d] = %#v (%T), want %#v (%T)", i, got[i], got[i], want[i], want[i])
		}
	}
}

func argEqual(got, want any) bool {
	if wantTime, ok := want.(time.Time); ok {
		gotTime, ok := got.(time.Time)
		return ok && gotTime.Equal(wantTime)
	}
	return reflect.DeepEqual(got, want)
}
