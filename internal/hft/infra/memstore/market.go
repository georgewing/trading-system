// Package memstore keeps a bounded in-memory snapshot of recent market data.
//
// It is the read side of the market data plane: the ingest pipeline applies
// every event here, REST handlers read snapshots from here, and WebSocket
// clients bootstrap stateful topics from here. It never influences matching.
package memstore

import (
	"sync"

	"trading-system/internal/hft/domain"
)

// Config bounds retention. Non-positive values fall back to the defaults, so a
// partial config cannot disable retention accidentally.
type Config struct {
	TradeCap int
	KlineCap int
}

// Default retention values.
const (
	defaultTradeCap = 200
	defaultKlineCap = 1000
)

// Market stores the latest depth, a bounded trade tape and bounded candle
// history per symbol. It is safe for concurrent use.
type Market struct {
	tradeCap int
	klineCap int

	mu     sync.RWMutex
	depths map[string]domain.Depth
	trades map[string][]domain.Trade
	klines map[string]map[string][]domain.Kline
}

var _ domain.Snapshotter = (*Market)(nil)

// New builds an empty market snapshot store.
func New(cfg Config) *Market {
	if cfg.TradeCap <= 0 {
		cfg.TradeCap = defaultTradeCap
	}
	if cfg.KlineCap <= 0 {
		cfg.KlineCap = defaultKlineCap
	}
	return &Market{
		tradeCap: cfg.TradeCap,
		klineCap: cfg.KlineCap,
		depths:   make(map[string]domain.Depth),
		trades:   make(map[string][]domain.Trade),
		klines:   make(map[string]map[string][]domain.Kline),
	}
}

// Apply folds one normalized event into the snapshot state.
func (m *Market) Apply(ev domain.Event) {
	switch ev.Kind {
	case domain.KindDepth:
		if ev.Depth == nil {
			return
		}
		m.mu.Lock()
		m.depths[ev.Symbol] = *ev.Depth
		m.mu.Unlock()

	case domain.KindTrade:
		if ev.Trade == nil {
			return
		}
		m.mu.Lock()
		buf := append(m.trades[ev.Symbol], *ev.Trade)
		if len(buf) > m.tradeCap {
			buf = buf[len(buf)-m.tradeCap:]
		}
		m.trades[ev.Symbol] = buf
		m.mu.Unlock()

	case domain.KindKline:
		if ev.Kline == nil || ev.Kline.Interval == "" {
			return
		}
		m.mu.Lock()
		byInterval := m.klines[ev.Symbol]
		if byInterval == nil {
			byInterval = make(map[string][]domain.Kline)
			m.klines[ev.Symbol] = byInterval
		}
		buf := byInterval[ev.Kline.Interval]
		// An update for the same bucket replaces it instead of appending.
		if n := len(buf); n > 0 && buf[n-1].OpenTime == ev.Kline.OpenTime {
			buf[n-1] = *ev.Kline
		} else {
			buf = append(buf, *ev.Kline)
			if len(buf) > m.klineCap {
				buf = buf[len(buf)-m.klineCap:]
			}
		}
		byInterval[ev.Kline.Interval] = buf
		m.mu.Unlock()

	default:
		// BBO and unknown kinds are not part of the snapshot surface yet.
	}
}

// Depth returns the latest snapshot for symbol.
func (m *Market) Depth(symbol string) (domain.Depth, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.depths[symbol]
	return d, ok
}

// Trades returns up to limit most recent prints, oldest first.
func (m *Market) Trades(symbol string, limit int) []domain.Trade {
	m.mu.RLock()
	defer m.mu.RUnlock()

	buf := m.trades[symbol]
	if limit > 0 && len(buf) > limit {
		buf = buf[len(buf)-limit:]
	}
	out := make([]domain.Trade, len(buf))
	copy(out, buf)
	return out
}

// Klines returns up to limit most recent candles, oldest first.
func (m *Market) Klines(symbol, interval string, limit int) []domain.Kline {
	m.mu.RLock()
	defer m.mu.RUnlock()

	buf := m.klines[symbol][interval]
	if limit > 0 && len(buf) > limit {
		buf = buf[len(buf)-limit:]
	}
	out := make([]domain.Kline, len(buf))
	copy(out, buf)
	return out
}
