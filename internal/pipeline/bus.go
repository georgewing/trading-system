package pipeline

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"
	"trading-system/internal/engine"
	"trading-system/internal/observability"
	"trading-system/pkg/ringbuffer"
)

var (
	ErrInboundQueueFull  = errors.New("eventbus inbound queue full")
	ErrOutboundQueueFull = errors.New("eventbus outbound queue full")
	ErrShuttingDown      = errors.New("eventbus is shutting down")
)

type OrderCommand struct {
	Order    engine.Order
	ResultCh chan<- MatchResult
}

type MatchResult struct {
	Trades []engine.Trade
	Err    error
}

type EventBusConfig struct {
	InboundCapacity      uint64
	OutboundCapacity     uint64
	SubmitTimeout        time.Duration
	ResultSendTimeout    time.Duration
	ReportEnqueueTimeout time.Duration
	IdleSleep            time.Duration
	RetrySleep           time.Duration
}

func DefaultEventBusConfig() EventBusConfig {
	return EventBusConfig{
		InboundCapacity:      1 << 20,
		OutboundCapacity:     1 << 20,
		SubmitTimeout:        2 * time.Millisecond,
		ResultSendTimeout:    10 * time.Millisecond,
		ReportEnqueueTimeout: 5 * time.Millisecond,
		IdleSleep:            50 * time.Microsecond,
		RetrySleep:           20 * time.Microsecond,
	}
}

type EventBus struct {
	queue    *ringbuffer.RingBuffer[OrderCommand]
	matcher  *engine.Matcher
	outbound *ringbuffer.RingBuffer[engine.ExecReport]
	metrics  observability.Recorder

	cfg      EventBusConfig
	submitMu sync.Mutex // ringbuffer 是 SPSC；多 producer 时必须保护 enqueue

	// riskGuard 为可选的事前风控阶段，nil 表示关闭；只在处理 goroutine 读取。
	riskGuard         RiskGuard
	riskQuantityScale int64
	riskPriceScale    int64

	// riskObserver 为可选的事中风控观察者，nil 表示关闭；只在处理 goroutine 调用。
	riskObserver RiskObserver

	// venueResolver is the optional routing stage. nil means orders go straight
	// to the matcher. It is only read on the processing goroutine.
	venueResolver VenueResolver

	accepting       atomic.Bool
	processing      atomic.Int64
	droppedInbound  atomic.Uint64
	droppedOutbound atomic.Uint64
}

func NewEventBus(matcher *engine.Matcher, capacity uint64) *EventBus {
	cfg := DefaultEventBusConfig()
	if capacity > 0 {
		cfg.InboundCapacity = capacity
		cfg.OutboundCapacity = capacity
	}
	return NewEventBusWithConfig(matcher, cfg)
}

func NewEventBusWithConfig(matcher *engine.Matcher, cfg EventBusConfig) *EventBus {
	if cfg.InboundCapacity == 0 {
		cfg.InboundCapacity = 1 << 20
	}
	if cfg.OutboundCapacity == 0 {
		cfg.OutboundCapacity = 1 << 20
	}
	if cfg.SubmitTimeout <= 0 {
		cfg.SubmitTimeout = 2 * time.Millisecond
	}
	if cfg.ResultSendTimeout <= 0 {
		cfg.ResultSendTimeout = 10 * time.Millisecond
	}
	if cfg.ReportEnqueueTimeout <= 0 {
		cfg.ReportEnqueueTimeout = 5 * time.Millisecond
	}
	if cfg.IdleSleep <= 0 {
		cfg.IdleSleep = 50 * time.Microsecond
	}
	if cfg.RetrySleep <= 0 {
		cfg.RetrySleep = 20 * time.Microsecond
	}

	bus := &EventBus{
		queue:    ringbuffer.New[OrderCommand](cfg.InboundCapacity),
		matcher:  matcher,
		outbound: ringbuffer.New[engine.ExecReport](cfg.OutboundCapacity),
		metrics:  observability.NoopRecorder(),
		cfg:      cfg,
	}
	bus.accepting.Store(true)
	return bus
}

// SetMetrics 注入指标采集器，nil 会回退为 noop。
func (b *EventBus) SetMetrics(recorder observability.Recorder) {
	if recorder == nil {
		b.metrics = observability.NoopRecorder()
		return
	}
	b.metrics = recorder
}

// 兼容旧接口
func (b *EventBus) Submit(cmd OrderCommand) bool {
	return b.SubmitWithContext(context.Background(), cmd) == nil
}

