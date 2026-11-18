package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	_ DB = (*sql.DB)(nil)
	_ DB = (*fakeDB)(nil)
)

type execCall struct {
	query string
	args  []any
}

// fakeDB records ExecContext calls and returns configurable errors. It never
// talks to a real database.
type fakeDB struct {
	mu sync.Mutex

	calls []execCall

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
// read queries yet, so the fake returns nil.
func (f *fakeDB) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
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
	defer f.mu.Unlock()

	f.closeCalls++
	return f.closeErr
}

func (f *fakeDB) execCalls() []execCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]execCall, len(f.calls))
	copy(out, f.calls)
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
