package strategy

import (
	"fmt"
	"sync"
)

// State is the lifecycle state of a running strategy.
type State string

const (
	StateRunning State = "running"
	StatePaused  State = "paused"
	StateStopped State = "stopped"
	StateFailed  State = "failed"
)

// Metrics is the optional observability surface used by Runner. A nil value or
// an empty implementation disables recording.
type Metrics interface {
	IncStrategyPanic(strategy string)
	IncStrategyOrder(strategy, result string)
	SetStrategyState(strategy, state string)
}

type noopMetrics struct{}

func (noopMetrics) IncStrategyPanic(string)         {}
func (noopMetrics) IncStrategyOrder(string, string) {}
func (noopMetrics) SetStrategyState(string, string) {}

// RunnerConfig bounds what one strategy is allowed to do. Zero values disable
// the corresponding limit.
type RunnerConfig struct {
	// MaxOrderRate is the maximum number of accepted submissions per second.
	MaxOrderRate int
	// MaxOpenOrders caps resting orders per symbol.
	MaxOpenOrders int
	// MaxPanics is the number of isolated panics after which the strategy is
	// paused. A zero value falls back to 3.
	MaxPanics int
}

// Runner drives one Strategy, isolating its failures from the caller.
//
// The runner is intentionally single-threaded: callbacks are invoked from the
// replay loop (or the live event loop) and the strategy must not call Context
// concurrently. Pause/State may be read from other goroutines.
type Runner struct {
	strat Strategy
	ctx   Context
	cfg   RunnerConfig
	met   Metrics

	mu        sync.Mutex
	state     State
	panics    int
	windowAt  int64
	windowCnt int
	stopOnce  bool
}

// NewRunner builds a runner. A nil context is reported by Init.
func NewRunner(strat Strategy, ctx Context, cfg RunnerConfig) *Runner {
	if cfg.MaxPanics <= 0 {
		cfg.MaxPanics = 3
	}
	return &Runner{
		strat: strat,
		ctx:   ctx,
		cfg:   cfg,
		met:   noopMetrics{},
		state: StateStopped,
	}
}

// SetMetrics installs a metrics recorder; nil falls back to a no-op.
func (r *Runner) SetMetrics(m Metrics) {
	if m == nil {
		r.met = noopMetrics{}
		return
	}
	r.met = m
}

// Init starts the strategy. Parameters are passed through unchanged.
func (r *Runner) Init(params map[string]string) error {
	if r.strat == nil {
		return fmt.Errorf("strategy: nil strategy")
	}
	if r.ctx == nil {
		return ErrNilContext
	}

	r.mu.Lock()
	r.state = StateRunning
	r.mu.Unlock()
	r.met.SetStrategyState(r.strat.Name(), string(StateRunning))

	guarded := &guardedContext{base: r.ctx, runner: r}
	err := r.safeCall(func() error { return r.strat.Init(guarded, params) })
	if err != nil {
		r.setState(StateFailed)
		return err
	}
	return nil
}

// OnBook forwards a market book snapshot.
func (r *Runner) OnBook(book BookView) error {
	return r.invoke(func(ctx Context) error { return r.strat.OnBook(ctx, book) })
}

// OnTrade forwards a public trade print.
func (r *Runner) OnTrade(trade TradeView) error {
	return r.invoke(func(ctx Context) error { return r.strat.OnTrade(ctx, trade) })
}

// OnTimer forwards a timer tick driven by the replay clock.
func (r *Runner) OnTimer(now int64) error {
	return r.invoke(func(ctx Context) error { return r.strat.OnTimer(ctx, now) })
}

// Pause stops callbacks until Resume is called. It is idempotent.
func (r *Runner) Pause() {
	r.setState(StatePaused)
}

// Resume re-enables callbacks after a pause.
func (r *Runner) Resume() {
	r.mu.Lock()
	wasPaused := r.state == StatePaused
	r.mu.Unlock()
	if !wasPaused {
		return
	}
	r.setState(StateRunning)
	r.mu.Lock()
	r.panics = 0
	r.mu.Unlock()
}

