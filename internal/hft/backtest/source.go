package backtest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"trading-system/internal/hft/strategy"
)

// Source is a monotonic replay stream. Next returns false when the stream is
// exhausted. Implementations must not read time.Now: the event timestamp is the
// only clock the simulator sees.
type Source interface {
	Next(ctx context.Context, ev *Event) (bool, error)
	Close() error
}

// SliceSource replays an in-memory slice. It is primarily useful in tests.
type SliceSource struct {
	events []Event
	next   int
}

// NewSliceSource copies events into a replayable source.
func NewSliceSource(events []Event) *SliceSource {
	cp := make([]Event, len(events))
	copy(cp, events)
	return &SliceSource{events: cp}
}

// Next implements Source.
func (s *SliceSource) Next(ctx context.Context, ev *Event) (bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	if s.next >= len(s.events) {
		return false, nil
	}
	*ev = s.events[s.next]
	s.next++
	return true, nil
}

// Close implements Source.
func (s *SliceSource) Close() error { return nil }

// JSONLSource reads one JSON object per line:
//
//	{"ts":1700000000000,"symbol":"BTCUSDT","kind":"book",
//	 "book":{"bids":[[10000,100]],"asks":[[10001,100]]}}
//	{"ts":1700000000001,"symbol":"BTCUSDT","kind":"trade",
//	 "trade":{"id":1,"price":10000,"qty":50}}
type JSONLSource struct {
	file    *os.File
	scanner *bufio.Scanner
	line    int
	lastTS  int64
}

// OpenJSONL opens path for replay.
func OpenJSONL(path string) (*JSONLSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &JSONLSource{file: f, scanner: sc}, nil
}

// Next implements Source. Timestamps must be non-decreasing.
func (s *JSONLSource) Next(ctx context.Context, ev *Event) (bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	for s.scanner.Scan() {
		s.line++
		line := s.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var wire wireEvent
		if err := json.Unmarshal(line, &wire); err != nil {
			return false, fmt.Errorf("backtest: %s line %d: %w", s.file.Name(), s.line, err)
		}
		parsed, err := wire.event()
		if err != nil {
			return false, fmt.Errorf("backtest: %s line %d: %w", s.file.Name(), s.line, err)
		}
		if parsed.TS < s.lastTS {
			return false, fmt.Errorf("backtest: %s line %d: timestamp went backwards (%d < %d)",
				s.file.Name(), s.line, parsed.TS, s.lastTS)
		}
		s.lastTS = parsed.TS
		*ev = parsed
		return true, nil
	}
	if err := s.scanner.Err(); err != nil {
		return false, fmt.Errorf("backtest: %s: %w", s.file.Name(), err)
	}
	return false, nil
}

// Close implements Source.
func (s *JSONLSource) Close() error { return s.file.Close() }

type wireEvent struct {
	TS     int64      `json:"ts"`
	Symbol string     `json:"symbol"`
	Kind   string     `json:"kind"`
	Book   *wireBook  `json:"book"`
	Trade  *wireTrade `json:"trade"`
}

type wireBook struct {
	Bids [][2]int64 `json:"bids"`
	Asks [][2]int64 `json:"asks"`
}

type wireTrade struct {
	ID    uint64 `json:"id"`
	Price int64  `json:"price"`
	Qty   int64  `json:"qty"`
}

func (w wireEvent) event() (Event, error) {
	if w.Symbol == "" {
		return Event{}, fmt.Errorf("symbol is required")
	}
	if w.TS <= 0 {
		return Event{}, fmt.Errorf("ts must be positive")
	}

	switch w.Kind {
	case "book":
		if w.Book == nil {
			return Event{}, fmt.Errorf("book payload is required for kind=book")
		}
		return Event{
			TS:     w.TS,
			Symbol: w.Symbol,
			Kind:   KindBook,
			Book: &BookSnapshot{
				Bids: levels(w.Book.Bids),
				Asks: levels(w.Book.Asks),
			},
		}, nil

	case "trade":
		if w.Trade == nil {
			return Event{}, fmt.Errorf("trade payload is required for kind=trade")
		}
		if w.Trade.Price <= 0 || w.Trade.Qty <= 0 {
			return Event{}, fmt.Errorf("trade price and qty must be positive")
		}
		return Event{
			TS:     w.TS,
			Symbol: w.Symbol,
			Kind:   KindTrade,
			Trade: &Trade{
				ID:    w.Trade.ID,
				Price: w.Trade.Price,
				Qty:   w.Trade.Qty,
			},
		}, nil

	default:
		return Event{}, fmt.Errorf("unknown kind %q", w.Kind)
	}
}

func levels(in [][2]int64) []strategy.Level {
	out := make([]strategy.Level, 0, len(in))
	for _, lv := range in {
		if lv[0] <= 0 || lv[1] <= 0 {
			continue
		}
		out = append(out, strategy.Level{Price: lv[0], Qty: lv[1]})
	}
	return out
}
