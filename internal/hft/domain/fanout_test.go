package domain

import (
	"testing"
	"time"
)

func tradeEvent(symbol string) Event {
	return Event{
		Kind:   KindTrade,
		Symbol: symbol,
		Trade:  &Trade{Price: 1, Qty: 1},
	}
}

func tickEvent(symbol string) Event {
	return Event{
		Kind:   KindTick,
		Symbol: symbol,
		Tick:   &Tick{UpdateID: 1},
	}
}

func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
		return Event{}
	}
}

func expectNoEvent(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestMemFanoutTopicMatching(t *testing.T) {
	f := NewMemFanout(8)

	exact, cancelExact, err := f.Subscribe([]string{"trade:BTCUSDT"})
	if err != nil {
		t.Fatalf("Subscribe(exact) error = %v", err)
	}
	kindWildcard, cancelKind, err := f.Subscribe([]string{"tick:*"})
	if err != nil {
		t.Fatalf("Subscribe(kind) error = %v", err)
	}
	all, cancelAll, err := f.Subscribe([]string{"*"})
	if err != nil {
		t.Fatalf("Subscribe(all) error = %v", err)
	}

	if got := f.Subscribers(); got != 3 {
		t.Fatalf("Subscribers() = %d, want 3", got)
	}

	f.Publish(tradeEvent("BTCUSDT"))
	if ev := recv(t, exact); ev.Symbol != "BTCUSDT" || ev.Kind != KindTrade {
		t.Fatalf("exact subscriber got %+v", ev)
	}
	recv(t, all)
	expectNoEvent(t, kindWildcard)

	f.Publish(tickEvent("ETHUSDT"))
	if ev := recv(t, kindWildcard); ev.Kind != KindTick || ev.Symbol != "ETHUSDT" {
		t.Fatalf("kind wildcard got %+v", ev)
	}
	recv(t, all)
	expectNoEvent(t, exact)

	cancelExact()
	cancelKind()
	cancelAll()

	if got := f.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() after cancel = %d, want 0", got)
	}
	if _, ok := <-exact; ok {
		t.Fatal("cancelled channel is still open")
	}
}

func TestMemFanoutDropsSlowSubscriber(t *testing.T) {
	f := NewMemFanout(1)

	ch, cancel, err := f.Subscribe([]string{"*"})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer cancel()

	// First event fills the buffer, the next two must be dropped instead of
	// blocking the publishing goroutine.
	f.Publish(tradeEvent("BTCUSDT"))
	f.Publish(tradeEvent("BTCUSDT"))
	f.Publish(tradeEvent("BTCUSDT"))

	if got := f.Dropped(); got != 2 {
		t.Fatalf("Dropped() = %d, want 2", got)
	}
	if got := len(ch); got != 1 {
		t.Fatalf("buffered events = %d, want 1", got)
	}
}

func TestMemFanoutCancelIsIdempotent(t *testing.T) {
	f := NewMemFanout(1)

	_, cancel, err := f.Subscribe([]string{"trade:BTCUSDT"})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	cancel()
	cancel() // must not panic on double close

	if got := f.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d, want 0", got)
	}
}

func TestMemFanoutRejectsBadTopics(t *testing.T) {
	f := NewMemFanout(1)

	for _, topic := range []string{
		"",
		"BTCUSDT",
		"trade:",
		":BTCUSDT",
		"kline:BTCUSDT",
		"trade:BTC:USDT",
	} {
		if _, _, err := f.Subscribe([]string{topic}); err == nil {
			t.Fatalf("Subscribe(%q) error = nil, want ErrBadTopic", topic)
		}
	}

	if _, _, err := f.Subscribe(nil); err == nil {
		t.Fatal("Subscribe(nil) error = nil, want ErrBadTopic")
	}
}

func TestEventTopic(t *testing.T) {
	if got := tradeEvent("btcusdt").Topic(); got != "trade:btcusdt" {
		t.Fatalf("Topic() = %q, want trade:btcusdt", got)
	}
	if got := tickEvent("BTCUSDT").Topic(); got != "tick:BTCUSDT" {
		t.Fatalf("Topic() = %q, want tick:BTCUSDT", got)
	}
}
