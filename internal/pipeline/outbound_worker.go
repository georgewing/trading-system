package pipeline

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
	"trading-system/internal/engine"
	"trading-system/internal/observability"
	"trading-system/pkg/ringbuffer"
)

type ExecReportSink interface {
	Name() string
	HandleExecReport(ctx context.Context, report engine.ExecReport) error
}

type DeadLetterHandler interface {
	HandleDeadLetter(ctx context.Context, sink string, report engine.ExecReport, err error)
}

type LogDeadLetter struct{}

func (d *LogDeadLetter) HandleDeadLetter(_ context.Context, sink string, report engine.ExecReport, err error) {
	log.Printf("dead-letter sink=%s orderID=%d err=%v", sink, report.OrderID, err)
}

type OutboundWorkerConfig struct {
	DispatchIdleSleep time.Duration
	DispatchTimeout   time.Duration
	SinkQueueSize     int

	RetryMaxAttempts int
	RetryBaseBackoff time.Duration
	RetryMaxBackoff  time.Duration
}

func DefaultOutboundWorkerConfig() OutboundWorkerConfig {
	return OutboundWorkerConfig{
		DispatchIdleSleep: 50 * time.Microsecond,
		DispatchTimeout:   2 * time.Millisecond,
		SinkQueueSize:     4096,
		RetryMaxAttempts:  5,
		RetryBaseBackoff:  10 * time.Millisecond,
		RetryMaxBackoff:   1 * time.Second,
	}
}

type sinkRuntime struct {
	sink ExecReportSink
	ch   chan engine.ExecReport
}

type OutboundWorker struct {
	queue *ringbuffer.RingBuffer[engine.ExecReport]
	cfg   OutboundWorkerConfig
	dlq   DeadLetterHandler
	met   observability.Recorder

	sinks []sinkRuntime
	wg    sync.WaitGroup

	stopCh   chan struct{}
	stopOnce sync.Once

	dispatchDropped atomic.Uint64
	sinkFailures    atomic.Uint64
}

func NewOutboundWorker(
	queue *ringbuffer.RingBuffer[engine.ExecReport],
	cfg OutboundWorkerConfig,
	dlq DeadLetterHandler,
	sinks ...ExecReportSink,
) *OutboundWorker {
	if cfg.DispatchIdleSleep <= 0 {
		cfg.DispatchIdleSleep = 50 * time.Microsecond
	}
	if cfg.DispatchTimeout <= 0 {
		cfg.DispatchTimeout = 2 * time.Millisecond
	}
	if cfg.SinkQueueSize <= 0 {
		cfg.SinkQueueSize = 4096
	}
	if cfg.RetryMaxAttempts <= 0 {
		cfg.RetryMaxAttempts = 1
	}
	if cfg.RetryBaseBackoff <= 0 {
		cfg.RetryBaseBackoff = 10 * time.Millisecond
	}
	if cfg.RetryMaxBackoff <= 0 {
		cfg.RetryMaxBackoff = 1 * time.Second
	}
	if dlq == nil {
		dlq = &LogDeadLetter{}
	}

	rt := make([]sinkRuntime, 0, len(sinks))
	for _, s := range sinks {
		if s == nil {
			continue
		}
		rt = append(rt, sinkRuntime{
			sink: s,
			ch:   make(chan engine.ExecReport, cfg.SinkQueueSize),
		})
	}

	return &OutboundWorker{
		queue:  queue,
		cfg:    cfg,
		dlq:    dlq,
		met:    observability.NoopRecorder(),
		sinks:  rt,
		stopCh: make(chan struct{}),
	}
}

// SetMetrics 注入指标采集器，nil 会回退为 noop。
func (w *OutboundWorker) SetMetrics(recorder observability.Recorder) {
	if recorder == nil {
		w.met = observability.NoopRecorder()
		return
	}
	w.met = recorder
}

func (w *OutboundWorker) Start(ctx context.Context) {
	for i := range w.sinks {
		i := i
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			w.runSink(ctx, &w.sinks[i])
		}()
	}

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.runDispatcher(ctx)
	}()
}

// Stop drains the outbound queue and waits for all sinks to finish. The ctx
// bounds the wait; it does not stop the drain itself.
func (w *OutboundWorker) Stop(ctx context.Context) error {
	w.stopOnce.Do(func() {
		close(w.stopCh)
	})

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *OutboundWorker) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *OutboundWorker) DispatchDropped() uint64 {
	return w.dispatchDropped.Load()
}

func (w *OutboundWorker) SinkFailures() uint64 {
	return w.sinkFailures.Load()
}

func (w *OutboundWorker) runDispatcher(ctx context.Context) {
	defer w.closeSinks()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			w.drainQueue(ctx)
			return
		default:
		}

		if w.queue == nil {
			time.Sleep(w.cfg.DispatchIdleSleep)
			continue
		}

		report, ok := w.queue.Dequeue()
		if !ok {
			time.Sleep(w.cfg.DispatchIdleSleep)
			continue
		}
		w.met.SetQueueBacklog("outbound", float64(w.queue.Len()))
		if !w.dispatch(ctx, report) {
			return
		}
	}
}

func (w *OutboundWorker) drainQueue(ctx context.Context) {
	if w.queue == nil {
		return
	}
	for {
		report, ok := w.queue.Dequeue()
		if !ok {
			w.met.SetQueueBacklog("outbound", 0)
			return
		}
		w.met.SetQueueBacklog("outbound", float64(w.queue.Len()))
		if !w.dispatch(ctx, report) {
			return
		}
	}
}

func (w *OutboundWorker) dispatch(ctx context.Context, report engine.ExecReport) bool {
	for i := range w.sinks {
		sr := &w.sinks[i]
		timer := time.NewTimer(w.cfg.DispatchTimeout)
		select {
		case sr.ch <- report:
		case <-ctx.Done():
			stopTimer(timer)
			return false
		case <-timer.C:
			w.dispatchDropped.Add(1)
			w.met.IncQueueDrop(sr.sink.Name())
			w.dlq.HandleDeadLetter(ctx, sr.sink.Name(), report, ErrOutboundQueueFull)
		}
		stopTimer(timer)
	}
	return true
}

func (w *OutboundWorker) closeSinks() {
	for i := range w.sinks {
		close(w.sinks[i].ch)
	}
}

func (w *OutboundWorker) runSink(ctx context.Context, sr *sinkRuntime) {
	for report := range sr.ch {
		if err := w.handleWithRetry(ctx, sr.sink, report); err != nil {
			w.sinkFailures.Add(1)
			w.met.IncSinkFailure(sr.sink.Name())
			w.dlq.HandleDeadLetter(ctx, sr.sink.Name(), report, err)
		}
	}
}

func (w *OutboundWorker) handleWithRetry(ctx context.Context, sink ExecReportSink, report engine.ExecReport) error {
	backoff := w.cfg.RetryBaseBackoff
	var lastErr error

	for attempt := 1; attempt <= w.cfg.RetryMaxAttempts; attempt++ {
		lastErr = sink.HandleExecReport(ctx, report)
		if lastErr == nil {
			return nil
		}
		if attempt == w.cfg.RetryMaxAttempts {
			break
		}

		wait := backoff
		if wait > w.cfg.RetryMaxBackoff {
			wait = w.cfg.RetryMaxBackoff
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return ctx.Err()
		case <-timer.C:
		}
		stopTimer(timer)

		backoff *= 2
		if backoff > w.cfg.RetryMaxBackoff {
			backoff = w.cfg.RetryMaxBackoff
		}
	}
	return lastErr
}
