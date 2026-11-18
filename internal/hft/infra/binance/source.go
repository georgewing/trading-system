package binance

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sync/atomic"
	"time"

	"trading-system/internal/hft/domain"
)

// Stream is one live connection to the exchange websocket feed.
//
// The concrete implementation (coder/websocket) lives in the dialer so this
// package stays free of network dependencies and can be tested with fakes.
type Stream interface {
	// Read returns the next complete frame. It must unblock when ctx is done.
	Read(ctx context.Context) ([]byte, error)
	Close() error
}

// Dialer opens a connection for the given symbols.
type Dialer func(ctx context.Context, symbols []string) (Stream, error)

// Backoff controls reconnect delay growth.
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

func (b Backoff) normalized() Backoff {
	if b.Base <= 0 {
		b.Base = 100 * time.Millisecond
	}
	if b.Max < b.Base {
		b.Max = 5 * time.Second
	}
	return b
}

// SourceConfig configures a Binance market data source.
type SourceConfig struct {
	// Name labels logs and metrics; empty means "binance".
	Name string
	// Decoder converts frames into normalized events. Its scales must be set.
	Decoder Decoder
	// Dial opens one connection; it is the only network seam.
	Dial Dialer
	// Backoff defaults to 100ms..5s.
	Backoff Backoff
	// OnError receives connection and decode errors; nil logs them.
	OnError func(err error)
	// Rand enables reconnect jitter; nil disables it (deterministic backoff).
	Rand *rand.Rand
}

// Source ingests Binance market data and reconnects until stopped.
type Source struct {
	cfg SourceConfig

	dialFailures atomic.Uint64
	reconnects   atomic.Uint64
	decodeErrors atomic.Uint64
}

var _ domain.Source = (*Source)(nil)

// NewSource validates cfg and returns a source. It does not connect.
func NewSource(cfg SourceConfig) (*Source, error) {
	if cfg.Dial == nil {
		return nil, errors.New("binance: dialer is required")
	}
	if cfg.Decoder.PriceScale <= 0 || cfg.Decoder.QtyScale <= 0 {
		return nil, errors.New("binance: decoder price/qty scales must be positive")
	}
	if cfg.Name == "" {
		cfg.Name = "binance"
	}
	cfg.Backoff = cfg.Backoff.normalized()
	return &Source{cfg: cfg}, nil
}

func (s *Source) Name() string { return s.cfg.Name }

// DialFailures returns the number of failed connection attempts.
func (s *Source) DialFailures() uint64 { return s.dialFailures.Load() }

// Reconnects returns the number of times an established connection was lost.
func (s *Source) Reconnects() uint64 { return s.reconnects.Load() }

// DecodeErrors returns the number of frames that could not be decoded. Such
// frames are counted and skipped without dropping the connection.
func (s *Source) DecodeErrors() uint64 { return s.decodeErrors.Load() }

// Run connects, emits normalized events for symbols and reconnects on failure.
// It blocks until ctx is cancelled and returns nil on a graceful stop.
func (s *Source) Run(ctx context.Context, symbols []string, emit domain.Emitter) error {
	if len(symbols) == 0 {
		return errors.New("binance: no symbols")
	}
	if emit == nil {
		return errors.New("binance: nil emitter")
	}

	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}

		stream, err := s.cfg.Dial(ctx, symbols)
		if err != nil {
			s.dialFailures.Add(1)
			attempt++
			s.report(fmt.Errorf("dial failed: %w", err))
			if !sleep(ctx, s.backoff(attempt)) {
				return nil
			}
			continue
		}

		attempt = 0
		err = s.readLoop(ctx, stream, emit)
		_ = stream.Close()

		if ctx.Err() != nil {
			return nil
		}

		s.reconnects.Add(1)
		attempt++
		s.report(fmt.Errorf("connection lost: %w", err))
		if !sleep(ctx, s.backoff(attempt)) {
			return nil
		}
	}
}

func (s *Source) readLoop(ctx context.Context, stream Stream, emit domain.Emitter) error {
	for {
		raw, err := stream.Read(ctx)
		if err != nil {
			return err
		}

		ev, ok, err := s.cfg.Decoder.Decode(raw)
		if err != nil {
			// One malformed frame must not tear down the connection.
			s.decodeErrors.Add(1)
			s.report(err)
			continue
		}
		if !ok {
			continue
		}
		emit(ev)
	}
}

func (s *Source) report(err error) {
	if s.cfg.OnError != nil {
		s.cfg.OnError(err)
		return
	}
	log.Printf("%s source: %v", s.cfg.Name, err)
}

// backoff returns the delay before the next attempt, doubling up to Max and
// optionally jittered by +/-20%.
func (s *Source) backoff(attempt int) time.Duration {
	b := s.cfg.Backoff

	d := b.Base
	for i := 1; i < attempt; i++ {
		if d >= b.Max {
			break
		}
		d *= 2
	}
	if d > b.Max {
		d = b.Max
	}

	if s.cfg.Rand == nil {
		return d
	}

	half := int64(d / 5)
	if half <= 0 {
		return d
	}
	out := d + time.Duration(s.cfg.Rand.Int63n(2*half+1)-half)
	if out <= 0 {
		return time.Millisecond
	}
	return out
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
