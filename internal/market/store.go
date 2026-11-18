package market

import (
	"sync"
	"sync/atomic"
	"time"
)

// Metrics is the narrow observability surface the store needs. Implementations
// live in internal/observability; a nil Metrics falls back to a no-op.
type Metrics interface {
	IncFeedDrop(topic string)
}

// Store keeps the latest market state in memory and fans updates out to
// subscribers. It is safe for concurrent use.
//
// Retention is deliberately small: the store exists to bootstrap a freshly
// connected chart, not to be a historical database.
type Store struct {
	depthCap int
	tradeCap int
	klineCap int

	mu     sync.RWMutex
	depths map[string]Depth
	trades map[string][]Trade
	klines map[string]map[string][]Kline

	subMu  sync.RWMutex
	nextID uint64
	subs   map[uint64]chan Event

	met Metrics

	dropped atomic.Uint64
}

// StoreConfig bounds in-memory retention.
type StoreConfig struct {
	DepthCap int // reserved; depth is always a full snapshot per symbol
	TradeCap int
	KlineCap int
}

// DefaultStoreConfig returns retention suitable for a trading terminal.
func DefaultStoreConfig() StoreConfig {
	return StoreConfig{
		DepthCap: 50,
		TradeCap: 200,
		KlineCap: 1000,
	}
}

// NewStore builds an empty store. Zero or negative capacities fall back to the
// defaults so a partial config cannot disable retention by accident.
func NewStore(cfg StoreConfig) *Store {
	def := DefaultStoreConfig()
	if cfg.TradeCap <= 0 {
		cfg.TradeCap = def.TradeCap
	}
	if cfg.KlineCap <= 0 {
		cfg.KlineCap = def.KlineCap
	}
	if cfg.DepthCap <= 0 {
		cfg.DepthCap = def.DepthCap
	}
	return &Store{
		depthCap: cfg.DepthCap,
		tradeCap: cfg.TradeCap,
		klineCap: cfg.KlineCap,
		depths:   make(map[string]Depth),
		trades:   make(map[string][]Trade),
		klines:   make(map[string]map[string][]Kline),
		subs:     make(map[uint64]chan Event),
	}
}

// SetMetrics injects the feed metrics recorder. Call it before feeding data.
func (s *Store) SetMetrics(m Metrics) { s.met = m }

// ApplyDepth replaces the snapshot for the symbol and broadcasts it.
func (s *Store) ApplyDepth(d Depth) {
	if d.Ts == 0 {
		d.Ts = nowMillis()
	}

	s.mu.Lock()
	s.depths[d.Symbol] = d
	s.mu.Unlock()

	s.broadcast(Event{Type: EventDepth, Symbol: d.Symbol, Depth: &d})
}

// ApplyTrade appends a print to the per-symbol ring and broadcasts it.
func (s *Store) ApplyTrade(t Trade) {
	if t.Ts == 0 {
		t.Ts = nowMillis()
	}

	s.mu.Lock()
	buf := append(s.trades[t.Symbol], t)
	if len(buf) > s.tradeCap {
		buf = buf[len(buf)-s.tradeCap:]
	}
	s.trades[t.Symbol] = buf
	s.mu.Unlock()

	s.broadcast(Event{Type: EventTrade, Symbol: t.Symbol, Trade: &t})
}

// ApplyKline merges a candle into the per-interval ring. An update for the same
// open time replaces the previous candle instead of appending a duplicate.
func (s *Store) ApplyKline(k Kline) {
	if k.Ts == 0 {
		k.Ts = nowMillis()
	}

	s.mu.Lock()
	byInterval := s.klines[k.Symbol]
	if byInterval == nil {
		byInterval = make(map[string][]Kline)
		s.klines[k.Symbol] = byInterval
	}
	buf := byInterval[k.Interval]
	if n := len(buf); n > 0 && buf[n-1].OpenTime == k.OpenTime {
		buf[n-1] = k
	} else {
		buf = append(buf, k)
		if len(buf) > s.klineCap {
			buf = buf[len(buf)-s.klineCap:]
		}
	}
	byInterval[k.Interval] = buf
	s.mu.Unlock()

	s.broadcast(Event{Type: EventKline, Symbol: k.Symbol, Kline: &k})
}

// Depth returns the current snapshot for symbol.
func (s *Store) Depth(symbol string) (Depth, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.depths[symbol]
	return d, ok
}

// Trades returns up to limit most recent prints, oldest first.
func (s *Store) Trades(symbol string, limit int) []Trade {
	s.mu.RLock()
	defer s.mu.RUnlock()

	buf := s.trades[symbol]
	if limit > 0 && len(buf) > limit {
		buf = buf[len(buf)-limit:]
	}
	out := make([]Trade, len(buf))
	copy(out, buf)
	return out
}

// Klines returns up to limit most recent candles, oldest first.
func (s *Store) Klines(symbol, interval string, limit int) []Kline {
	s.mu.RLock()
	defer s.mu.RUnlock()

	buf := s.klines[symbol][interval]
	if limit > 0 && len(buf) > limit {
		buf = buf[len(buf)-limit:]
	}
	out := make([]Kline, len(buf))
	copy(out, buf)
	return out
}

// Subscribe registers a subscriber and returns a buffered channel of events.
// The channel is closed by the returned cancel function.
func (s *Store) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 1024
	}

	id := atomic.AddUint64(&s.nextID, 1)
	ch := make(chan Event, buffer)

	s.subMu.Lock()
	s.subs[id] = ch
	s.subMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.subMu.Lock()
			if c, ok := s.subs[id]; ok {
				delete(s.subs, id)
				close(c)
			}
			s.subMu.Unlock()
		})
	}
	return ch, cancel
}

// Dropped reports how many subscriber deliveries were dropped because a
// consumer could not keep up.
func (s *Store) Dropped() uint64 { return s.dropped.Load() }

// broadcast delivers to every subscriber without blocking the feed reader. A
// slow consumer loses the event; the sequence number on the wire lets the
// client notice the gap.
func (s *Store) broadcast(ev Event) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()

	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default:
			s.dropped.Add(1)
			if s.met != nil {
				s.met.IncFeedDrop(ev.Type)
			}
		}
	}
}

func nowMillis() int64 { return time.Now().UnixMilli() }
