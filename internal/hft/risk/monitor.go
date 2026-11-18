package risk

import (
	"fmt"
	"sync"
	"time"
)

// Action is the risk supervisor's response to a strategy.
type Action string

const (
	ActionAllow      Action = "ALLOW"
	ActionReject     Action = "REJECT"
	ActionCancelOnly Action = "CANCEL_ONLY"
	ActionPause      Action = "PAUSE"
	ActionHalt       Action = "HALT"
)

// Decision codes produced by the intraday monitor. They share the Code space
// with the pre-trade guard so clients can match on one field.
const (
	CodeMaxOpenOrders  = "MAX_OPEN_ORDERS"
	CodeCancelRatio    = "CANCEL_RATIO"
	CodeDailyLoss      = "DAILY_LOSS"
	CodeDrawdown       = "DRAWDOWN"
	CodeStrategyPaused = "STRATEGY_PAUSED"
	CodeStrategyHalted = "STRATEGY_HALTED"
	CodeCancelOnly     = "CANCEL_ONLY"
	CodeResumed        = "RESUMED"
)

// IntradayConfig controls post-trade monitoring. Zero values disable the
// corresponding check.
type IntradayConfig struct {
	// MaxCancelRatioBps pauses a strategy when cancels/orders in the window
	// exceeds this ratio. For example 8000 means 80%.
	MaxCancelRatioBps int64
	// CancelWindow is the sliding window for the cancel ratio.
	CancelWindow time.Duration
	// MinCancelSamples is the number of orders plus cancels required before the
	// ratio is evaluated, so the first cancel cannot trip the breaker.
	MinCancelSamples int
	// MaxOpenOrders rejects new orders after this many resting orders.
	MaxOpenOrders int
	// MaxDailyLoss halts trading once realized plus unrealized PnL reaches
	// -MaxDailyLoss (notional units).
	MaxDailyLoss int64
	// MaxDrawdownBps halts or pauses once equity drops this far from its peak.
	MaxDrawdownBps int64
	// Cooldown is how long a pause lasts before auto-resume.
	Cooldown time.Duration
	// ResumeAuto re-enables a paused strategy after Cooldown. When false a
	// breach escalates to HALT and needs a manual Reset.
	ResumeAuto bool
}

// Enabled reports whether any intraday check is configured.
func (c IntradayConfig) Enabled() bool {
	return c.MaxCancelRatioBps > 0 ||
		c.MaxOpenOrders > 0 ||
		c.MaxDailyLoss > 0 ||
		c.MaxDrawdownBps > 0
}

// Trigger describes a state change caused by a breach. Callers persist and
// publish it.
type Trigger struct {
	Code    string
	Action  Action
	Message string
}

type monitorState struct {
	windowStart time.Time
	orders      int
	cancels     int
	open        int
	peakEquity  int64

	action  Action
	reason  string
	message string
	until   time.Time
}

// Monitor is the stateful intraday risk supervisor. One state exists per
// (account, symbol) pair.
type Monitor struct {
	cfg IntradayConfig
	pos *Positions
	now func() time.Time

	mu     sync.Mutex
	states map[PositionKey]*monitorState
}

// NewMonitor builds a monitor. positions may be nil, which disables the PnL
// checks.
func NewMonitor(cfg IntradayConfig, positions *Positions) *Monitor {
	return &Monitor{
		cfg:    cfg,
		pos:    positions,
		now:    time.Now,
		states: make(map[PositionKey]*monitorState),
	}
}

// SetClock overrides the wall clock; tests and replay use this.
func (m *Monitor) SetClock(now func() time.Time) {
	if now != nil {
		m.now = now
	}
}

