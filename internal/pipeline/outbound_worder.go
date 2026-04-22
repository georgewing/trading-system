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
		queue: queue,
		cfg:   cfg,
		dlq:   dlq,
		met:   observability.NoopRecorder(),
		sinks: rt,
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
		sw := w.sinks[i]
		w.wg.Add(1)
		go func(sr sinkRuntime) {
			defer w.wg.Done()
			w.runSink(ctx, sr)
		}(sw)
	}

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.runDispatcher(ctx)
	}()
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
	for {
		select {
		case <-ctx.Done():
			return
		default:
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

			for _, sr := range w.sinks {
				timer := time.NewTimer(w.cfg.DispatchTimeout)
				select {
				case sr.ch <- report:
				case <-ctx.Done():
					stopTimer(timer)
					return
				case <-timer.C:
					w.dispatchDropped.Add(1)
					w.dlq.HandleDeadLetter(ctx, sr.sink.Name(), report, ErrOutboundQueueFull)
				}
				stopTimer(timer)
			}
		}
	}
}

func (w *OutboundWorker) runSink(ctx context.Context, sr sinkRuntime) {
	for {
		select {
		case <-ctx.Done():
			return
		case report := <-sr.ch:
			if err := w.handleWithRetry(ctx, sr.sink, report); err != nil {
				w.sinkFailures.Add(1)
				w.dlq.HandleDeadLetter(ctx, sr.sink.Name(), report, err)
			}
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