func (b *EventBus) SubmitWithContext(ctx context.Context, cmd OrderCommand) error {
	timer := time.NewTimer(b.cfg.SubmitTimeout)
	defer stopTimer(timer)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		b.submitMu.Lock()
		if !b.accepting.Load() {
			b.submitMu.Unlock()
			b.metrics.IncOrderArrival("rejected")
			return ErrShuttingDown
		}
		ok := b.queue.Enqueue(cmd)
		b.submitMu.Unlock()
		if ok {
			b.metrics.IncOrderArrival("accepted")
			b.metrics.SetQueueBacklog("inbound", float64(b.queue.Len()))
			return nil
		}

		select {
		case <-ctx.Done():
			b.metrics.IncOrderArrival("canceled")
			return ctx.Err()
		case <-timer.C:
			b.droppedInbound.Add(1)
			b.metrics.IncOrderArrival("rejected")
			b.metrics.IncQueueDrop("inbound")
			b.metrics.SetQueueBacklog("inbound", float64(b.queue.Len()))
			return ErrInboundQueueFull
		default:
			time.Sleep(b.cfg.RetrySleep)
		}
	}
}

func (b *EventBus) Outbound() *ringbuffer.RingBuffer[engine.ExecReport] {
	return b.outbound
}

// StopAccepting rejects new submissions. It is safe to call more than once.
func (b *EventBus) StopAccepting() {
	b.submitMu.Lock()
	b.accepting.Store(false)
	b.submitMu.Unlock()
}

// Drain waits until all accepted commands have been processed and their reports
// have been enqueued to the outbound queue.
func (b *EventBus) Drain(ctx context.Context) error {
	ticker := time.NewTicker(b.cfg.IdleSleep)
	defer ticker.Stop()

	for {
		if b.queue.Len() == 0 && b.processing.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *EventBus) DroppedInbound() uint64 {
	return b.droppedInbound.Load()
}

func (b *EventBus) DroppedOutbound() uint64 {
	return b.droppedOutbound.Load()
}

func (b *EventBus) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				b.processing.Add(1)
				cmd, ok := b.queue.Dequeue()
				if !ok {
					b.processing.Add(-1)
					time.Sleep(b.cfg.IdleSleep)
					continue
				}
				b.processCommand(ctx, cmd)
			}
		}
	}()
}

func (b *EventBus) processCommand(ctx context.Context, cmd OrderCommand) {
	defer b.processing.Add(-1)

	b.metrics.SetQueueBacklog("inbound", float64(b.queue.Len()))

	if reason, ok := b.checkRisk(ctx, cmd.Order); !ok {
		b.rejectCommand(ctx, cmd, reason)
		return
	}

	// Routing is optional: without a resolver the bus keeps executing directly
	// against the matcher.
	if resolver := b.venueResolver; resolver != nil {
		venue, routeErr := resolver.Route(cmd.Order.Symbol)
		if routeErr != nil {
			b.rejectCommand(ctx, cmd, routingRejectReason(routeErr))
			return
		}
		if venue == nil {
			b.rejectCommand(ctx, cmd, routingRejectReason(errNilVenue))
			return
		}

		// Notify only after routing succeeds: a routing rejection never reaches
		// the venue and must not leave an open-order slot behind.
		b.notifyAccepted(cmd.Order)

		submitStart := time.Now()
		result, submitErr := venue.Submit(ctx, cmd.Order)
		b.observeSubmitLatency(submitStart, submitErr)
		b.publishResult(ctx, cmd, result, submitErr, venue.Name())
		return
	}

	b.notifyAccepted(cmd.Order)

	submitStart := time.Now()
	result, matchErr := b.matcher.SubmitOrderDetailed(ctx, cmd.Order)
	b.observeSubmitLatency(submitStart, matchErr)
	b.publishResult(ctx, cmd, result, matchErr, "internal")
}

func (b *EventBus) notifyAccepted(order engine.Order) {
	if b.riskObserver != nil {
		b.riskObserver.OnOrderAccepted(order)
	}
}

// observeSubmitLatency records matcher or venue submission time. Routing and
// risk are excluded so the metric keeps meaning submit latency.
func (b *EventBus) observeSubmitLatency(start time.Time, err error) {
	if err != nil {
		b.metrics.ObserveMatchLatency("error", time.Since(start))
		return
	}
	b.metrics.ObserveMatchLatency("ok", time.Since(start))
}

// publishResult delivers the synchronous submit result and enqueues the exec
// reports derived from it. Both the direct matcher path and the routed venue
// path share it so report semantics stay identical.
func (b *EventBus) publishResult(ctx context.Context, cmd OrderCommand, result engine.SubmitResult, submitErr error, venue string) {
	b.sendResult(ctx, cmd.ResultCh, MatchResult{
		Trades: result.Trades,
		Err:    submitErr,
	})

	for _, report := range buildExecReports(cmd.Order, result, submitErr, venue) {
		// Account for fills before enqueueing: outbound enqueue can drop under
		// backpressure, but positions and risk state must not.
		if b.riskObserver != nil {
			b.riskObserver.OnExecReport(cmd.Order, report)
		}
		if err := b.enqueueReport(ctx, report); err != nil {
			logEnqueueFailure(report, err)
		}
	}
}