// Check evaluates the supervisor state before an order is accepted. It returns
// the decision plus any state transitions the caller must publish.
func (m *Monitor) Check(accountID uint64, symbol string, now time.Time) (Decision, []Trigger) {
	if !m.cfg.Enabled() {
		return Decision{Approved: true, Code: CodeOK}, nil
	}
	if now.IsZero() {
		now = m.now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := PositionKey{AccountID: accountID, Symbol: symbol}
	st := m.stateLocked(key, now)
	var triggers []Trigger

	switch st.action {
	case ActionHalt:
		return reject(CodeStrategyHalted, st.message), nil

	case ActionPause:
		if m.cfg.ResumeAuto && !now.Before(st.until) {
			st.action = ActionAllow
			st.reason = ""
			st.message = ""
			st.until = time.Time{}
			triggers = append(triggers, Trigger{
				Code:    CodeResumed,
				Action:  ActionAllow,
				Message: "strategy resumed after cooldown",
			})
		} else {
			return reject(CodeStrategyPaused, st.message), nil
		}

	case ActionCancelOnly:
		return reject(CodeCancelOnly, st.message), nil
	}

	if m.cfg.MaxOpenOrders > 0 && st.open >= m.cfg.MaxOpenOrders {
		return reject(CodeMaxOpenOrders, fmt.Sprintf(
			"open orders %d reached max %d", st.open, m.cfg.MaxOpenOrders,
		)), triggers
	}

	if m.cancelRatioBreachedLocked(st) {
		action := m.breachAction()
		msg := fmt.Sprintf("cancel ratio %d bps exceeded %d bps over %s",
			m.cancelRatioBpsLocked(st), m.cfg.MaxCancelRatioBps, m.cfg.CancelWindow)
		triggers = append(triggers, m.tripLocked(st, CodeCancelRatio, action, msg, now))
		return reject(actionCode(action), msg), triggers
	}

	return Decision{Approved: true, Code: CodeOK}, triggers
}

// Evaluate checks PnL-based limits. Call it after fills are applied.
func (m *Monitor) Evaluate(accountID uint64, symbol string, now time.Time) []Trigger {
	if !m.cfg.Enabled() || m.pos == nil {
		return nil
	}
	if m.cfg.MaxDailyLoss <= 0 && m.cfg.MaxDrawdownBps <= 0 {
		return nil
	}
	if now.IsZero() {
		now = m.now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := PositionKey{AccountID: accountID, Symbol: symbol}
	st := m.stateLocked(key, now)
	if st.action == ActionHalt {
		return nil
	}

	equity := m.pos.AccountEquity(accountID)
	if equity > st.peakEquity {
		st.peakEquity = equity
	}

	if m.cfg.MaxDailyLoss > 0 && equity <= -m.cfg.MaxDailyLoss {
		msg := fmt.Sprintf("equity %d reached daily loss limit -%d", equity, m.cfg.MaxDailyLoss)
		return []Trigger{m.tripLocked(st, CodeDailyLoss, ActionHalt, msg, now)}
	}

	if m.cfg.MaxDrawdownBps > 0 && st.peakEquity > 0 {
		dd := (st.peakEquity - equity) * 10000 / st.peakEquity
		if dd > m.cfg.MaxDrawdownBps {
			action := m.breachAction()
			msg := fmt.Sprintf("drawdown %d bps exceeded %d bps", dd, m.cfg.MaxDrawdownBps)
			return []Trigger{m.tripLocked(st, CodeDrawdown, action, msg, now)}
		}
	}

	return nil
}

// OnOrderAccepted records one accepted order for open-order and cancel-ratio
// accounting.
func (m *Monitor) OnOrderAccepted(accountID uint64, symbol string, now time.Time) {
	if !m.cfg.Enabled() {
		return
	}
	if now.IsZero() {
		now = m.now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.stateLocked(PositionKey{AccountID: accountID, Symbol: symbol}, now)
	st.open++
	st.orders++
}

// OnOrderClosed releases one open order. It is idempotent with respect to
// underflow.
func (m *Monitor) OnOrderClosed(accountID uint64, symbol string) {
	if !m.cfg.Enabled() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.stateLocked(PositionKey{AccountID: accountID, Symbol: symbol}, m.now())
	if st.open > 0 {
		st.open--
	}
}

// OnCancel records one cancellation. The cancel API is not wired into the
// gateway yet, so this is called by tests and by the future cancel path.
func (m *Monitor) OnCancel(accountID uint64, symbol string, now time.Time) {
	if !m.cfg.Enabled() {
		return
	}
	if now.IsZero() {
		now = m.now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.stateLocked(PositionKey{AccountID: accountID, Symbol: symbol}, now)
	st.cancels++
	if st.open > 0 {
		st.open--
	}
}

// State returns the current action and its message.
func (m *Monitor) State(accountID uint64, symbol string) (Action, string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.states[PositionKey{AccountID: accountID, Symbol: symbol}]
	if st == nil || st.action == "" {
		return ActionAllow, ""
	}
	return st.action, st.message
}

// Reset clears a halt or pause. It is the manual override for operators.
func (m *Monitor) Reset(accountID uint64, symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, PositionKey{AccountID: accountID, Symbol: symbol})
}

func (m *Monitor) stateLocked(key PositionKey, now time.Time) *monitorState {
	st := m.states[key]
	if st == nil {
		st = &monitorState{windowStart: now, action: ActionAllow}
		m.states[key] = st
	}
	if m.cfg.CancelWindow > 0 && !st.windowStart.IsZero() && now.Sub(st.windowStart) >= m.cfg.CancelWindow {
		st.windowStart = now
		st.orders = 0
		st.cancels = 0
	}
	return st
}

func (m *Monitor) cancelRatioBreachedLocked(st *monitorState) bool {
	if m.cfg.MaxCancelRatioBps <= 0 || m.cfg.CancelWindow <= 0 {
		return false
	}
	minSamples := m.cfg.MinCancelSamples
	if minSamples <= 0 {
		minSamples = 5
	}
	total := st.orders + st.cancels
	if total < minSamples || total == 0 {
		return false
	}
	return int64(st.cancels)*10000/int64(total) > m.cfg.MaxCancelRatioBps
}

func (m *Monitor) cancelRatioBpsLocked(st *monitorState) int64 {
	total := st.orders + st.cancels
	if total == 0 {
		return 0
	}
	return int64(st.cancels) * 10000 / int64(total)
}

func (m *Monitor) breachAction() Action {
	if m.cfg.ResumeAuto && m.cfg.Cooldown > 0 {
		return ActionPause
	}
	return ActionHalt
}

func (m *Monitor) tripLocked(st *monitorState, code string, action Action, msg string, now time.Time) Trigger {
	st.action = action
	st.reason = code
	st.message = msg
	st.until = time.Time{}
	if action == ActionPause {
		st.until = now.Add(m.cfg.Cooldown)
	}
	st.orders = 0
	st.cancels = 0
	st.windowStart = now

	return Trigger{Code: code, Action: action, Message: msg}
}

func actionCode(action Action) string {
	switch action {
	case ActionHalt:
		return CodeStrategyHalted
	case ActionCancelOnly:
		return CodeCancelOnly
	default:
		return CodeStrategyPaused
	}
}
