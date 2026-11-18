package domain

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// defaultFanoutBuffer is the per-subscriber channel capacity used when the
// caller does not specify one.
const defaultFanoutBuffer = 1024

// ErrBadTopic reports a malformed subscription topic.
var ErrBadTopic = errors.New("market: bad topic")

// MemFanout is an in-process Fanout backed by bounded channels. It is the
// default implementation until a cross-process bus is needed; it is safe for
// concurrent use.
type MemFanout struct {
	mu      sync.RWMutex
	nextID  uint64
	subs    map[uint64]*fanoutSub
	bufSize int
	dropped atomic.Uint64
}

type fanoutSub struct {
	// patterns holds the raw subscription strings, for example "trade:BTCUSDT",
	// "trade:*" or "*".
	patterns map[string]struct{}
	ch       chan Event
}

var _ Fanout = (*MemFanout)(nil)

// NewMemFanout builds a fanout with the given per-subscriber buffer size.
func NewMemFanout(bufferSize int) *MemFanout {
	if bufferSize <= 0 {
		bufferSize = defaultFanoutBuffer
	}
	return &MemFanout{
		subs:    make(map[uint64]*fanoutSub),
		bufSize: bufferSize,
	}
}

// Publish delivers ev to every matching subscriber without blocking. A
// subscriber whose buffer is full loses the event and is counted in Dropped.
func (f *MemFanout) Publish(ev Event) {
	if f == nil {
		return
	}

	topic := ev.Topic()

	f.mu.RLock()
	defer f.mu.RUnlock()

	for _, sub := range f.subs {
		if !sub.match(topic) {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			f.dropped.Add(1)
		}
	}
}

// Subscribe registers topics ("trade:BTCUSDT", "tick:*", "*") and returns the
// stream plus an idempotent cancel function that closes it.
func (f *MemFanout) Subscribe(topics []string) (<-chan Event, func(), error) {
	if f == nil {
		return nil, nil, fmt.Errorf("%w: nil fanout", ErrBadTopic)
	}
	if len(topics) == 0 {
		return nil, nil, fmt.Errorf("%w: empty subscription", ErrBadTopic)
	}

	patterns := make(map[string]struct{}, len(topics))
	for _, topic := range topics {
		if err := validateTopic(topic); err != nil {
			return nil, nil, err
		}
		patterns[topic] = struct{}{}
	}

	ch := make(chan Event, f.bufSize)

	f.mu.Lock()
	f.nextID++
	id := f.nextID
	f.subs[id] = &fanoutSub{patterns: patterns, ch: ch}
	f.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			// Taking the write lock guarantees no Publish is mid-send, so
			// closing the channel cannot race with a send.
			f.mu.Lock()
			if sub, ok := f.subs[id]; ok {
				delete(f.subs, id)
				close(sub.ch)
			}
			f.mu.Unlock()
		})
	}

	return ch, cancel, nil
}

// Dropped returns how many events were discarded because a subscriber was slow.
func (f *MemFanout) Dropped() uint64 {
	if f == nil {
		return 0
	}
	return f.dropped.Load()
}

// Subscribers returns the current subscriber count.
func (f *MemFanout) Subscribers() int {
	if f == nil {
		return 0
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.subs)
}

func (s *fanoutSub) match(topic string) bool {
	if _, ok := s.patterns["*"]; ok {
		return true
	}
	if _, ok := s.patterns[topic]; ok {
		return true
	}
	if i := strings.IndexByte(topic, ':'); i > 0 {
		if _, ok := s.patterns[topic[:i]+":*"]; ok {
			return true
		}
	}
	return false
}

func validateTopic(topic string) error {
	if topic == "*" {
		return nil
	}
	i := strings.IndexByte(topic, ':')
	if i <= 0 || i == len(topic)-1 {
		return fmt.Errorf("%w: %q", ErrBadTopic, topic)
	}

	symbol := topic[i+1:]
	if symbol == "*" {
		// "trade:*" is accepted once the kind is known.
	} else if strings.ContainsAny(symbol, ":* ") {
		return fmt.Errorf("%w: %q", ErrBadTopic, topic)
	}

	switch topic[:i] {
	case KindTrade.String(), KindTick.String():
		return nil
	default:
		return fmt.Errorf("%w: unknown kind in %q", ErrBadTopic, topic)
	}
}