func (b *EventBus) sendResult(ctx context.Context, ch chan<- MatchResult, r MatchResult) {
	if ch == nil {
		return
	}
	timer := time.NewTimer(b.cfg.ResultSendTimeout)
	defer stopTimer(timer)

	select {
	case ch <- r:
	case <-ctx.Done():
	case <-timer.C:
		log.Printf("result channel blocked: drop response")
	}
}

func (b *EventBus) enqueueReport(ctx context.Context, report engine.ExecReport) error {
	if b.outbound == nil {
		return nil
	}

	timer := time.NewTimer(b.cfg.ReportEnqueueTimeout)
	defer stopTimer(timer)

	for {
		if b.outbound.Enqueue(report) {
			b.metrics.SetQueueBacklog("outbound", float64(b.outbound.Len()))
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			b.droppedOutbound.Add(1)
			b.metrics.IncQueueDrop("outbound")
			return ErrOutboundQueueFull
		default:
			time.Sleep(b.cfg.RetrySleep)
		}
	}
}

func buildExecReports(order engine.Order, result engine.SubmitResult, matchErr error, venue string) []engine.ExecReport {
	now := time.Now().UnixNano()

	if matchErr != nil {
		return []engine.ExecReport{
			newExecReport(order, venue, 1, now, engine.ExecTypeRejected, engine.OrderStatusRejected, 0, 0, 0, 0, matchErr.Error(), nil),
		}
	}

	if len(result.Trades) == 0 {
		reason := ""
		execType := engine.ExecTypeNew
		if result.CanceledQty > 0 || result.FinalStatus == engine.OrderStatusCanceled {
			reason = "remaining quantity canceled by order policy"
			execType = engine.ExecTypeCanceled
		}
		return []engine.ExecReport{
			newExecReport(order, venue, 1, now, execType, result.FinalStatus, 0, 0, 0, result.OpenLeaves, reason, nil),
		}
	}

	reports := make([]engine.ExecReport, 0, len(result.Trades))
	var cumFilled int64

	for i := range result.Trades {
		tr := result.Trades[i]
		cumFilled += tr.Quantity

		status := engine.OrderStatusPartiallyFilled
		reason := ""
		if i == len(result.Trades)-1 {
			status = result.FinalStatus
			if result.CanceledQty > 0 {
				reason = "remaining quantity canceled by order policy"
			}
		}

		trCopy := tr
		ts := tr.Timestamp
		if ts == 0 {
			ts = now
		}
		reports = append(reports, newExecReport(
			order, venue, uint64(i+1), ts, engine.ExecTypeTrade, status,
			cumFilled, tr.Price, tr.Quantity, result.OpenLeaves,
			reason, &trCopy,
		))
	}

	return reports
}

var execIDSeq atomic.Uint64

func init() {
	execIDSeq.Store(uint64(time.Now().UnixNano()))
}

func nextExecID() uint64 {
	return execIDSeq.Add(1)
}

func newExecReport(
	order engine.Order,
	venue string,
	seq uint64,
	ts int64,
	execType engine.ExecType,
	status engine.OrderStatus,
	cumQty, lastPx, lastQty, leavesQty int64,
	reason string,
	trade *engine.Trade,
) engine.ExecReport {
	liquidity := engine.Liquidity("")
	if trade != nil {
		liquidity = engine.LiquidityTaker
	}
	return engine.ExecReport{
		ExecID:        nextExecID(),
		Seq:           seq,
		OrderID:       order.ID,
		ClientOrderID: order.ClientOrderID,
		AccountID:     order.AccountID,
		Symbol:        order.Symbol,
		Side:          order.Side,
		ExecType:      execType,
		Status:        status,
		Filled:        cumQty,
		Leaves:        leavesQty,
		LastPx:        lastPx,
		LastQty:       lastQty,
		CumQty:        cumQty,
		LeavesQty:     leavesQty,
		Liquidity:     liquidity,
		OrderPx:       order.Price,
		OrderType:     order.Type,
		TIF:           order.TIF,
		Quantity:      order.Quantity,
		Trade:         trade,
		Reason:        reason,
		Venue:         venue,
		Timestamp:     ts,
	}
}

func logEnqueueFailure(report engine.ExecReport, err error) {
	log.Printf("enqueue exec report failed: orderID=%d err=%v", report.OrderID, err)
}

func stopTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}
