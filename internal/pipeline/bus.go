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

	return &EventBus{
		queue:    ringbuffer.New[OrderCommand](cfg.InboundCapacity),
		matcher:  matcher,
		outbound: ringbuffer.New[engine.ExecReport](cfg.OutboundCapacity),
		metrics:  observability.NoopRecorder(),
		cfg:      cfg,
	}
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
				cmd, ok := b.queue.Dequeue()
				if !ok {
					time.Sleep(b.cfg.IdleSleep)
					continue
				}
				b.metrics.SetQueueBacklog("inbound", float64(b.queue.Len()))

				matchStart := time.Now()
				result, matchErr := b.matcher.SubmitOrderDetailed(ctx, cmd.Order)
				if matchErr != nil {
					b.metrics.ObserveMatchLatency("error", time.Since(matchStart))
				} else {
					b.metrics.ObserveMatchLatency("ok", time.Since(matchStart))
				}

				b.sendResult(ctx, cmd.ResultCh, MatchResult{
					Trades: result.Trades,
					Err:    matchErr,
				})

				reports := buildExecReports(cmd.Order, result, matchErr)
				for _, report := range reports {
					if err := b.enqueueReport(ctx, report); err != nil {
						log.Printf("enqueue exec report failed: orderID=%d err=%v", report.OrderID, err)
					}
				}
			}
		}
	}()
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
			return ErrOutboundQueueFull
		default:
			time.Sleep(b.cfg.RetrySleep)
		}
	}
}

func buildExecReports(order engine.Order, result engine.SubmitResult, matchErr error) []engine.ExecReport {
	now := time.Now().UnixNano()

	if matchErr != nil {
		return []engine.ExecReport{
			{
				OrderID:   order.ID,
				Status:    engine.OrderStatusRejected,
				Filled:    0,
				Leaves:    0,
				Reason:    matchErr.Error(),
				Timestamp: now,
			},
		}
	}

	if len(result.Trades) == 0 {
		reason := ""
		if result.CanceledQty > 0 {
			reason = "remaining quantity canceled by order policy"
		}
		return []engine.ExecReport{
			{
				OrderID:   order.ID,
				Status:    result.FinalStatus,
				Filled:    0,
				Leaves:    result.OpenLeaves,
				Reason:    reason,
				Timestamp: now,
			},
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
		reports = append(reports, engine.ExecReport{
			OrderID:   order.ID,
			Status:    status,
			Filled:    cumFilled,
			Leaves:    result.OpenLeaves,
			Trade:     &trCopy,
			Reason:    reason,
			Timestamp: tr.Timestamp,
		})
	}

	return reports
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
