package strategy

import (
	"errors"
	"strings"
	"testing"

	"trading-system/internal/engine"
)

type fakeContext struct {
	now       int64
	positions map[string]int64
	books     map[string]BookView
	open      map[string][]uint64

	submits   []OrderRequest
	submitErr error
	nextID    uint64
	closed    []uint64
}

func newFakeContext() *fakeContext {
	return &fakeContext{
		positions: make(map[string]int64),
		books:     make(map[string]BookView),
		open:      make(map[string][]uint64),
	}
}

func (f *fakeContext) Now() int64 { return f.now }

func (f *fakeContext) Position(symbol string) int64 { return f.positions[symbol] }

func (f *fakeContext) Book(symbol string) (BookView, bool) {
	b, ok := f.books[symbol]
	return b, ok
}

func (f *fakeContext) Submit(req OrderRequest) (SubmitResult, error) {
	f.submits = append(f.submits, req)
	if f.submitErr != nil {
		return SubmitResult{}, f.submitErr
	}
	f.nextID++
	return SubmitResult{OrderID: f.nextID, Leaves: req.Qty, Status: engine.OrderStatusNew}, nil
}

func (f *fakeContext) Cancel(orderID uint64) error {
	f.closed = append(f.closed, orderID)
	return nil
}

func (f *fakeContext) OpenOrders(symbol string) []uint64 { return f.open[symbol] }

type scriptedStrategy struct {
	name    string
	onTimer func(ctx Context, now int64) error
	stopped int
}

func (s *scriptedStrategy) Name() string { return s.name }

func (s *scriptedStrategy) Init(Context, map[string]string) error { return nil }

func (s *scriptedStrategy) OnBook(Context, BookView) error { return nil }

func (s *scriptedStrategy) OnTrade(Context, TradeView) error { return nil }

func (s *scriptedStrategy) OnTimer(ctx Context, now int64) error {
	if s.onTimer == nil {
		return nil
	}
	return s.onTimer(ctx, now)
}

func (s *scriptedStrategy) Stop(Context) error {
	s.stopped++
	return nil
}

func TestRunnerIsolatesPanicsAndPauses(t *testing.T) {
	strat := &scriptedStrategy{
		name: "boom",
		onTimer: func(Context, int64) error {
			panic("kaboom")
		},
	}
	r := NewRunner(strat, newFakeContext(), RunnerConfig{MaxPanics: 2})

	if err := r.Init(nil); err != nil {
		t.Fatalf("init: %v", err)
	}

	err := r.OnTimer(1)
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("first panic err = %v", err)
	}
	if r.State() != StateRunning {
		t.Fatalf("state after one panic = %s, want running", r.State())
	}

	if err := r.OnTimer(2); err == nil {
		t.Fatal("second panic should return an error")
	}
	if r.State() != StatePaused {
		t.Fatalf("state after two panics = %s, want paused", r.State())
	}

	if err := r.OnTimer(3); err != nil {
		t.Fatalf("paused runner should skip callbacks, got %v", err)
	}

	r.Resume()
	if r.State() != StateRunning {
		t.Fatalf("state after resume = %s, want running", r.State())
	}
}

func TestRunnerRateLimit(t *testing.T) {
	base := newFakeContext()
	strat := &scriptedStrategy{
		name: "spam",
		onTimer: func(ctx Context, _ int64) error {
			_, err := ctx.Submit(OrderRequest{
				Symbol: "X",
				Side:   engine.SideBuy,
				Type:   engine.OrderTypeLimit,
				TIF:    engine.TimeInForceGTC,
				Price:  10,
				Qty:    1,
			})
			return err
		},
	}
	r := NewRunner(strat, base, RunnerConfig{MaxOrderRate: 1})
	if err := r.Init(nil); err != nil {
		t.Fatalf("init: %v", err)
	}

	base.now = 1000
	if err := r.OnTimer(1000); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if err := r.OnTimer(1001); !errors.Is(err, ErrOrderRateLimit) {
		t.Fatalf("second submit err = %v, want ErrOrderRateLimit", err)
	}
	if len(base.submits) != 1 {
		t.Fatalf("rejected submit reached the venue: %d", len(base.submits))
	}

	base.now = 2000
	if err := r.OnTimer(2000); err != nil {
		t.Fatalf("submit after window: %v", err)
	}
}

func TestRunnerMaxOpenOrders(t *testing.T) {
	base := newFakeContext()
	base.open["X"] = []uint64{1, 2}

	strat := &scriptedStrategy{
		name: "capped",
		onTimer: func(ctx Context, _ int64) error {
			_, err := ctx.Submit(OrderRequest{Symbol: "X", Side: engine.SideBuy, Qty: 1})
			return err
		},
	}
	r := NewRunner(strat, base, RunnerConfig{MaxOpenOrders: 2})
	if err := r.Init(nil); err != nil {
		t.Fatalf("init: %v", err)
	}

	if err := r.OnTimer(1); !errors.Is(err, ErrMaxOpenOrders) {
		t.Fatalf("err = %v, want ErrMaxOpenOrders", err)
	}
	if len(base.submits) != 0 {
		t.Fatalf("capped submit reached the venue: %d", len(base.submits))
	}
}

func TestRunnerStopIsIdempotent(t *testing.T) {
	strat := &scriptedStrategy{name: "once"}
	r := NewRunner(strat, newFakeContext(), RunnerConfig{})
	if err := r.Init(nil); err != nil {
		t.Fatalf("init: %v", err)
	}

	if err := r.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if strat.stopped != 1 {
		t.Fatalf("strategy stop calls = %d, want 1", strat.stopped)
	}
	if r.State() != StateStopped {
		t.Fatalf("state = %s, want stopped", r.State())
	}
}

func TestRunnerNilContext(t *testing.T) {
	r := NewRunner(&scriptedStrategy{name: "x"}, nil, RunnerConfig{})
	if err := r.Init(nil); !errors.Is(err, ErrNilContext) {
		t.Fatalf("err = %v, want ErrNilContext", err)
	}
}
