package strategy

import (
	"errors"
	"fmt"
	"math"

	"trading-system/internal/engine"
)

// StatArb trades the spread between two symbols.
//
// It computes the spread in basis points, z-scores it over a rolling window and
// enters when the deviation is large enough, flattening once the spread
// converges.
//
// Parameters:
//
//	symbol_a    first leg (required)
//	symbol_b    second leg (required)
//	window      rolling sample count (default 100)
//	min_samples samples required before trading (default 30)
//	entry_z     entry threshold in standard deviations (default 2)
//	exit_z      exit threshold in standard deviations (default 0.5)
//	order_qty   per-leg quantity in lots (required)
//
// Both legs use the same lot quantity; run it only when both symbols share the
// same quantity precision.
type StatArb struct {
	symbolA string
	symbolB string

	window     int
	minSamples int
	entryZ     float64
	exitZ      float64
	orderQty   int64

	midA, midB int64
	haveA      bool
	haveB      bool
	spreads    []float64
}

// NewStatArb builds an unconfigured statistical-arbitrage strategy.
func NewStatArb() *StatArb { return &StatArb{} }

func (s *StatArb) Name() string { return "stat_arb" }

func (s *StatArb) Init(_ Context, params map[string]string) error {
	var err error

	s.symbolA = stringParam(params, "symbol_a", "")
	s.symbolB = stringParam(params, "symbol_b", "")
	if s.symbolA == "" || s.symbolB == "" {
		return errors.New("stat_arb: symbol_a and symbol_b are required")
	}
	if s.symbolA == s.symbolB {
		return errors.New("stat_arb: symbol_a and symbol_b must differ")
	}

	window, err := intParam(params, "window", 100)
	if err != nil {
		return err
	}
	if window < 2 {
		return errors.New("stat_arb: window must be at least 2")
	}
	s.window = int(window)

	minSamples, err := intParam(params, "min_samples", 30)
	if err != nil {
		return err
	}
	if minSamples < 2 {
		return errors.New("stat_arb: min_samples must be at least 2")
	}
	if minSamples > int64(s.window) {
		minSamples = int64(s.window)
	}
	s.minSamples = int(minSamples)

	if s.entryZ, err = floatParam(params, "entry_z", 2); err != nil {
		return err
	}
	if s.exitZ, err = floatParam(params, "exit_z", 0.5); err != nil {
		return err
	}
	if s.entryZ <= 0 || s.exitZ < 0 || s.exitZ >= s.entryZ {
		return errors.New("stat_arb: require 0 <= exit_z < entry_z")
	}

	if s.orderQty, err = intParam(params, "order_qty", 0); err != nil {
		return err
	}
	if s.orderQty <= 0 {
		return errors.New("stat_arb: order_qty must be positive")
	}
	return nil
}

func (s *StatArb) OnBook(ctx Context, book BookView) error {
	mid, ok := book.Mid()
	if !ok {
		return nil
	}
	switch book.Symbol {
	case s.symbolA:
		s.midA, s.haveA = mid, true
	case s.symbolB:
		s.midB, s.haveB = mid, true
	default:
		return nil
	}
	if !s.haveA || !s.haveB || s.midB <= 0 {
		return nil
	}

	spread := float64(s.midA-s.midB) * 10000 / float64(s.midB)
	s.spreads = append(s.spreads, spread)
	if len(s.spreads) > s.window {
		s.spreads = s.spreads[len(s.spreads)-s.window:]
	}
	if len(s.spreads) < s.minSamples {
		return nil
	}

	z, ok := zScore(s.spreads)
	if !ok {
		return nil
	}

	pos := ctx.Position(s.symbolA)
	if pos == 0 {
		if len(ctx.OpenOrders(s.symbolA)) > 0 || len(ctx.OpenOrders(s.symbolB)) > 0 {
			return nil
		}
		switch {
		case z >= s.entryZ:
			return s.enter(ctx, engine.SideSell, engine.SideBuy)
		case z <= -s.entryZ:
			return s.enter(ctx, engine.SideBuy, engine.SideSell)
		}
		return nil
	}

	if math.Abs(z) <= s.exitZ {
		return s.flatten(ctx, pos)
	}
	return nil
}

func (s *StatArb) OnTrade(Context, TradeView) error { return nil }

func (s *StatArb) OnTimer(Context, int64) error { return nil }

func (s *StatArb) Stop(Context) error { return nil }

func (s *StatArb) enter(ctx Context, sideA, sideB engine.Side) error {
	if _, err := ctx.Submit(OrderRequest{
		Symbol: s.symbolA,
		Side:   sideA,
		Type:   engine.OrderTypeMarket,
		TIF:    engine.TimeInForceIOC,
		Price:  1,
		Qty:    s.orderQty,
		Tag:    "arb-a",
	}); err != nil {
		return fmt.Errorf("stat_arb: submit %s: %w", s.symbolA, err)
	}

	if _, err := ctx.Submit(OrderRequest{
		Symbol: s.symbolB,
		Side:   sideB,
		Type:   engine.OrderTypeMarket,
		TIF:    engine.TimeInForceIOC,
		Price:  1,
		Qty:    s.orderQty,
		Tag:    "arb-b",
	}); err != nil {
		if pos := ctx.Position(s.symbolA); pos != 0 {
			_ = s.flatten(ctx, pos)
		}
		return fmt.Errorf("stat_arb: submit %s: %w", s.symbolB, err)
	}
	return nil
}

// flatten closes the first leg with an IOC market order. The second leg is
// assumed to have the opposite position and is closed symmetrically.
func (s *StatArb) flatten(ctx Context, pos int64) error {
	side := engine.SideSell
	qty := pos
	if pos < 0 {
		side = engine.SideBuy
		qty = -pos
	}
	if _, err := ctx.Submit(OrderRequest{
		Symbol: s.symbolA,
		Side:   side,
		Type:   engine.OrderTypeMarket,
		TIF:    engine.TimeInForceIOC,
		Price:  1,
		Qty:    qty,
		Tag:    "arb-exit-a",
	}); err != nil {
		return fmt.Errorf("stat_arb: flatten %s: %w", s.symbolA, err)
	}

	// The pair is always entered with equal and opposite quantities, so the
	// second leg's closing quantity equals the first leg's position.
	if _, err := ctx.Submit(OrderRequest{
		Symbol: s.symbolB,
		Side:   opposite(side),
		Type:   engine.OrderTypeMarket,
		TIF:    engine.TimeInForceIOC,
		Price:  1,
		Qty:    qty,
		Tag:    "arb-exit-b",
	}); err != nil {
		return fmt.Errorf("stat_arb: flatten %s: %w", s.symbolB, err)
	}
	return nil
}

func opposite(side engine.Side) engine.Side {
	if side == engine.SideBuy {
		return engine.SideSell
	}
	return engine.SideBuy
}

// zScore returns the z-score of the last sample. It reports false when the
// sample has no dispersion.
func zScore(samples []float64) (float64, bool) {
	if len(samples) < 2 {
		return 0, false
	}
	var sum float64
	for _, v := range samples {
		sum += v
	}
	mean := sum / float64(len(samples))

	var sq float64
	for _, v := range samples {
		d := v - mean
		sq += d * d
	}
	std := math.Sqrt(sq / float64(len(samples)))
	if std == 0 {
		return 0, false
	}
	return (samples[len(samples)-1] - mean) / std, true
}

var _ Strategy = (*StatArb)(nil)