// State returns the current lifecycle state.
func (r *Runner) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Stop calls Strategy.Stop once and moves to StateStopped.
func (r *Runner) Stop() error {
	r.mu.Lock()
	if r.stopOnce {
		r.mu.Unlock()
		return nil
	}
	r.stopOnce = true
	r.state = StateStopped
	r.mu.Unlock()

	r.met.SetStrategyState(r.strat.Name(), string(StateStopped))
	return r.safeCall(func() error { return r.strat.Stop(&guardedContext{base: r.ctx, runner: r}) })
}

func (r *Runner) invoke(fn func(ctx Context) error) error {
	r.mu.Lock()
	running := r.state == StateRunning
	r.mu.Unlock()
	if !running {
		return nil
	}

	guarded := &guardedContext{base: r.ctx, runner: r}
	return r.safeCall(func() error { return fn(guarded) })
}

func (r *Runner) safeCall(fn func() error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("strategy %s panic: %v", r.name(), rec)
			r.onPanic()
		}
	}()
	return fn()
}

func (r *Runner) name() string {
	if r.strat == nil {
		return ""
	}
	return r.strat.Name()
}

func (r *Runner) onPanic() {
	r.met.IncStrategyPanic(r.name())

	r.mu.Lock()
	r.panics++
	paused := r.panics >= r.cfg.MaxPanics && r.state == StateRunning
	r.mu.Unlock()

	if paused {
		r.setState(StatePaused)
	}
}

func (r *Runner) setState(state State) {
	r.mu.Lock()
	if r.state == state {
		r.mu.Unlock()
		return
	}
	r.state = state
	r.mu.Unlock()
	r.met.SetStrategyState(r.name(), string(state))
}

// beforeSubmit enforces rate and open-order limits. It does not consume quota;
// afterSubmit counts only accepted submissions.
func (r *Runner) beforeSubmit(req OrderRequest) error {
	r.mu.Lock()
	if r.state != StateRunning {
		r.mu.Unlock()
		return fmt.Errorf("strategy %s is %s", r.name(), r.state)
	}
	now := r.ctx.Now()
	if r.cfg.MaxOrderRate > 0 {
		if now-r.windowAt >= 1000 || now < r.windowAt {
			r.windowAt = now
			r.windowCnt = 0
		}
		if r.windowCnt >= r.cfg.MaxOrderRate {
			r.mu.Unlock()
			r.met.IncStrategyOrder(r.name(), "rejected")
			return ErrOrderRateLimit
		}
	}
	maxOpen := r.cfg.MaxOpenOrders
	r.mu.Unlock()

	if maxOpen > 0 && len(r.ctx.OpenOrders(req.Symbol)) >= maxOpen {
		r.met.IncStrategyOrder(r.name(), "rejected")
		return ErrMaxOpenOrders
	}
	return nil
}

func (r *Runner) afterSubmit(_ SubmitResult, err error) {
	result := "accepted"
	if err != nil {
		result = "rejected"
	}
	r.met.IncStrategyOrder(r.name(), result)

	if err != nil {
		return
	}
	r.mu.Lock()
	r.windowCnt++
	r.mu.Unlock()
}

// guardedContext enforces the runner limits on every submission while passing
// every other call straight through.
type guardedContext struct {
	base   Context
	runner *Runner
}

func (g *guardedContext) Now() int64 { return g.base.Now() }

func (g *guardedContext) Position(symbol string) int64 { return g.base.Position(symbol) }

func (g *guardedContext) Book(symbol string) (BookView, bool) { return g.base.Book(symbol) }

func (g *guardedContext) Submit(req OrderRequest) (SubmitResult, error) {
	if req.Strategy == "" {
		req.Strategy = g.runner.name()
	}
	if err := g.runner.beforeSubmit(req); err != nil {
		return SubmitResult{}, err
	}
	res, err := g.base.Submit(req)
	g.runner.afterSubmit(res, err)
	return res, err
}

func (g *guardedContext) Cancel(orderID uint64) error { return g.base.Cancel(orderID) }

func (g *guardedContext) OpenOrders(symbol string) []uint64 { return g.base.OpenOrders(symbol) }

var _ Context = (*guardedContext)(nil)
