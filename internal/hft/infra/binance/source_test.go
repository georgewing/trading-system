package binance

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trading-system/internal/hft/domain"
)

const testTradeFrame = `{"stream":"btcusdt@trade","data":{"e":"trade","s":"BTCUSDT","t":1,"p":"100.0","q":"1.0","T":1700000000001}}`

type collector struct {
	mu  sync.Mutex
	evs []domain.Event
}

func (c *collector) emit(ev domain.Event) {
	c.mu.Lock()
	c.evs = append(c.evs, ev)
	c.mu.Unlock()
}

func (c *collector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.evs)
}

func (c *collector) events() []domain.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.Event, len(c.evs))
	copy(out, c.evs)
	return out
}

// fakeStream replays frames and then blocks until ctx is cancelled.
type fakeStream struct {
	frames [][]byte
	idx    int
}

func (f *fakeStream) Read(ctx context.Context) ([]byte, error) {
	if f.idx < len(f.frames) {
		frame := f.frames[f.idx]
		f.idx++
		return frame, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeStream) Close() error { return nil }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func newTestSource(t *testing.T, dial Dialer) *Source {
	t.Helper()
	src, err := NewSource(SourceConfig{
		Decoder: Decoder{PriceScale: testScale, QtyScale: testScale},
		Dial:    dial,
		Backoff: Backoff{Base: time.Millisecond, Max: 2 * time.Millisecond},
		OnError: func(error) {},
	})
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	return src
}

func TestSourceEmitsAndStopsOnCancel(t *testing.T) {
	stream := &fakeStream{frames: [][]byte{[]byte(testTradeFrame), []byte(testTradeFrame)}}
	collector := &collector{}
	src := newTestSource(t, func(context.Context, []string) (Stream, error) { return stream, nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, []string{"BTCUSDT"}, collector.emit) }()

	waitFor(t, "two events", func() bool { return collector.len() == 2 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil on graceful stop", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}

	for _, ev := range collector.events() {
		if ev.Symbol != "BTCUSDT" || ev.Kind != domain.KindTrade {
			t.Fatalf("unexpected event %+v", ev)
		}
	}
}

func TestSourceSkipsBadFrameWithoutReconnecting(t *testing.T) {
	stream := &fakeStream{frames: [][]byte{[]byte(`{"e":`), []byte(testTradeFrame)}}
	collector := &collector{}
	src := newTestSource(t, func(context.Context, []string) (Stream, error) { return stream, nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = src.Run(ctx, []string{"BTCUSDT"}, collector.emit) }()

	waitFor(t, "one good event", func() bool { return collector.len() == 1 })

	if got := src.DecodeErrors(); got != 1 {
		t.Fatalf("DecodeErrors() = %d, want 1", got)
	}
	if got := src.Reconnects(); got != 0 {
		t.Fatalf("Reconnects() = %d, want 0 (bad frame must not drop the connection)", got)
	}
}

func TestSourceRetriesDial(t *testing.T) {
	stream := &fakeStream{frames: [][]byte{[]byte(testTradeFrame)}}
	collector := &collector{}

	var attempts atomic.Int64
	dial := func(context.Context, []string) (Stream, error) {
		if attempts.Add(1) <= 2 {
			return nil, errors.New("dial boom")
		}
		return stream, nil
	}
	src := newTestSource(t, dial)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = src.Run(ctx, []string{"BTCUSDT"}, collector.emit) }()

	waitFor(t, "event after retries", func() bool { return collector.len() == 1 })

	if got := src.DialFailures(); got != 2 {
		t.Fatalf("DialFailures() = %d, want 2", got)
	}
	if got := src.Reconnects(); got != 0 {
		t.Fatalf("Reconnects() = %d, want 0 (no connection was established before)", got)
	}
}

func TestNewSourceValidatesConfig(t *testing.T) {
	if _, err := NewSource(SourceConfig{}); err == nil {
		t.Fatal("NewSource() without dialer: error = nil, want error")
	}
	if _, err := NewSource(SourceConfig{
		Dial: func(context.Context, []string) (Stream, error) { return nil, errors.New("unused") },
	}); err == nil {
		t.Fatal("NewSource() without scales: error = nil, want error")
	}

	src := newTestSource(t, func(context.Context, []string) (Stream, error) {
		return &fakeStream{}, nil
	})
	if err := src.Run(context.Background(), nil, func(domain.Event) {}); err == nil {
		t.Fatal("Run() without symbols: error = nil, want error")
	}
	if err := src.Run(context.Background(), []string{"BTCUSDT"}, nil); err == nil {
		t.Fatal("Run() with nil emitter: error = nil, want error")
	}
}

func TestSourceBackoffIsBounded(t *testing.T) {
	src := newTestSource(t, func(context.Context, []string) (Stream, error) {
		return &fakeStream{}, nil
	})

	for _, attempt := range []int{1, 2, 3, 4, 10, 100} {
		got := src.backoff(attempt)
		if got < time.Millisecond || got > 2*time.Millisecond {
			t.Fatalf("backoff(%d) = %v, want within [1ms, 2ms]", attempt, got)
		}
	}
}
